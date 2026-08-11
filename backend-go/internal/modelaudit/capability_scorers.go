package modelaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

type BuiltinCapabilityScorer struct {
	descriptor CapabilityScorerDescriptor
}

func NewBuiltinCapabilityScorer(kind CapabilityScorerKind) (*BuiltinCapabilityScorer, error) {
	id := ""
	switch kind {
	case CapabilityScorerExactMatch:
		id = "capability.scorer.exact-match"
	case CapabilityScorerSetMatch:
		id = "capability.scorer.set-match"
	case CapabilityScorerNumericTolerance:
		id = "capability.scorer.numeric-tolerance"
	case CapabilityScorerJSONStructure:
		id = "capability.scorer.json-structure"
	case CapabilityScorerToolCall:
		id = "capability.scorer.tool-call"
	case CapabilityScorerAssertions:
		id = "capability.scorer.assertions"
	default:
		return nil, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, fmt.Sprintf("没有内置能力判分器 %q", kind))
	}
	return &BuiltinCapabilityScorer{descriptor: CapabilityScorerDescriptor{
		Ref:  VersionedRef{ID: id, SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		Kind: kind, Objective: true, ConfigSchema: json.RawMessage(`{"type":"object"}`),
	}}, nil
}

func (s *BuiltinCapabilityScorer) Descriptor() CapabilityScorerDescriptor {
	if s == nil {
		return CapabilityScorerDescriptor{}
	}
	descriptor := s.descriptor
	descriptor.ConfigSchema = append(json.RawMessage(nil), descriptor.ConfigSchema...)
	return descriptor
}

func (s *BuiltinCapabilityScorer) ValidateConfig(raw json.RawMessage) error {
	if s == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力判分器未初始化")
	}
	switch s.descriptor.Kind {
	case CapabilityScorerExactMatch:
		_, err := decodeExactMatchConfig(raw)
		return err
	case CapabilityScorerSetMatch:
		_, err := decodeSetMatchConfig(raw)
		return err
	case CapabilityScorerNumericTolerance:
		_, err := decodeNumericToleranceConfig(raw)
		return err
	case CapabilityScorerJSONStructure:
		_, err := decodeJSONStructureConfig(raw)
		return err
	case CapabilityScorerToolCall:
		_, err := decodeToolCallConfig(raw)
		return err
	case CapabilityScorerAssertions:
		_, err := decodeAssertionsConfig(raw)
		return err
	default:
		return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力判分器类型不受支持")
	}
}

func (s *BuiltinCapabilityScorer) Score(ctx context.Context, input CapabilityScoringInput) (CapabilityScore, error) {
	if s == nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力判分器未初始化")
	}
	if err := ctx.Err(); err != nil {
		return CapabilityScore{}, err
	}
	if err := input.Validate(); err != nil {
		return CapabilityScore{}, err
	}
	if input.Task.Scorer != s.descriptor.Ref || input.Task.ScorerKind != s.descriptor.Kind {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务引用的判分器与实际实现不一致")
	}
	if err := s.ValidateConfig(input.Task.ScorerConfig); err != nil {
		return CapabilityScore{}, err
	}
	var score CapabilityScore
	var err error
	switch s.descriptor.Kind {
	case CapabilityScorerExactMatch:
		score, err = scoreExactMatch(input)
	case CapabilityScorerSetMatch:
		score, err = scoreSetMatch(input)
	case CapabilityScorerNumericTolerance:
		score, err = scoreNumericTolerance(input)
	case CapabilityScorerJSONStructure:
		score, err = scoreJSONStructure(input)
	case CapabilityScorerToolCall:
		score, err = scoreToolCalls(input)
	case CapabilityScorerAssertions:
		score, err = scoreExplicitAssertions(input)
	default:
		err = contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力判分器类型不受支持")
	}
	if err != nil {
		return CapabilityScore{}, err
	}
	if err := score.Validate(); err != nil {
		return CapabilityScore{}, err
	}
	return score, nil
}

