package eval

import (
	"fmt"
	"regexp"
	"strings"
)

var knownExtractKinds = map[string]struct{}{
	ExtractText:     {},
	ExtractUsage:    {},
	ExtractProtocol: {},
	ExtractSVG:      {},
	ExtractNumbers:  {},
}

var knownJudgeKinds = map[string]struct{}{
	JudgeExact:        {},
	JudgeRegex:        {},
	JudgeNumeric:      {},
	JudgeProtocol:     {},
	JudgeSVG:          {},
	JudgeDistribution: {},
	JudgeRubric:       {},
	JudgeObserve:      {},
}

var knownServiceTypes = map[string]struct{}{
	ServiceClaude:    {},
	ServiceOpenAI:    {},
	ServiceGemini:    {},
	ServiceResponses: {},
}

var knownIntervals = map[string]struct{}{
	Interval30m: {},
	Interval2h:  {},
	Interval1d:  {},
}

// ValidateProbe 检查一条探针能否直接入库。
// 分流：can_add_directly / invalid_fields / unsupported_grader / needs_new_grader。未知 kind 走 needs_new_grader。
func ValidateProbe(probe Probe) ValidateReport {
	report := ValidateReport{
		Mode:        ValidateCanAddDirectly,
		ExtractKind: strings.TrimSpace(probe.Extract.Kind),
		JudgeKind:   strings.TrimSpace(probe.Judge.Kind),
	}

	if strings.TrimSpace(probe.Slug) == "" {
		report.Errors = append(report.Errors, "slug 不能为空")
	}
	if strings.TrimSpace(probe.Name) == "" {
		report.Errors = append(report.Errors, "name 不能为空")
	}
	switch probe.Category {
	case CategoryAuthenticity, CategoryIQ:
	default:
		report.Errors = append(report.Errors, "category 必须是 authenticity 或 iq")
	}
	if strings.TrimSpace(probe.Stimulus.Prompt) == "" {
		report.Errors = append(report.Errors, "stimulus.prompt 不能为空")
	}
	if probe.SampleCount < 1 {
		report.Errors = append(report.Errors, "sampleCount 必须 >= 1")
	}

	if _, ok := knownExtractKinds[report.ExtractKind]; !ok {
		report.Mode = ValidateNeedsNewGrader
		if report.ExtractKind == "" {
			report.Errors = append(report.Errors, "extract.kind 不能为空")
		} else {
			report.Errors = append(report.Errors, fmt.Sprintf("未知抽取方式 %q，需要新的 grader", report.ExtractKind))
		}
	}
	if _, ok := knownJudgeKinds[report.JudgeKind]; !ok {
		report.Mode = ValidateNeedsNewGrader
		if report.JudgeKind == "" {
			report.Errors = append(report.Errors, "judge.kind 不能为空")
		} else {
			report.Errors = append(report.Errors, fmt.Sprintf("未知判定方式 %q，需要新的 grader", report.JudgeKind))
		}
	}

	switch report.JudgeKind {
	case JudgeExact:
		if strings.TrimSpace(probe.Judge.Expected) == "" {
			report.Errors = append(report.Errors, "exact 判定必须提供 judge.expected")
		}
	case JudgeRegex:
		if strings.TrimSpace(probe.Judge.Pattern) == "" {
			report.Errors = append(report.Errors, "regex 判定必须提供 judge.pattern")
		} else if _, err := regexp.Compile(probe.Judge.Pattern); err != nil {
			report.Errors = append(report.Errors, "judge.pattern 不是合法正则: "+err.Error())
		}
	case JudgeNumeric:
		if probe.Judge.Expected == "" && probe.Judge.ExpectedNumber == 0 {
			report.Errors = append(report.Errors, "numeric 判定必须提供 judge.expected 或 judge.expectedNumber")
		}
	case JudgeRubric:
		if strings.TrimSpace(probe.Judge.AnalysisPrompt) == "" {
			report.Errors = append(report.Errors, "rubric 判定必须提供 judge.analysisPrompt")
		}
		if probe.SampleCount > 1 {
			report.Errors = append(report.Errors, "rubric 判定 sampleCount 必须为 1（信封自评只跑一次）")
		}
	case JudgeDistribution:
		if report.ExtractKind != ExtractNumbers {
			report.Errors = append(report.Errors, "distribution 判定必须搭配 extract.kind=numbers")
		}
		if probe.SampleCount < 2 {
			report.Errors = append(report.Errors, "distribution 判定 sampleCount 必须 >= 2")
		}
	}

	if probe.Category == CategoryAuthenticity && report.JudgeKind == JudgeRubric {
		report.Errors = append(report.Errors, "真伪套件禁止使用 rubric（自评会偏）")
		if report.Mode == ValidateCanAddDirectly {
			report.Mode = ValidateUnsupportedGrader
		}
	}

	for _, serviceType := range probe.ApplicableServiceTypes {
		if _, ok := knownServiceTypes[serviceType]; !ok {
			report.Errors = append(report.Errors, fmt.Sprintf("不支持的 serviceType %q", serviceType))
		}
	}

	report.Cheap = probeIsCheap(probe)
	if len(report.Errors) == 0 {
		report.OK = true
		report.Mode = ValidateCanAddDirectly
	} else if report.Mode == ValidateCanAddDirectly {
		report.Mode = ValidateInvalidFields
	}
	return report
}

