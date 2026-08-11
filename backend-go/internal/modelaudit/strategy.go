package modelaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type StrategyKind string

const (
	StrategyKindIdentity   StrategyKind = "identity"
	StrategyKindCapability StrategyKind = "capability"
)

func (k StrategyKind) Valid() bool {
	return k == StrategyKindIdentity || k == StrategyKindCapability
}

type CombinationPolicy string

const (
	CombinationAllowAny  CombinationPolicy = "allow_any"
	CombinationAllowList CombinationPolicy = "allow_list"
)

func (p CombinationPolicy) Valid() bool {
	return p == CombinationAllowAny || p == CombinationAllowList
}

type DisclosureRisk string

const (
	DisclosureRiskLow    DisclosureRisk = "low"
	DisclosureRiskMedium DisclosureRisk = "medium"
	DisclosureRiskHigh   DisclosureRisk = "high"
)

func (r DisclosureRisk) Valid() bool {
	return r == DisclosureRiskLow || r == DisclosureRiskMedium || r == DisclosureRiskHigh
}

type VersionedRef struct {
	ID                    string `json:"id"`
	SemanticVersion       string `json:"semanticVersion"`
	ImplementationVersion string `json:"implementationVersion"`
}

func (r VersionedRef) Validate(label string) error {
	if !stableIDPattern.MatchString(r.ID) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("%s ID %q 无效", label, r.ID))
	}
	if !semanticVersionPattern.MatchString(r.SemanticVersion) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("%s 语义版本 %q 无效", label, r.SemanticVersion))
	}
	if strings.TrimSpace(r.ImplementationVersion) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("%s 缺少实现版本", label))
	}
	return nil
}

type StrategyApplicability struct {
	ModelFamilies    []string        `json:"modelFamilies,omitempty"`
	Models           []string        `json:"models,omitempty"`
	Protocols        []Protocol      `json:"protocols,omitempty"`
	RequestProfiles  []string        `json:"requestProfiles,omitempty"`
	ThinkingLevels   []ThinkingLevel `json:"thinkingLevels,omitempty"`
	MinContextTokens int             `json:"minContextTokens,omitempty"`
	MaxContextTokens int             `json:"maxContextTokens,omitempty"`
}

type StrategyPipelineDescriptor struct {
	RequestGenerator   string `json:"requestGenerator"`
	ResponseNormalizer string `json:"responseNormalizer"`
	FeatureExtractor   string `json:"featureExtractor"`
	Scorer             string `json:"scorer"`
	FailureClassifier  string `json:"failureClassifier"`
}

type BaselineRequirement struct {
	Required        bool   `json:"required"`
	BaselineID      string `json:"baselineId,omitempty"`
	BaselineVersion string `json:"baselineVersion,omitempty"`
	MinimumSamples  int    `json:"minimumSamples,omitempty"`
}

type StrategyDescriptor struct {
	Ref                    VersionedRef               `json:"ref"`
	Kind                   StrategyKind               `json:"kind"`
	Description            string                     `json:"description"`
	ConfigSchema           json.RawMessage            `json:"configSchema"`
	Applicability          StrategyApplicability      `json:"applicability"`
	Pipeline               StrategyPipelineDescriptor `json:"pipeline"`
	SeedRuleVersion        string                     `json:"seedRuleVersion"`
	MaximumSamples         int                        `json:"maximumSamples"`
	EstimatedMaxTokens     int                        `json:"estimatedMaxTokens"`
	EstimatedMaxCostMicros int64                      `json:"estimatedMaxCostMicros,omitempty"`
	Baseline               BaselineRequirement        `json:"baseline"`
	FormalEligible         bool                       `json:"formalEligible"`
	CombinationPolicy      CombinationPolicy          `json:"combinationPolicy"`
	CompatibleWith         []string                   `json:"compatibleWith,omitempty"`
	ParallelSafe           bool                       `json:"parallelSafe"`
	DisclosureRisk         DisclosureRisk             `json:"disclosureRisk"`
}

type Strategy interface {
	Descriptor() StrategyDescriptor
	ValidateConfig(json.RawMessage) error
	Plan(context.Context, StrategyPlanInput) ([]SamplePlan, error)
	Evaluate(context.Context, StrategyEvaluationInput) (StrategyEvaluation, error)
}

type StrategySelection struct {
	StrategyID string          `json:"strategyId"`
	Enabled    bool            `json:"enabled"`
	Config     json.RawMessage `json:"config"`
}