type ExactMatchConfig struct {
	CaseSensitive      bool `json:"caseSensitive"`
	TrimSpace          bool `json:"trimSpace"`
	CollapseWhitespace bool `json:"collapseWhitespace"`
}

func decodeExactMatchConfig(raw json.RawMessage) (ExactMatchConfig, error) {
	var config ExactMatchConfig
	if err := decodeStrictObject(raw, &config); err != nil {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "精确匹配配置无效", err)
	}
	return config, nil
}

func scoreExactMatch(input CapabilityScoringInput) (CapabilityScore, error) {
	config, _ := decodeExactMatchConfig(input.Task.ScorerConfig)
	var expected string
	if err := json.Unmarshal(input.ExpectedOutput, &expected); err != nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "精确匹配期望输出必须是字符串", err)
	}
	expected = normalizeScoredText(expected, config.CaseSensitive, config.TrimSpace, config.CollapseWhitespace)
	observed := normalizeScoredText(input.Observed.Text, config.CaseSensitive, config.TrimSpace, config.CollapseWhitespace)
	passed := expected == observed
	return capabilityScoreFromAssertions([]CapabilityAssertionResult{
		capabilityAssertion("exact-match", 1, boolScore(passed), "输出与期望字符串不一致", input.Evidence),
	}, "incorrect_answer"), nil
}

type SetMatchConfig struct {
	CaseSensitive bool `json:"caseSensitive"`
	TrimSpace     bool `json:"trimSpace"`
}

func decodeSetMatchConfig(raw json.RawMessage) (SetMatchConfig, error) {
	var config SetMatchConfig
	if err := decodeStrictObject(raw, &config); err != nil {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "集合匹配配置无效", err)
	}
	return config, nil
}

func scoreSetMatch(input CapabilityScoringInput) (CapabilityScore, error) {
	config, _ := decodeSetMatchConfig(input.Task.ScorerConfig)
	var expectedValues []string
	if err := json.Unmarshal(input.ExpectedOutput, &expectedValues); err != nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "集合匹配期望输出必须是字符串数组", err)
	}
	observedRaw := observedStructuredBytes(input.Observed)
	var observedValues []string
	if err := json.Unmarshal(observedRaw, &observedValues); err != nil {
		return capabilityScoreFromAssertions([]CapabilityAssertionResult{
			capabilityAssertion("set-match", 1, 0, "输出不是字符串数组", input.Evidence),
		}, "invalid_format"), nil
	}
	expected := normalizedStringSet(expectedValues, config)
	observed := normalizedStringSet(observedValues, config)
	truePositives := 0
	for value := range expected {
		if _, ok := observed[value]; ok {
			truePositives++
		}
	}
	denominator := len(expected) + len(observed)
	f1 := 1.0
	if denominator > 0 {
		f1 = 2 * float64(truePositives) / float64(denominator)
	}
	return capabilityScoreFromAssertions([]CapabilityAssertionResult{
		capabilityAssertion("set-match", 1, f1, "集合元素缺失或包含多余元素", input.Evidence),
	}, "set_mismatch"), nil
}

type NumericToleranceConfig struct {
	AbsoluteTolerance float64 `json:"absoluteTolerance"`
	RelativeTolerance float64 `json:"relativeTolerance"`
}

func decodeNumericToleranceConfig(raw json.RawMessage) (NumericToleranceConfig, error) {
	var config NumericToleranceConfig
	if err := decodeStrictObject(raw, &config); err != nil || !finiteNumber(config.AbsoluteTolerance) || config.AbsoluteTolerance < 0 ||
		!finiteNumber(config.RelativeTolerance) || config.RelativeTolerance < 0 {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "数值容差配置无效", err)
	}
	return config, nil
}

