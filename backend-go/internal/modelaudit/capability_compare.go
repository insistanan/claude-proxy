package modelaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

type CapabilityProtocolFeatures struct {
	Streaming SupportLevel `json:"streaming"`
	Tools     SupportLevel `json:"tools"`
	Images    SupportLevel `json:"images"`
	MultiTurn SupportLevel `json:"multiTurn"`
	Thinking  SupportLevel `json:"thinking"`
}

func (f CapabilityProtocolFeatures) Validate() error {
	if !validSupportLevel(f.Streaming) || !validSupportLevel(f.Tools) || !validSupportLevel(f.Images) ||
		!validSupportLevel(f.MultiTurn) || !validSupportLevel(f.Thinking) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较协议能力面无效")
	}
	return nil
}

type CapabilityProtocolSnapshot struct {
	Protocol              Protocol                   `json:"protocol"`
	WireProtocol          Protocol                   `json:"wireProtocol"`
	RequestProfile        string                     `json:"requestProfile"`
	RequestProfileSupport SupportLevel               `json:"requestProfileSupport"`
	RequestedFeatures     CapabilityProtocolFeatures `json:"requestedFeatures"`
	WireFeatures          CapabilityProtocolFeatures `json:"wireFeatures"`
	Converted             bool                       `json:"converted"`
}

func (s CapabilityProtocolSnapshot) Validate() error {
	if !s.Protocol.Valid() || !s.WireProtocol.Valid() || !stableIDPattern.MatchString(s.RequestProfile) ||
		!validSupportLevel(s.RequestProfileSupport) || s.RequestProfileSupport == SupportUnsupported {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较协议快照无效")
	}
	if s.Converted != (s.Protocol != s.WireProtocol) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较协议转换状态与请求/线协议不一致")
	}
	if err := s.RequestedFeatures.Validate(); err != nil {
		return err
	}
	return s.WireFeatures.Validate()
}

type CapabilityThinkingSnapshot struct {
	Level        ThinkingLevel `json:"level,omitempty"`
	Support      SupportLevel  `json:"support,omitempty"`
	Parameter    string        `json:"parameter,omitempty"`
	Mode         string        `json:"mode,omitempty"`
	Effort       string        `json:"effort,omitempty"`
	BudgetTokens *int          `json:"budgetTokens,omitempty"`
}

func (s CapabilityThinkingSnapshot) Validate() error {
	if !s.Level.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较思考档位无效")
	}
	if s.Level == ThinkingUnset {
		if s.Support != "" || s.Parameter != "" || s.Mode != "" || s.Effort != "" || s.BudgetTokens != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未设置思考档位时不能携带映射")
		}
		return nil
	}
	if !validSupportLevel(s.Support) || s.Support == SupportUnsupported || strings.TrimSpace(s.Parameter) == "" ||
		(s.Mode == "" && s.Effort == "" && s.BudgetTokens == nil) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较思考映射无效")
	}
	return nil
}

type CapabilityRunSettingsSnapshot struct {
	Preset              VersionedRef         `json:"preset"`
	Mode                CapabilityPresetMode `json:"mode"`
	TaskSelectionSHA256 string               `json:"taskSelectionSha256"`
	Stream              bool                 `json:"stream"`
	TimeoutMillis       int64                `json:"timeoutMs"`
	MaxOutputTokens     int                  `json:"maxOutputTokens"`
	Features            RequestFeatures      `json:"features"`
	MultiTurn           bool                 `json:"multiTurn"`
}

func (s CapabilityRunSettingsSnapshot) Validate() error {
	if err := s.Preset.Validate("能力比较运行预设"); err != nil {
		return err
	}
	if !s.Mode.Valid() || !validSHA256(s.TaskSelectionSHA256) || s.TimeoutMillis <= 0 || s.MaxOutputTokens <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较主要运行设置无效")
	}
	return nil
}

type CapabilityModelSnapshot struct {
	RequestedModel string `json:"requestedModel,omitempty"`
	DefaultModel   string `json:"defaultModel,omitempty"`
	ResolvedModel  string `json:"resolvedModel"`
	UsedDefault    bool   `json:"usedDefault"`
}