type StrategyConfigSnapshot struct {
	Ref          VersionedRef    `json:"ref"`
	Enabled      bool            `json:"enabled"`
	Config       json.RawMessage `json:"config"`
	ConfigSHA256 string          `json:"configSha256"`
}

type StrategyRegistry struct {
	mu         sync.RWMutex
	strategies map[string]registeredStrategy
}

type registeredStrategy struct {
	implementation Strategy
	descriptor     StrategyDescriptor
}

var (
	stableIDPattern        = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	semanticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
)

func NewStrategyRegistry(strategies ...Strategy) (*StrategyRegistry, error) {
	registry := &StrategyRegistry{strategies: make(map[string]registeredStrategy)}
	for _, strategy := range strategies {
		if err := registry.Register(strategy); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *StrategyRegistry) Register(strategy Strategy) error {
	if r == nil || r.strategies == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "策略注册表未初始化")
	}
	if strategy == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "策略不能为空")
	}
	descriptor := strategy.Descriptor()
	if err := validateStrategyDescriptor(descriptor); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.strategies[descriptor.Ref.ID]; exists {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 ID %q 已注册", descriptor.Ref.ID))
	}
	r.strategies[descriptor.Ref.ID] = registeredStrategy{
		implementation: strategy,
		descriptor:     cloneStrategyDescriptor(descriptor),
	}
	return nil
}

func (r *StrategyRegistry) Get(id string) (Strategy, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	registered, ok := r.strategies[strings.TrimSpace(id)]
	return registered.implementation, ok
}

func (r *StrategyRegistry) Descriptors() []StrategyDescriptor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptors := make([]StrategyDescriptor, 0, len(r.strategies))
	for _, registered := range r.strategies {
		descriptors = append(descriptors, cloneStrategyDescriptor(registered.descriptor))
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Ref.ID < descriptors[j].Ref.ID })
	return descriptors
}

func (r *StrategyRegistry) Freeze(selections []StrategySelection) ([]StrategyConfigSnapshot, error) {
	if r == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "策略注册表未初始化")
	}
	seen := make(map[string]struct{}, len(selections))
	snapshots := make([]StrategyConfigSnapshot, 0, len(selections))
	descriptors := make(map[string]StrategyDescriptor, len(selections))
	for index, selection := range selections {
		id := strings.TrimSpace(selection.StrategyID)
		if _, duplicate := seen[id]; duplicate {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略选择包含重复 ID %q", id))
		}
		seen[id] = struct{}{}
		registered, ok := r.lookup(id)
		if !ok {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个策略 %q 未注册", index+1, id))
		}
		canonical, digest, err := canonicalJSONObject(selection.Config)
		if err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 配置无效", id), err)
		}
		if err := registered.implementation.ValidateConfig(append(json.RawMessage(nil), canonical...)); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 配置校验失败", id), err)
		}
		descriptor := registered.descriptor
		descriptors[id] = descriptor
		snapshots = append(snapshots, StrategyConfigSnapshot{
			Ref: descriptor.Ref, Enabled: selection.Enabled, Config: canonical, ConfigSHA256: digest,
		})
	}
	for i := range snapshots {
		if !snapshots[i].Enabled {
			continue
		}
		for j := i + 1; j < len(snapshots); j++ {
			if !snapshots[j].Enabled {
				continue
			}
			left, right := descriptors[snapshots[i].Ref.ID], descriptors[snapshots[j].Ref.ID]
			if !strategiesCompatible(left, right) || !strategiesCompatible(right, left) {
				return nil, contractError(
					ErrorCodeInvalidRequest, ErrorCategoryRequest,
					fmt.Sprintf("策略 %q 与 %q 不允许组合", left.Ref.ID, right.Ref.ID),
				)
			}
		}
	}
	return snapshots, nil
}

