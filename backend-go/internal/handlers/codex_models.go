package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pelletier/go-toml/v2"
)

// Codex 模型目录（models.json）管理：
// - 预设模板 + 少量可调字段生成模型元数据条目（与 DeepSeek 官方 Codex 接入文档的字段保持一致）
// - 按 slug upsert / delete，保留用户手工写入的未知字段与条目
// - setDefault 同步 config.toml 根级 model / model_catalog_json / model_reasoning_effort
//
// Codex（0.144+）对条目的两条硬性约束，违反将导致 Codex 启动失败：
// - 每个条目必须携带 base_instructions 或 model_messages.instructions_template 之一；
//   自定义条目的指令从动态获取的内置清单复制，不在代码里硬编码大段提示词。
// - model_catalog_json 是整体替换语义：设置后内置模型全部失效，因此每次写入都保证
//   内置清单条目完整（已存在的同 slug 条目保持不动）；目录删空时必须回退该配置键。

const (
	codexModelCatalogFileName = "models.json"

	codexRootKeyModel          = "model"
	codexRootKeyModelCatalog   = "model_catalog_json"
	codexRootKeyReasoningLevel = "model_reasoning_effort"

	codexMaxContextWindowLimit = 100000000
)

var (
	codexSlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	codexRootKeys    = []string{codexRootKeyModel, codexRootKeyModelCatalog, codexRootKeyReasoningLevel}
)

// codexReasoningLevel 与 codexDefaultReasoningLevels 是自定义模型的固定推理档位骨架
// （与 DeepSeek 官方 Codex 接入文档的 supported_reasoning_levels 一致）。
type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

var codexDefaultReasoningLevels = []codexReasoningLevel{
	{Effort: "low", Description: "Fast responses with lighter reasoning"},
	{Effort: "high", Description: "Extra high reasoning depth for complex problems"},
	{Effort: "max", Description: "Maximum reasoning depth for the hardest problems"},
}

// codexEntryModelMessages 只解析自定义条目关心的指令字段，其余子结构原样透传。
type codexEntryModelMessages struct {
	InstructionsTemplate string `json:"instructions_template"`
}

// codexBuiltinCatalog 是通过 `codex debug models` 动态获取的内置模型清单。
// 不硬编码内置模型：不同 codex 版本的清单与指令模板不同，以运行时实际输出为准。
type codexBuiltinCatalog struct {
	entries []json.RawMessage
	index   map[string]json.RawMessage
}

func (b *codexBuiltinCatalog) has(slug string) bool {
	_, ok := b.index[slug]
	return ok
}

func (b *codexBuiltinCatalog) entryMeta(slug string) (codexModelEntryMeta, error) {
	raw, ok := b.index[slug]
	if !ok {
		return codexModelEntryMeta{}, newCodexInputError("模型 %s 不存在", slug)
	}
	return codexCatalogEntryMeta(raw)
}

// modelMessages 挑选一份通用指令模板作为自定义条目的 model_messages 来源：
// 优先 gpt-5.6-*（无插值变量、指令最新），否则退回第一个携带非空模板的条目。
func (b *codexBuiltinCatalog) modelMessages() (json.RawMessage, error) {
	var fallback json.RawMessage
	for _, entry := range b.entries {
		var wrapper struct {
			Slug          string          `json:"slug"`
			ModelMessages json.RawMessage `json:"model_messages"`
		}
		if err := json.Unmarshal(entry, &wrapper); err != nil || len(wrapper.ModelMessages) == 0 {
			continue
		}
		var parsed codexEntryModelMessages
		if err := json.Unmarshal(wrapper.ModelMessages, &parsed); err != nil || parsed.InstructionsTemplate == "" {
			continue
		}
		raw := append(json.RawMessage(nil), wrapper.ModelMessages...)
		if fallback == nil {
			fallback = raw
		}
		if strings.HasPrefix(wrapper.Slug, "gpt-5.6-") {
			return raw, nil
		}
	}
	if fallback == nil {
		return nil, fmt.Errorf("内置清单中没有任何携带 model_messages.instructions_template 的条目")
	}
	return fallback, nil
}

// builtinViews 输出内置清单视图（含 visibility=hide 条目，展示与否由前端决定）。
func (b *codexBuiltinCatalog) builtinViews() []codexModelView {
	views := make([]codexModelView, 0, len(b.entries))
	for _, entry := range b.entries {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			continue
		}
		views = append(views, meta.view(true))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Slug < views[j].Slug })
	return views
}

