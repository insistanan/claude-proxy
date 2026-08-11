package modelaudit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type fixtureStrategy struct {
	descriptor StrategyDescriptor
}

func (s fixtureStrategy) Descriptor() StrategyDescriptor {
	return s.descriptor
}

func (fixtureStrategy) ValidateConfig(config json.RawMessage) error {
	var value struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(config, &value); err != nil {
		return err
	}
	if value.Mode != "strict" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "mode 必须为 strict")
	}
	return nil
}

func (fixtureStrategy) Plan(context.Context, StrategyPlanInput) ([]SamplePlan, error) {
	return nil, nil
}

func (s fixtureStrategy) Evaluate(context.Context, StrategyEvaluationInput) (StrategyEvaluation, error) {
	return StrategyEvaluation{Strategy: s.descriptor.Ref, Status: EvaluationSkipped, Reason: "fixture"}, nil
}

func fixtureStrategyDescriptor(id string) StrategyDescriptor {
	return StrategyDescriptor{
		Ref:          VersionedRef{ID: id, SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		Kind:         StrategyKindIdentity,
		Description:  "固定夹具策略",
		ConfigSchema: json.RawMessage(`{"type":"object","required":["mode"]}`),
		Applicability: StrategyApplicability{
			ModelFamilies: []string{"gpt"}, Protocols: []Protocol{ProtocolResponses},
		},
		Pipeline: StrategyPipelineDescriptor{
			RequestGenerator: "fixture.generator.v1", ResponseNormalizer: "fixture.normalizer.v1",
			FeatureExtractor: "fixture.features.v1", Scorer: "fixture.scorer.v1", FailureClassifier: "fixture.failures.v1",
		},
		SeedRuleVersion: "seed-v1", MaximumSamples: 4, EstimatedMaxTokens: 4096,
		FormalEligible: true, CombinationPolicy: CombinationAllowAny, ParallelSafe: true, DisclosureRisk: DisclosureRiskLow,
	}
}

func TestStrategyRegistryFreeze(t *testing.T) {
	first := fixtureStrategy{descriptor: fixtureStrategyDescriptor("identity.first")}
	second := fixtureStrategy{descriptor: fixtureStrategyDescriptor("identity.second")}
	registry, err := NewStrategyRegistry(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(first); err == nil {
		t.Fatal("重复策略 ID 应被拒绝")
	}

	selections := []StrategySelection{
		{StrategyID: "identity.first", Enabled: true, Config: json.RawMessage(`{"mode":"strict","count":2}`)},
		{StrategyID: "identity.second", Enabled: false, Config: json.RawMessage(`{"count":2,"mode":"strict"}`)},
	}
	snapshots, err := registry.Freeze(selections)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || string(snapshots[0].Config) != `{"count":2,"mode":"strict"}` {
		t.Fatalf("配置快照 = %#v", snapshots)
	}
	if snapshots[0].ConfigSHA256 != snapshots[1].ConfigSHA256 {
		t.Fatalf("等价配置哈希不一致: %s != %s", snapshots[0].ConfigSHA256, snapshots[1].ConfigSHA256)
	}
	if _, err := registry.Freeze([]StrategySelection{{StrategyID: "identity.first", Enabled: true, Config: json.RawMessage(`[]`)}}); err == nil {
		t.Fatal("非对象配置应被拒绝")
	}
	if _, err := registry.Freeze([]StrategySelection{{StrategyID: "identity.first", Enabled: true, Config: json.RawMessage(`{"mode":"loose"}`)}}); err == nil {
		t.Fatal("策略配置校验错误应被返回")
	}
}

func TestStrategyRegistryRejectsIncompatibleCombination(t *testing.T) {
	firstDescriptor := fixtureStrategyDescriptor("identity.first")
	firstDescriptor.CombinationPolicy = CombinationAllowList
	firstDescriptor.CompatibleWith = []string{"identity.third"}
	registry, err := NewStrategyRegistry(
		fixtureStrategy{descriptor: firstDescriptor},
		fixtureStrategy{descriptor: fixtureStrategyDescriptor("identity.second")},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.Freeze([]StrategySelection{
		{StrategyID: "identity.first", Enabled: true, Config: json.RawMessage(`{"mode":"strict"}`)},
		{StrategyID: "identity.second", Enabled: true, Config: json.RawMessage(`{"mode":"strict"}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "不允许组合") {
		t.Fatalf("不兼容组合错误 = %v", err)
	}
}

func TestBaselineImportAndAuthorization(t *testing.T) {
	data := json.RawMessage(`{"distribution":{"sol":0.8,"terra":0.2}}`)
	_, digest, err := canonicalJSONObject(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest := fixtureBaselineManifest(digest)
	raw, err := json.Marshal(BaselineEnvelope{Manifest: manifest, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	asset, err := ImportBaseline(`D:\external\trusted.json`, raw)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Manifest.Origin != BaselineOriginLocalExternal || asset.Manifest.LocalSourcePath == "" {
		t.Fatalf("本地外部基线来源未冻结: %#v", asset.Manifest)
	}
	snapshot, err := NewBaselineSnapshot(asset)
	if err != nil || snapshot.Ref.SHA256 != digest {
		t.Fatalf("基线快照无效: %v, %#v", err, snapshot)
	}

	manifest.Ref.SHA256 = strings.Repeat("0", 64)
	raw, _ = json.Marshal(BaselineEnvelope{Manifest: manifest, Data: data})
	if _, err := ImportBaseline(`D:\external\trusted.json`, raw); err == nil {
		t.Fatal("内容哈希不匹配应被拒绝")
	}

	manifest = fixtureBaselineManifest(digest)
	manifest.Origin = BaselineOriginBuiltin
	manifest.Authorization = BaselineAuthorization{}
	raw, _ = json.Marshal(BaselineEnvelope{Manifest: manifest, Data: data})
	if _, err := ImportBaseline("", raw); err == nil {
		t.Fatal("缺少再分发授权的内置基线应被拒绝")
	}
}

func TestFreezeReportInput(t *testing.T) {
	strategy := fixtureStrategy{descriptor: fixtureStrategyDescriptor("identity.first")}
	registry, err := NewStrategyRegistry(strategy)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := registry.Freeze([]StrategySelection{{
		StrategyID: "identity.first", Enabled: true, Config: json.RawMessage(`{"mode":"strict"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	finished := now.Add(time.Second)
	result := ExecutionResult{
		ExecutionID: "execution-1", Purpose: PurposeIdentityProbe,
		Target: TargetSnapshot{ChannelID: "responses-1", ChannelKind: ChannelKindResponses, Protocol: ProtocolResponses, WireProtocol: ProtocolResponses, ResolvedModel: "gpt-test"},
		Status: StatusCompleted, ProtocolTerminal: ProtocolTerminalCompleted, Text: "fixture",
		Retry: RetrySummary{Attempts: 1}, RequestSummary: RedactedRequestSummary{Profile: "responses.standard.v1"},
		StartedAt: now, FinishedAt: &finished,
	}
	sample := StrategySample{
		SampleID: "sample-1", RunID: "run-1", Strategy: configs[0].Ref, ConfigSHA256: configs[0].ConfigSHA256,
		Applicability: ApplicabilityApplicable, Execution: &result, CapturedAt: finished,
	}
	feature, err := NewFeatureSnapshot(FeatureSchemaRef{ID: "identity.features", Version: "1.0.0"}, json.RawMessage(`{"score":0.75}`))
	if err != nil {
		t.Fatal(err)
	}
	evaluation := StrategyEvaluation{
		Strategy: configs[0].Ref, Status: EvaluationCompleted, Formal: true,
		SampleCount: 1, ApplicableSampleCount: 1, Features: []FeatureSnapshot{feature}, Metrics: json.RawMessage(`{"confidence":0.8}`),
	}
	input := FrozenReportInput{
		RunID: "run-1", StrategySetVersion: "1.0.0", Targets: []TargetSnapshot{result.Target},
		Strategies: configs, Samples: []StrategySample{sample}, Evaluations: []StrategyEvaluation{evaluation}, CreatedAt: finished,
	}
	frozen, err := FreezeReportInput(input)
	if err != nil {
		t.Fatal(err)
	}
	if !validSHA256(frozen.SnapshotSHA256) {
		t.Fatalf("报告快照哈希无效: %q", frozen.SnapshotSHA256)
	}
	input.Strategies[0].Config[0] = '['
	if string(frozen.Strategies[0].Config) != `{"mode":"strict"}` {
		t.Fatalf("冻结配置被外部修改: %s", frozen.Strategies[0].Config)
	}
}

func fixtureBaselineManifest(digest string) BaselineManifest {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return BaselineManifest{
		SchemaVersion: BaselineSchemaVersion,
		Ref:           BaselineRef{ID: "gpt.sol.reference", Version: "1.0.0", SHA256: digest},
		Origin:        BaselineOriginLocalExternal,
		TargetModels:  []string{"gpt-5.6-sol"}, Protocols: []Protocol{ProtocolResponses},
		CollectionStartedAt: start, CollectionEndedAt: start.Add(time.Hour), SampleSize: 20,
		FeatureSchema: FeatureSchemaRef{ID: "identity.features", Version: "1.0.0"},
		Source:        "user supplied fixture", GenerationMethod: "offline fixture",
		CompatibleStrategies: []VersionedRef{{ID: "identity.first", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"}},
	}
}
