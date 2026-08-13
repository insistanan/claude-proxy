package modelaudit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/tidwall/gjson"
)

type AuditModRuleOperator string

const (
	AuditModRuleExists    AuditModRuleOperator = "exists"
	AuditModRuleNotExists AuditModRuleOperator = "not_exists"
	AuditModRuleEqual     AuditModRuleOperator = "eq"
	AuditModRuleNotEqual  AuditModRuleOperator = "neq"
	AuditModRuleGreater   AuditModRuleOperator = "gt"
	AuditModRuleGTE       AuditModRuleOperator = "gte"
	AuditModRuleLess      AuditModRuleOperator = "lt"
	AuditModRuleLTE       AuditModRuleOperator = "lte"
	AuditModRuleContains  AuditModRuleOperator = "contains"
)

func (o AuditModRuleOperator) Valid() bool {
	switch o {
	case AuditModRuleExists, AuditModRuleNotExists, AuditModRuleEqual, AuditModRuleNotEqual,
		AuditModRuleGreater, AuditModRuleGTE, AuditModRuleLess, AuditModRuleLTE, AuditModRuleContains:
		return true
	default:
		return false
	}
}

type AuditModVerdict struct {
	Verdict    string  `json:"verdict"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
	Message    string  `json:"message"`
}

func (v AuditModVerdict) validate(label string) error {
	if strings.TrimSpace(v.Verdict) == "" || !unitInterval(v.Score) || !unitInterval(v.Confidence) || strings.TrimSpace(v.Message) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, label+"结论、分数、置信度或说明无效")
	}
	return nil
}

type AuditModRule struct {
	ID       string               `json:"id"`
	Path     string               `json:"path"`
	Operator AuditModRuleOperator `json:"operator"`
	Value    json.RawMessage      `json:"value,omitempty"`
	Outcome  AuditModVerdict      `json:"outcome"`
}

type AuditModRuleSet struct {
	Rules    []AuditModRule  `json:"rules"`
	Fallback AuditModVerdict `json:"fallback"`
}

type AuditModRuleResult struct {
	MatchedRuleID string          `json:"matchedRuleId,omitempty"`
	Path          string          `json:"path,omitempty"`
	Actual        json.RawMessage `json:"actual,omitempty"`
	AuditModVerdict
}

func decodeAuditModRuleSet(raw json.RawMessage) (AuditModRuleSet, error) {
	var rules AuditModRuleSet
	if err := decodeStrictObject(raw, &rules); err != nil {
		return rules, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 规则文件无效", err)
	}
	if err := rules.Validate(); err != nil {
		return AuditModRuleSet{}, err
	}
	return rules, nil
}

func (r AuditModRuleSet) Validate() error {
	if err := r.Fallback.validate("Mod fallback "); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(r.Rules))
	for _, rule := range r.Rules {
		if !stableIDPattern.MatchString(rule.ID) || strings.TrimSpace(rule.Path) == "" || !rule.Operator.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 规则 ID、路径或操作符无效")
		}
		if _, duplicate := seen[rule.ID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 规则 ID 重复")
		}
		seen[rule.ID] = struct{}{}
		if err := rule.Outcome.validate("Mod 规则 "); err != nil {
			return err
		}
		if rule.Operator != AuditModRuleExists && rule.Operator != AuditModRuleNotExists && len(rule.Value) == 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 规则比较操作缺少 value")
		}
		if len(rule.Value) > 0 && !json.Valid(rule.Value) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 规则 value 不是有效 JSON")
		}
	}
	return nil
}

func (r AuditModRuleSet) Evaluate(summary json.RawMessage) (AuditModRuleResult, error) {
	if err := r.Validate(); err != nil {
		return AuditModRuleResult{}, err
	}
	if !json.Valid(summary) {
		return AuditModRuleResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 规则输入不是有效 JSON")
	}
	for _, rule := range r.Rules {
		actual := gjson.GetBytes(summary, rule.Path)
		matched, err := auditModRuleMatches(rule, actual)
		if err != nil {
			return AuditModRuleResult{}, err
		}
		if !matched {
			continue
		}
		actualJSON := json.RawMessage("null")
		if actual.Exists() {
			actualJSON, err = json.Marshal(actual.Value())
			if err != nil {
				return AuditModRuleResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 规则实际值失败", err)
			}
		}
		return AuditModRuleResult{
			MatchedRuleID: rule.ID, Path: rule.Path, Actual: actualJSON, AuditModVerdict: rule.Outcome,
		}, nil
	}
	return AuditModRuleResult{AuditModVerdict: r.Fallback}, nil
}

func auditModRuleMatches(rule AuditModRule, actual gjson.Result) (bool, error) {
	switch rule.Operator {
	case AuditModRuleExists:
		return actual.Exists(), nil
	case AuditModRuleNotExists:
		return !actual.Exists(), nil
	}
	if !actual.Exists() {
		return false, nil
	}
	var expected any
	decoder := json.NewDecoder(bytes.NewReader(rule.Value))
	decoder.UseNumber()
	if err := decoder.Decode(&expected); err != nil {
		return false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("解码 Mod 规则 %q 的 value 失败", rule.ID), err)
	}
	switch rule.Operator {
	case AuditModRuleEqual, AuditModRuleNotEqual:
		left := normalizeAuditModComparable(actual.Value())
		right := normalizeAuditModComparable(expected)
		equal := reflect.DeepEqual(left, right)
		if rule.Operator == AuditModRuleNotEqual {
			equal = !equal
		}
		return equal, nil
	case AuditModRuleGreater, AuditModRuleGTE, AuditModRuleLess, AuditModRuleLTE:
		actualNumber, ok := auditModFloat(actual.Value())
		if !ok {
			return false, nil
		}
		expectedNumber, ok := auditModFloat(expected)
		if !ok {
			return false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("Mod 数值规则 %q 的 value 不是数字", rule.ID))
		}
		switch rule.Operator {
		case AuditModRuleGreater:
			return actualNumber > expectedNumber, nil
		case AuditModRuleGTE:
			return actualNumber >= expectedNumber, nil
		case AuditModRuleLess:
			return actualNumber < expectedNumber, nil
		default:
			return actualNumber <= expectedNumber, nil
		}
	case AuditModRuleContains:
		expectedString, ok := expected.(string)
		return ok && strings.Contains(fmt.Sprint(actual.Value()), expectedString), nil
	default:
		return false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行了不支持的 Mod 规则操作符")
	}
}

func normalizeAuditModComparable(value any) any {
	if number, ok := auditModFloat(value); ok {
		return number
	}
	return value
}

func auditModFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