// fetchCodexBuiltinCatalog 在隔离的 CODEX_HOME 下执行 `codex debug models` 获取内置清单。
// 必须用空目录隔离：model_catalog_json 是替换语义，用户已设置时直接执行会输出用户目录。
// 包级变量便于测试注入。
var fetchCodexBuiltinCatalog = func(ctx context.Context) (*codexBuiltinCatalog, error) {
	executable, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("未找到 codex 命令，无法获取内置模型清单: %w", err)
	}
	home, err := os.MkdirTemp("", "codex-home-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时 CODEX_HOME 失败: %w", err)
	}
	defer os.RemoveAll(home)

	// Windows 上 npm 全局安装的 codex 是 .cmd 脚本，CreateProcess 无法直接执行，需经 cmd /c。
	args := []string{"debug", "models"}
	var cmd *exec.Cmd
	if ext := strings.ToLower(filepath.Ext(executable)); ext == ".cmd" || ext == ".bat" {
		cmd = exec.CommandContext(ctx, "cmd", append([]string{"/c", executable}, args...)...)
	} else {
		cmd = exec.CommandContext(ctx, executable, args...)
	}
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	stdout, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("codex debug models 执行失败: %v: %s", err, bytes.TrimSpace(exitErr.Stderr))
		}
		return nil, fmt.Errorf("codex debug models 执行失败: %w", err)
	}
	var doc struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(stdout, &doc); err != nil {
		return nil, fmt.Errorf("解析 codex debug models 输出失败: %w", err)
	}
	if len(doc.Models) == 0 {
		return nil, fmt.Errorf("codex debug models 输出为空，无法获取内置模型清单")
	}
	catalog := &codexBuiltinCatalog{entries: doc.Models, index: make(map[string]json.RawMessage, len(doc.Models))}
	for _, entry := range doc.Models {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			return nil, fmt.Errorf("内置清单条目无法解析: %w", err)
		}
		catalog.index[meta.Slug] = entry
	}
	return catalog, nil
}

const codexBuiltinCacheTTL = 5 * time.Minute

var (
	codexBuiltinMu    sync.Mutex
	codexBuiltinCache *codexBuiltinCatalog
	codexBuiltinAt    time.Time
)

// loadCodexBuiltinCatalog 返回内置清单，带进程内缓存。
// 写入操作（upsert / setDefault）必须 force 刷新，避免 codex 升级后抄到陈旧指令。
func loadCodexBuiltinCatalog(ctx context.Context, force bool) (*codexBuiltinCatalog, error) {
	codexBuiltinMu.Lock()
	defer codexBuiltinMu.Unlock()
	if !force && codexBuiltinCache != nil && time.Since(codexBuiltinAt) < codexBuiltinCacheTTL {
		return codexBuiltinCache, nil
	}
	catalog, err := fetchCodexBuiltinCatalog(ctx)
	if err != nil {
		return nil, err
	}
	codexBuiltinCache = catalog
	codexBuiltinAt = time.Now()
	return catalog, nil
}

// codexFieldsHaveInstructions 判断条目是否满足 Codex 的指令硬性要求：
// 携带 base_instructions 或含非空 instructions_template 的 model_messages。
func codexFieldsHaveInstructions(fields map[string]json.RawMessage) bool {
	if bi, ok := fields["base_instructions"]; ok && !bytes.Equal(bytes.TrimSpace(bi), []byte("null")) {
		return true
	}
	mm, ok := fields["model_messages"]
	if !ok || bytes.Equal(bytes.TrimSpace(mm), []byte("null")) {
		return false
	}
	var parsed codexEntryModelMessages
	return json.Unmarshal(mm, &parsed) == nil && parsed.InstructionsTemplate != ""
}

