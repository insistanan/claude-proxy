package modelaudit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const AuditModSchemaVersion = 1

const maximumAuditSampleDelayMillis = int64(^uint64(0)>>1) / int64(time.Millisecond)

type AuditModAnalysisMode string

const (
	AuditModAnalysisLLM    AuditModAnalysisMode = "llm"
	AuditModAnalysisPython AuditModAnalysisMode = "python"
	AuditModAnalysisManual AuditModAnalysisMode = "manual"
)

func (m AuditModAnalysisMode) Valid() bool {
	return m == AuditModAnalysisLLM || m == AuditModAnalysisPython || m == AuditModAnalysisManual
}

type AuditModSamplingMode string

const (
	AuditModSamplingIndependent AuditModSamplingMode = "independent"
	AuditModSamplingBatch       AuditModSamplingMode = "batch"
)

func (m AuditModSamplingMode) Valid() bool {
	return m == AuditModSamplingIndependent || m == AuditModSamplingBatch
}

type AuditModParserType string

const (
	AuditModParserText   AuditModParserType = "text"
	AuditModParserNumber AuditModParserType = "number"
	AuditModParserJSON   AuditModParserType = "json"
	AuditModParserRegex  AuditModParserType = "regex"
	AuditModParserEnum   AuditModParserType = "enum"
)

func (t AuditModParserType) Valid() bool {
	return t == AuditModParserText || t == AuditModParserNumber || t == AuditModParserJSON ||
		t == AuditModParserRegex || t == AuditModParserEnum
}

type AuditModAggregationType string

const (
	AuditModAggregationValues    AuditModAggregationType = "values"
	AuditModAggregationFrequency AuditModAggregationType = "frequency"
	AuditModAggregationHistogram AuditModAggregationType = "histogram"
	AuditModAggregationNumeric   AuditModAggregationType = "numeric"
)

func (t AuditModAggregationType) Valid() bool {
	return t == AuditModAggregationValues || t == AuditModAggregationFrequency ||
		t == AuditModAggregationHistogram || t == AuditModAggregationNumeric
}

type AuditModSampling struct {
	Mode               AuditModSamplingMode `json:"mode"`
	DefaultSampleCount int                  `json:"defaultSampleCount"`
	MaximumSamples     int                  `json:"maximumSamples"`
	SampleIntervalMs   int64                `json:"sampleIntervalMs,omitempty"`
	MaxOutputTokens    int                  `json:"maxOutputTokens"`
}

type AuditModParser struct {
	Type       AuditModParserType `json:"type"`
	JSONPath   string             `json:"jsonPath,omitempty"`
	Pattern    string             `json:"pattern,omitempty"`
	Group      string             `json:"group,omitempty"`
	Minimum    *float64           `json:"minimum,omitempty"`
	Maximum    *float64           `json:"maximum,omitempty"`
	Choices    []string           `json:"choices,omitempty"`
	IgnoreCase bool               `json:"ignoreCase,omitempty"`
}

type AuditModHistogramBucket struct {
	ID      string  `json:"id"`
	Minimum float64 `json:"minimum"`
	Maximum float64 `json:"maximum"`
}

type AuditModAggregation struct {
	Type    AuditModAggregationType   `json:"type"`
	Buckets []AuditModHistogramBucket `json:"buckets,omitempty"`
}

type AuditModPythonAnalysis struct {
	Entry          string   `json:"entry"`
	Interpreter    string   `json:"interpreter,omitempty"`
	Arguments      []string `json:"arguments,omitempty"`
	TimeoutSeconds int      `json:"timeoutSeconds,omitempty"`
}

type AuditModManualAnalysis struct {
	Runtime        string `json:"runtime"`
	CommandExample string `json:"commandExample"`
}

type AuditModAnalysis struct {
	Mode             AuditModAnalysisMode    `json:"mode"`
	PromptFile       string                  `json:"promptFile,omitempty"`
	MethodFile       string                  `json:"methodFile"`
	ResultSchemaFile string                  `json:"resultSchemaFile"`
	Python           *AuditModPythonAnalysis `json:"python,omitempty"`
	Manual           *AuditModManualAnalysis `json:"manual,omitempty"`
}

