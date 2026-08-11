package modelaudit

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type TemporalPattern string

const (
	TemporalStableMatched        TemporalPattern = "stable_matched"
	TemporalStableShifted        TemporalPattern = "stable_shifted"
	TemporalIntermittent         TemporalPattern = "intermittent"
	TemporalStageSwitch          TemporalPattern = "stage_switch"
	TemporalShortAnomaly         TemporalPattern = "short_anomaly"
	TemporalInsufficientEvidence TemporalPattern = "insufficient_evidence"
)

func (p TemporalPattern) Valid() bool {
	switch p {
	case TemporalStableMatched, TemporalStableShifted, TemporalIntermittent, TemporalStageSwitch,
		TemporalShortAnomaly, TemporalInsufficientEvidence:
		return true
	default:
		return false
	}
}

type TemporalConfig struct {
	Detector                   VersionedRef `json:"detector"`
	WindowSize                 int          `json:"windowSize"`
	WindowStep                 int          `json:"windowStep"`
	MinimumWindows             int          `json:"minimumWindows"`
	MinimumSegmentWindows      int          `json:"minimumSegmentWindows"`
	MatchedThreshold           float64      `json:"matchedThreshold"`
	ShiftedThreshold           float64      `json:"shiftedThreshold"`
	ChangeThreshold            float64      `json:"changeThreshold"`
	SegmentSpreadThreshold     float64      `json:"segmentSpreadThreshold"`
	AnomalyThreshold           float64      `json:"anomalyThreshold"`
	MaximumShortAnomalyWindows int          `json:"maximumShortAnomalyWindows"`
}

func (c TemporalConfig) validate() error {
	if err := c.Detector.Validate("时序检测器"); err != nil {
		return err
	}
	if c.WindowSize <= 0 || c.WindowStep <= 0 || c.WindowStep > c.WindowSize || c.MinimumWindows <= 0 ||
		c.MinimumSegmentWindows <= 0 || c.MinimumWindows < 2*c.MinimumSegmentWindows ||
		c.MaximumShortAnomalyWindows <= 0 || c.MaximumShortAnomalyWindows >= c.MinimumWindows {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "时序窗口或分段配置无效")
	}
	if !unitInterval(c.MatchedThreshold) || !unitInterval(c.ShiftedThreshold) || c.ShiftedThreshold >= c.MatchedThreshold ||
		!unitInterval(c.ChangeThreshold) || c.ChangeThreshold <= 0 || !unitInterval(c.SegmentSpreadThreshold) ||
		!unitInterval(c.AnomalyThreshold) || c.AnomalyThreshold <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "时序判定阈值无效")
	}
	return nil
}

type TemporalObservation struct {
	SampleID   string              `json:"sampleId"`
	Ordinal    int                 `json:"ordinal"`
	CapturedAt time.Time           `json:"capturedAt"`
	Score      float64             `json:"score"`
	Evidence   []EvidenceReference `json:"evidence,omitempty"`
}

type TemporalWindow struct {
	Index        int                 `json:"index"`
	StartOrdinal int                 `json:"startOrdinal"`
	EndOrdinal   int                 `json:"endOrdinal"`
	StartedAt    time.Time           `json:"startedAt"`
	EndedAt      time.Time           `json:"endedAt"`
	SampleCount  int                 `json:"sampleCount"`
	SampleIDs    []string            `json:"sampleIds"`
	Mean         float64             `json:"mean"`
	Minimum      float64             `json:"minimum"`
	Maximum      float64             `json:"maximum"`
	Evidence     []EvidenceReference `json:"evidence,omitempty"`
}

type TemporalChangePoint struct {
	AfterWindow int     `json:"afterWindow"`
	BeforeMean  float64 `json:"beforeMean"`
	AfterMean   float64 `json:"afterMean"`
	Delta       float64 `json:"delta"`
	Direction   string  `json:"direction"`
}

type TemporalAssessment struct {
	Detector       VersionedRef         `json:"detector"`
	Pattern        TemporalPattern      `json:"pattern"`
	Reason         string               `json:"reason,omitempty"`
	SampleCount    int                  `json:"sampleCount"`
	OverallMean    float64              `json:"overallMean"`
	Windows        []TemporalWindow     `json:"windows"`
	ChangePoint    *TemporalChangePoint `json:"changePoint,omitempty"`
	AnomalyWindows []int                `json:"anomalyWindows,omitempty"`
	Evidence       []EvidenceReference  `json:"evidence,omitempty"`
}

