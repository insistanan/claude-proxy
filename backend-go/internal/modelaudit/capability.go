package modelaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

type CapabilityDimension string

const (
	CapabilityMathLogic            CapabilityDimension = "math_logic"
	CapabilityCode                 CapabilityDimension = "code"
	CapabilityInstructionFollowing CapabilityDimension = "instruction_following"
	CapabilityToolUse              CapabilityDimension = "tool_use"
	CapabilityLongContextMultiturn CapabilityDimension = "long_context_multiturn"
	CapabilityKnowledgeFactuality  CapabilityDimension = "knowledge_factuality"
	CapabilityRepeatability        CapabilityDimension = "repeatability"
)

var capabilityDimensions = []CapabilityDimension{
	CapabilityMathLogic,
	CapabilityCode,
	CapabilityInstructionFollowing,
	CapabilityToolUse,
	CapabilityLongContextMultiturn,
	CapabilityKnowledgeFactuality,
	CapabilityRepeatability,
}

func (d CapabilityDimension) Valid() bool {
	for _, candidate := range capabilityDimensions {
		if d == candidate {
			return true
		}
	}
	return false
}

func CapabilityDimensions() []CapabilityDimension {
	return append([]CapabilityDimension(nil), capabilityDimensions...)
}

type CapabilityTaskVisibility string

const (
	CapabilityTaskPublicFixed   CapabilityTaskVisibility = "public_fixed"
	CapabilityTaskParameterized CapabilityTaskVisibility = "parameterized"
	CapabilityTaskHeldOut       CapabilityTaskVisibility = "held_out"
)

func (v CapabilityTaskVisibility) Valid() bool {
	return v == CapabilityTaskPublicFixed || v == CapabilityTaskParameterized || v == CapabilityTaskHeldOut
}

type CapabilityModality string

const (
	CapabilityModalityText       CapabilityModality = "text"
	CapabilityModalityImage      CapabilityModality = "image"
	CapabilityModalityMultimodal CapabilityModality = "multimodal"
)

func (m CapabilityModality) Valid() bool {
	return m == CapabilityModalityText || m == CapabilityModalityImage || m == CapabilityModalityMultimodal
}

type CapabilityScorerKind string

const (
	CapabilityScorerExactMatch       CapabilityScorerKind = "exact_match"
	CapabilityScorerSetMatch         CapabilityScorerKind = "set_match"
	CapabilityScorerNumericTolerance CapabilityScorerKind = "numeric_tolerance"
	CapabilityScorerJSONStructure    CapabilityScorerKind = "json_structure"
	CapabilityScorerToolCall         CapabilityScorerKind = "tool_call"
	CapabilityScorerAssertions       CapabilityScorerKind = "assertions"
	CapabilityScorerSandboxTests     CapabilityScorerKind = "sandbox_tests"
)

func (k CapabilityScorerKind) Valid() bool {
	switch k {
	case CapabilityScorerExactMatch, CapabilityScorerSetMatch, CapabilityScorerNumericTolerance,
		CapabilityScorerJSONStructure, CapabilityScorerToolCall, CapabilityScorerAssertions,
		CapabilityScorerSandboxTests:
		return true
	default:
		return false
	}
}

type CapabilityScorerDescriptor struct {
	Ref          VersionedRef         `json:"ref"`
	Kind         CapabilityScorerKind `json:"kind"`
	Objective    bool                 `json:"objective"`
	ConfigSchema json.RawMessage      `json:"configSchema"`
}

func (d CapabilityScorerDescriptor) Validate() error {
	if err := d.Ref.Validate("能力判分器"); err != nil {
		return err
	}
	if !d.Kind.Valid() || !d.Objective {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "首期能力判分器必须是受支持的客观判分器")
	}
	if _, _, err := canonicalJSONObject(d.ConfigSchema); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分器配置 Schema 必须是 JSON 对象", err)
	}
	return nil
}

type CapabilityTaskDefinition struct {
	Ref                 VersionedRef             `json:"ref"`
	Dimension           CapabilityDimension      `json:"dimension"`
	Difficulty          string                   `json:"difficulty"`
	Visibility          CapabilityTaskVisibility `json:"visibility"`
	Modalities          []CapabilityModality     `json:"modalities"`
	Protocols           []Protocol               `json:"protocols"`
	RequestProfiles     []string                 `json:"requestProfiles"`
	ThinkingLevels      []ThinkingLevel          `json:"thinkingLevels,omitempty"`
	Generator           VersionedRef             `json:"generator"`
	SeedRuleVersion     string                   `json:"seedRuleVersion"`
	Scorer              VersionedRef             `json:"scorer"`
	ScorerKind          CapabilityScorerKind     `json:"scorerKind"`
	ScorerConfig        json.RawMessage          `json:"scorerConfig"`
	ExpectedContract    json.RawMessage          `json:"expectedContract"`
	MaximumInputTokens  int                      `json:"maximumInputTokens"`
	MaximumOutputTokens int                      `json:"maximumOutputTokens"`
	TimeoutMillis       int64                    `json:"timeoutMs"`
	Repetitions         int                      `json:"repetitions"`
	Weight              float64                  `json:"weight"`
	DisclosureRisk      DisclosureRisk           `json:"disclosureRisk"`
}

