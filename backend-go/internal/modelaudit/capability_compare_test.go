package modelaudit

import (
	"reflect"
	"testing"
	"time"
)

func TestCompareCapabilityRunsAllowsEquivalentSnapshots(t *testing.T) {
	left := capabilityComparisonSnapshotFixture(t)
	right := left

	decision, err := CompareCapabilityRuns(capabilityComparatorFixture(), left, right)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.DirectlyComparable || len(decision.Mismatches) != 0 {
		t.Fatalf("等价运行应可直接比较: %#v", decision)
	}
}

func TestCompareCapabilityRunsReportsFairnessMismatches(t *testing.T) {
	left := capabilityComparisonSnapshotFixture(t)
	right := left
	right.PackageSHA256 = testSignalDigest("other-package")
	right.Protocol.RequestProfile = "responses.native_codex.v1"
	right.Protocol.RequestProfileSupport = SupportChannelProfile
	right.Protocol.WireProtocol = ProtocolChat
	right.Protocol.WireFeatures = protocolFeatureSnapshot(protocolDescriptors[ProtocolChat])
	right.Protocol.Converted = true
	right.Thinking.Level = ThinkingHigh
	right.Thinking.Effort = "high"
	right.Settings.MaxOutputTokens++
	right.Model.RequestedModel = "gpt-5.6-terra"
	right.Model.ResolvedModel = "gpt-5.6-terra"

	decision, err := CompareCapabilityRuns(capabilityComparatorFixture(), left, right)
	if err != nil {
		t.Fatal(err)
	}
	want := []CapabilityComparisonMismatch{
		CapabilityMismatchTaskPackage,
		CapabilityMismatchProtocolCapability,
		CapabilityMismatchProtocolConversion,
		CapabilityMismatchThinking,
		CapabilityMismatchRunSettings,
		CapabilityMismatchRequestedModel,
		CapabilityMismatchResolvedModel,
	}
	if decision.DirectlyComparable || !reflect.DeepEqual(decision.Mismatches, want) {
		t.Fatalf("公平比较不兼容原因 = %#v，期望 %#v", decision, want)
	}
}

func TestCompareCapabilityRunsUsesDefaultModelOnlyWhenSelected(t *testing.T) {
	left := capabilityComparisonSnapshotFixture(t)
	right := left
	right.Model.DefaultModel = "changed-unused-default"
	decision, err := CompareCapabilityRuns(capabilityComparatorFixture(), left, right)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.DirectlyComparable {
		t.Fatalf("显式模型相同时，未使用的默认模型变化不应阻止比较: %#v", decision)
	}

	left.Model.RequestedModel = ""
	left.Model.DefaultModel = "gpt-5.6-sol"
	left.Model.UsedDefault = true
	right = left
	right.Model.DefaultModel = "gpt-5.6-terra"
	decision, err = CompareCapabilityRuns(capabilityComparatorFixture(), left, right)
	if err != nil {
		t.Fatal(err)
	}
	if decision.DirectlyComparable || !reflect.DeepEqual(decision.Mismatches, []CapabilityComparisonMismatch{CapabilityMismatchDefaultModel}) {
		t.Fatalf("实际使用的默认模型变化必须阻止直接比较: %#v", decision)
	}
}

func TestNewCapabilityComparisonSnapshotFreezesProtocolModelAndSettings(t *testing.T) {
	snapshot, plan, spec, target := capabilityComparisonFixture(t)
	if snapshot.Package != plan.Package || snapshot.PackageSHA256 != plan.PackageSHA256 ||
		snapshot.Protocol.Protocol != ProtocolResponses || snapshot.Protocol.WireProtocol != ProtocolResponses || snapshot.Protocol.Converted ||
		snapshot.Thinking.Level != ThinkingMedium || snapshot.Thinking.Effort != "medium" ||
		snapshot.Settings.Preset != plan.Preset || snapshot.Settings.MaxOutputTokens != spec.MaxOutputTokens ||
		snapshot.Model.RequestedModel != target.RequestedModel || snapshot.Model.ResolvedModel != target.ResolvedModel || snapshot.Model.UsedDefault {
		t.Fatalf("能力公平比较快照 = %#v", snapshot)
	}

	target.RequestProfile = "responses.native_codex.v1"
	if _, err := NewCapabilityComparisonSnapshot(plan, spec, target); err == nil {
		t.Fatal("目标请求轮廓与执行设置不一致时应拒绝快照")
	}
}

