package modelaudit

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

type AuditModParsedRequest struct {
	SampleID string            `json:"sampleId"`
	Ordinal  int               `json:"ordinal"`
	Status   string            `json:"status"`
	Values   []json.RawMessage `json:"values,omitempty"`
	Error    string            `json:"error,omitempty"`
}

type AuditModBucketSummary struct {
	Count int     `json:"count"`
	Rate  float64 `json:"rate"`
}

type AuditModNumericSummary struct {
	Count             int     `json:"count"`
	Minimum           float64 `json:"minimum"`
	Maximum           float64 `json:"maximum"`
	Mean              float64 `json:"mean"`
	StandardDeviation float64 `json:"standardDeviation"`
	P50               float64 `json:"p50"`
	P90               float64 `json:"p90"`
}

type AuditModSummary struct {
	SamplingMode          AuditModSamplingMode             `json:"samplingMode"`
	RequestedObservations int                              `json:"requestedObservations"`
	RequestCount          int                              `json:"requestCount"`
	ParsedRequestCount    int                              `json:"parsedRequestCount"`
	FailedRequestCount    int                              `json:"failedRequestCount"`
	ObservationCount      int                              `json:"observationCount"`
	Values                []json.RawMessage                `json:"values"`
	Frequencies           map[string]int                   `json:"frequencies,omitempty"`
	Histogram             map[string]AuditModBucketSummary `json:"histogram,omitempty"`
	Numeric               *AuditModNumericSummary          `json:"numeric,omitempty"`
}

type AuditModEvaluationFeatures struct {
	Mod            AuditModReference       `json:"mod"`
	Summary        AuditModSummary         `json:"summary"`
	RuleResult     AuditModRuleResult      `json:"ruleResult"`
	ParsedRequests []AuditModParsedRequest `json:"parsedRequests"`
}

func evaluateDeclarativeAuditMod(
	descriptor StrategyDescriptor,
	bundle AuditModBundle,
	rules AuditModRuleSet,
	config AuditModStrategyConfig,
	input StrategyEvaluationInput,
) (StrategyEvaluation, error) {
	parsedRequests := make([]AuditModParsedRequest, 0, len(input.Samples))
	values := make([]json.RawMessage, 0, config.SampleCount)
	parsedRequestCount := 0
	for _, sample := range input.Samples {
		if err := sample.Validate(); err != nil {
			return StrategyEvaluation{}, err
		}
		parsed := AuditModParsedRequest{SampleID: sample.SampleID, Ordinal: sample.Ordinal}
		if sample.Execution == nil || sample.Execution.Status != StatusCompleted {
			parsed.Status = "execution_failed"
			parsed.Error = "请求没有成功完成"
			parsedRequests = append(parsedRequests, parsed)
			continue
		}
		value, err := parseAuditModExecution(bundle.Manifest.Parser, *sample.Execution)
		if err != nil {
			parsed.Status = "parse_failed"
			parsed.Error = err.Error()
			parsedRequests = append(parsedRequests, parsed)
			continue
		}
		requestValues, err := auditModObservationValues(bundle.Manifest.Sampling.Mode, value)
		if err != nil {
			parsed.Status = "parse_failed"
			parsed.Error = err.Error()
			parsedRequests = append(parsedRequests, parsed)
			continue
		}
		parsed.Status = "parsed"
		parsed.Values = requestValues
		parsedRequestCount++
		parsedRequests = append(parsedRequests, parsed)
		values = append(values, requestValues...)
	}
	if len(values) > config.SampleCount {
		values = values[:config.SampleCount]
	}
	summary, err := aggregateAuditModValues(bundle.Manifest, config, len(input.Samples), parsedRequestCount, values)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 统计摘要失败", err)
	}
	ruleResult, err := rules.Evaluate(summaryJSON)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	featuresValue := AuditModEvaluationFeatures{
		Mod: bundle.Reference, Summary: summary, RuleResult: ruleResult, ParsedRequests: parsedRequests,
	}
	featuresJSON, err := json.Marshal(featuresValue)
	if err != nil {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 评估特征失败", err)
	}
	features, _, err := canonicalJSONObject(featuresJSON)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	signal := IdentitySignal{
		ID: descriptor.Ref.ID, Kind: SignalCustomMod, CorrelationGroup: "mod." + descriptor.Ref.ID,
		SampleCount: len(values), Reliability: ruleResult.Confidence, Features: features,
	}
	if len(values) == 0 {
		signal.Status = SignalInsufficientEvidence
		signal.Reason = "Mod 没有产生可解析的观测值"
	} else {
		score := ruleResult.Score
		signal.Status = SignalObserved
		signal.Score = &score
	}
	if err := signal.Validate(); err != nil {
		return StrategyEvaluation{}, err
	}
	metrics, err := json.Marshal(struct {
		Signal IdentitySignal `json:"signal"`
	}{Signal: signal})
	if err != nil {
		return StrategyEvaluation{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 身份信号失败", err)
	}
	metrics, _, err = canonicalJSONObject(metrics)
	if err != nil {
		return StrategyEvaluation{}, err
	}
	evaluation := StrategyEvaluation{
		Strategy: descriptor.Ref, Formal: false, SampleCount: len(input.Samples), ApplicableSampleCount: parsedRequestCount,
		Metrics: metrics,
	}
	if len(values) == 0 {
		evaluation.Status = EvaluationInsufficientEvidence
		evaluation.Reason = signal.Reason
	} else {
		evaluation.Status = EvaluationCompleted
		feature, err := NewFeatureSnapshot(FeatureSchemaRef{ID: "audit.mod.features", Version: "1.0.0"}, features)
		if err != nil {
			return StrategyEvaluation{}, err
		}
		evaluation.Features = []FeatureSnapshot{feature}
	}
	failedRequests := len(input.Samples) - parsedRequestCount
	if failedRequests > 0 {
		evaluation.AlternativeExplanations = []string{fmt.Sprintf("%d 个请求执行或解析失败，统计仅使用有效观测", failedRequests)}
	}
	if err := evaluation.Validate(); err != nil {
		return StrategyEvaluation{}, err
	}
	return evaluation, nil
}