func (t CapabilityTaskDefinition) Validate() error {
	if err := t.Ref.Validate("能力任务"); err != nil {
		return err
	}
	if !t.Dimension.Valid() || !stableIDPattern.MatchString(strings.TrimSpace(t.Difficulty)) || !t.Visibility.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务维度、难度或可见性无效")
	}
	if len(t.Modalities) == 0 || len(t.Protocols) == 0 || len(t.RequestProfiles) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务必须声明模态、协议和请求轮廓")
	}
	if err := validateUniqueModalities(t.Modalities); err != nil {
		return err
	}
	if err := validateUniqueProtocols(t.Protocols); err != nil {
		return err
	}
	profiles := make(map[string]struct{}, len(t.RequestProfiles))
	for _, profile := range t.RequestProfiles {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务请求轮廓不能为空")
		}
		if _, duplicate := profiles[profile]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务请求轮廓重复")
		}
		profiles[profile] = struct{}{}
	}
	thinking := make(map[ThinkingLevel]struct{}, len(t.ThinkingLevels))
	for _, level := range t.ThinkingLevels {
		if !level.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务思考档位无效")
		}
		if _, duplicate := thinking[level]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务思考档位重复")
		}
		thinking[level] = struct{}{}
	}
	if err := t.Generator.Validate("能力任务生成器"); err != nil {
		return err
	}
	if strings.TrimSpace(t.SeedRuleVersion) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务缺少随机种子规则版本")
	}
	if err := t.Scorer.Validate("能力任务判分器"); err != nil {
		return err
	}
	if !t.ScorerKind.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务判分器类型无效")
	}
	if _, _, err := canonicalJSONObject(t.ScorerConfig); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务判分配置必须是 JSON 对象", err)
	}
	if _, _, err := canonicalJSONObject(t.ExpectedContract); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务预期输出合同必须是 JSON 对象", err)
	}
	if t.MaximumInputTokens <= 0 || t.MaximumOutputTokens <= 0 || t.TimeoutMillis <= 0 || t.Repetitions <= 0 ||
		!finiteNumber(t.Weight) || t.Weight <= 0 || !t.DisclosureRisk.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务预算、重复次数、权重或泄漏风险无效")
	}
	return nil
}

type CapabilityTaskPackage struct {
	Ref              VersionedRef                    `json:"ref"`
	Description      string                          `json:"description"`
	ScoringModel     VersionedRef                    `json:"scoringModel"`
	DimensionWeights map[CapabilityDimension]float64 `json:"dimensionWeights"`
	Tasks            []CapabilityTaskDefinition      `json:"tasks"`
}

func (p CapabilityTaskPackage) Validate() error {
	if err := p.Ref.Validate("能力任务包"); err != nil {
		return err
	}
	if strings.TrimSpace(p.Description) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务包缺少说明")
	}
	if err := p.ScoringModel.Validate("能力评分模型"); err != nil {
		return err
	}
	if len(p.DimensionWeights) != len(capabilityDimensions) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "标准能力任务包必须声明全部七维权重")
	}
	weightTotal := 0.0
	for _, dimension := range capabilityDimensions {
		weight, exists := p.DimensionWeights[dimension]
		if !exists || !finiteNumber(weight) || weight <= 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力维度 %q 缺少有效权重", dimension))
		}
		weightTotal += weight
	}
	if math.Abs(weightTotal-1) > 1e-9 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务包维度权重之和必须为 1")
	}
	if len(p.Tasks) < len(capabilityDimensions) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "标准能力任务包至少需要覆盖七个任务")
	}
	seenTasks := make(map[string]struct{}, len(p.Tasks))
	covered := make(map[CapabilityDimension]bool, len(capabilityDimensions))
	for _, task := range p.Tasks {
		if err := task.Validate(); err != nil {
			return err
		}
		if _, duplicate := seenTasks[task.Ref.ID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力任务 %q 重复", task.Ref.ID))
		}
		seenTasks[task.Ref.ID] = struct{}{}
		covered[task.Dimension] = true
	}
	for _, dimension := range capabilityDimensions {
		if !covered[dimension] {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力任务包未覆盖维度 %q", dimension))
		}
	}
	return nil
}

