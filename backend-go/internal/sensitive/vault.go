package sensitive

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var reservedPlaceholderPattern = regexp.MustCompile(`\{\{[A-Z0-9]+_[A-Z2-7]{13}\}\}`)

// RedactionMatch 是可逆脱敏使用的统一命中区间。Original 必须与 Text 的对应
// 字节区间完全一致；调用方不得记录该字段。
type RedactionMatch struct {
	Type     string
	Rule     string
	Original string
	Start    int
	End      int
}

// RestoreJSON 递归还原 JSON 中的全部字符串值。非 JSON 响应按普通文本还原。
func (v *Vault) RestoreJSON(body []byte) ([]byte, error) {
	if v == nil || v.Empty() || len(body) == 0 {
		return body, nil
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return []byte(v.Restore(string(body))), nil
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != nil {
		if err != io.EOF {
			return nil, fmt.Errorf("解析待还原响应体尾部失败: %w", err)
		}
	} else {
		return nil, fmt.Errorf("解析待还原响应体失败: JSON 包含多个顶层值")
	}
	value = restoreJSONStrings(value, v)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("序列化已还原响应体失败: %w", err)
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n")), nil
}

func restoreJSONStrings(value interface{}, vault *Vault) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, item := range typed {
			typed[key] = restoreJSONStrings(item, vault)
		}
	case []interface{}:
		for index, item := range typed {
			typed[index] = restoreJSONStrings(item, vault)
		}
	case string:
		return vault.Restore(typed)
	}
	return value
}

// Vault 保存单次请求的明文与随机占位符映射。Vault 只能挂在请求上下文中，
// 不得放入全局缓存、持久化存储或日志。
type Vault struct {
	mu            sync.RWMutex
	byOriginal    map[string]string
	byPlaceholder map[string]string
	reserved      map[string]struct{}
}

// ReserveText 登记同一请求中尚未处理的原始文本，用于避免随机占位符与请求
// 自带文本发生冲突。
func (v *Vault) ReserveText(text string) {
	if v == nil || text == "" {
		return
	}
	placeholders := reservedPlaceholderPattern.FindAllString(text, -1)
	if len(placeholders) == 0 {
		return
	}
	v.mu.Lock()
	for _, placeholder := range placeholders {
		v.reserved[placeholder] = struct{}{}
	}
	v.mu.Unlock()
}

func NewVault() *Vault {
	return &Vault{
		byOriginal:    make(map[string]string),
		byPlaceholder: make(map[string]string),
		reserved:      make(map[string]struct{}),
	}
}

func (v *Vault) Empty() bool {
	if v == nil {
		return true
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.byPlaceholder) == 0
}

// Clear 释放当前请求持有的全部明文、占位符和冲突检查文本。
func (v *Vault) Clear() {
	if v == nil {
		return
	}
	v.mu.Lock()
	clear(v.byOriginal)
	clear(v.byPlaceholder)
	clear(v.reserved)
	v.mu.Unlock()
}

// Mask 使用语义类型和加密随机标识替换命中区间。同一请求内相同明文复用
// 同一占位符，占位符不包含明文哈希、长度或尾号。
func (v *Vault) Mask(text string, matches []RedactionMatch) (string, error) {
	if v == nil {
		return "", fmt.Errorf("敏感信息映射仓未初始化")
	}
	if len(matches) == 0 {
		return text, nil
	}
	matches = append([]RedactionMatch(nil), matches...)
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Start != matches[j].Start {
			return matches[i].Start < matches[j].Start
		}
		return matches[i].End > matches[j].End
	})

	var builder strings.Builder
	builder.Grow(len(text))
	last := 0
	for _, match := range matches {
		if match.Start < last || match.Start < 0 || match.End > len(text) || match.Start >= match.End {
			return "", fmt.Errorf("敏感信息规则 %q 返回了无效或重叠区间", match.Rule)
		}
		if text[match.Start:match.End] != match.Original {
			return "", fmt.Errorf("敏感信息规则 %q 返回的原文与命中区间不一致", match.Rule)
		}
		placeholder, err := v.placeholder(match.Type, match.Original, text)
		if err != nil {
			return "", err
		}
		builder.WriteString(text[last:match.Start])
		builder.WriteString(placeholder)
		last = match.End
	}
	builder.WriteString(text[last:])
	return builder.String(), nil
}

