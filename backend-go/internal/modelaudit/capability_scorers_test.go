package modelaudit

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestBuiltinCapabilityScorers(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		scorer, input := builtinScoringFixture(t, CapabilityScorerExactMatch,
			`{"caseSensitive":false,"trimSpace":true,"collapseWhitespace":true}`,
			`"Hello world"`, CapabilityObservedOutput{Text: "  HELLO   world  "})
		score, err := scorer.Score(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		assertCapabilityScore(t, score, 1, "")
	})

	t.Run("set_partial", func(t *testing.T) {
		scorer, input := builtinScoringFixture(t, CapabilityScorerSetMatch,
			`{"caseSensitive":true,"trimSpace":true}`,
			`["a","b"]`, CapabilityObservedOutput{StructuredOutput: []byte(`["a","c"]`)})
		score, err := scorer.Score(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		assertCapabilityScore(t, score, 0.5, "set_mismatch")
	})

	t.Run("numeric", func(t *testing.T) {
		scorer, input := builtinScoringFixture(t, CapabilityScorerNumericTolerance,
			`{"absoluteTolerance":0.1,"relativeTolerance":0}`,
			`10`, CapabilityObservedOutput{Text: "10.05"})
		score, err := scorer.Score(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		assertCapabilityScore(t, score, 1, "")
	})

	t.Run("json_structure_partial", func(t *testing.T) {
		scorer, input := builtinScoringFixture(t, CapabilityScorerJSONStructure,
			`{"requiredPaths":["/answer"],"forbiddenPaths":["/debug"],"types":{"/answer":"string"},"exactPaths":["/answer"],"allowAdditionalTopLevel":false,"allowedTopLevel":["answer"]}`,
			`{"answer":"ok"}`, CapabilityObservedOutput{Text: `{"answer":"wrong","debug":true}`})
		score, err := scorer.Score(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		assertCapabilityScore(t, score, 0.5, "json_constraint")
		if len(score.Assertions) != 6 {
			t.Fatalf("JSON 结构断言数 = %d", len(score.Assertions))
		}
	})

	t.Run("tool_call_partial", func(t *testing.T) {
		scorer, input := builtinScoringFixture(t, CapabilityScorerToolCall,
			`{"orderSensitive":true,"allowExtraCalls":false,"requireFinalText":true}`,
			`{"calls":[{"name":"search","arguments":{"query":"expected"}}]}`,
			CapabilityObservedOutput{ToolCalls: []ToolCall{{Name: "search", Arguments: []byte(`{"query":"other"}`)}}})
		score, err := scorer.Score(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		assertCapabilityScore(t, score, 0.5, "tool_call_mismatch")
	})

	t.Run("tool_call_unordered_duplicate_names", func(t *testing.T) {
		scorer, input := builtinScoringFixture(t, CapabilityScorerToolCall,
			`{"orderSensitive":false,"allowExtraCalls":false,"requireFinalText":false}`,
			`{"calls":[{"name":"search","arguments":{"query":"first"}},{"name":"search","arguments":{"query":"second"}}]}`,
			CapabilityObservedOutput{ToolCalls: []ToolCall{
				{Name: "search", Arguments: []byte(`{"query":"second"}`)},
				{Name: "search", Arguments: []byte(`{"query":"first"}`)},
			}})
		score, err := scorer.Score(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		assertCapabilityScore(t, score, 1, "")
	})

	t.Run("explicit_assertions_partial", func(t *testing.T) {
		scorer, input := builtinScoringFixture(t, CapabilityScorerAssertions,
			`{"assertions":[{"id":"greeting","kind":"text_contains","weight":1,"caseSensitive":false},{"id":"status","kind":"json_pointer_equals","weight":1,"path":"/status"}]}`,
			`{"greeting":"hello","status":"ok"}`,
			CapabilityObservedOutput{Text: "HELLO user", StructuredOutput: []byte(`{"status":"bad"}`)})
		score, err := scorer.Score(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		assertCapabilityScore(t, score, 0.5, "assertion_failed")
	})
}

func TestJSONValueEqualityUsesNumericSemantics(t *testing.T) {
	if !rawJSONValuesEqual([]byte(`{"value":1}`), []byte(`{"value":1.0}`)) {
		t.Fatal("JSON 数字的等价性不应依赖文本表示")
	}
}

func TestJSONStructureScorerTreatsMalformedOutputAsScoredFailure(t *testing.T) {
	scorer, input := builtinScoringFixture(t, CapabilityScorerJSONStructure,
		`{"requiredPaths":["/answer"],"forbiddenPaths":[],"types":{},"exactPaths":[],"allowAdditionalTopLevel":true,"allowedTopLevel":[]}`,
		`{"answer":"ok"}`, CapabilityObservedOutput{Text: "not-json"})
	score, err := scorer.Score(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	assertCapabilityScore(t, score, 0, "invalid_format")
}

func TestBuiltinCapabilityScorerRejectsUnknownConfigAndWrongVersion(t *testing.T) {
	scorer, input := builtinScoringFixture(t, CapabilityScorerExactMatch,
		`{"caseSensitive":true,"trimSpace":true,"collapseWhitespace":false}`,
		`"ok"`, CapabilityObservedOutput{Text: "ok"})
	input.Task.ScorerConfig = []byte(`{"caseSensitive":true,"unknown":1}`)
	if _, err := scorer.Score(context.Background(), input); err == nil {
		t.Fatal("未知判分配置字段应被拒绝")
	}

	_, input = builtinScoringFixture(t, CapabilityScorerExactMatch,
		`{"caseSensitive":true,"trimSpace":true,"collapseWhitespace":false}`,
		`"ok"`, CapabilityObservedOutput{Text: "ok"})
	input.Task.Scorer.ImplementationVersion = "other"
	if _, err := scorer.Score(context.Background(), input); err == nil {
		t.Fatal("任务引用错误判分器版本时应被拒绝")
	}
}

func builtinScoringFixture(t *testing.T, kind CapabilityScorerKind, config, expected string, observed CapabilityObservedOutput) (*BuiltinCapabilityScorer, CapabilityScoringInput) {
	t.Helper()
	scorer, err := NewBuiltinCapabilityScorer(kind)
	if err != nil {
		t.Fatal(err)
	}
	taskPackage := capabilityTaskPackageFixture()
	task := taskPackage.Tasks[0]
	task.Scorer = scorer.Descriptor().Ref
	task.ScorerKind = kind
	task.ScorerConfig = []byte(config)
	spec := validSpec(ProtocolResponses, "channel-capability-scorer")
	spec.Purpose = PurposeCapabilityEval
	instance := CapabilityTaskInstance{
		InstanceID: "capability.scorer.instance", Package: taskPackage.Ref, Task: task.Ref, Dimension: task.Dimension,
		Seed: 7, Repetition: 0, InputSHA256: testSignalDigest("scorer-input"), ExpectedOutput: []byte(expected),
		Execution: spec, CreatedAt: time.Date(2026, 8, 11, 13, 0, 0, 0, time.UTC),
	}
	return scorer, CapabilityScoringInput{
		Task: task, Instance: instance, ExpectedOutput: []byte(expected), Observed: observed,
		Evidence: []EvidenceReference{testSignalEvidence("scorer-output")},
	}
}

func assertCapabilityScore(t *testing.T, score CapabilityScore, expected float64, errorClass string) {
	t.Helper()
	if math.Abs(score.Score-expected) > 1e-12 || score.ErrorClass != errorClass {
		t.Fatalf("能力判分 = %#v，期望 score=%v errorClass=%q", score, expected, errorClass)
	}
	if err := score.Validate(); err != nil {
		t.Fatal(err)
	}
}