func parseAuditModExecution(parser AuditModParser, execution ExecutionResult) (any, error) {
	text := strings.TrimSpace(execution.Text)
	switch parser.Type {
	case AuditModParserText:
		if text == "" {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "文本回答为空")
		}
		return text, nil
	case AuditModParserNumber:
		value, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "回答不是有效数字", err)
		}
		if parser.Minimum != nil && value < *parser.Minimum || parser.Maximum != nil && value > *parser.Maximum {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "数字回答超出 Mod 允许范围")
		}
		return value, nil
	case AuditModParserJSON:
		raw := []byte(text)
		if len(execution.StructuredOutput) > 0 {
			raw = execution.StructuredOutput
		}
		if !json.Valid(raw) {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "回答不是有效 JSON")
		}
		if strings.TrimSpace(parser.JSONPath) != "" {
			selected := gjson.GetBytes(raw, parser.JSONPath)
			if !selected.Exists() {
				return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "JSON 回答缺少配置的 jsonPath")
			}
			return selected.Value(), nil
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "解码 JSON 回答失败", err)
		}
		return value, nil
	case AuditModParserRegex:
		compiled := regexp.MustCompile(parser.Pattern)
		matches := compiled.FindStringSubmatch(text)
		if matches == nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "回答不匹配 Mod 正则表达式")
		}
		if parser.Group != "" {
			return matches[compiled.SubexpIndex(parser.Group)], nil
		}
		return matches[0], nil
	case AuditModParserEnum:
		for _, choice := range parser.Choices {
			if parser.IgnoreCase && strings.EqualFold(text, choice) || !parser.IgnoreCase && text == choice {
				return choice, nil
			}
		}
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "回答不在 Mod 枚举选项中")
	default:
		return nil, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "Mod 解析器类型不可用")
	}
}

func auditModObservationValues(mode AuditModSamplingMode, value any) ([]json.RawMessage, error) {
	values := []any{value}
	if mode == AuditModSamplingBatch {
		array, ok := value.([]any)
		if !ok {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "batch Mod 的 JSON 结果不是数组")
		}
		values = array
	}
	result := make([]json.RawMessage, 0, len(values))
	for _, item := range values {
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 观测值失败", err)
		}
		result = append(result, encoded)
	}
	return result, nil
}

func aggregateAuditModValues(
	manifest AuditModManifest,
	config AuditModStrategyConfig,
	requestCount int,
	parsedRequestCount int,
	values []json.RawMessage,
) (AuditModSummary, error) {
	summary := AuditModSummary{
		SamplingMode: manifest.Sampling.Mode, RequestedObservations: config.SampleCount, RequestCount: requestCount,
		ParsedRequestCount: parsedRequestCount, FailedRequestCount: requestCount - parsedRequestCount,
		ObservationCount: len(values), Values: append([]json.RawMessage(nil), values...),
	}
	switch manifest.Aggregation.Type {
	case AuditModAggregationValues:
		return summary, nil
	case AuditModAggregationFrequency:
		summary.Frequencies = make(map[string]int)
		for _, raw := range values {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return AuditModSummary{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码 Mod 频次观测值失败", err)
			}
			key := fmt.Sprint(value)
			if value == nil {
				key = "null"
			}
			summary.Frequencies[key]++
		}
		return summary, nil
	case AuditModAggregationHistogram, AuditModAggregationNumeric:
		numbers, err := auditModNumericValues(values)
		if err != nil {
			return AuditModSummary{}, err
		}
		if len(numbers) == 0 {
			return summary, nil
		}
		summary.Numeric = auditModNumericStatistics(numbers)
		if manifest.Aggregation.Type == AuditModAggregationHistogram {
			summary.Histogram = make(map[string]AuditModBucketSummary, len(manifest.Aggregation.Buckets))
			for _, bucket := range manifest.Aggregation.Buckets {
				count := 0
				for _, number := range numbers {
					if number >= bucket.Minimum && number <= bucket.Maximum {
						count++
					}
				}
				summary.Histogram[bucket.ID] = AuditModBucketSummary{Count: count, Rate: float64(count) / float64(len(numbers))}
			}
		}
		return summary, nil
	default:
		return AuditModSummary{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "Mod 聚合器不可用")
	}
}

func auditModNumericValues(values []json.RawMessage) ([]float64, error) {
	numbers := make([]float64, 0, len(values))
	for _, raw := range values {
		var number float64
		if err := json.Unmarshal(raw, &number); err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "数值聚合器收到非数字观测值", err)
		}
		numbers = append(numbers, number)
	}
	return numbers, nil
}

func auditModNumericStatistics(values []float64) *AuditModNumericSummary {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, value := range sorted {
		sum += value
	}
	mean := sum / float64(len(sorted))
	variance := 0.0
	for _, value := range sorted {
		delta := value - mean
		variance += delta * delta
	}
	variance /= float64(len(sorted))
	return &AuditModNumericSummary{
		Count: len(sorted), Minimum: sorted[0], Maximum: sorted[len(sorted)-1], Mean: mean,
		StandardDeviation: math.Sqrt(variance), P50: auditModQuantile(sorted, 0.5), P90: auditModQuantile(sorted, 0.9),
	}
}

func auditModQuantile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := quantile * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}