// codexCatalogModelEntry 是本功能管理的 models.json 条目字段全集。
// ModelMessages 原样透传（从内置清单复制，结构随 codex 版本演进，不逐字段建模）。
type codexCatalogModelEntry struct {
	Slug                           string                `json:"slug"`
	PreferWebsockets               bool                  `json:"prefer_websockets"`
	SupportVerbosity               bool                  `json:"support_verbosity"`
	DefaultVerbosity               string                `json:"default_verbosity"`
	ApplyPatchToolType             string                `json:"apply_patch_tool_type"`
	WebSearchToolType              string                `json:"web_search_tool_type"`
	InputModalities                []string              `json:"input_modalities"`
	SupportsImageDetailOriginal    bool                  `json:"supports_image_detail_original"`
	TruncationPolicy               codexTruncationPolicy `json:"truncation_policy"`
	SupportsParallelToolCalls      bool                  `json:"supports_parallel_tool_calls"`
	ToolMode                       *string               `json:"tool_mode"`
	MultiAgentVersion              string                `json:"multi_agent_version"`
	UseResponsesLite               bool                  `json:"use_responses_lite"`
	IncludeSkillsUsageInstructions bool                  `json:"include_skills_usage_instructions"`
	AutoReviewModelOverride        *string               `json:"auto_review_model_override"`
	ContextWindow                  int64                 `json:"context_window"`
	MaxContextWindow               int64                 `json:"max_context_window"`
	EffectiveContextWindowPercent  int                   `json:"effective_context_window_percent"`
	AutoCompactTokenLimit          *int64                `json:"auto_compact_token_limit"`
	CompHash                       string                `json:"comp_hash"`
	ReasoningSummaryFormat         string                `json:"reasoning_summary_format"`
	DefaultReasoningSummary        string                `json:"default_reasoning_summary"`
	DisplayName                    string                `json:"display_name"`
	Description                    string                `json:"description"`
	DefaultReasoningLevel          string                `json:"default_reasoning_level"`
	SupportedReasoningLevels       []codexReasoningLevel `json:"supported_reasoning_levels"`
	ShellType                      string                `json:"shell_type"`
	Visibility                     string                `json:"visibility"`
	MinimalClientVersion           string                `json:"minimal_client_version"`
	SupportedInAPI                 bool                  `json:"supported_in_api"`
	AvailabilityNux                *string               `json:"availability_nux"`
	Upgrade                        *string               `json:"upgrade"`
	Priority                       int                   `json:"priority"`
	ExperimentalSupportedTools     []string              `json:"experimental_supported_tools"`
	SupportsSearchTool             bool                  `json:"supports_search_tool"`
	DefaultServiceTier             *string               `json:"default_service_tier"`
	SupportsReasoningSummaries     bool                  `json:"supports_reasoning_summaries"`
	ModelMessages                  json.RawMessage       `json:"model_messages"`
}

type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int64  `json:"limit"`
}

// codexModelEntryMeta 从条目中提取的最小元数据集合，用于列表展示与更新校验。
type codexModelEntryMeta struct {
	Slug                     string                `json:"slug"`
	DisplayName              string                `json:"display_name"`
	Description              string                `json:"description"`
	InputModalities          []string              `json:"input_modalities"`
	ContextWindow            int64                 `json:"context_window"`
	MaxContextWindow         int64                 `json:"max_context_window"`
	DefaultReasoningLevel    string                `json:"default_reasoning_level"`
	SupportedReasoningLevels []codexReasoningLevel `json:"supported_reasoning_levels"`
	Priority                 int                   `json:"priority"`
	Visibility               string                `json:"visibility"`
	SupportsSearchTool       bool                  `json:"supports_search_tool"`
}

func (meta codexModelEntryMeta) hasEffort(effort string) bool {
	for _, level := range meta.SupportedReasoningLevels {
		if level.Effort == effort {
			return true
		}
	}
	return false
}

func (meta codexModelEntryMeta) view(builtin bool) codexModelView {
	levels := make([]string, 0, len(meta.SupportedReasoningLevels))
	for _, level := range meta.SupportedReasoningLevels {
		levels = append(levels, level.Effort)
	}
	return codexModelView{
		Slug:                  meta.Slug,
		DisplayName:           meta.DisplayName,
		Description:           meta.Description,
		SupportsImage:         slices.Contains(meta.InputModalities, "image"),
		ContextWindow:         meta.ContextWindow,
		DefaultReasoningLevel: meta.DefaultReasoningLevel,
		ReasoningLevels:       levels,
		SupportsSearchTool:    meta.SupportsSearchTool,
		Priority:              meta.Priority,
		Visibility:            meta.Visibility,
		Builtin:               builtin,
	}
}

// codexModelView 是 GET /settings/codex 返回的模型条目视图。
type codexModelView struct {
	Slug                  string   `json:"slug"`
	DisplayName           string   `json:"displayName"`
	Description           string   `json:"description"`
	SupportsImage         bool     `json:"supportsImage"`
	ContextWindow         int64    `json:"contextWindow"`
	DefaultReasoningLevel string   `json:"defaultReasoningLevel"`
	ReasoningLevels       []string `json:"reasoningLevels"`
	SupportsSearchTool    bool     `json:"supportsSearchTool"`
	Priority              int      `json:"priority"`
	Visibility            string   `json:"visibility"`
	Builtin               bool     `json:"builtin"`
}

type saveCodexModelCatalogRequest struct {
	Action                string `json:"action"`
	Slug                  string `json:"slug"`
	DisplayName           string `json:"displayName"`
	Description           string `json:"description"`
	SupportsImage         bool   `json:"supportsImage"`
	ContextWindow         int64  `json:"contextWindow"`
	DefaultReasoningLevel string `json:"defaultReasoningLevel"`
	ReasoningEffort       string `json:"reasoningEffort"`
	ClearReasoningEffort  bool   `json:"clearReasoningEffort"`
}