func scoreNumericTolerance(input CapabilityScoringInput) (CapabilityScore, error) {
	config, _ := decodeNumericToleranceConfig(input.Task.ScorerConfig)
	expected, err := decodeJSONNumber(input.ExpectedOutput)
	if err != nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "数值容差期望输出必须是有限数字", err)
	}
	observedRaw := observedStructuredBytes(input.Observed)
	observed, err := decodeJSONNumber(observedRaw)
	if err != nil {
		return capabilityScoreFromAssertions([]CapabilityAssertionResult{
			capabilityAssertion("numeric-tolerance", 1, 0, "输出不是有限数字", input.Evidence),
		}, "invalid_format"), nil
	}
	allowed := config.AbsoluteTolerance + config.RelativeTolerance*math.Abs(expected)
	passed := math.Abs(observed-expected) <= allowed
	return capabilityScoreFromAssertions([]CapabilityAssertionResult{
		capabilityAssertion("numeric-tolerance", 1, boolScore(passed), "数值误差超过允许容差", input.Evidence),
	}, "numeric_out_of_tolerance"), nil
}

type JSONStructureConfig struct {
	RequiredPaths           []string          `json:"requiredPaths"`
	ForbiddenPaths          []string          `json:"forbiddenPaths"`
	Types                   map[string]string `json:"types"`
	ExactPaths              []string          `json:"exactPaths"`
	AllowAdditionalTopLevel bool              `json:"allowAdditionalTopLevel"`
	AllowedTopLevel         []string          `json:"allowedTopLevel"`
}

func decodeJSONStructureConfig(raw json.RawMessage) (JSONStructureConfig, error) {
	var config JSONStructureConfig
	if err := decodeStrictObject(raw, &config); err != nil {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "JSON 结构配置无效", err)
	}
	allPaths := append(append(append([]string(nil), config.RequiredPaths...), config.ForbiddenPaths...), config.ExactPaths...)
	for path := range config.Types {
		allPaths = append(allPaths, path)
		if !validJSONValueType(config.Types[path]) {
			return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("JSON 路径 %q 的类型约束无效", path))
		}
	}
	for _, path := range allPaths {
		if !validJSONPointer(path) {
			return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("JSON Pointer %q 无效", path))
		}
	}
	for _, paths := range [][]string{config.RequiredPaths, config.ForbiddenPaths, config.ExactPaths} {
		seen := make(map[string]struct{}, len(paths))
		for _, path := range paths {
			if _, duplicate := seen[path]; duplicate {
				return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("同类 JSON Pointer %q 重复", path))
			}
			seen[path] = struct{}{}
		}
	}
	allowed := make(map[string]struct{}, len(config.AllowedTopLevel))
	for _, field := range config.AllowedTopLevel {
		field = strings.TrimSpace(field)
		if field == "" {
			return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "JSON 顶层允许字段不能为空")
		}
		if _, duplicate := allowed[field]; duplicate {
			return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "JSON 顶层允许字段重复")
		}
		allowed[field] = struct{}{}
	}
	if !config.AllowAdditionalTopLevel && len(config.AllowedTopLevel) == 0 {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "禁止额外顶层字段时必须声明允许字段")
	}
	return config, nil
}

