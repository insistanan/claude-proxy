package eval

import "testing"

func TestValidateProbeRejectsUnknownJudge(t *testing.T) {
	report := ValidateProbe(Probe{
		Slug:        "custom",
		Name:        "自定义",
		Category:    CategoryIQ,
		Stimulus:    Stimulus{Prompt: "hello"},
		Extract:     ExtractSpec{Kind: ExtractText},
		Judge:       JudgeSpec{Kind: "llm_script"},
		SampleCount: 1,
	})
	if report.OK {
		t.Fatal("未知判定方式不应通过")
	}
	if report.Mode != ValidateNeedsNewGrader {
		t.Fatalf("期望 %s，得到 %s", ValidateNeedsNewGrader, report.Mode)
	}
}

func TestValidateProbeRejectsAuthenticityRubric(t *testing.T) {
	report := ValidateProbe(Probe{
		Slug:        "auth-rubric",
		Name:        "真伪自评",
		Category:    CategoryAuthenticity,
		Stimulus:    Stimulus{Prompt: "hello"},
		Extract:     ExtractSpec{Kind: ExtractText},
		Judge:       JudgeSpec{Kind: JudgeRubric, AnalysisPrompt: "判断真伪"},
		SampleCount: 1,
	})
	if report.OK {
		t.Fatal("真伪套件使用 rubric 不应通过")
	}
	if report.Mode != ValidateUnsupportedGrader {
		t.Fatalf("期望 %s，得到 %s", ValidateUnsupportedGrader, report.Mode)
	}
}

func TestValidateProbeRejectsRubricSampleCount(t *testing.T) {
	report := ValidateProbe(Probe{
		Slug:        "iq-rubric-many",
		Name:        "开放题",
		Category:    CategoryIQ,
		Stimulus:    Stimulus{Prompt: "hello"},
		Extract:     ExtractSpec{Kind: ExtractText},
		Judge:       JudgeSpec{Kind: JudgeRubric, AnalysisPrompt: "判断是否达标"},
		SampleCount: 3,
	})
	if report.OK {
		t.Fatal("rubric sampleCount>1 不应通过")
	}
	if report.Mode != ValidateInvalidFields {
		t.Fatalf("期望 %s，得到 %s", ValidateInvalidFields, report.Mode)
	}
}

func TestValidateWatchRejectsExpensiveSuite(t *testing.T) {
	probe := Probe{
		ID:          "p1",
		Slug:        "iq",
		Name:        "智商",
		Category:    CategoryIQ,
		Stimulus:    Stimulus{Prompt: "1+1"},
		Extract:     ExtractSpec{Kind: ExtractText},
		Judge:       JudgeSpec{Kind: JudgeExact, Expected: "2"},
		SampleCount: 1,
	}
	suite := Suite{ID: "s1", Slug: "iq", Name: "智商", ProbeIDs: []string{"p1"}, Cheap: false}
	err := ValidateWatch(WatchConfig{
		Enabled:    true,
		SuiteID:    "s1",
		Interval:   Interval2h,
		ChannelIDs: []string{"ch_1"},
	}, suite, []Probe{probe})
	if err == nil {
		t.Fatal("不便宜套件不应允许值班")
	}
}

func TestProbeIsCheap(t *testing.T) {
	cheap := Probe{Category: CategoryAuthenticity, SampleCount: 1, Judge: JudgeSpec{Kind: JudgeProtocol}}
	if !probeIsCheap(cheap) {
		t.Fatal("真伪单次协议探针应是便宜的")
	}
	fingerprint := Probe{Category: CategoryAuthenticity, SampleCount: 25, Judge: JudgeSpec{Kind: JudgeDistribution}}
	if probeIsCheap(fingerprint) {
		t.Fatal("随机数指纹不应是便宜的")
	}
}