type codexModelUpsertInput struct {
	Slug                  string
	DisplayName           string
	Description           string
	SupportsImage         bool
	ContextWindow         int64
	DefaultReasoningLevel string
}

type codexModelCatalogResponse struct {
	Success    bool   `json:"success"`
	ModelsPath string `json:"modelsPath"`
	ConfigPath string `json:"configPath"`
}

// SaveCodexModelCatalog 管理 Codex 模型目录（models.json）与 config.toml 根级模型字段。
func SaveCodexModelCatalog() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req saveCodexModelCatalogRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Codex 模型目录请求无效"})
			return
		}
		if err := validateCodexModelCatalogRequest(req); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		codexSettingsMu.Lock()
		defer codexSettingsMu.Unlock()

		configPath, _, modelsPath, err := resolveCodexPaths()
		if err != nil {
			c.JSON(500, gin.H{"error": fmt.Sprintf("解析 Codex 配置目录失败: %v", err)})
			return
		}
		response := codexModelCatalogResponse{ModelsPath: modelsPath, ConfigPath: configPath}
		// 内置清单需要执行外部命令，限制总时长防止请求悬挂。
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()

		switch req.Action {
		case "upsert":
			builtin, err := loadCodexBuiltinCatalog(ctx, true)
			if err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("获取 Codex 内置模型清单失败: %v", err)})
				return
			}
			updated, err := upsertCodexModelCatalog(modelsPath, codexModelUpsertInput{
				Slug:                  strings.TrimSpace(req.Slug),
				DisplayName:           strings.TrimSpace(req.DisplayName),
				Description:           strings.TrimSpace(req.Description),
				SupportsImage:         req.SupportsImage,
				ContextWindow:         req.ContextWindow,
				DefaultReasoningLevel: strings.TrimSpace(req.DefaultReasoningLevel),
			}, builtin)
			if err != nil {
				respondCodexModelError(c, err)
				return
			}
			if err := writeCodexFile(modelsPath, updated.previous, updated.data, ".codex-models-*.tmp"); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("保存 models.json 失败: %v", err)})
				return
			}
		case "delete":
			updated, empty, err := deleteCodexModelCatalog(modelsPath, strings.TrimSpace(req.Slug))
			if err != nil {
				respondCodexModelError(c, err)
				return
			}
			if err := writeCodexFile(modelsPath, updated.previous, updated.data, ".codex-models-*.tmp"); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("保存 models.json 失败: %v", err)})
				return
			}
			if err := cleanupCodexConfigAfterModelDelete(configPath, strings.TrimSpace(req.Slug), empty); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("更新 Codex config.toml 失败: %v", err)})
				return
			}
		case "setDefault":
			if err := setCodexDefaultModel(ctx, configPath, modelsPath, strings.TrimSpace(req.Slug), req.ReasoningEffort, req.ClearReasoningEffort); err != nil {
				respondCodexModelError(c, err)
				return
			}
		default:
			c.JSON(400, gin.H{"error": "action 仅支持 upsert / delete / setDefault"})
			return
		}

		response.Success = true
		c.JSON(200, response)
	}
}

// respondCodexModelError 按错误类型区分状态码：用户输入问题 400，其余 500。
func respondCodexModelError(c *gin.Context, err error) {
	var inputErr codexUserInputError
	if errors.As(err, &inputErr) {
		c.JSON(400, gin.H{"error": inputErr.Error()})
		return
	}
	c.JSON(500, gin.H{"error": err.Error()})
}

// codexUserInputError 标识由请求参数导致的错误（应返回 400）。
type codexUserInputError struct{ message string }

func (e codexUserInputError) Error() string { return e.message }

func newCodexInputError(format string, args ...any) error {
	return codexUserInputError{message: fmt.Sprintf(format, args...)}
}

type codexCatalogWriteResult struct {
	previous []byte
	data     []byte
}

func upsertCodexModelCatalog(modelsPath string, input codexModelUpsertInput, builtin *codexBuiltinCatalog) (codexCatalogWriteResult, error) {
	catalog, err := readCodexModelCatalog(modelsPath)
	if err != nil {
		return codexCatalogWriteResult{}, err
	}
	if err := catalog.upsertModel(input, builtin); err != nil {
		return codexCatalogWriteResult{}, err
	}
	catalog.ensureBuiltinEntries(builtin)
	encoded, err := catalog.encode()
	if err != nil {
		return codexCatalogWriteResult{}, fmt.Errorf("编码 models.json 失败: %w", err)
	}
	return codexCatalogWriteResult{previous: catalog.raw, data: encoded}, nil
}

