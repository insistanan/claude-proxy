package eval

import "testing"

func TestJudgeExactAndNumeric(t *testing.T) {
	pass, _, _ := Judge(JudgeInput{
		Probe:   Probe{Judge: JudgeSpec{Kind: JudgeExact, Expected: "北京", CaseInsensitive: true}},
		Samples: []Extracted{{Text: "北京"}},
	})
	if pass != VerdictPass {
		t.Fatalf("exact 应得 pass，得到 %s", pass)
	}
	numeric, _, _ := Judge(JudgeInput{
		Probe:   Probe{Judge: JudgeSpec{Kind: JudgeNumeric, Expected: "21"}},
		Samples: []Extracted{{Text: "答案是 21", Numbers: []float64{21}}},
	})
	if numeric != VerdictPass {
		t.Fatalf("numeric 应得 pass，得到 %s", numeric)
	}
}

func TestJudgeDistributionIdenticalFails(t *testing.T) {
	samples := make([]Extracted, 20)
	for i := range samples {
		samples[i] = Extracted{Numbers: []float64{7}}
	}
	verdict, _, detail := Judge(JudgeInput{
		Probe:   Probe{Judge: JudgeSpec{Kind: JudgeDistribution, MinSamples: 20, FailIfIdentical: true}},
		Samples: samples,
	})
	if verdict != VerdictFail {
		t.Fatalf("全相同应得 fail，得到 %s detail=%v", verdict, detail)
	}
}

func TestJudgeProtocolMissingSignatureIsSuspect(t *testing.T) {
	verdict, _, _ := Judge(JudgeInput{
		Probe:   Probe{Judge: JudgeSpec{Kind: JudgeProtocol, ExpectSignature: "present"}},
		Samples: []Extracted{{Text: "323", HasThinking: true, HasSignature: false}},
	})
	if verdict != VerdictSuspect {
		t.Fatalf("缺签名应得 suspect，得到 %s", verdict)
	}
}

func TestAggregateVerdicts(t *testing.T) {
	if got := AggregateVerdicts([]string{VerdictPass, VerdictFail}); got != AggregateFail {
		t.Fatalf("fail 应优先，得到 %s", got)
	}
	if got := AggregateVerdicts([]string{VerdictPass, VerdictSuspect}); got != AggregateSuspect {
		t.Fatalf("suspect 次优先，得到 %s", got)
	}
	if got := AggregateVerdicts([]string{VerdictPass, VerdictPass}); got != AggregatePass {
		t.Fatalf("全 pass 应得 pass，得到 %s", got)
	}
	if got := AggregateVerdicts([]string{VerdictError, VerdictInapplicable}); got != AggregateNeutral {
		t.Fatalf("仅 error/inapplicable 应得中性，得到 %s", got)
	}
}

func TestParseRubricReasonsAcceptsLegacyReason(t *testing.T) {
	reasons := parseRubricReasons(map[string]interface{}{
		"reasons":  []interface{}{"缺机制", "跑题"},
		"evidence": "第二段",
	})
	if len(reasons) != 2 || reasons[0] != "缺机制" {
		t.Fatalf("应抽出 reasons 数组，得到 %v", reasons)
	}
	legacy := parseRubricReasons(map[string]interface{}{"reason": "只给了旧字段"})
	if len(legacy) != 1 || legacy[0] != "只给了旧字段" {
		t.Fatalf("应兼容 reason 字符串，得到 %v", legacy)
	}
}