type CapabilityTaskPackageSnapshot struct {
	Package CapabilityTaskPackage `json:"package"`
	SHA256  string                `json:"sha256"`
}

func NewCapabilityTaskPackageSnapshot(taskPackage CapabilityTaskPackage) (CapabilityTaskPackageSnapshot, error) {
	if err := taskPackage.Validate(); err != nil {
		return CapabilityTaskPackageSnapshot{}, err
	}
	cloned := cloneCapabilityTaskPackage(taskPackage)
	for index := range cloned.Tasks {
		canonicalConfig, _, err := canonicalJSONObject(cloned.Tasks[index].ScorerConfig)
		if err != nil {
			return CapabilityTaskPackageSnapshot{}, err
		}
		canonicalContract, _, err := canonicalJSONObject(cloned.Tasks[index].ExpectedContract)
		if err != nil {
			return CapabilityTaskPackageSnapshot{}, err
		}
		cloned.Tasks[index].ScorerConfig = canonicalConfig
		cloned.Tasks[index].ExpectedContract = canonicalContract
	}
	encoded, err := json.Marshal(cloned)
	if err != nil {
		return CapabilityTaskPackageSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力任务包编码失败", err)
	}
	digest := sha256.Sum256(encoded)
	return CapabilityTaskPackageSnapshot{Package: cloned, SHA256: hex.EncodeToString(digest[:])}, nil
}

func (s CapabilityTaskPackageSnapshot) Validate() error {
	if err := s.Package.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(s.Package)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力任务包快照编码失败", err)
	}
	digest := sha256.Sum256(encoded)
	if s.SHA256 != hex.EncodeToString(digest[:]) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务包快照哈希不匹配")
	}
	return nil
}

type CapabilityTaskInstance struct {
	InstanceID     string              `json:"instanceId"`
	Package        VersionedRef        `json:"package"`
	Task           VersionedRef        `json:"task"`
	Dimension      CapabilityDimension `json:"dimension"`
	Seed           uint64              `json:"seed"`
	Repetition     int                 `json:"repetition"`
	InputSHA256    string              `json:"inputSha256"`
	ExpectedOutput json.RawMessage     `json:"expectedOutput"`
	Execution      ExecutionSpec       `json:"execution"`
	CreatedAt      time.Time           `json:"createdAt"`
}

func (i CapabilityTaskInstance) Validate() error {
	if !stableIDPattern.MatchString(i.InstanceID) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务实例 ID 无效")
	}
	if err := i.Package.Validate("能力任务实例任务包"); err != nil {
		return err
	}
	if err := i.Task.Validate("能力任务实例任务"); err != nil {
		return err
	}
	if !i.Dimension.Valid() || i.Repetition < 0 || !validSHA256(i.InputSHA256) || !json.Valid(i.ExpectedOutput) || i.CreatedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务实例维度、重复序号、哈希、期望输出或时间无效")
	}
	if i.Execution.Purpose != PurposeCapabilityEval {
		return contractError(ErrorCodeInvalidPurpose, ErrorCategoryRequest, "能力任务实例必须使用 capability_eval 运行目的")
	}
	return i.Execution.Validate()
}

type CapabilityTaskResultStatus string

const (
	CapabilityTaskScored          CapabilityTaskResultStatus = "scored"
	CapabilityTaskSkipped         CapabilityTaskResultStatus = "skipped"
	CapabilityTaskUnsupported     CapabilityTaskResultStatus = "unsupported"
	CapabilityTaskIndeterminate   CapabilityTaskResultStatus = "indeterminate"
	CapabilityTaskExecutionFailed CapabilityTaskResultStatus = "execution_failed"
	CapabilityTaskScoringFailed   CapabilityTaskResultStatus = "scoring_failed"
)

func (s CapabilityTaskResultStatus) Valid() bool {
	switch s {
	case CapabilityTaskScored, CapabilityTaskSkipped, CapabilityTaskUnsupported, CapabilityTaskIndeterminate,
		CapabilityTaskExecutionFailed, CapabilityTaskScoringFailed:
		return true
	default:
		return false
	}
}

type CapabilityAssertionResult struct {
	ID       string              `json:"id"`
	Passed   bool                `json:"passed"`
	Weight   float64             `json:"weight"`
	Earned   float64             `json:"earned"`
	Reason   string              `json:"reason,omitempty"`
	Evidence []EvidenceReference `json:"evidence,omitempty"`
}