func (a TemporalAssessment) Validate() error {
	if err := a.Detector.Validate("时序检测器"); err != nil {
		return err
	}
	if !a.Pattern.Valid() || a.SampleCount < 0 || !unitInterval(a.OverallMean) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "时序判定模式、样本数或均值无效")
	}
	if a.Pattern == TemporalInsufficientEvidence && strings.TrimSpace(a.Reason) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "时序证据不足必须说明原因")
	}
	for index, window := range a.Windows {
		if window.Index != index || window.SampleCount <= 0 || len(window.SampleIDs) != window.SampleCount || window.StartOrdinal > window.EndOrdinal ||
			window.StartedAt.IsZero() || window.EndedAt.IsZero() || window.StartedAt.After(window.EndedAt) ||
			!unitInterval(window.Mean) || !unitInterval(window.Minimum) || !unitInterval(window.Maximum) ||
			window.Minimum > window.Mean || window.Mean > window.Maximum {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "时序窗口无效")
		}
	}
	if a.ChangePoint != nil {
		if a.ChangePoint.AfterWindow <= 0 || a.ChangePoint.AfterWindow >= len(a.Windows) ||
			!unitInterval(a.ChangePoint.BeforeMean) || !unitInterval(a.ChangePoint.AfterMean) ||
			!finiteNumber(a.ChangePoint.Delta) || math.Abs(a.ChangePoint.Delta) > 1 ||
			math.Abs(a.ChangePoint.Delta-(a.ChangePoint.AfterMean-a.ChangePoint.BeforeMean)) > 1e-12 ||
			(a.ChangePoint.Direction != "toward_declared" && a.ChangePoint.Direction != "away_from_declared") ||
			(a.ChangePoint.Direction == "toward_declared") != (a.ChangePoint.Delta > 0) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "时序变化点无效")
		}
	}
	if (a.Pattern == TemporalStageSwitch) != (a.ChangePoint != nil) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "阶段切换模式与变化点不一致")
	}
	for _, index := range a.AnomalyWindows {
		if index < 0 || index >= len(a.Windows) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "时序异常窗口索引无效")
		}
	}
	if (a.Pattern == TemporalShortAnomaly) != (len(a.AnomalyWindows) > 0) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "短期异常模式与异常窗口不一致")
	}
	return nil
}

func AnalyzeTemporalIdentity(config TemporalConfig, observations []TemporalObservation) (TemporalAssessment, error) {
	if err := config.validate(); err != nil {
		return TemporalAssessment{}, err
	}
	ordered := append([]TemporalObservation(nil), observations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Ordinal < ordered[j].Ordinal })
	seenOrdinals := make(map[int]struct{}, len(ordered))
	var evidence []EvidenceReference
	for index := range ordered {
		observation := &ordered[index]
		observation.SampleID = strings.TrimSpace(observation.SampleID)
		if observation.SampleID == "" || observation.Ordinal < 0 || observation.CapturedAt.IsZero() || !unitInterval(observation.Score) {
			return TemporalAssessment{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个时序观察值无效", index+1))
		}
		if _, duplicate := seenOrdinals[observation.Ordinal]; duplicate {
			return TemporalAssessment{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("时序样本序号 %d 重复", observation.Ordinal))
		}
		if index > 0 && observation.CapturedAt.Before(ordered[index-1].CapturedAt) {
			return TemporalAssessment{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "时序样本时间与序号顺序不一致")
		}
		seenOrdinals[observation.Ordinal] = struct{}{}
		var err error
		evidence, err = mergeSignalEvidence(evidence, observation.Evidence)
		if err != nil {
			return TemporalAssessment{}, err
		}
	}
	windows, err := buildTemporalWindows(config, ordered)
	if err != nil {
		return TemporalAssessment{}, err
	}
	overallMean := 0.0
	if len(ordered) > 0 {
		scores := make([]float64, len(ordered))
		for index, observation := range ordered {
			scores[index] = observation.Score
		}
		overallMean = meanNumbers(scores)
	}
	assessment := TemporalAssessment{
		Detector: config.Detector, SampleCount: len(ordered), OverallMean: overallMean,
		Windows: windows, Evidence: evidence,
	}
	if len(windows) < config.MinimumWindows {
		assessment.Pattern = TemporalInsufficientEvidence
		assessment.Reason = fmt.Sprintf("完整窗口数量 %d 低于最低要求 %d", len(windows), config.MinimumWindows)
		return assessment, assessment.Validate()
	}

	means := make([]float64, len(windows))
	for index, window := range windows {
		means[index] = window.Mean
	}
	if changePoint, stable := strongestStableChange(means, config); stable {
		assessment.Pattern = TemporalStageSwitch
		assessment.ChangePoint = changePoint
		return assessment, assessment.Validate()
	}
	if anomalies := shortAnomalyWindows(means, config); len(anomalies) > 0 && len(anomalies) <= config.MaximumShortAnomalyWindows {
		assessment.Pattern = TemporalShortAnomaly
		assessment.AnomalyWindows = anomalies
		return assessment, assessment.Validate()
	}
	minimum, maximum := minMaxNumbers(means)
	if maximum-minimum <= config.SegmentSpreadThreshold {
		switch {
		case meanNumbers(means) >= config.MatchedThreshold:
			assessment.Pattern = TemporalStableMatched
		case meanNumbers(means) <= config.ShiftedThreshold:
			assessment.Pattern = TemporalStableShifted
		default:
			assessment.Pattern = TemporalIntermittent
		}
	} else {
		assessment.Pattern = TemporalIntermittent
	}
	return assessment, assessment.Validate()
}