func (s CapabilityModelSnapshot) Validate() error {
	if strings.TrimSpace(s.RequestedModel) != s.RequestedModel || strings.TrimSpace(s.DefaultModel) != s.DefaultModel ||
		strings.TrimSpace(s.ResolvedModel) != s.ResolvedModel || s.ResolvedModel == "" || s.UsedDefault != (s.RequestedModel == "") ||
		(s.UsedDefault && s.DefaultModel == "") {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较模型快照无效")
	}
	return nil
}

type CapabilityComparisonSnapshot struct {
	Package       VersionedRef                  `json:"package"`
	PackageSHA256 string                        `json:"packageSha256"`
	Protocol      CapabilityProtocolSnapshot    `json:"protocol"`
	Thinking      CapabilityThinkingSnapshot    `json:"thinking"`
	Settings      CapabilityRunSettingsSnapshot `json:"settings"`
	Model         CapabilityModelSnapshot       `json:"model"`
}

func (s CapabilityComparisonSnapshot) Validate() error {
	if err := s.Package.Validate("能力比较任务包"); err != nil {
		return err
	}
	if !validSHA256(s.PackageSHA256) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较任务包哈希无效")
	}
	if err := s.Protocol.Validate(); err != nil {
		return err
	}
	if err := s.Thinking.Validate(); err != nil {
		return err
	}
	if err := s.Settings.Validate(); err != nil {
		return err
	}
	return s.Model.Validate()
}