func (a CapabilityAssertionResult) Validate() error {
	if !stableIDPattern.MatchString(a.ID) || !finiteNumber(a.Weight) || a.Weight <= 0 || !finiteNumber(a.Earned) || a.Earned < 0 || a.Earned > a.Weight {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分断言无效")
	}
	if a.Passed != (math.Abs(a.Earned-a.Weight) <= 1e-12) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分断言通过状态与得分不一致")
	}
	if !a.Passed && strings.TrimSpace(a.Reason) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未通过的能力判分断言必须说明原因")
	}
	if _, err := mergeSignalEvidence(nil, a.Evidence); err != nil {
		return err
	}
	return nil
}

type CapabilityScore struct {
	Score      float64                     `json:"score"`
	ErrorClass string                      `json:"errorClass,omitempty"`
	Assertions []CapabilityAssertionResult `json:"assertions"`
}

func (s CapabilityScore) Validate() error {
	if !unitInterval(s.Score) || len(s.Assertions) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分结果缺少有效分数或断言")
	}
	totalWeight, totalEarned := 0.0, 0.0
	seen := make(map[string]struct{}, len(s.Assertions))
	for _, assertion := range s.Assertions {
		if err := assertion.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[assertion.ID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分结果包含重复断言")
		}
		seen[assertion.ID] = struct{}{}
		totalWeight += assertion.Weight
		totalEarned += assertion.Earned
	}
	if math.Abs(s.Score-totalEarned/totalWeight) > 1e-9 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分结果与断言加权得分不一致")
	}
	if s.Score < 1 && strings.TrimSpace(s.ErrorClass) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "非满分能力判分结果必须提供错误分类")
	}
	return nil
}

type CapabilityObservedOutput struct {
	Text             string          `json:"text,omitempty"`
	StructuredOutput json.RawMessage `json:"structuredOutput,omitempty"`
	ToolCalls        []ToolCall      `json:"toolCalls,omitempty"`
}

type CapabilityScoringInput struct {
	Task           CapabilityTaskDefinition `json:"task"`
	Instance       CapabilityTaskInstance   `json:"instance"`
	ExpectedOutput json.RawMessage          `json:"expectedOutput"`
	Observed       CapabilityObservedOutput `json:"observed"`
	Evidence       []EvidenceReference      `json:"evidence,omitempty"`
}

func (i CapabilityScoringInput) Validate() error {
	if err := i.Task.Validate(); err != nil {
		return err
	}
	if err := i.Instance.Validate(); err != nil {
		return err
	}
	if i.Task.Ref != i.Instance.Task || i.Task.Dimension != i.Instance.Dimension {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分输入的任务定义与实例不一致")
	}
	expected, err := canonicalJSONValue(i.ExpectedOutput)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分输入的期望输出不是有效 JSON")
	}
	frozenExpected, err := canonicalJSONValue(i.Instance.ExpectedOutput)
	if err != nil || string(expected) != string(frozenExpected) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分输入的期望输出与任务实例快照不一致", err)
	}
	if len(i.Observed.StructuredOutput) > 0 && !json.Valid(i.Observed.StructuredOutput) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分输入的结构化输出不是有效 JSON")
	}
	for _, call := range i.Observed.ToolCalls {
		if strings.TrimSpace(call.Name) == "" || (len(call.Arguments) > 0 && !json.Valid(call.Arguments)) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力判分输入包含无效工具调用")
		}
	}
	if _, err := mergeSignalEvidence(nil, i.Evidence); err != nil {
		return err
	}
	return nil
}

type CapabilityScorer interface {
	Descriptor() CapabilityScorerDescriptor
	ValidateConfig(json.RawMessage) error
	Score(context.Context, CapabilityScoringInput) (CapabilityScore, error)
}

type CapabilityTaskResult struct {
	InstanceID  string                      `json:"instanceId"`
	Task        VersionedRef                `json:"task"`
	Dimension   CapabilityDimension         `json:"dimension"`
	Status      CapabilityTaskResultStatus  `json:"status"`
	Reason      string                      `json:"reason,omitempty"`
	ErrorClass  string                      `json:"errorClass,omitempty"`
	Score       *float64                    `json:"score,omitempty"`
	Scorer      *VersionedRef               `json:"scorer,omitempty"`
	Assertions  []CapabilityAssertionResult `json:"assertions,omitempty"`
	ExecutionID string                      `json:"executionId,omitempty"`
	Usage       Usage                       `json:"usage"`
	Timing      ExecutionTiming             `json:"timing"`
	Evidence    []EvidenceReference         `json:"evidence,omitempty"`
	CapturedAt  time.Time                   `json:"capturedAt"`
}