// deleteCodexModelCatalog 删除条目，第二返回值表示删除后目录是否为空
// （空目录会让 Codex 拒绝启动，调用方需同步回退 config.toml 的 model_catalog_json）。
func deleteCodexModelCatalog(modelsPath, slug string) (codexCatalogWriteResult, bool, error) {
	catalog, err := readCodexModelCatalog(modelsPath)
	if err != nil {
		return codexCatalogWriteResult{}, false, err
	}
	if err := catalog.deleteModel(slug); err != nil {
		return codexCatalogWriteResult{}, false, err
	}
	empty := len(catalog.entries) == 0
	encoded, err := catalog.encode()
	if err != nil {
		return codexCatalogWriteResult{}, false, fmt.Errorf("编码 models.json 失败: %w", err)
	}
	return codexCatalogWriteResult{previous: catalog.raw, data: encoded}, empty, nil
}

// setCodexDefaultModel 将 config.toml 根级 model / model_catalog_json / model_reasoning_effort
// 指向目标模型。状态机：
//   - 目标在 catalog 中：正常写入；顺带补齐缺失的内置条目（替换语义下不补则内置模型失效）
//   - 目标是内置模型且 catalog 非空：把内置条目并入 catalog 后写入
//   - 目标是内置模型且 catalog 为空：只写 model 键并移除 model_catalog_json（Codex 直接用内置清单）
func setCodexDefaultModel(ctx context.Context, configPath, modelsPath, slug, reasoningEffort string, clearReasoningEffort bool) error {
	catalog, err := readCodexModelCatalog(modelsPath)
	if err != nil {
		return err
	}
	builtin, builtinErr := loadCodexBuiltinCatalog(ctx, true)

	catalogChanged := false
	meta, findErr := catalog.findModel(slug)
	switch {
	case findErr == nil:
		if builtinErr == nil {
			catalogChanged = catalog.ensureBuiltinEntries(builtin)
		}
	case builtinErr != nil:
		return findErr
	case !builtin.has(slug):
		return newCodexInputError("模型 %s 不存在", slug)
	case len(catalog.entries) == 0:
		if meta, findErr = builtin.entryMeta(slug); findErr != nil {
			return findErr
		}
	default:
		catalogChanged = catalog.ensureBuiltinEntries(builtin)
		if meta, findErr = catalog.findModel(slug); findErr != nil {
			return findErr
		}
	}
	if reasoningEffort != "" && !meta.hasEffort(reasoningEffort) {
		return newCodexInputError("模型 %s 不支持推理档位 %q", slug, reasoningEffort)
	}

	if catalogChanged {
		encoded, err := catalog.encode()
		if err != nil {
			return fmt.Errorf("编码 models.json 失败: %w", err)
		}
		// 先落盘 models.json，再写 config.toml，保证 model_catalog_json 指向的文件先就绪。
		if err := writeCodexFile(modelsPath, catalog.raw, encoded, ".codex-models-*.tmp"); err != nil {
			return fmt.Errorf("保存 models.json 失败: %w", err)
		}
	}

	_, configRaw, _, err := readCodexConfig(configPath)
	if err != nil {
		return err
	}
	updatedConfig := configRaw
	model := slug
	if updatedConfig, err = mergeCodexRootKey(updatedConfig, codexRootKeyModel, &model); err != nil {
		return err
	}
	if len(catalog.entries) > 0 {
		catalogPath := modelsPath
		if updatedConfig, err = mergeCodexRootKey(updatedConfig, codexRootKeyModelCatalog, &catalogPath); err != nil {
			return err
		}
	} else if updatedConfig, err = mergeCodexRootKey(updatedConfig, codexRootKeyModelCatalog, nil); err != nil {
		return err
	}
	if reasoningEffort != "" {
		effort := reasoningEffort
		if updatedConfig, err = mergeCodexRootKey(updatedConfig, codexRootKeyReasoningLevel, &effort); err != nil {
			return err
		}
	} else if clearReasoningEffort {
		if updatedConfig, err = mergeCodexRootKey(updatedConfig, codexRootKeyReasoningLevel, nil); err != nil {
			return err
		}
	}
	if !bytes.Equal(configRaw, updatedConfig) {
		if err := writeCodexFile(configPath, configRaw, updatedConfig, ".codex-config-*.tmp"); err != nil {
			return fmt.Errorf("保存 Codex config.toml 失败: %w", err)
		}
	}
	return nil
}