func scoreJSONStructure(input CapabilityScoringInput) (CapabilityScore, error) {
	config, _ := decodeJSONStructureConfig(input.Task.ScorerConfig)
	observed, err := decodeJSONAny(observedStructuredBytes(input.Observed))
	if err != nil {
		return capabilityScoreFromAssertions([]CapabilityAssertionResult{
			capabilityAssertion("valid-json", 1, 0, "输出不是有效 JSON", input.Evidence),
		}, "invalid_format"), nil
	}
	expected, err := decodeJSONAny(input.ExpectedOutput)
	if err != nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "JSON 结构期望输出无效", err)
	}
	assertions := []CapabilityAssertionResult{capabilityAssertion("valid-json", 1, 1, "", input.Evidence)}
	for index, path := range config.RequiredPaths {
		_, found := resolveJSONPointer(observed, path)
		assertions = append(assertions, capabilityAssertion(fmt.Sprintf("required.%d", index), 1, boolScore(found), "缺少必需 JSON 路径 "+path, input.Evidence))
	}
	for index, path := range config.ForbiddenPaths {
		_, found := resolveJSONPointer(observed, path)
		assertions = append(assertions, capabilityAssertion(fmt.Sprintf("forbidden.%d", index), 1, boolScore(!found), "出现禁止 JSON 路径 "+path, input.Evidence))
	}
	typePaths := make([]string, 0, len(config.Types))
	for path := range config.Types {
		typePaths = append(typePaths, path)
	}
	sort.Strings(typePaths)
	for index, path := range typePaths {
		expectedType := config.Types[path]
		value, found := resolveJSONPointer(observed, path)
		matched := found && jsonValueType(value) == expectedType
		assertions = append(assertions, capabilityAssertion(fmt.Sprintf("type.%d", index), 1, boolScore(matched), "JSON 路径类型不匹配 "+path, input.Evidence))
	}
	for index, path := range config.ExactPaths {
		observedValue, observedFound := resolveJSONPointer(observed, path)
		expectedValue, expectedFound := resolveJSONPointer(expected, path)
		matched := observedFound && expectedFound && jsonValuesEqual(observedValue, expectedValue)
		assertions = append(assertions, capabilityAssertion(fmt.Sprintf("exact.%d", index), 1, boolScore(matched), "JSON 路径值不匹配 "+path, input.Evidence))
	}
	if !config.AllowAdditionalTopLevel {
		object, ok := observed.(map[string]interface{})
		matched := ok
		if ok {
			allowed := make(map[string]struct{}, len(config.AllowedTopLevel))
			for _, field := range config.AllowedTopLevel {
				allowed[field] = struct{}{}
			}
			for field := range object {
				if _, exists := allowed[field]; !exists {
					matched = false
					break
				}
			}
		}
		assertions = append(assertions, capabilityAssertion("additional-top-level", 1, boolScore(matched), "JSON 包含额外顶层字段或不是对象", input.Evidence))
	}
	return capabilityScoreFromAssertions(assertions, "json_constraint"), nil
}

type ToolCallScorerConfig struct {
	OrderSensitive   bool `json:"orderSensitive"`
	AllowExtraCalls  bool `json:"allowExtraCalls"`
	RequireFinalText bool `json:"requireFinalText"`
}

type expectedToolCalls struct {
	Calls []ToolCall `json:"calls"`
}

func decodeToolCallConfig(raw json.RawMessage) (ToolCallScorerConfig, error) {
	var config ToolCallScorerConfig
	if err := decodeStrictObject(raw, &config); err != nil {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "工具调用判分配置无效", err)
	}
	return config, nil
}

