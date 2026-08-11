package modelaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const BuiltinCapabilityPackageID = "capability.pack.builtin-core"

type BuiltinCapabilityAssets struct {
	packageSnapshot CapabilityTaskPackageSnapshot
	presets         map[string]CapabilityRunPreset
	scorers         map[CapabilityScorerKind]CapabilityScorer
}

func NewBuiltinCapabilityAssets() (*BuiltinCapabilityAssets, error) {
	exact, err := NewBuiltinCapabilityScorer(CapabilityScorerExactMatch)
	if err != nil {
		return nil, err
	}
	tool, err := NewBuiltinCapabilityScorer(CapabilityScorerToolCall)
	if err != nil {
		return nil, err
	}
	packageRef := VersionedRef{ID: BuiltinCapabilityPackageID, SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"}
	dimensions := CapabilityDimensions()
	weights := make(map[CapabilityDimension]float64, len(dimensions))
	tasks := make([]CapabilityTaskDefinition, len(dimensions))
	for index, dimension := range dimensions {
		weights[dimension] = 1 / float64(len(dimensions))
		scorer := exact.Descriptor()
		scorerConfig := json.RawMessage(`{"caseSensitive":false,"trimSpace":true,"collapseWhitespace":true}`)
		if dimension == CapabilityToolUse {
			scorer = tool.Descriptor()
			scorerConfig = json.RawMessage(`{"orderSensitive":true,"allowExtraCalls":false,"requireFinalText":false}`)
		}
		tasks[index] = CapabilityTaskDefinition{
			Ref:       VersionedRef{ID: "capability.task.builtin." + string(dimension), SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
			Dimension: dimension, Difficulty: "core", Visibility: CapabilityTaskParameterized,
			Modalities: []CapabilityModality{CapabilityModalityText},
			Protocols:  []Protocol{ProtocolMessages, ProtocolResponses, ProtocolChat, ProtocolGemini},
			RequestProfiles: []string{
				"messages.standard.v1", "responses.standard.v1", "chat.standard.v1", "gemini.standard.v1",
			},
			Generator:           VersionedRef{ID: "capability.generator.builtin." + string(dimension), SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
			SeedRuleVersion:     "fnv-run-target-task-repetition-v1",
			Scorer:              scorer.Ref,
			ScorerKind:          scorer.Kind,
			ScorerConfig:        scorerConfig,
			ExpectedContract:    json.RawMessage(`{"type":"json_value","reportDisclosure":"excluded"}`),
			MaximumInputTokens:  512,
			MaximumOutputTokens: 128,
			TimeoutMillis:       30_000,
			Repetitions:         3,
			Weight:              1,
			DisclosureRisk:      DisclosureRiskLow,
		}
	}
	taskPackage := CapabilityTaskPackage{
		Ref: packageRef, Description: "七维参数化核心能力任务包；实例答案按运行种子生成并默认不进入 HTML 报告。",
		ScoringModel:     VersionedRef{ID: "capability.aggregate.weighted", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		DimensionWeights: weights, Tasks: tasks,
	}
	snapshot, err := NewCapabilityTaskPackageSnapshot(taskPackage)
	if err != nil {
		return nil, err
	}
	presets := make(map[string]CapabilityRunPreset, 3)
	for _, preset := range []CapabilityRunPreset{
		builtinCapabilityPreset(packageRef, CapabilityPresetQuick, 1),
		builtinCapabilityPreset(packageRef, CapabilityPresetStandard, 2),
		builtinCapabilityPreset(packageRef, CapabilityPresetDeep, 3),
	} {
		if _, err := PrepareCapabilityRun(preset, snapshot); err != nil {
			return nil, err
		}
		presets[preset.Ref.ID] = preset
	}
	return &BuiltinCapabilityAssets{
		packageSnapshot: snapshot,
		presets:         presets,
		scorers: map[CapabilityScorerKind]CapabilityScorer{
			CapabilityScorerExactMatch: exact,
			CapabilityScorerToolCall:   tool,
		},
	}, nil
}

func builtinCapabilityPreset(packageRef VersionedRef, mode CapabilityPresetMode, repetitions int) CapabilityRunPreset {
	tasks := make([]CapabilityPresetTask, 0, len(capabilityDimensions))
	for _, dimension := range capabilityDimensions {
		tasks = append(tasks, CapabilityPresetTask{TaskID: "capability.task.builtin." + string(dimension), Repetitions: repetitions})
	}
	requests := len(tasks) * repetitions
	inputTokens := int64(requests * 512)
	outputTokens := int64(requests * 128)
	return CapabilityRunPreset{
		Ref:  VersionedRef{ID: "capability.preset.builtin." + string(mode), SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		Mode: mode, Package: packageRef, FormalEligible: mode != CapabilityPresetQuick, Tasks: tasks,
		Limits: CapabilityBudgetLimits{Requests: requests, InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: inputTokens + outputTokens},
	}
}

func (a *BuiltinCapabilityAssets) Catalog() AuditCapabilityAssetCatalog {
	if a == nil {
		return AuditCapabilityAssetCatalog{Available: false, Reason: "能力资产未初始化", Packages: []CapabilityTaskPackageSnapshot{}, Presets: []CapabilityRunPreset{}}
	}
	presets := make([]CapabilityRunPreset, 0, len(a.presets))
	for _, mode := range []CapabilityPresetMode{CapabilityPresetQuick, CapabilityPresetStandard, CapabilityPresetDeep} {
		preset := a.presets["capability.preset.builtin."+string(mode)]
		preset.Tasks = append([]CapabilityPresetTask(nil), preset.Tasks...)
		presets = append(presets, preset)
	}
	return AuditCapabilityAssetCatalog{
		Available: true,
		Packages:  []CapabilityTaskPackageSnapshot{a.packageSnapshot},
		Presets:   presets,
	}
}

func (a *BuiltinCapabilityAssets) ResolvePreset(reference VersionedRef) (CapabilityRunPreset, CapabilityRunPlan, bool, error) {
	if a == nil {
		return CapabilityRunPreset{}, CapabilityRunPlan{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力资产未初始化")
	}
	preset, found := a.presets[strings.TrimSpace(reference.ID)]
	if !found || preset.Ref != reference {
		return CapabilityRunPreset{}, CapabilityRunPlan{}, false, nil
	}
	plan, err := PrepareCapabilityRun(preset, a.packageSnapshot)
	return preset, plan, true, err
}

func (a *BuiltinCapabilityAssets) Task(id string) (CapabilityTaskDefinition, bool) {
	if a == nil {
		return CapabilityTaskDefinition{}, false
	}
	for _, task := range a.packageSnapshot.Package.Tasks {
		if task.Ref.ID == strings.TrimSpace(id) {
			return task, true
		}
	}
	return CapabilityTaskDefinition{}, false
}

func (a *BuiltinCapabilityAssets) Score(
	ctx context.Context,
	task CapabilityTaskDefinition,
	instance CapabilityTaskInstance,
	execution ExecutionResult,
	evidence []EvidenceReference,
) (CapabilityScore, error) {
	if a == nil {
		return CapabilityScore{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力资产未初始化")
	}
	scorer := a.scorers[task.ScorerKind]
	if scorer == nil {
		return CapabilityScore{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力任务引用的判分器不可用")
	}
	return scorer.Score(ctx, CapabilityScoringInput{
		Task: task, Instance: instance, ExpectedOutput: instance.ExpectedOutput,
		Observed: CapabilityObservedOutput{Text: execution.Text, StructuredOutput: execution.StructuredOutput, ToolCalls: execution.ToolCalls},
		Evidence: evidence,
	})
}

func (a *BuiltinCapabilityAssets) GenerateInstance(
	runID string,
	target TargetSnapshot,
	task CapabilityTaskDefinition,
	repetition int,
	seed uint64,
	createdAt time.Time,
) (CapabilityTaskInstance, error) {
	if a == nil {
		return CapabilityTaskInstance{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力资产未初始化")
	}
	if !capabilityTaskSupportsTarget(task, target) {
		return CapabilityTaskInstance{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力任务不支持当前协议、请求轮廓或思考档位")
	}
	prompt, expected, tools, err := generateBuiltinCapabilityInput(task.Dimension, seed)
	if err != nil {
		return CapabilityTaskInstance{}, err
	}
	features := RequestFeatures{Tools: len(tools) > 0}
	execution := ExecutionSpec{
		Purpose: PurposeCapabilityEval, Target: ChannelTarget{ChannelID: target.ChannelID, ChannelKind: target.ChannelKind},
		Protocol: target.Protocol, Model: target.RequestedModel, Thinking: target.Thinking, RequestProfile: target.RequestProfile,
		Stream: false, TimeoutMillis: task.TimeoutMillis, MaxOutputTokens: task.MaximumOutputTokens,
		Features: features, Input: ExecutionInput{Prompt: prompt, Tools: tools}, Redaction: RedactionDigest,
	}
	inputBytes, err := json.Marshal(execution.Input)
	if err != nil {
		return CapabilityTaskInstance{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码能力任务输入失败", err)
	}
	digest := sha256.Sum256(inputBytes)
	instance := CapabilityTaskInstance{
		InstanceID: runID + "." + target.ChannelID + "." + task.Ref.ID + "." + strconv.Itoa(repetition),
		Package:    a.packageSnapshot.Package.Ref, Task: task.Ref, Dimension: task.Dimension, Seed: seed, Repetition: repetition,
		InputSHA256: hex.EncodeToString(digest[:]), ExpectedOutput: expected, Execution: execution, CreatedAt: createdAt,
	}
	if err := instance.Validate(); err != nil {
		return CapabilityTaskInstance{}, err
	}
	return instance, nil
}

func capabilityTaskSupportsTarget(task CapabilityTaskDefinition, target TargetSnapshot) bool {
	protocolSupported := false
	for _, protocol := range task.Protocols {
		protocolSupported = protocolSupported || protocol == target.Protocol
	}
	profileSupported := false
	for _, profile := range task.RequestProfiles {
		profileSupported = profileSupported || profile == target.RequestProfile
	}
	thinkingSupported := len(task.ThinkingLevels) == 0
	for _, thinking := range task.ThinkingLevels {
		thinkingSupported = thinkingSupported || thinking == target.Thinking
	}
	return protocolSupported && profileSupported && thinkingSupported
}

func generateBuiltinCapabilityInput(
	dimension CapabilityDimension,
	seed uint64,
) (string, json.RawMessage, []ToolDefinition, error) {
	left := int(seed%37) + 11
	right := int((seed/37)%29) + 7
	var prompt string
	var expected any
	var tools []ToolDefinition
	switch dimension {
	case CapabilityMathLogic:
		prompt = fmt.Sprintf("只输出十进制整数，不要解释：(%d * %d) + %d = ?", left, right, left)
		expected = strconv.Itoa(left*right + left)
	case CapabilityCode:
		prompt = fmt.Sprintf("阅读伪代码并只输出最终整数：x=%d; repeat %d times { x=x+3 }; output x", left, right%5+2)
		expected = strconv.Itoa(left + (right%5+2)*3)
	case CapabilityInstructionFollowing:
		token := fmt.Sprintf("AUDIT-%d-%d", left, right)
		prompt = "严格只输出下面引号内的内容，不要引号、标点或解释：\"" + token + "\""
		expected = token
	case CapabilityToolUse:
		recordID := left*100 + right
		prompt = fmt.Sprintf("必须调用 lookup_audit_record 工具查询记录 %d；不要猜测结果，也不要输出普通文本。", recordID)
		arguments, _ := json.Marshal(map[string]int{"id": recordID})
		expected = struct {
			Calls []ToolCall `json:"calls"`
		}{Calls: []ToolCall{{Name: "lookup_audit_record", Arguments: arguments}}}
		tools = []ToolDefinition{{
			Name: "lookup_audit_record", Description: "按 ID 查询固定审计记录",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}`),
		}}
	case CapabilityLongContextMultiturn:
		needle := fmt.Sprintf("K%d-%d", left, right)
		prompt = fmt.Sprintf("材料：A=蓝色；B=17；C=%s；D=周四；E=封存。忽略常识，只根据材料回答。只输出 C 的值。", needle)
		expected = needle
	case CapabilityKnowledgeFactuality:
		prompt = "仅依据给定材料回答，不使用外部知识。材料：虚构行星 Arin 的卫星叫 Noma。问题：Arin 的卫星叫什么？只输出名称。"
		expected = "Noma"
	case CapabilityRepeatability:
		prompt = "只输出十进制整数，不要解释：17 * 19 = ?"
		expected = "323"
	default:
		return "", nil, nil, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力维度没有内置生成器")
	}
	encodedExpected, err := json.Marshal(expected)
	if err != nil {
		return "", nil, nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码能力任务期望输出失败", err)
	}
	return prompt, encodedExpected, tools, nil
}