func buildTemporalWindows(config TemporalConfig, observations []TemporalObservation) ([]TemporalWindow, error) {
	windows := make([]TemporalWindow, 0)
	for start := 0; start+config.WindowSize <= len(observations); start += config.WindowStep {
		samples := observations[start : start+config.WindowSize]
		scores := make([]float64, len(samples))
		sampleIDs := make([]string, len(samples))
		var evidence []EvidenceReference
		for index, sample := range samples {
			scores[index] = sample.Score
			sampleIDs[index] = sample.SampleID
			var err error
			evidence, err = mergeSignalEvidence(evidence, sample.Evidence)
			if err != nil {
				return nil, err
			}
		}
		minimum, maximum := minMaxNumbers(scores)
		windows = append(windows, TemporalWindow{
			Index: len(windows), StartOrdinal: samples[0].Ordinal, EndOrdinal: samples[len(samples)-1].Ordinal,
			StartedAt: samples[0].CapturedAt, EndedAt: samples[len(samples)-1].CapturedAt,
			SampleCount: len(samples), SampleIDs: sampleIDs, Mean: meanNumbers(scores), Minimum: minimum, Maximum: maximum, Evidence: evidence,
		})
	}
	return windows, nil
}

func strongestStableChange(means []float64, config TemporalConfig) (*TemporalChangePoint, bool) {
	bestIndex := -1
	bestAbsoluteDelta := 0.0
	bestBefore, bestAfter := 0.0, 0.0
	for split := config.MinimumSegmentWindows; split <= len(means)-config.MinimumSegmentWindows; split++ {
		before, after := means[:split], means[split:]
		beforeMin, beforeMax := minMaxNumbers(before)
		afterMin, afterMax := minMaxNumbers(after)
		if beforeMax-beforeMin > config.SegmentSpreadThreshold || afterMax-afterMin > config.SegmentSpreadThreshold {
			continue
		}
		beforeMean, afterMean := meanNumbers(before), meanNumbers(after)
		absoluteDelta := math.Abs(afterMean - beforeMean)
		if absoluteDelta >= config.ChangeThreshold && absoluteDelta > bestAbsoluteDelta {
			bestIndex, bestAbsoluteDelta, bestBefore, bestAfter = split, absoluteDelta, beforeMean, afterMean
		}
	}
	if bestIndex < 0 {
		return nil, false
	}
	direction := "away_from_declared"
	if bestAfter > bestBefore {
		direction = "toward_declared"
	}
	return &TemporalChangePoint{
		AfterWindow: bestIndex, BeforeMean: bestBefore, AfterMean: bestAfter,
		Delta: bestAfter - bestBefore, Direction: direction,
	}, true
}

func shortAnomalyWindows(means []float64, config TemporalConfig) []int {
	center := medianNumber(means)
	anomalies := make([]int, 0)
	rest := make([]float64, 0, len(means))
	for index, mean := range means {
		if math.Abs(mean-center) >= config.AnomalyThreshold {
			anomalies = append(anomalies, index)
		} else {
			rest = append(rest, mean)
		}
	}
	if len(anomalies) == 0 || len(rest) < config.MinimumWindows-config.MaximumShortAnomalyWindows {
		return nil
	}
	minimum, maximum := minMaxNumbers(rest)
	if maximum-minimum > config.SegmentSpreadThreshold {
		return nil
	}
	return anomalies
}

func minMaxNumbers(values []float64) (float64, float64) {
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	return minimum, maximum
}

func medianNumber(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}
