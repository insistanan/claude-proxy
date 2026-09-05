package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// 两个哨兵错误让调用方能区分错误类别，而不是去匹配错误文本：
// 管理端 API 要据此选 404 / 500 / 400，靠 strings.Contains 猜文案改一次文字就会错。
var (
	// ErrModelNotListed 目标模型不在单价表里（Delete 用）。
	ErrModelNotListed = errors.New("单价表里没有该模型")
	// ErrPricingPersist 单价表落盘失败；此时内存状态已回滚到修改前。
	ErrPricingPersist = errors.New("单价配置落盘失败")
)

// DefaultFilePath 单价配置的默认路径，由 main.go 注入 NewStore。
const DefaultFilePath = ".config/pricing.json"

// FileVersion 是 pricing.json 的结构版本号。读到更高版本直接拒绝加载：
// 加载后回写会把未来版本新增的字段静默截掉，降级运行一次就永久丢数据。
const FileVersion = 1

// pricingFile 是 pricing.json 的磁盘结构，只存"用户改写"与"删除墓碑"。
// 内置价格表（DefaultBuiltinPricing）始终留在代码里、绝不落盘：一旦导出，
// 内置价格就变成了用户改写，后续版本修正内置价格时再也覆盖不回来。
type pricingFile struct {
	Version         int            `json:"version"`
	Overrides       []ModelPricing `json:"overrides,omitempty"`
	DeletedModelIDs []string       `json:"deletedModelIds,omitempty"`
}

// Store 承载模型单价表：磁盘上的用户改写 + 代码里的内置价格，合并结果常驻内存，
// 供请求出口按模型名取价。Store 是该文件的唯一写者，只在自身变更时重建索引，
// 因此不需要监听文件变化。
type Store struct {
	mu       sync.RWMutex
	filePath string

	file pricingFile
	// resolved 是内置表去掉墓碑后并入用户改写的最终单价表，按归一化模型名升序。
	resolved []ModelPricing
	// index 归一化模型名 → resolved 下标，精确匹配用。
	index map[string]int
	// matchKeys 是 index 的全部键，按"长度降序 + 同长度字典升序"排列，
	// 保证包含匹配的结果只取决于表内容，不受加载顺序影响。
	matchKeys []string
}

// NewStore 加载单价配置。文件缺失按"没有任何用户改写"处理；格式非法或版本过高
// 一律返回错误，不静默重置——单价表被静默清空只会表现为费用凭空变少。
func NewStore(filePath string) (*Store, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, fmt.Errorf("单价配置文件路径为空")
	}
	store := &Store{filePath: filePath}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// NormalizeModelID 归一化模型名，只用于**查表**：去空白 + 转小写。
// 单价解析绝不改写模型名本身，也绝不剥离日期 / 上下文后缀
//（见 docs/invariants.md「模型名一律原样匹配」）——后缀由包含匹配吸收。
func NormalizeModelID(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// Lookup 解析模型名对应的单价，返回副本。命中顺序：
// ① 归一化后精确匹配；② 最长包含匹配（表内键必须是模型名的子串）。
// 未配置单价时返回 nil，调用方必须显式处理，绝不按零价静默计费。
func (s *Store) Lookup(model string) *ModelPricing {
	// 计费挂在请求出口路径上，单价存储未注入时不能让它 panic。
	if s == nil {
		return nil
	}
	key := NormalizeModelID(model)
	if key == "" {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if pos, ok := s.index[key]; ok {
		entry := s.resolved[pos]
		return &entry
	}
	// 包含匹配是**单向**的：表内键必须是模型名的子串，这样
	// `claude-3-5-sonnet-20241022` 能落到 `claude-3-5-sonnet` 上。
	// 绝不照搬 config.redirectModelList 的双向 Contains——反向命中会让
	// `gpt-4o` 匹配到 `gpt-4o-mini`，按另一个模型的价格计费。
	for _, candidate := range s.matchKeys {
		if strings.Contains(key, candidate) {
			entry := s.resolved[s.index[candidate]]
			return &entry
		}
	}
	return nil
}

// CostFor 解析单价并折算本次用量的花费。缓存是否已计入输入量由协议决定，
// 统一走 IsCacheInclusiveAPIType，调用方不要自行判断。
// 未配置单价时返回 ok=false，调用方据此把记录标成"未计价"，
// 绝不按 0 元入账——那样花费统计会凭空少算且看不出来。
func (s *Store) CostFor(model string, usage TokenUsage, apiType string, costMultiplier float64) (CostBreakdown, bool) {
	entry := s.Lookup(model)
	if entry == nil {
		return CostBreakdown{}, false
	}
	return Calculate(usage, entry, costMultiplier, IsCacheInclusiveAPIType(apiType)), true
}

// All 返回合并后的完整单价表副本，按归一化模型名升序。
func (s *Store) All() []ModelPricing {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ModelPricing(nil), s.resolved...)
}

// Overrides 返回用户改写副本，供管理界面区分"内置价"与"我改过的价"。
func (s *Store) Overrides() []ModelPricing {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ModelPricing(nil), s.file.Overrides...)
}