func scoreToolCalls(input CapabilityScoringInput) (CapabilityScore, error) {
	config, _ := decodeToolCallConfig(input.Task.ScorerConfig)
	var expected expectedToolCalls
	if err := decodeStrictObject(input.ExpectedOutput, &expected); err != nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "工具调用期望输出无效", err)
	}
	for _, call := range expected.Calls {
		if strings.TrimSpace(call.Name) == "" || (len(call.Arguments) > 0 && !json.Valid(call.Arguments)) {
			return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "工具调用期望包含无效调用")
		}
	}
	assertions := make([]CapabilityAssertionResult, 0, 1+2*len(expected.Calls))
	countMatched := len(input.Observed.ToolCalls) == len(expected.Calls) || (config.AllowExtraCalls && len(input.Observed.ToolCalls) >= len(expected.Calls))
	assertions = append(assertions, capabilityAssertion("call-count", 1, boolScore(countMatched), "工具调用数量不匹配", input.Evidence))
	used := make(map[int]bool)
	for index, expectedCall := range expected.Calls {
		observedIndex := -1
		if config.OrderSensitive {
			if index < len(input.Observed.ToolCalls) {
				observedIndex = index
			}
		} else {
			nameFallback := -1
			for candidate, observedCall := range input.Observed.ToolCalls {
				if used[candidate] || observedCall.Name != expectedCall.Name {
					continue
				}
				if nameFallback < 0 {
					nameFallback = candidate
				}
				if rawJSONValuesEqual(expectedCall.Arguments, observedCall.Arguments) {
					observedIndex = candidate
					break
				}
			}
			if observedIndex < 0 {
				observedIndex = nameFallback
			}
		}
		nameMatched := observedIndex >= 0 && input.Observed.ToolCalls[observedIndex].Name == expectedCall.Name
		assertions = append(assertions, capabilityAssertion(fmt.Sprintf("call.%d.name", index), 1, boolScore(nameMatched), "工具名称不匹配", input.Evidence))
		argumentsMatched := false
		if nameMatched {
			used[observedIndex] = true
			argumentsMatched = rawJSONValuesEqual(expectedCall.Arguments, input.Observed.ToolCalls[observedIndex].Arguments)
		}
		assertions = append(assertions, capabilityAssertion(fmt.Sprintf("call.%d.arguments", index), 1, boolScore(argumentsMatched), "工具参数不匹配", input.Evidence))
	}
	if config.RequireFinalText {
		assertions = append(assertions, capabilityAssertion("final-text", 1, boolScore(strings.TrimSpace(input.Observed.Text) != ""), "缺少最终文本答复", input.Evidence))
	}
	return capabilityScoreFromAssertions(assertions, "tool_call_mismatch"), nil
}

type ExplicitAssertionKind string

const (
	AssertionTextContains      ExplicitAssertionKind = "text_contains"
	AssertionTextNotContains   ExplicitAssertionKind = "text_not_contains"
	AssertionTextPrefix        ExplicitAssertionKind = "text_prefix"
	AssertionTextSuffix        ExplicitAssertionKind = "text_suffix"
	AssertionJSONPointerEquals ExplicitAssertionKind = "json_pointer_equals"
	AssertionJSONPointerExists ExplicitAssertionKind = "json_pointer_exists"
)

type ExplicitAssertionRule struct {
	ID            string                `json:"id"`
	Kind          ExplicitAssertionKind `json:"kind"`
	Weight        float64               `json:"weight"`
	Path          string                `json:"path,omitempty"`
	CaseSensitive bool                  `json:"caseSensitive"`
}

type AssertionsScorerConfig struct {
	Assertions []ExplicitAssertionRule `json:"assertions"`
}

func decodeAssertionsConfig(raw json.RawMessage) (AssertionsScorerConfig, error) {
	var config AssertionsScorerConfig
	if err := decodeStrictObject(raw, &config); err != nil || len(config.Assertions) == 0 {
		return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "显式断言配置无效", err)
	}
	seen := make(map[string]struct{}, len(config.Assertions))
	for _, assertion := range config.Assertions {
		if !stableIDPattern.MatchString(assertion.ID) || !finiteNumber(assertion.Weight) || assertion.Weight <= 0 {
			return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "显式断言 ID 或权重无效")
		}
		if _, duplicate := seen[assertion.ID]; duplicate {
			return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "显式断言 ID 重复")
		}
		seen[assertion.ID] = struct{}{}
		switch assertion.Kind {
		case AssertionTextContains, AssertionTextNotContains, AssertionTextPrefix, AssertionTextSuffix:
			if assertion.Path != "" {
				return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "文本断言不能携带 JSON 路径")
			}
		case AssertionJSONPointerEquals, AssertionJSONPointerExists:
			if !validJSONPointer(assertion.Path) {
				return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "显式 JSON 断言路径无效")
			}
		default:
			return config, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "显式断言类型无效")
		}
	}
	return config, nil
}