// cleanupCodexConfigAfterModelDelete 在删除条目后回退 config.toml：
//   - 目录删空：移除 model_catalog_json（空目录会让 Codex 拒绝启动，回退后使用内置清单）
//   - 被删模型是当前默认模型：移除 model 与 model_reasoning_effort（指向不存在的模型会让 Codex 启动失败）
func cleanupCodexConfigAfterModelDelete(configPath, slug string, catalogEmpty bool) error {
	config, configRaw, exists, err := readCodexConfig(configPath)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	updated := configRaw
	if catalogEmpty {
		if updated, err = mergeCodexRootKey(updated, codexRootKeyModelCatalog, nil); err != nil {
			return err
		}
	}
	if strings.TrimSpace(config.Model) == slug {
		if updated, err = mergeCodexRootKey(updated, codexRootKeyModel, nil); err != nil {
			return err
		}
		if updated, err = mergeCodexRootKey(updated, codexRootKeyReasoningLevel, nil); err != nil {
			return err
		}
	}
	if bytes.Equal(configRaw, updated) {
		return nil
	}
	return writeCodexFile(configPath, configRaw, updated, ".codex-config-*.tmp")
}

func validateCodexModelCatalogRequest(req saveCodexModelCatalogRequest) error {
	switch req.Action {
	case "upsert", "delete", "setDefault":
	default:
		return newCodexInputError("action 仅支持 upsert / delete / setDefault")
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		return newCodexInputError("模型 slug 不能为空")
	}
	if !codexSlugPattern.MatchString(slug) {
		return newCodexInputError("slug 仅支持字母、数字、点、下划线和连字符，且以字母或数字开头")
	}
	if req.Action == "upsert" {
		if req.ContextWindow <= 0 || req.ContextWindow > codexMaxContextWindowLimit {
			return newCodexInputError("上下文窗口必须在 1 ~ %d 之间", codexMaxContextWindowLimit)
		}
	}
	if req.ReasoningEffort != "" && req.ClearReasoningEffort {
		return newCodexInputError("reasoningEffort 与 clearReasoningEffort 不能同时设置")
	}
	return nil
}

// codexModelCatalog 是 models.json 的内存表示：
// doc 保留顶层未知字段，entries 为 models 数组的原始条目。
type codexModelCatalog struct {
	doc     map[string]json.RawMessage
	entries []json.RawMessage
	raw     []byte
	exists  bool
}

func readCodexModelCatalog(path string) (*codexModelCatalog, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &codexModelCatalog{doc: map[string]json.RawMessage{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, newCodexInputError("models.json JSON 格式无效: %v", err)
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	catalog := &codexModelCatalog{doc: doc, raw: raw, exists: true}
	if modelsRaw, ok := doc["models"]; ok && !bytes.Equal(bytes.TrimSpace(modelsRaw), []byte("null")) {
		if err := json.Unmarshal(modelsRaw, &catalog.entries); err != nil {
			return nil, newCodexInputError("models.json 的 models 字段无效: %v", err)
		}
	}
	return catalog, nil
}

func codexCatalogEntryMeta(raw json.RawMessage) (codexModelEntryMeta, error) {
	var meta codexModelEntryMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return codexModelEntryMeta{}, newCodexInputError("models.json 中存在无法解析的模型条目: %v", err)
	}
	return meta, nil
}

func (catalog *codexModelCatalog) findModel(slug string) (codexModelEntryMeta, error) {
	for _, entry := range catalog.entries {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			return codexModelEntryMeta{}, err
		}
		if meta.Slug == slug {
			return meta, nil
		}
	}
	return codexModelEntryMeta{}, newCodexInputError("模型 %s 不存在", slug)
}

func (catalog *codexModelCatalog) modelViews(builtin *codexBuiltinCatalog) []codexModelView {
	views := make([]codexModelView, 0, len(catalog.entries))
	for _, entry := range catalog.entries {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			continue
		}
		views = append(views, meta.view(builtin != nil && builtin.has(meta.Slug)))
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Slug < views[j].Slug })
	return views
}

// ensureBuiltinEntries 补齐缺失的内置条目，返回是否有新增。
// model_catalog_json 对 Codex 是整体替换语义：设置后未写入的内置模型将不可见，
// 因此每次写入都保证内置清单完整；已存在的同 slug 条目保持不动（尊重用户手工修改）。
func (catalog *codexModelCatalog) ensureBuiltinEntries(builtin *codexBuiltinCatalog) bool {
	if builtin == nil {
		return false
	}
	existing := make(map[string]struct{}, len(catalog.entries))
	for _, entry := range catalog.entries {
		if meta, err := codexCatalogEntryMeta(entry); err == nil {
			existing[meta.Slug] = struct{}{}
		}
	}
	changed := false
	for _, entry := range builtin.entries {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			continue
		}
		if _, ok := existing[meta.Slug]; ok {
			continue
		}
		catalog.entries = append(catalog.entries, entry)
		changed = true
	}
	return changed
}