func (r CapabilityTaskResult) Validate() error {
	if !stableIDPattern.MatchString(r.InstanceID) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力单题结果实例 ID 无效")
	}
	if err := r.Task.Validate("能力单题结果任务"); err != nil {
		return err
	}
	if !r.Dimension.Valid() || !r.Status.Valid() || r.CapturedAt.IsZero() || invalidUsage(r.Usage) || r.Timing.FirstByteMillis < 0 || r.Timing.TotalMillis < 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力单题结果维度、状态、时间、usage 或耗时无效")
	}
	if r.Status == CapabilityTaskScored {
		if r.Score == nil || !unitInterval(*r.Score) || r.Scorer == nil || len(r.Assertions) == 0 || strings.TrimSpace(r.ExecutionID) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "已判分能力结果缺少分数、判分器或断言")
		}
		if err := r.Scorer.Validate("能力单题结果判分器"); err != nil {
			return err
		}
		score := CapabilityScore{Score: *r.Score, ErrorClass: r.ErrorClass, Assertions: r.Assertions}
		if err := score.Validate(); err != nil {
			return err
		}
	} else {
		if strings.TrimSpace(r.Reason) == "" || r.Score != nil || r.Scorer != nil || len(r.Assertions) > 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未判分能力结果必须说明原因且不能伪装成已有分数")
		}
		if (r.Status == CapabilityTaskExecutionFailed || r.Status == CapabilityTaskScoringFailed) && strings.TrimSpace(r.ExecutionID) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "执行或判分失败结果缺少 executionId")
		}
	}
	if _, err := mergeSignalEvidence(nil, r.Evidence); err != nil {
		return err
	}
	return nil
}

func validateUniqueModalities(values []CapabilityModality) error {
	seen := make(map[CapabilityModality]struct{}, len(values))
	for _, value := range values {
		if !value.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务包含无效模态")
		}
		if _, duplicate := seen[value]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务包含重复模态")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateUniqueProtocols(values []Protocol) error {
	seen := make(map[Protocol]struct{}, len(values))
	for _, value := range values {
		if !value.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务包含无效协议")
		}
		if _, duplicate := seen[value]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务包含重复协议")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func cloneCapabilityTaskPackage(taskPackage CapabilityTaskPackage) CapabilityTaskPackage {
	cloned := taskPackage
	cloned.DimensionWeights = make(map[CapabilityDimension]float64, len(taskPackage.DimensionWeights))
	for dimension, weight := range taskPackage.DimensionWeights {
		cloned.DimensionWeights[dimension] = weight
	}
	cloned.Tasks = append([]CapabilityTaskDefinition(nil), taskPackage.Tasks...)
	for index := range cloned.Tasks {
		cloned.Tasks[index].Modalities = append([]CapabilityModality(nil), cloned.Tasks[index].Modalities...)
		cloned.Tasks[index].Protocols = append([]Protocol(nil), cloned.Tasks[index].Protocols...)
		cloned.Tasks[index].RequestProfiles = append([]string(nil), cloned.Tasks[index].RequestProfiles...)
		cloned.Tasks[index].ThinkingLevels = append([]ThinkingLevel(nil), cloned.Tasks[index].ThinkingLevels...)
		cloned.Tasks[index].ScorerConfig = append(json.RawMessage(nil), cloned.Tasks[index].ScorerConfig...)
		cloned.Tasks[index].ExpectedContract = append(json.RawMessage(nil), cloned.Tasks[index].ExpectedContract...)
		sort.Slice(cloned.Tasks[index].Modalities, func(i, j int) bool { return cloned.Tasks[index].Modalities[i] < cloned.Tasks[index].Modalities[j] })
		sort.Slice(cloned.Tasks[index].Protocols, func(i, j int) bool { return cloned.Tasks[index].Protocols[i] < cloned.Tasks[index].Protocols[j] })
		sort.Strings(cloned.Tasks[index].RequestProfiles)
		sort.Slice(cloned.Tasks[index].ThinkingLevels, func(i, j int) bool {
			return cloned.Tasks[index].ThinkingLevels[i] < cloned.Tasks[index].ThinkingLevels[j]
		})
	}
	sort.Slice(cloned.Tasks, func(i, j int) bool { return cloned.Tasks[i].Ref.ID < cloned.Tasks[j].Ref.ID })
	return cloned
}

func canonicalJSONValue(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("JSON 值不能为空")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
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
	return json.Marshal(value)
}