func scoreExplicitAssertions(input CapabilityScoringInput) (CapabilityScore, error) {
	config, _ := decodeAssertionsConfig(input.Task.ScorerConfig)
	var expected map[string]json.RawMessage
	if err := json.Unmarshal(input.ExpectedOutput, &expected); err != nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "显式断言期望输出必须是按断言 ID 索引的对象", err)
	}
	var observedJSON interface{}
	observedJSONReady := false
	assertions := make([]CapabilityAssertionResult, 0, len(config.Assertions))
	for _, rule := range config.Assertions {
		expectedValue, hasExpected := expected[rule.ID]
		matched := false
		reason := "断言不满足"
		switch rule.Kind {
		case AssertionTextContains, AssertionTextNotContains, AssertionTextPrefix, AssertionTextSuffix:
			var expectedText string
			if !hasExpected || json.Unmarshal(expectedValue, &expectedText) != nil {
				return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("文本断言 %q 缺少字符串期望值", rule.ID))
			}
			if expectedText == "" {
				return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("文本断言 %q 的期望值不能为空", rule.ID))
			}
			observedText := input.Observed.Text
			if !rule.CaseSensitive {
				expectedText, observedText = strings.ToLower(expectedText), strings.ToLower(observedText)
			}
			switch rule.Kind {
			case AssertionTextContains:
				matched = strings.Contains(observedText, expectedText)
			case AssertionTextNotContains:
				matched = !strings.Contains(observedText, expectedText)
			case AssertionTextPrefix:
				matched = strings.HasPrefix(observedText, expectedText)
			case AssertionTextSuffix:
				matched = strings.HasSuffix(observedText, expectedText)
			}
		case AssertionJSONPointerEquals, AssertionJSONPointerExists:
			if !observedJSONReady {
				var err error
				observedJSON, err = decodeJSONAny(observedStructuredBytes(input.Observed))
				if err != nil {
					reason = "输出不是有效 JSON"
				}
				observedJSONReady = true
			}
			observedValue, found := resolveJSONPointer(observedJSON, rule.Path)
			if rule.Kind == AssertionJSONPointerExists {
				matched = found
			} else {
				if !hasExpected {
					return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("JSON 断言 %q 缺少期望值", rule.ID))
				}
				expectedDecoded, err := decodeJSONAny(expectedValue)
				if err != nil {
					return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("JSON 断言 %q 的期望值无效", rule.ID), err)
				}
				matched = found && jsonValuesEqual(observedValue, expectedDecoded)
			}
		}
		assertions = append(assertions, capabilityAssertion(rule.ID, rule.Weight, rule.Weight*boolScore(matched), reason, input.Evidence))
	}
	return capabilityScoreFromAssertions(assertions, "assertion_failed"), nil
}

func capabilityAssertion(id string, weight, earned float64, reason string, evidence []EvidenceReference) CapabilityAssertionResult {
	passed := math.Abs(weight-earned) <= 1e-12
	if passed {
		reason = ""
	}
	return CapabilityAssertionResult{
		ID: id, Passed: passed, Weight: weight, Earned: earned, Reason: reason,
		Evidence: append([]EvidenceReference(nil), evidence...),
	}
}

func capabilityScoreFromAssertions(assertions []CapabilityAssertionResult, errorClass string) CapabilityScore {
	totalWeight, totalEarned := 0.0, 0.0
	for _, assertion := range assertions {
		totalWeight += assertion.Weight
		totalEarned += assertion.Earned
	}
	score := totalEarned / totalWeight
	if math.Abs(score-1) <= 1e-12 {
		score = 1
		errorClass = ""
	}
	return CapabilityScore{Score: score, ErrorClass: errorClass, Assertions: assertions}
}

func normalizeScoredText(value string, caseSensitive, trimSpace, collapseWhitespace bool) string {
	if trimSpace {
		value = strings.TrimSpace(value)
	}
	if collapseWhitespace {
		value = strings.Join(strings.Fields(value), " ")
	}
	if !caseSensitive {
		value = strings.ToLower(value)
	}
	return value
}