func probeIsCheap(probe Probe) bool {
	if probe.SampleCount > 3 {
		return false
	}
	if probe.Judge.Kind == JudgeRubric || probe.Judge.Kind == JudgeDistribution {
		return false
	}
	if probe.Category == CategoryIQ {
		return false
	}
	return true
}

// ValidateSuite 检查套件引用的探针是否存在、值班套件是否够便宜。
func ValidateSuite(suite Suite, probes []Probe) error {
	if strings.TrimSpace(suite.Slug) == "" {
		return fmt.Errorf("slug 不能为空")
	}
	if strings.TrimSpace(suite.Name) == "" {
		return fmt.Errorf("name 不能为空")
	}
	if len(suite.ProbeIDs) == 0 {
		return fmt.Errorf("套件至少包含一条探针")
	}
	byID := make(map[string]Probe, len(probes))
	for _, probe := range probes {
		byID[probe.ID] = probe
	}
	hasAuthenticity := false
	hasRubric := false
	for _, probeID := range suite.ProbeIDs {
		probe, ok := byID[probeID]
		if !ok {
			return fmt.Errorf("套件引用了不存在的探针 %s", probeID)
		}
		if suite.Cheap && !probeIsCheap(probe) {
			return fmt.Errorf("便宜套件不能包含探针 %s（sampleCount/rubric/智商/指纹）", probe.Slug)
		}
		if probe.Category == CategoryAuthenticity && probe.Judge.Kind == JudgeRubric {
			return fmt.Errorf("真伪套件禁止包含 rubric 探针 %s", probe.Slug)
		}
		if probe.Category == CategoryAuthenticity {
			hasAuthenticity = true
		}
		if probe.Judge.Kind == JudgeRubric {
			hasRubric = true
		}
	}
	if hasAuthenticity && hasRubric {
		return fmt.Errorf("真伪套件禁止混入 rubric")
	}
	return nil
}

// ValidateWatch 值班只能挂便宜套件。
func ValidateWatch(watch WatchConfig, suite Suite, probes []Probe) error {
	if !watch.Enabled && strings.TrimSpace(watch.SuiteID) == "" {
		return nil
	}
	if strings.TrimSpace(watch.SuiteID) == "" {
		return fmt.Errorf("值班必须指定套件")
	}
	if _, ok := knownIntervals[watch.Interval]; !ok {
		return fmt.Errorf("interval 只允许 30m / 2h / 1d")
	}
	if !suite.Cheap {
		return fmt.Errorf("值班只能使用便宜套件")
	}
	if err := ValidateSuite(suite, probes); err != nil {
		return err
	}
	for _, probe := range probes {
		if !probeIsCheap(probe) {
			return fmt.Errorf("值班套件含有不便宜的探针 %s", probe.Slug)
		}
	}
	if watch.Enabled && len(watch.ChannelIDs) == 0 {
		return fmt.Errorf("开启值班必须勾选至少一个渠道")
	}
	return nil
}

func parseInterval(value string) (duration int64, err error) {
	switch value {
	case Interval30m:
		return int64((30 * 60)), nil
	case Interval2h:
		return int64((2 * 60 * 60)), nil
	case Interval1d:
		return int64((24 * 60 * 60)), nil
	default:
		return 0, fmt.Errorf("未知值班间隔 %s", value)
	}
}
