package modelaudit

import (
	"encoding/json"
	"strings"
)

const (
	AuditModAnalysisInputSchema  = "audit.mod-analysis-input.v1"
	AuditModAnalysisResultSchema = "audit.mod-analysis-result.v1"
)

type AuditModAnalysisSample struct {
	SampleID         string            `json:"sampleId"`
	Ordinal          int               `json:"ordinal"`
	Status           ExecutionStatus   `json:"status"`
	ReturnedModel    string            `json:"returnedModel,omitempty"`
	RawResponse      string            `json:"rawResponse,omitempty"`
	StructuredOutput json.RawMessage   `json:"structuredOutput,omitempty"`
	ParsedStatus     string            `json:"parsedStatus"`
	ParsedValues     []json.RawMessage `json:"parsedValues,omitempty"`
	ParseError       string            `json:"parseError,omitempty"`
	Usage            Usage             `json:"usage"`
	Timing           ExecutionTiming   `json:"timing"`
}

type AuditModAnalysisInput struct {
	Schema     string                   `json:"schema"`
	RunID      string                   `json:"runId"`
	JobID      string                   `json:"jobId"`
	TargetID   string                   `json:"targetId"`
	Target     TargetSnapshot           `json:"target"`
	Mod        AuditModReference        `json:"mod"`
	Samples    []AuditModAnalysisSample `json:"samples"`
	Summary    AuditModSummary          `json:"summary"`
	RuleResult AuditModRuleResult       `json:"ruleResult"`
}

type AuditModAnalysisResult struct {
	Schema     string          `json:"schema"`
	Verdict    string          `json:"verdict"`
	Confidence float64         `json:"confidence"`
	Summary    string          `json:"summary"`
	Metrics    json.RawMessage `json:"metrics,omitempty"`
	Evidence   []string        `json:"evidence,omitempty"`
	Warnings   []string        `json:"warnings,omitempty"`
}

func (r AuditModAnalysisResult) Validate() error {
	if r.Schema != AuditModAnalysisResultSchema || strings.TrimSpace(r.Verdict) == "" || !unitInterval(r.Confidence) || strings.TrimSpace(r.Summary) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析结果 Schema、结论、置信度或摘要无效")
	}
	if len(r.Metrics) > 0 {
		canonical, _, err := canonicalJSONObject(r.Metrics)
		if err != nil || string(canonical) != string(r.Metrics) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析 metrics 必须是规范化 JSON 对象", err)
		}
	}
	for _, value := range append(append([]string(nil), r.Evidence...), r.Warnings...) {
		if strings.TrimSpace(value) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析证据或警告包含空文本")
		}
	}
	return nil
}

func buildAuditModAnalysisInput(
	run AuditRun,
	targetID string,
	target TargetSnapshot,
	mod AuditModReference,
	samples []StrategySample,
	features AuditModEvaluationFeatures,
) (json.RawMessage, error) {
	parsedByID := make(map[string]AuditModParsedRequest, len(features.ParsedRequests))
	for _, parsed := range features.ParsedRequests {
		parsedByID[parsed.SampleID] = parsed
	}
	analysisSamples := make([]AuditModAnalysisSample, 0, len(samples))
	for _, sample := range samples {
		parsed := parsedByID[sample.SampleID]
		value := AuditModAnalysisSample{
			SampleID: sample.SampleID, Ordinal: sample.Ordinal, ParsedStatus: parsed.Status,
			ParsedValues: append([]json.RawMessage(nil), parsed.Values...), ParseError: parsed.Error,
		}
		if sample.Execution != nil {
			value.Status = sample.Execution.Status
			value.ReturnedModel = sample.Execution.ReturnedModel
			value.RawResponse = sample.Execution.Text
			value.StructuredOutput = append(json.RawMessage(nil), sample.Execution.StructuredOutput...)
			value.Usage = sample.Execution.Usage
			value.Timing = sample.Execution.Timing
		}
		analysisSamples = append(analysisSamples, value)
	}
	input := AuditModAnalysisInput{
		Schema: AuditModAnalysisInputSchema, RunID: run.ID, JobID: run.JobID, TargetID: targetID,
		Target: target, Mod: mod, Samples: analysisSamples, Summary: features.Summary, RuleResult: features.RuleResult,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 分析输入失败", err)
	}
	canonical, _, err := canonicalJSONObject(encoded)
	return canonical, err
}

func decodeAuditModAnalysisResult(raw json.RawMessage) (json.RawMessage, AuditModAnalysisResult, error) {
	canonical, _, err := canonicalJSONObject(raw)
	if err != nil {
		return nil, AuditModAnalysisResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析输出必须是 JSON 对象", err)
	}
	var result AuditModAnalysisResult
	if err := decodeStrictObject(canonical, &result); err != nil {
		return nil, AuditModAnalysisResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析输出字段无效", err)
	}
	if len(result.Metrics) > 0 {
		metrics, _, err := canonicalJSONObject(result.Metrics)
		if err != nil {
			return nil, AuditModAnalysisResult{}, err
		}
		result.Metrics = metrics
		canonical, _, err = canonicalJSONObject(mustMarshalAuditModAnalysisResult(result))
		if err != nil {
			return nil, AuditModAnalysisResult{}, err
		}
	}
	if err := result.Validate(); err != nil {
		return nil, AuditModAnalysisResult{}, err
	}
	return canonical, result, nil
}

func mustMarshalAuditModAnalysisResult(result AuditModAnalysisResult) json.RawMessage {
	encoded, _ := json.Marshal(result)
	return encoded
}