func normalizedStringSet(values []string, config SetMatchConfig) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = normalizeScoredText(value, config.CaseSensitive, config.TrimSpace, false)
		result[value] = struct{}{}
	}
	return result
}

func observedStructuredBytes(observed CapabilityObservedOutput) []byte {
	if len(observed.StructuredOutput) > 0 {
		return observed.StructuredOutput
	}
	return []byte(strings.TrimSpace(observed.Text))
}

func decodeJSONNumber(raw []byte) (float64, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value json.Number
	if err := decoder.Decode(&value); err != nil {
		return 0, err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("只能包含一个数字")
	}
	parsed, err := strconv.ParseFloat(value.String(), 64)
	if err != nil || !finiteNumber(parsed) {
		return 0, fmt.Errorf("数字无效")
	}
	return parsed, nil
}

func decodeJSONAny(raw []byte) (interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("只能包含一个 JSON 值")
		}
		return nil, err
	}
	return value, nil
}

func decodeStrictObject(raw []byte, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("只能包含一个 JSON 对象")
		}
		return err
	}
	return nil
}

func validJSONPointer(path string) bool {
	if path == "" {
		return true
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, segment := range strings.Split(path[1:], "/") {
		for index := 0; index < len(segment); index++ {
			if segment[index] == '~' && (index+1 >= len(segment) || (segment[index+1] != '0' && segment[index+1] != '1')) {
				return false
			}
			if segment[index] == '~' {
				index++
			}
		}
	}
	return true
}

func resolveJSONPointer(value interface{}, path string) (interface{}, bool) {
	if path == "" {
		return value, true
	}
	if !validJSONPointer(path) {
		return nil, false
	}
	current := value
	for _, encoded := range strings.Split(path[1:], "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]interface{}:
			var ok bool
			current, ok = typed[segment]
			if !ok {
				return nil, false
			}
		case []interface{}:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func validJSONValueType(value string) bool {
	switch value {
	case "string", "number", "boolean", "object", "array", "null":
		return true
	default:
		return false
	}
}

func jsonValueType(value interface{}) string {
	switch value.(type) {
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	case nil:
		return "null"
	default:
		return ""
	}
}

func jsonValuesEqual(left, right interface{}) bool {
	switch leftValue := left.(type) {
	case nil:
		return right == nil
	case string:
		rightValue, ok := right.(string)
		return ok && leftValue == rightValue
	case bool:
		rightValue, ok := right.(bool)
		return ok && leftValue == rightValue
	case json.Number:
		rightValue, ok := right.(json.Number)
		if !ok {
			return false
		}
		leftNumber, leftOK := new(big.Rat).SetString(leftValue.String())
		rightNumber, rightOK := new(big.Rat).SetString(rightValue.String())
		return leftOK && rightOK && leftNumber.Cmp(rightNumber) == 0
	case []interface{}:
		rightValue, ok := right.([]interface{})
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for index := range leftValue {
			if !jsonValuesEqual(leftValue[index], rightValue[index]) {
				return false
			}
		}
		return true
	case map[string]interface{}:
		rightValue, ok := right.(map[string]interface{})
		if !ok || len(leftValue) != len(rightValue) {
			return false
		}
		for key, value := range leftValue {
			rightItem, exists := rightValue[key]
			if !exists || !jsonValuesEqual(value, rightItem) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func rawJSONValuesEqual(left, right json.RawMessage) bool {
	if len(bytes.TrimSpace(left)) == 0 && len(bytes.TrimSpace(right)) == 0 {
		return true
	}
	leftValue, leftErr := decodeJSONAny(left)
	rightValue, rightErr := decodeJSONAny(right)
	return leftErr == nil && rightErr == nil && jsonValuesEqual(leftValue, rightValue)
}

func boolScore(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