type AuditModManifest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	ID            string              `json:"id"`
	Version       string              `json:"version"`
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	ProbeFile     string              `json:"probeFile"`
	RulesFile     string              `json:"rulesFile"`
	Sampling      AuditModSampling    `json:"sampling"`
	Parser        AuditModParser      `json:"parser"`
	Aggregation   AuditModAggregation `json:"aggregation"`
	Analysis      AuditModAnalysis    `json:"analysis"`
}

type AuditModReference struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	ContentSHA256 string `json:"contentSha256"`
}

func (r AuditModReference) Validate() error {
	if !stableIDPattern.MatchString(r.ID) || !semanticVersionPattern.MatchString(r.Version) || !validSHA256(r.ContentSHA256) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 引用无效")
	}
	return nil
}

type AuditModDescriptor struct {
	Reference    AuditModReference    `json:"reference"`
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	AnalysisMode AuditModAnalysisMode `json:"analysisMode"`
	SourcePath   string               `json:"sourcePath"`
	SnapshotPath string               `json:"snapshotPath"`
	LoadedAt     time.Time            `json:"loadedAt"`
}

type AuditModLoadIssue struct {
	Directory string `json:"directory"`
	ModID     string `json:"modId,omitempty"`
	Message   string `json:"message"`
}

type AuditModCatalogSnapshot struct {
	Mods     []AuditModDescriptor `json:"mods"`
	Issues   []AuditModLoadIssue  `json:"issues"`
	LoadedAt time.Time            `json:"loadedAt"`
}

type AuditModBundle struct {
	Manifest     AuditModManifest
	Reference    AuditModReference
	SourcePath   string
	SnapshotPath string
	Files        map[string][]byte
	LoadedAt     time.Time
}

func decodeAuditModManifest(content []byte) (AuditModManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var manifest AuditModManifest
	if err := decoder.Decode(&manifest); err != nil {
		return AuditModManifest{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "解析 Mod manifest.json 失败", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return AuditModManifest{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod manifest.json 只能包含一个 JSON 对象")
	}
	if err := manifest.Validate(); err != nil {
		return AuditModManifest{}, err
	}
	return manifest, nil
}

func (m AuditModManifest) Validate() error {
	if m.SchemaVersion != AuditModSchemaVersion {
		return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, fmt.Sprintf("不支持 Mod schemaVersion %d", m.SchemaVersion))
	}
	if !stableIDPattern.MatchString(m.ID) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod ID 无效")
	}
	if !semanticVersionPattern.MatchString(m.Version) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 版本必须是语义版本")
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Description) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 缺少名称或说明")
	}
	if err := requireAuditModMarkdownPath(m.ProbeFile, "探针提示词"); err != nil {
		return err
	}
	if _, err := normalizeAuditModRelativePath(m.RulesFile, "规则文件"); err != nil {
		return err
	}
	if !m.Sampling.Mode.Valid() || m.Sampling.DefaultSampleCount <= 0 || m.Sampling.MaximumSamples <= 0 ||
		m.Sampling.DefaultSampleCount > m.Sampling.MaximumSamples || m.Sampling.SampleIntervalMs < 0 ||
		m.Sampling.SampleIntervalMs > maximumAuditSampleDelayMillis || m.Sampling.MaxOutputTokens <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 采样配置无效")
	}
	if m.Sampling.Mode == AuditModSamplingBatch && m.Parser.Type != AuditModParserJSON {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "batch 采样必须使用 JSON 解析器读取数组")
	}
	if err := m.Parser.validate(); err != nil {
		return err
	}
	if err := m.Aggregation.validate(); err != nil {
		return err
	}
	return m.Analysis.validate()
}