// DeletedModelIDs 返回删除墓碑副本（已归一化）。
func (s *Store) DeletedModelIDs() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.file.DeletedModelIDs...)
}

// Upsert 写入或更新用户单价改写。同名改写整条替换（含内置价格），并清掉该模型的
// 删除墓碑——这也是把误删的内置模型放回表里的唯一路径。
func (s *Store) Upsert(entries ...ModelPricing) error {
	if len(entries) == 0 {
		return fmt.Errorf("没有要写入的单价条目")
	}

	normalized := make([]ModelPricing, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for i := range entries {
		entry := entries[i]
		if err := normalizeModelPricing(&entry); err != nil {
			return fmt.Errorf("第 %d 条单价无效: %w", i+1, err)
		}
		key := NormalizeModelID(entry.ModelID)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("同一批次里模型 %q 出现多次", entry.ModelID)
		}
		seen[key] = struct{}{}
		normalized = append(normalized, entry)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	previous := s.file.clone()
	for _, entry := range normalized {
		key := NormalizeModelID(entry.ModelID)
		s.file.putOverride(key, entry)
		s.file.dropTombstone(key)
	}
	return s.commitLocked(previous)
}

// Delete 把模型从单价表里移除：删掉用户改写；若被删的是内置条目，补一条删除墓碑，
// 否则下次启动内置表会把它带回来。想恢复走 Upsert 重新写价。
// 只按精确模型名删除，不走包含匹配——删表是行操作，模糊命中会删错行。
func (s *Store) Delete(modelID string) error {
	key := NormalizeModelID(modelID)
	if key == "" {
		return fmt.Errorf("模型名为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, listed := s.index[key]; !listed {
		return fmt.Errorf("单价表里没有模型 %q: %w", modelID, ErrModelNotListed)
	}

	previous := s.file.clone()
	s.file.dropOverride(key)
	if builtinHasModelID(key) {
		s.file.addTombstone(key)
	}
	return s.commitLocked(previous)
}

// commitLocked 落盘并重建索引；落盘失败时回滚到 previous，
// 避免内存里的单价表与磁盘内容分叉。
func (s *Store) commitLocked(previous pricingFile) error {
	if err := s.saveLocked(); err != nil {
		s.file = previous
		s.rebuildLocked()
		return fmt.Errorf("%w: %w", ErrPricingPersist, err)
	}
	s.rebuildLocked()
	return nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.filePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取单价配置 %s 失败: %w", s.filePath, err)
	}

	file := pricingFile{Version: FileVersion}
	// 文件缺失或为空按"没有任何用户改写"处理：首次 Upsert 才落盘，
	// 启动阶段不写文件，少一条失败路径。
	if strings.TrimSpace(string(raw)) != "" {
		if err := json.Unmarshal(raw, &file); err != nil {
			return fmt.Errorf("单价配置 %s 不是合法 JSON: %w", s.filePath, err)
		}
		if file.Version > FileVersion {
			return fmt.Errorf("单价配置 %s 的版本 %d 高于当前支持的 %d，拒绝加载以免回写时丢掉新增字段",
				s.filePath, file.Version, FileVersion)
		}
		if file.Version <= 0 {
			file.Version = FileVersion
		}
	}

	if err := normalizePricingFile(&file); err != nil {
		return fmt.Errorf("单价配置 %s 无效: %w", s.filePath, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.file = file
	s.rebuildLocked()
	return nil
}

// rebuildLocked 把内置表与用户改写合并成最终单价表并重建两个索引。
func (s *Store) rebuildLocked() {
	deleted := make(map[string]struct{}, len(s.file.DeletedModelIDs))
	for _, id := range s.file.DeletedModelIDs {
		if key := NormalizeModelID(id); key != "" {
			deleted[key] = struct{}{}
		}
	}

	merged := make(map[string]ModelPricing, len(DefaultBuiltinPricing)+len(s.file.Overrides))
	for _, builtin := range DefaultBuiltinPricing {
		key := NormalizeModelID(builtin.ModelID)
		if _, dropped := deleted[key]; dropped {
			continue
		}
		merged[key] = builtin
	}
	// 同名改写整条替换内置价格。只覆盖非零字段会造出"输入价是我改的、输出价还是
	// 内置值"这种半改写状态，用户对着界面也分不清哪个数在生效。
	for _, override := range s.file.Overrides {
		merged[NormalizeModelID(override.ModelID)] = override
	}

	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	s.resolved = make([]ModelPricing, 0, len(keys))
	s.index = make(map[string]int, len(keys))
	for _, key := range keys {
		s.index[key] = len(s.resolved)
		s.resolved = append(s.resolved, merged[key])
	}

	// keys 已是字典升序，稳定排序按长度降序后即"长度降序 + 同长度字典升序"，
	// 与 config.redirectModelList 的确定性排序同一套口径。
	s.matchKeys = append([]string(nil), keys...)
	sort.SliceStable(s.matchKeys, func(i, j int) bool {
		return len(s.matchKeys[i]) > len(s.matchKeys[j])
	})
}

func (s *Store) saveLocked() error {
	s.file.Version = FileVersion
	sort.Slice(s.file.Overrides, func(i, j int) bool {
		return NormalizeModelID(s.file.Overrides[i].ModelID) < NormalizeModelID(s.file.Overrides[j].ModelID)
	})
	sort.Strings(s.file.DeletedModelIDs)

	data, err := json.MarshalIndent(s.file, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化单价配置失败: %w", err)
	}
	return writeFileAtomic(s.filePath, append(data, '\n'))
}

// writeFileAtomic 以"同目录临时文件 + 重命名"写入，避免写一半中断留下截断的单价表。
// os.Rename 在 Windows 上走 MoveFileEx(MOVEFILE_REPLACE_EXISTING)，可直接覆盖旧文件。
// 需要更强的落盘保证（写穿 + 备份 + revision）时用 piagent.WriteFileAtomic，
// 但那套带 pi-agent 自己的备份目录与返回结构，单价表用不上。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建单价配置目录失败: %w", err)
	}

	temp, err := os.CreateTemp(dir, ".pricing-*.tmp")
	if err != nil {
		return fmt.Errorf("创建单价配置临时文件失败: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("写入单价配置临时文件失败: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("同步单价配置临时文件失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("关闭单价配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("替换单价配置失败: %w", err)
	}
	return nil
}

// normalizePricingFile 校验并归一化整份磁盘配置。手工编辑出的重复条目一律显式报错，
// 不做"后者胜"的静默兜底——两条同名改写里究竟哪条在生效，用户从界面上看不出来。
func normalizePricingFile(file *pricingFile) error {
	overrides := make([]ModelPricing, 0, len(file.Overrides))
	seen := make(map[string]struct{}, len(file.Overrides))
	for i := range file.Overrides {
		entry := file.Overrides[i]
		if err := normalizeModelPricing(&entry); err != nil {
			return fmt.Errorf("第 %d 条改写无效: %w", i+1, err)
		}
		key := NormalizeModelID(entry.ModelID)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("模型 %q 有多条改写", entry.ModelID)
		}
		seen[key] = struct{}{}
		overrides = append(overrides, entry)
	}

	tombstones := make([]string, 0, len(file.DeletedModelIDs))
	dropped := make(map[string]struct{}, len(file.DeletedModelIDs))
	for _, id := range file.DeletedModelIDs {
		key := NormalizeModelID(id)
		if key == "" {
			continue
		}
		// 同名条目同时出现在两个列表里，无法判断用户是想改价还是想删除。
		if _, overridden := seen[key]; overridden {
			return fmt.Errorf("模型 %q 同时出现在 overrides 与 deletedModelIds 里", id)
		}
		// 墓碑重复只是同一个"已删除"事实写了两遍，去重即可，不必报错。
		if _, dup := dropped[key]; dup {
			continue
		}
		dropped[key] = struct{}{}
		tombstones = append(tombstones, key)
	}

	sort.Slice(overrides, func(i, j int) bool {
		return NormalizeModelID(overrides[i].ModelID) < NormalizeModelID(overrides[j].ModelID)
	})
	sort.Strings(tombstones)
	file.Overrides = overrides
	file.DeletedModelIDs = tombstones
	return nil
}

// normalizeModelPricing 校验单条单价。四个价格全为 0 是合法的（免费模型），
// 但 NaN / Inf / 负数一律拒绝：这类值会一路累加进总花费，事后只表现为账单里出现
// 无法解释的数字，追不回来源。
func normalizeModelPricing(entry *ModelPricing) error {
	entry.ModelID = strings.TrimSpace(entry.ModelID)
	entry.DisplayName = strings.TrimSpace(entry.DisplayName)
	if entry.ModelID == "" {
		return fmt.Errorf("模型名不能为空")
	}

	prices := []struct {
		label string
		value float64
	}{
		{"输入", entry.InputCostPerM},
		{"输出", entry.OutputCostPerM},
		{"缓存读取", entry.CacheReadCostPerM},
		{"缓存写入", entry.CacheCreationCostPerM},
	}
	for _, price := range prices {
		if math.IsNaN(price.value) || math.IsInf(price.value, 0) {
			return fmt.Errorf("模型 %q 的%s单价不是有效数字", entry.ModelID, price.label)
		}
		if price.value < 0 {
			return fmt.Errorf("模型 %q 的%s单价不能为负数: %v", entry.ModelID, price.label, price.value)
		}
	}
	return nil
}

// builtinHasModelID 判断归一化模型名是否来自内置价格表，决定 Delete 要不要补墓碑。
func builtinHasModelID(normalizedID string) bool {
	for _, builtin := range DefaultBuiltinPricing {
		if NormalizeModelID(builtin.ModelID) == normalizedID {
			return true
		}
	}
	return false
}

// clone 深拷贝磁盘结构，供落盘失败时回滚。
func (f *pricingFile) clone() pricingFile {
	return pricingFile{
		Version:         f.Version,
		Overrides:       append([]ModelPricing(nil), f.Overrides...),
		DeletedModelIDs: append([]string(nil), f.DeletedModelIDs...),
	}
}

// putOverride 就地整条替换同名改写，没有则追加。key 必须已归一化。
func (f *pricingFile) putOverride(key string, entry ModelPricing) {
	for i := range f.Overrides {
		if NormalizeModelID(f.Overrides[i].ModelID) == key {
			f.Overrides[i] = entry
			return
		}
	}
	f.Overrides = append(f.Overrides, entry)
}

func (f *pricingFile) dropOverride(key string) {
	kept := make([]ModelPricing, 0, len(f.Overrides))
	for _, entry := range f.Overrides {
		if NormalizeModelID(entry.ModelID) == key {
			continue
		}
		kept = append(kept, entry)
	}
	f.Overrides = kept
}

// addTombstone 追加删除墓碑；DeletedModelIDs 里的元素在 normalizePricingFile
// 里已归一化，这里直接按字符串比对。
func (f *pricingFile) addTombstone(key string) {
	for _, id := range f.DeletedModelIDs {
		if id == key {
			return
		}
	}
	f.DeletedModelIDs = append(f.DeletedModelIDs, key)
}

func (f *pricingFile) dropTombstone(key string) {
	kept := make([]string, 0, len(f.DeletedModelIDs))
	for _, id := range f.DeletedModelIDs {
		if id == key {
			continue
		}
		kept = append(kept, id)
	}
	f.DeletedModelIDs = kept
}