func (r *StrategyRegistry) lookup(id string) (registeredStrategy, bool) {
	if r == nil {
		return registeredStrategy{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	registered, ok := r.strategies[strings.TrimSpace(id)]
	return registered, ok
}

func validateStrategyDescriptor(descriptor StrategyDescriptor) error {
	if err := descriptor.Ref.Validate("策略"); err != nil {
		return err
	}
	if !descriptor.Kind.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 类型无效", descriptor.Ref.ID))
	}
	if strings.TrimSpace(descriptor.Description) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 缺少说明", descriptor.Ref.ID))
	}
	canonicalSchema, _, err := canonicalJSONObject(descriptor.ConfigSchema)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 配置 Schema 无效", descriptor.Ref.ID), err)
	}
	var schema struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(canonicalSchema, &schema); err != nil || schema.Type != "object" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 配置 Schema 必须声明 object 类型", descriptor.Ref.ID), err)
	}
	components := []string{
		descriptor.Pipeline.RequestGenerator, descriptor.Pipeline.ResponseNormalizer,
		descriptor.Pipeline.FeatureExtractor, descriptor.Pipeline.Scorer, descriptor.Pipeline.FailureClassifier,
	}
	for _, component := range components {
		if strings.TrimSpace(component) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的流水线组件不完整", descriptor.Ref.ID))
		}
	}
	if strings.TrimSpace(descriptor.SeedRuleVersion) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 缺少随机种子规则版本", descriptor.Ref.ID))
	}
	if descriptor.MaximumSamples <= 0 || descriptor.EstimatedMaxTokens < 0 || descriptor.EstimatedMaxCostMicros < 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的样本或 token 上限无效", descriptor.Ref.ID))
	}
	if descriptor.Baseline.Required {
		if !stableIDPattern.MatchString(descriptor.Baseline.BaselineID) || !semanticVersionPattern.MatchString(descriptor.Baseline.BaselineVersion) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的基线要求无效", descriptor.Ref.ID))
		}
		if descriptor.Baseline.MinimumSamples <= 0 || descriptor.Baseline.MinimumSamples > descriptor.MaximumSamples {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的最低基线样本数无效", descriptor.Ref.ID))
		}
	}
	if !descriptor.CombinationPolicy.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的组合策略无效", descriptor.Ref.ID))
	}
	if descriptor.CombinationPolicy == CombinationAllowList && len(descriptor.CompatibleWith) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的组合白名单不能为空", descriptor.Ref.ID))
	}
	for _, id := range descriptor.CompatibleWith {
		if !stableIDPattern.MatchString(id) || id == descriptor.Ref.ID {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的兼容策略 ID %q 无效", descriptor.Ref.ID, id))
		}
	}
	if !descriptor.DisclosureRisk.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的泄漏风险无效", descriptor.Ref.ID))
	}
	if descriptor.Applicability.MinContextTokens < 0 || descriptor.Applicability.MaxContextTokens < 0 ||
		(descriptor.Applicability.MaxContextTokens > 0 && descriptor.Applicability.MinContextTokens > descriptor.Applicability.MaxContextTokens) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 的上下文范围无效", descriptor.Ref.ID))
	}
	for _, protocol := range descriptor.Applicability.Protocols {
		if !protocol.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 包含无效协议 %q", descriptor.Ref.ID, protocol))
		}
	}
	for _, thinking := range descriptor.Applicability.ThinkingLevels {
		if !thinking.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("策略 %q 包含无效思考档位 %q", descriptor.Ref.ID, thinking))
		}
	}
	return nil
}

func strategiesCompatible(descriptor StrategyDescriptor, other StrategyDescriptor) bool {
	if descriptor.CombinationPolicy == CombinationAllowAny {
		return true
	}
	for _, id := range descriptor.CompatibleWith {
		if id == other.Ref.ID {
			return true
		}
	}
	return false
}

func canonicalJSONObject(raw json.RawMessage) (json.RawMessage, string, error) {
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("JSON 对象不能为空")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, "", err
	}
	if _, ok := value.(map[string]interface{}); !ok {
		return nil, "", fmt.Errorf("值必须是 JSON 对象")
	}
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, "", fmt.Errorf("只能包含一个 JSON 对象")
		}
		return nil, "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:]), nil
}

func cloneStrategyDescriptor(descriptor StrategyDescriptor) StrategyDescriptor {
	descriptor.ConfigSchema = append(json.RawMessage(nil), descriptor.ConfigSchema...)
	descriptor.Applicability.ModelFamilies = append([]string(nil), descriptor.Applicability.ModelFamilies...)
	descriptor.Applicability.Models = append([]string(nil), descriptor.Applicability.Models...)
	descriptor.Applicability.Protocols = append([]Protocol(nil), descriptor.Applicability.Protocols...)
	descriptor.Applicability.RequestProfiles = append([]string(nil), descriptor.Applicability.RequestProfiles...)
	descriptor.Applicability.ThinkingLevels = append([]ThinkingLevel(nil), descriptor.Applicability.ThinkingLevels...)
	descriptor.CompatibleWith = append([]string(nil), descriptor.CompatibleWith...)
	return descriptor
}