func (v *Vault) placeholder(kind, original, source string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if placeholder, ok := v.byOriginal[original]; ok {
		return placeholder, nil
	}
	kind = normalizePlaceholderType(kind)
	for attempts := 0; attempts < 16; attempts++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("生成敏感信息占位符失败: %w", err)
		}
		token := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random)
		placeholder := "{{" + kind + "_" + token + "}}"
		if strings.Contains(source, placeholder) {
			continue
		}
		if _, exists := v.reserved[placeholder]; exists {
			continue
		}
		if _, exists := v.byPlaceholder[placeholder]; exists {
			continue
		}
		v.byOriginal[original] = placeholder
		v.byPlaceholder[placeholder] = original
		return placeholder, nil
	}
	return "", fmt.Errorf("生成无冲突的敏感信息占位符失败")
}

func normalizePlaceholderType(kind string) string {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	var builder strings.Builder
	for _, current := range kind {
		if current >= 'A' && current <= 'Z' || current >= '0' && current <= '9' {
			builder.WriteRune(current)
		}
	}
	if builder.Len() == 0 {
		return "SENSITIVE"
	}
	return builder.String()
}

// Restore 只还原当前请求映射表中的完整占位符。
func (v *Vault) Restore(text string) string {
	if v == nil || text == "" {
		return text
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.byPlaceholder) == 0 {
		return text
	}
	for placeholder, original := range v.byPlaceholder {
		text = strings.ReplaceAll(text, placeholder, original)
	}
	return text
}

// MaskKnown 将文本中已经登记到当前请求 Vault 的敏感原文替换为对应占位符。
// 它用于对协议解析阶段提前提取的观测文本应用与请求体一致的脱敏映射。
func (v *Vault) MaskKnown(text string) string {
	if v == nil || text == "" {
		return text
	}
	v.mu.RLock()
	pairs := make([][2]string, 0, len(v.byOriginal))
	for original, placeholder := range v.byOriginal {
		pairs = append(pairs, [2]string{original, placeholder})
	}
	v.mu.RUnlock()
	sort.Slice(pairs, func(i, j int) bool {
		return len(pairs[i][0]) > len(pairs[j][0])
	})
	for _, pair := range pairs {
		text = strings.ReplaceAll(text, pair[0], pair[1])
	}
	return text
}

func (v *Vault) mappings() map[string]string {
	result := make(map[string]string)
	if v == nil {
		return result
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	for placeholder, original := range v.byPlaceholder {
		result[placeholder] = original
	}
	return result
}

// StreamRestorer 对一个逻辑文本通道执行增量精确还原。它只暂存可能成为
// 当前 Vault 占位符的尾部前缀，普通文本立即返回。
type StreamRestorer struct {
	vault   *Vault
	pending string
}

func NewStreamRestorer(vault *Vault) *StreamRestorer {
	return &StreamRestorer{vault: vault}
}

func (r *StreamRestorer) Feed(fragment string) string {
	if r == nil || r.vault == nil || r.vault.Empty() {
		return fragment
	}
	input := r.pending + fragment
	r.pending = ""
	mappings := r.vault.mappings()
	var output strings.Builder
	for len(input) > 0 {
		start := strings.IndexByte(input, '{')
		if start < 0 {
			output.WriteString(input)
			break
		}
		output.WriteString(input[:start])
		input = input[start:]
		matched := false
		for placeholder, original := range mappings {
			if strings.HasPrefix(input, placeholder) {
				output.WriteString(original)
				input = input[len(placeholder):]
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		possiblePrefix := false
		for placeholder := range mappings {
			if strings.HasPrefix(placeholder, input) {
				possiblePrefix = true
				break
			}
		}
		if possiblePrefix {
			r.pending = input
			break
		}
		output.WriteByte(input[0])
		input = input[1:]
	}
	return output.String()
}

func (r *StreamRestorer) Pending() bool {
	return r != nil && r.pending != ""
}

func (r *StreamRestorer) Flush() string {
	if r == nil {
		return ""
	}
	pending := r.pending
	r.pending = ""
	return pending
}