// upsertModel 按 slug 新增或更新条目：
// 新增时从内置清单复制 model_messages 并套用固定骨架；更新时仅覆盖可调字段，保留其余字段，
// 缺失指令字段的坏条目（旧版本写入）会顺带补全。
func (catalog *codexModelCatalog) upsertModel(input codexModelUpsertInput, builtin *codexBuiltinCatalog) error {
	for index, entry := range catalog.entries {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			return err
		}
		if meta.Slug != input.Slug {
			continue
		}
		return catalog.updateModel(index, meta, input, builtin)
	}

	if builtin == nil {
		return fmt.Errorf("内置模型清单不可用，无法为新条目复制 model_messages")
	}
	messages, err := builtin.modelMessages()
	if err != nil {
		return fmt.Errorf("获取指令模板失败: %w", err)
	}
	entry := buildCodexModelEntry(input, messages)
	// 统一走 map 序列化，保证多次保存的键序稳定（按字母序）。
	fields, err := codexEntryFieldMap(entry)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("编码模型条目失败: %w", err)
	}
	catalog.entries = append(catalog.entries, encoded)
	return nil
}

func (catalog *codexModelCatalog) updateModel(index int, meta codexModelEntryMeta, input codexModelUpsertInput, builtin *codexBuiltinCatalog) error {
	if input.DefaultReasoningLevel != "" && !meta.hasEffort(input.DefaultReasoningLevel) {
		return newCodexInputError("模型 %s 不支持推理档位 %q", input.Slug, input.DefaultReasoningLevel)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(catalog.entries[index], &fields); err != nil {
		return newCodexInputError("解析模型 %s 条目失败: %v", input.Slug, err)
	}
	if !codexFieldsHaveInstructions(fields) {
		if builtin == nil {
			return fmt.Errorf("模型 %s 缺少指令字段且内置清单不可用，无法修复条目", input.Slug)
		}
		messages, err := builtin.modelMessages()
		if err != nil {
			return fmt.Errorf("获取指令模板失败: %w", err)
		}
		fields["model_messages"] = messages
	}
	displayName := input.DisplayName
	if displayName == "" {
		displayName = input.Slug
	}
	maxContextWindow := meta.MaxContextWindow
	if maxContextWindow < input.ContextWindow {
		maxContextWindow = input.ContextWindow
	}
	overrides := map[string]any{
		"display_name":                   displayName,
		"input_modalities":               codexInputModalities(input.SupportsImage),
		"supports_image_detail_original": input.SupportsImage,
		"context_window":                 input.ContextWindow,
		"max_context_window":             maxContextWindow,
	}
	if input.Description != "" {
		overrides["description"] = input.Description
	}
	if input.DefaultReasoningLevel != "" {
		overrides["default_reasoning_level"] = input.DefaultReasoningLevel
	}
	for key, value := range overrides {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("编码模型字段 %s 失败: %w", key, err)
		}
		fields[key] = encoded
	}
	updated, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("编码模型条目失败: %w", err)
	}
	catalog.entries[index] = updated
	return nil
}

func (catalog *codexModelCatalog) deleteModel(slug string) error {
	for index, entry := range catalog.entries {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			return err
		}
		if meta.Slug == slug {
			catalog.entries = append(catalog.entries[:index], catalog.entries[index+1:]...)
			return nil
		}
	}
	return newCodexInputError("模型 %s 不存在", slug)
}