func NewCapabilityComparisonSnapshot(plan CapabilityRunPlan, spec ExecutionSpec, target TargetSnapshot) (CapabilityComparisonSnapshot, error) {
	if err := plan.Validate(); err != nil {
		return CapabilityComparisonSnapshot{}, err
	}
	if err := spec.Validate(); err != nil {
		return CapabilityComparisonSnapshot{}, err
	}
	if err := ValidateSpecCapabilities(spec); err != nil {
		return CapabilityComparisonSnapshot{}, err
	}
	if spec.Purpose != PurposeCapabilityEval {
		return CapabilityComparisonSnapshot{}, contractError(ErrorCodeInvalidPurpose, ErrorCategoryRequest, "能力比较快照必须来自 capability_eval 运行")
	}
	if strings.TrimSpace(target.ChannelID) == "" || target.ChannelID != spec.Target.ChannelID || target.ChannelKind != spec.Target.ChannelKind ||
		target.Protocol != spec.Protocol || target.RequestedModel != strings.TrimSpace(spec.Model) || target.Thinking != spec.Thinking ||
		target.RequestProfile != spec.RequestProfile || target.CapturedAt.IsZero() {
		return CapabilityComparisonSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较目标快照与执行设置不一致")
	}
	if !target.WireProtocol.Valid() || strings.TrimSpace(target.ResolvedModel) == "" {
		return CapabilityComparisonSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较目标缺少线协议或解析模型")
	}
	requestedDescriptor, err := DescribeProtocol(target.Protocol)
	if err != nil {
		return CapabilityComparisonSnapshot{}, err
	}
	wireDescriptor, err := DescribeProtocol(target.WireProtocol)
	if err != nil {
		return CapabilityComparisonSnapshot{}, err
	}
	profileSupport, found := requestProfileSupport(requestedDescriptor, target.RequestProfile)
	if !found || profileSupport == SupportUnsupported {
		return CapabilityComparisonSnapshot{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力比较请求轮廓不受协议支持")
	}
	thinking, err := capabilityThinkingSnapshot(target)
	if err != nil {
		return CapabilityComparisonSnapshot{}, err
	}
	taskSelectionSHA256, err := capabilityTaskSelectionSHA256(plan.Tasks)
	if err != nil {
		return CapabilityComparisonSnapshot{}, err
	}
	multiTurn := len(spec.Input.Messages) > 1 || spec.Conversation.PreviousResponseID != "" || spec.Conversation.PreviousInteractionID != ""
	snapshot := CapabilityComparisonSnapshot{
		Package: plan.Package, PackageSHA256: plan.PackageSHA256,
		Protocol: CapabilityProtocolSnapshot{
			Protocol: target.Protocol, WireProtocol: target.WireProtocol, RequestProfile: target.RequestProfile,
			RequestProfileSupport: profileSupport, RequestedFeatures: protocolFeatureSnapshot(requestedDescriptor),
			WireFeatures: protocolFeatureSnapshot(wireDescriptor), Converted: target.Protocol != target.WireProtocol,
		},
		Thinking: thinking,
		Settings: CapabilityRunSettingsSnapshot{
			Preset: plan.Preset, Mode: plan.Mode, TaskSelectionSHA256: taskSelectionSHA256,
			Stream: spec.Stream, TimeoutMillis: spec.TimeoutMillis, MaxOutputTokens: spec.MaxOutputTokens,
			Features: spec.Features, MultiTurn: multiTurn,
		},
		Model: CapabilityModelSnapshot{
			RequestedModel: strings.TrimSpace(target.RequestedModel), DefaultModel: strings.TrimSpace(target.DefaultModel),
			ResolvedModel: strings.TrimSpace(target.ResolvedModel), UsedDefault: strings.TrimSpace(target.RequestedModel) == "",
		},
	}
	if err := snapshot.Validate(); err != nil {
		return CapabilityComparisonSnapshot{}, err
	}
	return snapshot, nil
}

type CapabilityComparisonMismatch string

const (
	CapabilityMismatchTaskPackage        CapabilityComparisonMismatch = "task_package"
	CapabilityMismatchProtocolCapability CapabilityComparisonMismatch = "protocol_capability"
	CapabilityMismatchProtocolConversion CapabilityComparisonMismatch = "protocol_conversion"
	CapabilityMismatchThinking           CapabilityComparisonMismatch = "thinking"
	CapabilityMismatchRunSettings        CapabilityComparisonMismatch = "run_settings"
	CapabilityMismatchRequestedModel     CapabilityComparisonMismatch = "requested_model"
	CapabilityMismatchDefaultModel       CapabilityComparisonMismatch = "default_model"
	CapabilityMismatchResolvedModel      CapabilityComparisonMismatch = "resolved_model"
)

func (m CapabilityComparisonMismatch) Valid() bool {
	switch m {
	case CapabilityMismatchTaskPackage, CapabilityMismatchProtocolCapability, CapabilityMismatchProtocolConversion,
		CapabilityMismatchThinking, CapabilityMismatchRunSettings, CapabilityMismatchRequestedModel,
		CapabilityMismatchDefaultModel, CapabilityMismatchResolvedModel:
		return true
	default:
		return false
	}
}

type CapabilityComparisonDecision struct {
	Comparator         VersionedRef                   `json:"comparator"`
	DirectlyComparable bool                           `json:"directlyComparable"`
	Mismatches         []CapabilityComparisonMismatch `json:"mismatches,omitempty"`
}

func (d CapabilityComparisonDecision) Validate() error {
	if err := d.Comparator.Validate("能力公平比较器"); err != nil {
		return err
	}
	if d.DirectlyComparable != (len(d.Mismatches) == 0) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力公平比较结论与不兼容原因不一致")
	}
	seen := make(map[CapabilityComparisonMismatch]struct{}, len(d.Mismatches))
	for _, mismatch := range d.Mismatches {
		if !mismatch.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力公平比较包含无效原因")
		}
		if _, duplicate := seen[mismatch]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力公平比较包含重复原因")
		}
		seen[mismatch] = struct{}{}
	}
	return nil
}

func CompareCapabilityRuns(comparator VersionedRef, left, right CapabilityComparisonSnapshot) (CapabilityComparisonDecision, error) {
	if err := comparator.Validate("能力公平比较器"); err != nil {
		return CapabilityComparisonDecision{}, err
	}
	if err := left.Validate(); err != nil {
		return CapabilityComparisonDecision{}, err
	}
	if err := right.Validate(); err != nil {
		return CapabilityComparisonDecision{}, err
	}
	mismatches := make([]CapabilityComparisonMismatch, 0, 8)
	if left.Package != right.Package || left.PackageSHA256 != right.PackageSHA256 {
		mismatches = append(mismatches, CapabilityMismatchTaskPackage)
	}
	if !sameProtocolCapabilities(left.Protocol, right.Protocol) {
		mismatches = append(mismatches, CapabilityMismatchProtocolCapability)
	}
	if left.Protocol.Protocol != right.Protocol.Protocol || left.Protocol.WireProtocol != right.Protocol.WireProtocol ||
		left.Protocol.Converted != right.Protocol.Converted {
		mismatches = append(mismatches, CapabilityMismatchProtocolConversion)
	}
	if !sameThinkingSnapshot(left.Thinking, right.Thinking) {
		mismatches = append(mismatches, CapabilityMismatchThinking)
	}
	if left.Settings != right.Settings {
		mismatches = append(mismatches, CapabilityMismatchRunSettings)
	}
	if left.Model.RequestedModel != right.Model.RequestedModel || left.Model.UsedDefault != right.Model.UsedDefault {
		mismatches = append(mismatches, CapabilityMismatchRequestedModel)
	}
	if (left.Model.UsedDefault || right.Model.UsedDefault) && left.Model.DefaultModel != right.Model.DefaultModel {
		mismatches = append(mismatches, CapabilityMismatchDefaultModel)
	}
	if left.Model.ResolvedModel != right.Model.ResolvedModel {
		mismatches = append(mismatches, CapabilityMismatchResolvedModel)
	}
	decision := CapabilityComparisonDecision{Comparator: comparator, DirectlyComparable: len(mismatches) == 0, Mismatches: mismatches}
	if err := decision.Validate(); err != nil {
		return CapabilityComparisonDecision{}, err
	}
	return decision, nil
}

