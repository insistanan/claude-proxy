package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/pelletier/go-toml/v2"
)

// Codex 模型目录（models.json）管理：
// - 预设模板 + 少量可调字段生成模型元数据条目（与 DeepSeek 官方 Codex 接入文档的字段保持一致）
// - 按 slug upsert / delete，保留用户手工写入的未知字段与条目
// - setDefault 同步 config.toml 根级 model / model_catalog_json / model_reasoning_effort
//
// 注意：刻意不写 model_messages / base_instructions 等巨型提示词字段，
// 省略时 Codex 会回退到客户端内置默认指令，避免在代码里硬编码大段提示词。

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

type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// codexModelTemplate 预设模板，同时作为 API 返回给前端的模板视图。
type codexModelTemplate struct {
	ID                    string                `json:"id"`
	Name                  string                `json:"name"`
	Description           string                `json:"description"`
	SupportsImage         bool                  `json:"supportsImage"`
	ContextWindow         int64                 `json:"contextWindow"`
	DefaultReasoningLevel string                `json:"defaultReasoningLevel"`
	ReasoningLevels       []codexReasoningLevel `json:"reasoningLevels"`
	SupportsSearchTool    bool                  `json:"supportsSearchTool"`
	Priority              int                   `json:"priority"`
}

// codexStandardReasoningLevels 与 DeepSeek 官方 models.json 中的 supported_reasoning_levels 一致。
var codexStandardReasoningLevels = []codexReasoningLevel{
	{Effort: "low", Description: "Fast responses with lighter reasoning"},
	{Effort: "high", Description: "Extra high reasoning depth for complex problems"},
	{Effort: "max", Description: "Maximum reasoning depth for the hardest problems"},
}

var codexModelTemplates = []codexModelTemplate{
	{
		ID:                    "deepseek-flash",
		Name:                  "DeepSeek-Flash",
		Description:           "Latest frontier agentic coding model with image input.",
		SupportsImage:         true,
		ContextWindow:         1048576,
		DefaultReasoningLevel: "high",
		ReasoningLevels:       codexStandardReasoningLevels,
		SupportsSearchTool:    true,
		Priority:              1,
	},
	{
		ID:                    "deepseek-v4-pro",
		Name:                  "DeepSeek-V4-Pro",
		Description:           "Most capable frontier agentic coding model.",
		SupportsImage:         false,
		ContextWindow:         1048576,
		DefaultReasoningLevel: "high",
		ReasoningLevels:       codexStandardReasoningLevels,
		SupportsSearchTool:    false,
		Priority:              2,
	},
	{
		ID:                    "generic",
		Name:                  "通用 OpenAI 兼容模型",
		Description:           "通用 Responses 协议模型模板，按需调整上下文窗口等字段。",
		SupportsImage:         false,
		ContextWindow:         128000,
		DefaultReasoningLevel: "high",
		ReasoningLevels:       codexStandardReasoningLevels,
		SupportsSearchTool:    false,
		Priority:              99,
	},
}

func codexModelTemplateByID(id string) (codexModelTemplate, bool) {
	for _, template := range codexModelTemplates {
		if template.ID == id {
			return template, true
		}
	}
	return codexModelTemplate{}, false
}

// codexCatalogModelEntry 是本功能管理的 models.json 条目字段全集
// （省略 model_messages / base_instructions）。
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

func (meta codexModelEntryMeta) view() codexModelView {
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
}