func (p AuditModParser) validate() error {
	if !p.Type.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 解析器类型无效")
	}
	if p.Minimum != nil && p.Maximum != nil && *p.Minimum > *p.Maximum {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 数字解析范围无效")
	}
	if p.Type == AuditModParserRegex && strings.TrimSpace(p.Pattern) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正则解析器缺少 pattern")
	}
	if p.Type == AuditModParserRegex {
		compiled, err := regexp.Compile(p.Pattern)
		if err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正则解析器 pattern 无效", err)
		}
		if p.Group != "" && compiled.SubexpIndex(p.Group) < 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正则解析器找不到指定命名分组")
		}
	}
	if p.Type == AuditModParserEnum {
		if len(p.Choices) == 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "枚举解析器缺少 choices")
		}
		seen := make(map[string]struct{}, len(p.Choices))
		for _, choice := range p.Choices {
			value := strings.TrimSpace(choice)
			if value == "" {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "枚举解析器包含空选项")
			}
			key := value
			if p.IgnoreCase {
				key = strings.ToLower(key)
			}
			if _, duplicate := seen[key]; duplicate {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "枚举解析器包含重复选项")
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func (a AuditModAggregation) validate() error {
	if !a.Type.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 聚合器类型无效")
	}
	if a.Type != AuditModAggregationHistogram {
		if len(a.Buckets) > 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "非直方图聚合器不能配置 buckets")
		}
		return nil
	}
	if len(a.Buckets) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "直方图聚合器缺少 buckets")
	}
	seen := make(map[string]struct{}, len(a.Buckets))
	for _, bucket := range a.Buckets {
		if !stableIDPattern.MatchString(bucket.ID) || bucket.Minimum > bucket.Maximum {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "直方图区间无效")
		}
		if _, duplicate := seen[bucket.ID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "直方图区间 ID 重复")
		}
		seen[bucket.ID] = struct{}{}
	}
	return nil
}

func (a AuditModAnalysis) validate() error {
	if !a.Mode.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析模式无效")
	}
	if err := requireAuditModMarkdownPath(a.MethodFile, "分析方法"); err != nil {
		return err
	}
	if _, err := normalizeAuditModRelativePath(a.ResultSchemaFile, "分析结果 Schema"); err != nil {
		return err
	}
	switch a.Mode {
	case AuditModAnalysisLLM:
		if err := requireAuditModMarkdownPath(a.PromptFile, "分析提示词"); err != nil {
			return err
		}
		if a.Python != nil || a.Manual != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "LLM 分析不能混入脚本配置")
		}
	case AuditModAnalysisPython:
		if a.PromptFile != "" || a.Python == nil || a.Manual != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Python 分析配置不完整或混入其他模式")
		}
		entry, err := normalizeAuditModRelativePath(a.Python.Entry, "Python 入口")
		if err != nil {
			return err
		}
		if !strings.EqualFold(filepath.Ext(entry), ".py") || a.Python.TimeoutSeconds < 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Python 分析入口或超时无效")
		}
		if len(a.Python.Arguments) > 0 {
			joined := strings.Join(a.Python.Arguments, "\x00")
			if !strings.Contains(joined, "{input}") || !strings.Contains(joined, "{output}") {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "自定义 Python 参数必须包含 {input} 和 {output}")
			}
		}
	case AuditModAnalysisManual:
		if a.PromptFile != "" || a.Python != nil || a.Manual == nil || strings.TrimSpace(a.Manual.Runtime) == "" || strings.TrimSpace(a.Manual.CommandExample) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "手动分析配置不完整或混入其他模式")
		}
	}
	return nil
}

func (m AuditModManifest) referencedFiles() []string {
	paths := []string{m.ProbeFile, m.RulesFile, m.Analysis.MethodFile, m.Analysis.ResultSchemaFile}
	switch m.Analysis.Mode {
	case AuditModAnalysisLLM:
		paths = append(paths, m.Analysis.PromptFile)
	case AuditModAnalysisPython:
		paths = append(paths, m.Analysis.Python.Entry)
	}
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		normalized, err := normalizeAuditModRelativePath(value, "Mod 文件")
		if err != nil {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}

func requireAuditModMarkdownPath(value, label string) error {
	normalized, err := normalizeAuditModRelativePath(value, label)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Ext(normalized), ".md") {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, label+"必须使用 .md 文件")
	}
	return nil
}

func normalizeAuditModRelativePath(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, label+"路径无效")
	}
	cleaned := filepath.Clean(filepath.FromSlash(value))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, label+"不能离开 Mod 目录")
	}
	return filepath.ToSlash(cleaned), nil
}