func protocolFeatureSnapshot(descriptor ProtocolDescriptor) CapabilityProtocolFeatures {
	return CapabilityProtocolFeatures{
		Streaming: descriptor.Streaming.Level,
		Tools:     descriptor.Tools.Level,
		Images:    descriptor.Images.Level,
		MultiTurn: descriptor.MultiTurn.Level,
		Thinking:  descriptor.Thinking.Support,
	}
}

func requestProfileSupport(descriptor ProtocolDescriptor, profileID string) (SupportLevel, bool) {
	for _, profile := range descriptor.Profiles {
		if profile.ID == profileID {
			return profile.Support, true
		}
	}
	return "", false
}

func capabilityThinkingSnapshot(target TargetSnapshot) (CapabilityThinkingSnapshot, error) {
	if target.Thinking == ThinkingUnset {
		if target.ThinkingMap != nil {
			return CapabilityThinkingSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未设置思考档位时目标不能携带映射")
		}
		return CapabilityThinkingSnapshot{}, nil
	}
	if target.ThinkingMap == nil || target.ThinkingMap.Level != target.Thinking {
		return CapabilityThinkingSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力比较目标思考映射缺失或档位不一致")
	}
	mapping := target.ThinkingMap
	var budgetTokens *int
	if mapping.BudgetTokens != nil {
		value := *mapping.BudgetTokens
		budgetTokens = &value
	}
	snapshot := CapabilityThinkingSnapshot{
		Level: target.Thinking, Support: mapping.Support, Parameter: mapping.Parameter,
		Mode: mapping.Mode, Effort: mapping.Effort, BudgetTokens: budgetTokens,
	}
	if err := snapshot.Validate(); err != nil {
		return CapabilityThinkingSnapshot{}, err
	}
	return snapshot, nil
}

func capabilityTaskSelectionSHA256(tasks []CapabilityTaskCoveragePlan) (string, error) {
	ordered := append([]CapabilityTaskCoveragePlan(nil), tasks...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Task.ID != ordered[j].Task.ID {
			return ordered[i].Task.ID < ordered[j].Task.ID
		}
		return ordered[i].Task.SemanticVersion < ordered[j].Task.SemanticVersion
	})
	encoded, err := json.Marshal(ordered)
	if err != nil {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力任务选择编码失败", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sameProtocolCapabilities(left, right CapabilityProtocolSnapshot) bool {
	return left.Protocol == right.Protocol && left.RequestProfile == right.RequestProfile &&
		left.RequestProfileSupport == right.RequestProfileSupport && left.RequestedFeatures == right.RequestedFeatures &&
		left.WireFeatures == right.WireFeatures
}

func sameThinkingSnapshot(left, right CapabilityThinkingSnapshot) bool {
	if left.Level != right.Level || left.Support != right.Support || left.Parameter != right.Parameter ||
		left.Mode != right.Mode || left.Effort != right.Effort {
		return false
	}
	if left.BudgetTokens == nil || right.BudgetTokens == nil {
		return left.BudgetTokens == nil && right.BudgetTokens == nil
	}
	return *left.BudgetTokens == *right.BudgetTokens
}

func validSupportLevel(level SupportLevel) bool {
	return level == SupportOfficial || level == SupportChannelProfile || level == SupportUnsupported
}