type saveCodexModelCatalogRequest struct {
	Action                string `json:"action"`
	Template              string `json:"template"`
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
	Template              string
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

		switch req.Action {
		case "upsert":
			updated, err := upsertCodexModelCatalog(modelsPath, codexModelUpsertInput{
				Template:              strings.TrimSpace(req.Template),
				Slug:                  strings.TrimSpace(req.Slug),
				DisplayName:           strings.TrimSpace(req.DisplayName),
				Description:           strings.TrimSpace(req.Description),
				SupportsImage:         req.SupportsImage,
				ContextWindow:         req.ContextWindow,
				DefaultReasoningLevel: strings.TrimSpace(req.DefaultReasoningLevel),
			})
			if err != nil {
				respondCodexModelError(c, err)
				return
			}
			if err := writeCodexFile(modelsPath, updated.previous, updated.data, ".codex-models-*.tmp"); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("保存 models.json 失败: %v", err)})
				return
			}
		case "delete":
			updated, err := deleteCodexModelCatalog(modelsPath, strings.TrimSpace(req.Slug))
			if err != nil {
				respondCodexModelError(c, err)
				return
			}
			if err := writeCodexFile(modelsPath, updated.previous, updated.data, ".codex-models-*.tmp"); err != nil {
				c.JSON(500, gin.H{"error": fmt.Sprintf("保存 models.json 失败: %v", err)})
				return
			}
		case "setDefault":
			if err := setCodexDefaultModel(configPath, modelsPath, strings.TrimSpace(req.Slug), req.ReasoningEffort, req.ClearReasoningEffort); err != nil {
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

func upsertCodexModelCatalog(modelsPath string, input codexModelUpsertInput) (codexCatalogWriteResult, error) {
	catalog, err := readCodexModelCatalog(modelsPath)
	if err != nil {
		return codexCatalogWriteResult{}, err
	}
	if err := catalog.upsertModel(input); err != nil {
		return codexCatalogWriteResult{}, err
	}
	encoded, err := catalog.encode()
	if err != nil {
		return codexCatalogWriteResult{}, fmt.Errorf("编码 models.json 失败: %w", err)
	}
	return codexCatalogWriteResult{previous: catalog.raw, data: encoded}, nil
}

func deleteCodexModelCatalog(modelsPath, slug string) (codexCatalogWriteResult, error) {
	catalog, err := readCodexModelCatalog(modelsPath)
	if err != nil {
		return codexCatalogWriteResult{}, err
	}
	if err := catalog.deleteModel(slug); err != nil {
		return codexCatalogWriteResult{}, err
	}
	encoded, err := catalog.encode()
	if err != nil {
		return codexCatalogWriteResult{}, fmt.Errorf("编码 models.json 失败: %w", err)
	}
	return codexCatalogWriteResult{previous: catalog.raw, data: encoded}, nil
}

func setCodexDefaultModel(configPath, modelsPath, slug, reasoningEffort string, clearReasoningEffort bool) error {
	catalog, err := readCodexModelCatalog(modelsPath)
	if err != nil {
		return err
	}
	meta, err := catalog.findModel(slug)
	if err != nil {
		return err
	}
	if reasoningEffort != "" && !meta.hasEffort(reasoningEffort) {
		return newCodexInputError("模型 %s 不支持推理档位 %q", slug, reasoningEffort)
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
	catalogPath := modelsPath
	if updatedConfig, err = mergeCodexRootKey(updatedConfig, codexRootKeyModelCatalog, &catalogPath); err != nil {
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
		if req.Template != "" {
			if _, ok := codexModelTemplateByID(strings.TrimSpace(req.Template)); !ok {
				return newCodexInputError("未知的预设模板: %s", req.Template)
			}
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

func (catalog *codexModelCatalog) modelViews() []codexModelView {
	views := make([]codexModelView, 0, len(catalog.entries))
	for _, entry := range catalog.entries {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			continue
		}
		views = append(views, meta.view())
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Slug < views[j].Slug })
	return views
}

// upsertModel 按 slug 新增或更新条目：
// 新增时必须提供模板并生成完整条目；更新时仅覆盖可调字段，保留其余字段。
func (catalog *codexModelCatalog) upsertModel(input codexModelUpsertInput) error {
	for index, entry := range catalog.entries {
		meta, err := codexCatalogEntryMeta(entry)
		if err != nil {
			return err
		}
		if meta.Slug != input.Slug {
			continue
		}
		return catalog.updateModel(index, meta, input)
	}

	template, ok := codexModelTemplateByID(input.Template)
	if !ok {
		return newCodexInputError("新增模型必须选择预设模板")
	}
	entry := buildCodexModelEntry(template, input)
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

func (catalog *codexModelCatalog) updateModel(index int, meta codexModelEntryMeta, input codexModelUpsertInput) error {
	if input.DefaultReasoningLevel != "" && !meta.hasEffort(input.DefaultReasoningLevel) {
		return newCodexInputError("模型 %s 不支持推理档位 %q", input.Slug, input.DefaultReasoningLevel)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(catalog.entries[index], &fields); err != nil {
		return newCodexInputError("解析模型 %s 条目失败: %v", input.Slug, err)
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

func buildCodexModelEntry(template codexModelTemplate, input codexModelUpsertInput) codexCatalogModelEntry {
	displayName := input.DisplayName
	if displayName == "" {
		displayName = input.Slug
	}
	description := input.Description
	if description == "" {
		description = template.Description
	}
	reasoningLevel := input.DefaultReasoningLevel
	if reasoningLevel == "" {
		reasoningLevel = template.DefaultReasoningLevel
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
		Description:                    description,
		DefaultReasoningLevel:          reasoningLevel,
		SupportedReasoningLevels:       template.ReasoningLevels,
		ShellType:                      "shell_command",
		Visibility:                     "list",
		MinimalClientVersion:           "0.144.0",
		SupportedInAPI:                 true,
		AvailabilityNux:                nil,
		Upgrade:                        nil,
		Priority:                       template.Priority,
		ExperimentalSupportedTools:     []string{},
		SupportsSearchTool:             template.SupportsSearchTool,
		DefaultServiceTier:             nil,
		SupportsReasoningSummaries:     true,
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
