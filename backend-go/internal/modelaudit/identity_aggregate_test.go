package modelaudit

import (
	"math"
	"testing"
	"time"
)

func TestAggregateIdentityLimitsCorrelatedSignals(t *testing.T) {
	config, input := identityAggregationFixture(t)
	report, err := AggregateIdentity(config, input)
	if err != nil {
		t.Fatal(err)
	}
	if report.Conclusion != IdentityMatched || !report.Formal || !report.Qualification.Eligible {
		t.Fatalf("匹配聚合报告 = %#v", report)
	}
	if report.ApplicableSignals != 3 || report.CorrelationGroups != 2 || len(report.GroupScores) != 2 {
		t.Fatalf("相关组计数 = %#v", report.GroupScores)
	}
	// Juice 与合成覆盖属于同一相关组，先得到 0.5；再与元数据组的 1.0 等权融合。
	if math.Abs(report.ConsistencyScore-0.75) > 1e-12 {
		t.Fatalf("相关组限权后的分数 = %v", report.ConsistencyScore)
	}
	if len(report.BaselineChecks) != 1 || !report.BaselineChecks[0].Compatible || report.Qualification.CompatibleBaselines != 1 {
		t.Fatalf("基线兼容结果 = %#v", report.BaselineChecks)
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateIdentitySubstitutionMixtureAndOOD(t *testing.T) {
	t.Run("substitution", func(t *testing.T) {
		config, input := identityAggregationFixture(t)
		for index := range input.Signals {
			score := 0.1
			input.Signals[index].Score = &score
		}
		input.CandidateFit = &CandidateClassification{
			BestCandidate: "terra",
			Fits:          []CandidateFit{{Candidate: "sol", JSD: 0.4, LogLikelihood: -20}, {Candidate: "terra", JSD: 0.02, LogLikelihood: -2}},
		}
		input.Temporal = mustTemporalAssessment(t, temporalFixture(0.1, 0.12, 0.11, 0.09, 0.13, 0.1, 0.12, 0.11))
		report, err := AggregateIdentity(config, input)
		if err != nil {
			t.Fatal(err)
		}
		if report.Conclusion != IdentitySuspectedSubstitution || !report.Formal {
			t.Fatalf("替换结论 = %#v", report)
		}
	})

	t.Run("mixture", func(t *testing.T) {
		config, input := identityAggregationFixture(t)
		input.Mixture = &MixtureEstimate{
			Components: []MixtureComponent{
				{Candidate: "sol", Proportion: 0.5, Lower: 0.4, Upper: 0.6},
				{Candidate: "terra", Proportion: 0.5, Lower: 0.4, Upper: 0.6},
			},
			LogLikelihood: -10, Iterations: 20, Converged: true,
		}
		report, err := AggregateIdentity(config, input)
		if err != nil {
			t.Fatal(err)
		}
		if report.Conclusion != IdentitySuspectedMixture || !report.Formal || report.Mixture == nil {
			t.Fatalf("混用结论 = %#v", report)
		}
	})

	t.Run("ood", func(t *testing.T) {
		config, input := identityAggregationFixture(t)
		input.CandidateFit = &CandidateClassification{
			OOD:  true,
			Fits: []CandidateFit{{Candidate: "sol", JSD: 0.4, LogLikelihood: -20}, {Candidate: "terra", JSD: 0.45, LogLikelihood: -22}},
		}
		report, err := AggregateIdentity(config, input)
		if err != nil {
			t.Fatal(err)
		}
		if report.Conclusion != IdentityUnknown || !report.Formal || len(report.AlternativeExplanations) == 0 {
			t.Fatalf("OOD 结论 = %#v", report)
		}
	})

	t.Run("stage_switch", func(t *testing.T) {
		config, input := identityAggregationFixture(t)
		input.Temporal = mustTemporalAssessment(t, temporalFixture(
			0.9, 0.92, 0.91, 0.89, 0.9, 0.93, 0.1, 0.12, 0.11, 0.09, 0.1, 0.13,
		))
		report, err := AggregateIdentity(config, input)
		if err != nil {
			t.Fatal(err)
		}
		if report.Conclusion != IdentitySuspectedMixture || report.Temporal == nil || report.Temporal.Pattern != TemporalStageSwitch {
			t.Fatalf("阶段切换结论 = %#v", report)
		}
	})
}

func TestAggregateIdentityInsufficientEvidence(t *testing.T) {
	t.Run("run_samples", func(t *testing.T) {
		config, input := identityAggregationFixture(t)
		input.SampleCount = 4
		report, err := AggregateIdentity(config, input)
		if err != nil {
			t.Fatal(err)
		}
		if report.Conclusion != IdentityInsufficientEvidence || report.Formal || report.Qualification.Eligible {
			t.Fatalf("运行样本不足结论 = %#v", report)
		}
	})

	t.Run("baseline_incompatible", func(t *testing.T) {
		config, input := identityAggregationFixture(t)
		input.Baselines[0].Protocols = []Protocol{ProtocolChat}
		report, err := AggregateIdentity(config, input)
		if err != nil {
			t.Fatal(err)
		}
		if report.Conclusion != IdentityInsufficientEvidence || report.Formal || report.Qualification.Eligible {
			t.Fatalf("基线不兼容结论 = %#v", report)
		}
		if len(report.BaselineChecks) != 1 || report.BaselineChecks[0].Compatible {
			t.Fatalf("基线不兼容检查 = %#v", report.BaselineChecks)
		}
	})

	t.Run("short_anomaly", func(t *testing.T) {
		config, input := identityAggregationFixture(t)
		input.Temporal = mustTemporalAssessment(t, temporalFixture(
			0.9, 0.92, 0.91, 0.89, 0.2, 0.22, 0.93, 0.9, 0.91, 0.89,
		))
		report, err := AggregateIdentity(config, input)
		if err != nil {
			t.Fatal(err)
		}
		if report.Conclusion != IdentityInsufficientEvidence || report.Formal || report.Temporal.Pattern != TemporalShortAnomaly {
			t.Fatalf("短期异常正式资格 = %#v", report)
		}
	})
}

func identityAggregationFixture(t *testing.T) (IdentityAggregationConfig, IdentityAggregationInput) {
	t.Helper()
	strategy := VersionedRef{ID: "strategy.identity.juice", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"}
	baseline := identityBaselineFixture(strategy)
	baselineRef := baseline.Ref
	config := IdentityAggregationConfig{
		Aggregator:        VersionedRef{ID: "identity.aggregator", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
		DeclaredCandidate: "sol", MinimumSamples: 8, MinimumApplicableSignals: 3, MinimumFormalSignals: 2,
		MinimumCorrelationGroups: 2, MatchedThreshold: 0.7, SubstitutionThreshold: 0.3, MixtureComponentLowerBound: 0.2,
		RequireCompatibleBaselineForFormal: true, RequireCandidateFitForMatch: true,
		RequireMixtureEstimateForMatch: true, RequireTemporalAssessmentForMatch: true,
		GroupWeights: map[string]float64{"behavior.juice": 1, "metadata": 1},
		SignalRules: []IdentitySignalRule{
			{
				SignalID: "identity.juice", MinimumSamples: 4, FormalEligible: true, Strategy: strategy,
				Baseline: BaselineRequirement{Required: true, BaselineID: baseline.Ref.ID, BaselineVersion: baseline.Ref.Version, MinimumSamples: 50},
			},
			{
				SignalID: "identity.synthetic", MinimumSamples: 4, FormalEligible: true,
				Strategy: VersionedRef{ID: "strategy.identity.synthetic", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
			},
			{
				SignalID: "identity.metadata", MinimumSamples: 4, FormalEligible: true,
				Strategy: VersionedRef{ID: "strategy.identity.metadata", SemanticVersion: "1.0.0", ImplementationVersion: "fixture-1"},
			},
		},
	}
	input := IdentityAggregationInput{
		DeclaredModel: "gpt-5.6-sol", RequestedModel: "gpt-5.6-sol", ReturnedModels: []string{"gpt-5.6-sol"},
		Protocol: ProtocolResponses, Thinking: ThinkingLow, SampleCount: 8,
		Signals: []IdentitySignal{
			observedIdentitySignal("identity.juice", SignalJuiceBehavior, "behavior.juice", 1, &baselineRef),
			observedIdentitySignal("identity.synthetic", SignalSyntheticCoverage, "behavior.juice", 0, nil),
			observedIdentitySignal("identity.metadata", SignalMetadataConsistency, "metadata", 1, nil),
		},
		CandidateFit: &CandidateClassification{
			BestCandidate: "sol",
			Fits:          []CandidateFit{{Candidate: "sol", JSD: 0.01, LogLikelihood: -1}, {Candidate: "terra", JSD: 0.3, LogLikelihood: -20}},
		},
		Mixture: &MixtureEstimate{
			Components: []MixtureComponent{
				{Candidate: "sol", Proportion: 0.98, Lower: 0.94, Upper: 1},
				{Candidate: "terra", Proportion: 0.02, Lower: 0, Upper: 0.06},
			},
			LogLikelihood: -2, Iterations: 10, Converged: true,
		},
		Temporal:  mustTemporalAssessment(t, temporalFixture(0.9, 0.92, 0.91, 0.89, 0.93, 0.9, 0.91, 0.92)),
		Baselines: []BaselineSnapshot{baseline},
	}
	return config, input
}

func observedIdentitySignal(id string, kind IdentitySignalKind, group string, score float64, baseline *BaselineRef) IdentitySignal {
	return IdentitySignal{
		ID: id, Kind: kind, CorrelationGroup: group, Status: SignalObserved, SampleCount: 8,
		Reliability: 0.8, Score: &score, Features: []byte(`{"fixture":true}`), Baseline: baseline,
	}
}

func identityBaselineFixture(strategy VersionedRef) BaselineSnapshot {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return BaselineSnapshot{
		SchemaVersion: BaselineSchemaVersion,
		Ref:           BaselineRef{ID: "baseline.sol.identity", Version: "1.0.0", SHA256: testSignalDigest("baseline-sol")},
		Origin:        BaselineOriginUserGenerated, TargetModels: []string{"gpt-5.6-sol"},
		Protocols: []Protocol{ProtocolResponses}, ThinkingLevels: []ThinkingLevel{ThinkingLow},
		CollectionStartedAt: startedAt, CollectionEndedAt: startedAt.Add(24 * time.Hour), SampleSize: 100,
		FeatureSchema: FeatureSchemaRef{ID: "identity.features", Version: "1.0.0"},
		Source:        "fixed fixture", GenerationMethod: "offline deterministic fixture",
		CompatibleStrategies: []VersionedRef{strategy},
	}
}

func mustTemporalAssessment(t *testing.T, observations []TemporalObservation) *TemporalAssessment {
	t.Helper()
	assessment, err := AnalyzeTemporalIdentity(testTemporalConfig(), observations)
	if err != nil {
		t.Fatal(err)
	}
	return &assessment
}
