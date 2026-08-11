package modelaudit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestBuiltinIdentityStrategiesAreProductionReachable(t *testing.T) {
	registry, err := NewStrategyRegistry(BuiltinIdentityStrategies()...)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 7 {
		t.Fatalf("生产身份策略数量 = %d", len(descriptors))
	}
	expected := map[string]struct{}{
		BuiltinMetadataStrategyID: {}, BuiltinSolReasoningStrategyID: {}, BuiltinSolRewriteStrategyID: {},
		BuiltinSolCoverageStrategyID: {}, BuiltinSolProbabilityStrategyID: {}, BuiltinSolModeStrategyID: {},
		BuiltinSolVariationStrategyID: {},
	}
	for _, descriptor := range descriptors {
		if _, found := expected[descriptor.Ref.ID]; !found {
			t.Fatalf("出现未知生产身份策略 %q", descriptor.Ref.ID)
		}
		delete(expected, descriptor.Ref.ID)
	}
	if len(expected) != 0 {
		t.Fatalf("生产身份策略缺失: %#v", expected)
	}

	target := builtinProbeTarget(t)
	tests := []struct {
		strategyID string
		config     string
		count      int
	}{
		{BuiltinSolReasoningStrategyID, `{"sampleCount":4}`, 4},
		{BuiltinSolRewriteStrategyID, `{"sampleCount":4}`, 4},
		{BuiltinSolCoverageStrategyID, `{"sampleCount":4}`, 4},
		{BuiltinSolProbabilityStrategyID, `{"sampleCount":6}`, 6},
		{BuiltinSolModeStrategyID, `{"sampleCount":6}`, 6},
		{BuiltinSolVariationStrategyID, `{"sampleCount":6}`, 6},
	}
	for _, test := range tests {
		t.Run(test.strategyID, func(t *testing.T) {
			strategy, found := registry.Get(test.strategyID)
			if !found {
				t.Fatal("策略未注册")
			}
			snapshots, err := registry.Freeze([]StrategySelection{{
				StrategyID: test.strategyID, Enabled: true, Config: json.RawMessage(test.config),
			}})
			if err != nil {
				t.Fatal(err)
			}
			plans, err := strategy.Plan(context.Background(), StrategyPlanInput{
				RunID: "run.probe", Target: target, Config: snapshots[0], BaseSeed: 17, MaximumSamples: strategy.Descriptor().MaximumSamples,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(plans) != test.count {
				t.Fatalf("样本计划数量 = %d", len(plans))
			}
			for ordinal, plan := range plans {
				if plan.Ordinal != ordinal || plan.Execution.Purpose != PurposeIdentityProbe || plan.Execution.Target.ChannelID != target.ChannelID {
					t.Fatalf("样本计划 %d = %#v", ordinal, plan)
				}
			}
		})
	}
}

func TestBuiltinReasoningAndProbabilityEvaluationBoundaries(t *testing.T) {
	registry, err := NewStrategyRegistry(BuiltinIdentityStrategies()...)
	if err != nil {
		t.Fatal(err)
	}
	target := builtinProbeTarget(t)

	reasoning, found := registry.Get(BuiltinSolReasoningStrategyID)
	if !found {
		t.Fatal("reasoning profile 策略未注册")
	}
	reasoningSnapshot := freezeBuiltinProbe(t, registry, BuiltinSolReasoningStrategyID, `{"sampleCount":4}`)
	reasoningPlans, err := reasoning.Plan(context.Background(), StrategyPlanInput{
		RunID: "run.reasoning", Target: target, Config: reasoningSnapshot, BaseSeed: 100, MaximumSamples: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	reasoningSamples := builtinProbeSamples(t, reasoningSnapshot, reasoningPlans, target, func(ordinal int) (string, Usage) {
		values := []int{10, 12, 40, 44}
		return "8", Usage{InputTokens: 20, OutputTokens: 8, ReasoningTokens: values[ordinal], TotalTokens: 28}
	})
	evaluation, err := reasoning.Evaluate(context.Background(), StrategyEvaluationInput{Config: reasoningSnapshot, Samples: reasoningSamples})
	if err != nil {
		t.Fatal(err)
	}
	signal, err := identitySignalFromEvaluation(evaluation)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Status != EvaluationCompleted || signal.Status != SignalObserved || signal.Score == nil || *signal.Score < 0.99 {
		t.Fatalf("reasoning profile 评估 = %#v signal=%#v", evaluation, signal)
	}

	probability, found := registry.Get(BuiltinSolProbabilityStrategyID)
	if !found {
		t.Fatal("probability profile 策略未注册")
	}
	probabilitySnapshot := freezeBuiltinProbe(t, registry, BuiltinSolProbabilityStrategyID, `{"sampleCount":6}`)
	probabilityPlans, err := probability.Plan(context.Background(), StrategyPlanInput{
		RunID: "run.probability", Target: target, Config: probabilitySnapshot, BaseSeed: 200, MaximumSamples: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{"France", "owl", "3", "Japan", "robin", "3"}
	probabilitySamples := builtinProbeSamples(t, probabilitySnapshot, probabilityPlans, target, func(ordinal int) (string, Usage) {
		return outputs[ordinal], Usage{InputTokens: 8, OutputTokens: 1, TotalTokens: 9}
	})
	evaluation, err = probability.Evaluate(context.Background(), StrategyEvaluationInput{Config: probabilitySnapshot, Samples: probabilitySamples})
	if err != nil {
		t.Fatal(err)
	}
	signal, err = identitySignalFromEvaluation(evaluation)
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.Status != EvaluationInsufficientEvidence || signal.Status != SignalInsufficientEvidence || signal.SampleCount != 6 || len(signal.Features) == 0 {
		t.Fatalf("无基线概率评估 = %#v signal=%#v", evaluation, signal)
	}
}

func TestReturnedModelAssessmentsDetectMixtureAndStageSwitch(t *testing.T) {
	target := builtinProbeTarget(t)
	startedAt := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	samples := make([]StrategySample, 8)
	for ordinal := range samples {
		model := "gpt-5.6-sol"
		if ordinal >= 4 {
			model = "gpt-5.6-terra"
		}
		samples[ordinal] = StrategySample{
			SampleID: fmt.Sprintf("sample-%d", ordinal), CapturedAt: startedAt.Add(time.Duration(ordinal) * time.Second),
			Execution: &ExecutionResult{ReturnedModel: model},
		}
	}
	classification, mixture, temporal, err := returnedModelAssessments(target, samples)
	if err != nil {
		t.Fatal(err)
	}
	if classification == nil || mixture == nil || temporal == nil || temporal.Pattern != TemporalStageSwitch {
		t.Fatalf("返回模型判定 = classification=%#v mixture=%#v temporal=%#v", classification, mixture, temporal)
	}
	if len(mixture.Components) != 2 {
		t.Fatalf("混用分量 = %#v", mixture.Components)
	}
	for _, component := range mixture.Components {
		if component.Proportion < 0.49 || component.Proportion > 0.51 {
			t.Fatalf("混用比例 = %#v", mixture.Components)
		}
	}
	if !returnedModelEvidenceSupportsSuspicion(target, classification, mixture, temporal) {
		t.Fatal("明确返回模型混用应支持疑似结论")
	}
}

func builtinProbeTarget(t *testing.T) TargetSnapshot {
	t.Helper()
	thinking, err := ResolveThinking(ProtocolResponses, ThinkingMedium)
	if err != nil {
		t.Fatal(err)
	}
	return TargetSnapshot{
		ChannelID: "channel-responses", ChannelKind: ChannelKindResponses, ChannelName: "fixture", ChannelStatus: "enabled",
		ServiceType: "responses", Protocol: ProtocolResponses, WireProtocol: ProtocolResponses,
		RequestedModel: "gpt-5.6-sol", ResolvedModel: "gpt-5.6-sol", DefaultModel: "gpt-5.6-sol",
		Thinking: ThinkingMedium, ThinkingMap: thinking, RequestProfile: "responses.standard.v1", CapturedAt: time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC),
	}
}

func freezeBuiltinProbe(t *testing.T, registry *StrategyRegistry, strategyID, config string) StrategyConfigSnapshot {
	t.Helper()
	snapshots, err := registry.Freeze([]StrategySelection{{
		StrategyID: strategyID, Enabled: true, Config: json.RawMessage(config),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshots[0]
}

func builtinProbeSamples(
	t *testing.T,
	snapshot StrategyConfigSnapshot,
	plans []SamplePlan,
	target TargetSnapshot,
	result func(int) (string, Usage),
) []StrategySample {
	t.Helper()
	startedAt := time.Date(2026, 8, 11, 13, 0, 0, 0, time.UTC)
	samples := make([]StrategySample, len(plans))
	for ordinal, plan := range plans {
		text, usage := result(ordinal)
		finishedAt := startedAt.Add(time.Duration(ordinal+1) * time.Second)
		executionTarget := target
		executionTarget.Thinking = plan.Execution.Thinking
		execution := ExecutionResult{
			ExecutionID: fmt.Sprintf("execution-%d", ordinal), Purpose: PurposeIdentityProbe, Target: executionTarget,
			Status: StatusCompleted, ProtocolTerminal: ProtocolTerminalCompleted, DeclaredModel: target.ResolvedModel,
			ReturnedModel: target.ResolvedModel, Text: text, Usage: usage,
			Retry: RetrySummary{Attempts: 1}, RequestSummary: RedactedRequestSummary{Profile: target.RequestProfile},
			StartedAt: startedAt.Add(time.Duration(ordinal) * time.Second), FinishedAt: &finishedAt,
		}
		samples[ordinal] = StrategySample{
			SampleID: plan.SampleID, RunID: "run.fixture", Strategy: snapshot.Ref, ConfigSHA256: snapshot.ConfigSHA256,
			Ordinal: ordinal, Seed: plan.Seed, Applicability: ApplicabilityApplicable, Execution: &execution, CapturedAt: finishedAt,
		}
		if err := samples[ordinal].Validate(); err != nil {
			t.Fatalf("样本 %d 无效: %v", ordinal, err)
		}
	}
	return samples
}
