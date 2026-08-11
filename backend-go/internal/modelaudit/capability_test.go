package modelaudit

import (
	"testing"
	"time"
)

func TestCapabilityTaskPackageSnapshot(t *testing.T) {
	taskPackage := capabilityTaskPackageFixture()
	snapshot, err := NewCapabilityTaskPackageSnapshot(taskPackage)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if !validSHA256(snapshot.SHA256) || len(snapshot.Package.Tasks) != 7 {
		t.Fatalf("能力任务包快照 = %#v", snapshot)
	}

	originalHash := snapshot.SHA256
	taskPackage.Tasks[0].ScorerConfig[0] = '['
	taskPackage.DimensionWeights[CapabilityMathLogic] = 0.9
	if snapshot.SHA256 != originalHash || snapshot.Package.DimensionWeights[CapabilityMathLogic] == 0.9 || snapshot.Package.Tasks[0].ScorerConfig[0] == '[' {
		t.Fatal("能力任务包快照未与调用方输入隔离")
	}

	equivalent := capabilityTaskPackageFixture()
	for left, right := 0, len(equivalent.Tasks)-1; left < right; left, right = left+1, right-1 {
		equivalent.Tasks[left], equivalent.Tasks[right] = equivalent.Tasks[right], equivalent.Tasks[left]
	}
	equivalent.Tasks[0].ScorerConfig = []byte("{ \"caseSensitive\" : true }")
	equivalentSnapshot, err := NewCapabilityTaskPackageSnapshot(equivalent)
	if err != nil {
		t.Fatal(err)
	}
	if equivalentSnapshot.SHA256 != originalHash {
		t.Fatal("任务顺序或 JSON 空白不应改变规范化任务包哈希")
	}
}

func TestCapabilityTaskPackageRequiresSevenDimensions(t *testing.T) {
	taskPackage := capabilityTaskPackageFixture()
	delete(taskPackage.DimensionWeights, CapabilityRepeatability)
	if err := taskPackage.Validate(); err == nil {
		t.Fatal("缺少维度权重的标准任务包应被拒绝")
	}

	taskPackage = capabilityTaskPackageFixture()
	taskPackage.Tasks = taskPackage.Tasks[:6]
	if err := taskPackage.Validate(); err == nil {
		t.Fatal("缺少维度任务的标准任务包应被拒绝")
	}
}

func TestCapabilityTaskInstanceAndResultContracts(t *testing.T) {
	taskPackage := capabilityTaskPackageFixture()
	task := taskPackage.Tasks[0]
	spec := validSpec(ProtocolResponses, "channel-capability")
	spec.Purpose = PurposeCapabilityEval
	instance := CapabilityTaskInstance{
		InstanceID: "capability.instance.1", Package: taskPackage.Ref, Task: task.Ref, Dimension: task.Dimension,
		Seed: 42, Repetition: 0, InputSHA256: testSignalDigest("capability-input"), ExpectedOutput: []byte(`{"answer":"42"}`),
		Execution: spec, CreatedAt: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
	}
	if err := instance.Validate(); err != nil {
		t.Fatal(err)
	}

	score := 0.75
	scorer := task.Scorer
	result := CapabilityTaskResult{
		InstanceID: instance.InstanceID, Task: task.Ref, Dimension: task.Dimension, Status: CapabilityTaskScored,
		Score: &score, Scorer: &scorer, ErrorClass: "format_constraint",
		Assertions: []CapabilityAssertionResult{
			{ID: "answer", Passed: true, Weight: 3, Earned: 3},
			{ID: "format", Passed: false, Weight: 1, Earned: 0, Reason: "输出格式不符"},
		},
		ExecutionID: "execution-1", CapturedAt: time.Date(2026, 8, 11, 12, 1, 0, 0, time.UTC),
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}

	unsupported := CapabilityTaskResult{
		InstanceID: instance.InstanceID, Task: task.Ref, Dimension: task.Dimension,
		Status: CapabilityTaskUnsupported, Reason: "协议不支持工具调用", CapturedAt: result.CapturedAt,
	}
	if err := unsupported.Validate(); err != nil {
		t.Fatal(err)
	}
	if unsupported.Score != nil {
		t.Fatal("不支持状态不能被静默记为零分")
	}

	scoringInput := CapabilityScoringInput{Task: task, Instance: instance, ExpectedOutput: []byte(`{"answer":"different"}`)}
	if err := scoringInput.Validate(); err == nil {
		t.Fatal("判分输入与实例冻结答案不一致时应被拒绝")
	}

	instance.Execution.Purpose = PurposeQuickTest
	if err := instance.Validate(); err == nil {
		t.Fatal("能力任务实例使用非 capability_eval 目的时应被拒绝")
	}
}

func capabilityTaskPackageFixture() CapabilityTaskPackage {
	dimensions := CapabilityDimensions()
	weights := make(map[CapabilityDimension]float64, len(dimensions))
	tasks := make([]CapabilityTaskDefinition, len(dimensions))
	for index, dimension := range dimensions {
		weights[dimension] = 1.0 / float64(len(dimensions))
		tasks[index] = CapabilityTaskDefinition{
			Ref:       VersionedRef{ID: "capability.task." + string(dimension), SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
			Dimension: dimension, Difficulty: "core", Visibility: CapabilityTaskParameterized,
			Modalities: []CapabilityModality{CapabilityModalityText}, Protocols: []Protocol{ProtocolResponses},
			RequestProfiles: []string{"responses.standard.v1"}, ThinkingLevels: []ThinkingLevel{ThinkingLow},
			Generator:       VersionedRef{ID: "capability.generator." + string(dimension), SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
			SeedRuleVersion: "seed-v1",
			Scorer:          VersionedRef{ID: "capability.scorer.exact", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
			ScorerKind:      CapabilityScorerExactMatch, ScorerConfig: []byte(`{"caseSensitive":true}`),
			ExpectedContract: []byte(`{"type":"string"}`), MaximumInputTokens: 256, MaximumOutputTokens: 128,
			TimeoutMillis: 30_000, Repetitions: 2, Weight: 1, DisclosureRisk: DisclosureRiskLow,
		}
	}
	return CapabilityTaskPackage{
		Ref:              VersionedRef{ID: "capability.pack.standard", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		Description:      "七维离线固定夹具",
		ScoringModel:     VersionedRef{ID: "capability.aggregate.weighted", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		DimensionWeights: weights, Tasks: tasks,
	}
}