func TestNewCapabilityComparisonSnapshotCanonicalizesTaskOrderAndConversion(t *testing.T) {
	native, plan, spec, target := capabilityComparisonFixture(t)
	for left, right := 0, len(plan.Tasks)-1; left < right; left, right = left+1, right-1 {
		plan.Tasks[left], plan.Tasks[right] = plan.Tasks[right], plan.Tasks[left]
	}
	reordered, err := NewCapabilityComparisonSnapshot(plan, spec, target)
	if err != nil {
		t.Fatal(err)
	}
	if reordered.Settings.TaskSelectionSHA256 != native.Settings.TaskSelectionSHA256 {
		t.Fatalf("任务选择哈希受切片顺序影响: %q != %q", reordered.Settings.TaskSelectionSHA256, native.Settings.TaskSelectionSHA256)
	}

	target.WireProtocol = ProtocolChat
	target.ServiceType = "openai"
	target.ThinkingMap, err = ResolveThinking(ProtocolChat, target.Thinking)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := NewCapabilityComparisonSnapshot(plan, spec, target)
	if err != nil {
		t.Fatal(err)
	}
	if !converted.Protocol.Converted || converted.Protocol.WireProtocol != ProtocolChat ||
		converted.Protocol.WireFeatures != protocolFeatureSnapshot(protocolDescriptors[ProtocolChat]) {
		t.Fatalf("协议转换快照 = %#v", converted.Protocol)
	}
}

func capabilityComparisonSnapshotFixture(t *testing.T) CapabilityComparisonSnapshot {
	t.Helper()
	snapshot, _, _, _ := capabilityComparisonFixture(t)
	return snapshot
}

func capabilityComparisonFixture(t *testing.T) (CapabilityComparisonSnapshot, CapabilityRunPlan, ExecutionSpec, TargetSnapshot) {
	t.Helper()
	packageSnapshot := capabilityPackageSnapshotFixture(t)
	preset := capabilityPresetFixture(packageSnapshot, CapabilityPresetStandard, packageSnapshot.Package.Tasks)
	plan, err := PrepareCapabilityRun(preset, packageSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	spec := ExecutionSpec{
		Purpose:  PurposeCapabilityEval,
		Target:   ChannelTarget{ChannelID: "responses-1", ChannelKind: ChannelKindResponses},
		Protocol: ProtocolResponses, Model: "gpt-5.6-sol", Thinking: ThinkingMedium,
		RequestProfile: "responses.standard.v1", Stream: true, TimeoutMillis: 30_000, MaxOutputTokens: 128,
		Features: RequestFeatures{Tools: true}, Input: ExecutionInput{Prompt: "fixture"}, Redaction: RedactionDigest,
	}
	thinking, err := ResolveThinking(ProtocolResponses, ThinkingMedium)
	if err != nil {
		t.Fatal(err)
	}
	target := TargetSnapshot{
		ChannelID: "responses-1", ChannelKind: ChannelKindResponses, ChannelName: "fixture", ChannelStatus: "enabled",
		ServiceType: "responses", Protocol: ProtocolResponses, WireProtocol: ProtocolResponses,
		RequestedModel: "gpt-5.6-sol", ResolvedModel: "gpt-5.6-sol", DefaultModel: "gpt-5.6-sol-default",
		Thinking: ThinkingMedium, ThinkingMap: thinking, RequestProfile: "responses.standard.v1", CapturedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	snapshot, err := NewCapabilityComparisonSnapshot(plan, spec, target)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, plan, spec, target
}

func capabilityComparatorFixture() VersionedRef {
	return VersionedRef{ID: "capability.comparator.fairness", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"}
}