func (catalog *codexModelCatalog) encode() ([]byte, error) {
	if catalog.doc == nil {
		catalog.doc = map[string]json.RawMessage{}
	}
	entries := catalog.entries
	if entries == nil {
		entries = []json.RawMessage{}
	}
	encodedModels, err := json.Marshal(entries)
	if err != nil {
		return nil, err
	}
	catalog.doc["models"] = encodedModels
	encoded, err := json.MarshalIndent(catalog.doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func codexEntryFieldMap(entry codexCatalogModelEntry) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func codexInputModalities(supportsImage bool) []string {
	if supportsImage {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// buildCodexModelEntry 基于用户输入与固定骨架生成完整条目；messages 为从内置清单复制的指令模板。
// 固定骨架决策：supports_search_tool=false（web_search 是 OpenAI 特有工具，第三方模型默认不启用）、
// priority=99（自定义模型排在内置模型之后）、推理档位固定 low/high/max、默认档位 high。
func buildCodexModelEntry(input codexModelUpsertInput, messages json.RawMessage) codexCatalogModelEntry {
	displayName := input.DisplayName
	if displayName == "" {
		displayName = input.Slug
	}
	reasoningLevel := input.DefaultReasoningLevel
	if reasoningLevel == "" {
		reasoningLevel = "high"
	}
	return codexCatalogModelEntry{
		Slug:                           input.Slug,
		PreferWebsockets:               false,
		SupportVerbosity:               true,
		DefaultVerbosity:               "low",
		ApplyPatchToolType:             "freeform",
		WebSearchToolType:              "text",
		InputModalities:                codexInputModalities(input.SupportsImage),
		SupportsImageDetailOriginal:    input.SupportsImage,
		TruncationPolicy:               codexTruncationPolicy{Mode: "tokens", Limit: 10000},
		SupportsParallelToolCalls:      true,
		ToolMode:                       nil,
		MultiAgentVersion:              "v2",
		UseResponsesLite:               false,
		IncludeSkillsUsageInstructions: false,
		AutoReviewModelOverride:        nil,
		ContextWindow:                  input.ContextWindow,
		MaxContextWindow:               input.ContextWindow,
		EffectiveContextWindowPercent:  95,
		AutoCompactTokenLimit:          nil,
		CompHash:                       "3000",
		ReasoningSummaryFormat:         "experimental",
		DefaultReasoningSummary:        "none",
		DisplayName:                    displayName,
		Description:                    input.Description,
		DefaultReasoningLevel:          reasoningLevel,
		SupportedReasoningLevels:       codexDefaultReasoningLevels,
		ShellType:                      "shell_command",
		Visibility:                     "list",
		MinimalClientVersion:           "0.144.0",
		SupportedInAPI:                 true,
		AvailabilityNux:                nil,
		Upgrade:                        nil,
		Priority:                       99,
		ExperimentalSupportedTools:     []string{},
		SupportsSearchTool:             false,
		DefaultServiceTier:             nil,
		SupportsReasoningSummaries:     true,
		ModelMessages:                  messages,
	}
}

// mergeCodexRootKey 定点修改 config.toml 根级键（仅限白名单键），保留其余内容与格式。
// value 为 nil 时删除该键；键不存在时插入到首个表头之前（TOML 根级键必须位于任何表头之前）。
func mergeCodexRootKey(raw []byte, key string, value *string) ([]byte, error) {
	if !slices.Contains(codexRootKeys, key) {
		return nil, fmt.Errorf("不支持的 config.toml 根级字段: %s", key)
	}
	lineEnding := "\n"
	if bytes.Contains(raw, []byte("\r\n")) {
		lineEnding = "\r\n"
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	firstTable := len(lines)
	for index, line := range lines {
		if isTOMLTableHeader(line) {
			firstTable = index
			break
		}
	}

	keyPattern := regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `\s*=`)
	keyIndex := -1
	indent := ""
	for index := 0; index < firstTable; index++ {
		if match := keyPattern.FindStringSubmatch(lines[index]); match != nil {
			keyIndex = index
			indent = match[1]
			break
		}
	}

	switch {
	case keyIndex >= 0 && value == nil:
		lines = append(lines[:keyIndex], lines[keyIndex+1:]...)
	case keyIndex >= 0:
		lines[keyIndex] = indent + key + " = " + quoteTOMLString(*value)
	case value != nil:
		insertAt := firstTable
		for insertAt > 0 {
			previous := strings.TrimSpace(lines[insertAt-1])
			if previous == "" || strings.HasPrefix(previous, "#") {
				insertAt--
				continue
			}
			break
		}
		keyLine := key + " = " + quoteTOMLString(*value)
		lines = append(lines[:insertAt], append([]string{keyLine}, lines[insertAt:]...)...)
	}

	updated := strings.Join(lines, "\n")
	if lineEnding == "\r\n" {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	result := []byte(updated)
	var validation map[string]interface{}
	if err := toml.Unmarshal(result, &validation); err != nil {
		return nil, fmt.Errorf("更新后的 config.toml 无效: %w", err)
	}
	return result, nil
}

// quoteTOMLString 生成 TOML 字符串字面量：优先使用单引号字面量字符串（无需转义），
// 值中含单引号或控制字符时退回到双引号基本字符串并做 TOML 转义。
func quoteTOMLString(value string) string {
	needsEscape := strings.ContainsRune(value, '\'')
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		return "'" + value + "'"
	}
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				builder.WriteString(fmt.Sprintf(`\u%04X`, r))
			} else {
				builder.WriteRune(r)
			}
		}
	}
	builder.WriteByte('"')
	return builder.String()
}
