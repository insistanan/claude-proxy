package modelaudit

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

type CapabilityAggregationStatus string

const (
	CapabilityAggregationComplete             CapabilityAggregationStatus = "complete"
	CapabilityAggregationProvisional          CapabilityAggregationStatus = "provisional"
	CapabilityAggregationPartialBudget        CapabilityAggregationStatus = "partial_budget"
	CapabilityAggregationInsufficientEvidence CapabilityAggregationStatus = "insufficient_evidence"
	capabilityCoverageEpsilon                                             = 1e-12
)

func (s CapabilityAggregationStatus) Valid() bool {
	return s == CapabilityAggregationComplete || s == CapabilityAggregationProvisional ||
		s == CapabilityAggregationPartialBudget || s == CapabilityAggregationInsufficientEvidence
}

type CapabilityAggregationConfig struct {
	Aggregator                         VersionedRef `json:"aggregator"`
	MinimumScoredInstancesPerDimension int          `json:"minimumScoredInstancesPerDimension"`
	MinimumDimensionsForIndex          int          `json:"minimumDimensionsForIndex"`
	MinimumFormalDimensions            int          `json:"minimumFormalDimensions"`
	FormalCoverageThreshold            float64      `json:"formalCoverageThreshold"`
	BootstrapSamples                   int          `json:"bootstrapSamples"`
	ConfidenceLevel                    float64      `json:"confidenceLevel"`
	BootstrapSeed                      int64        `json:"bootstrapSeed"`
	MinimumScoredInstancesForInterval  int          `json:"minimumScoredInstancesForInterval"`
}

func (c CapabilityAggregationConfig) validate() error {
	if err := c.Aggregator.Validate("能力聚合器"); err != nil {
		return err
	}
	if c.MinimumScoredInstancesPerDimension <= 0 || c.MinimumDimensionsForIndex <= 0 ||
		c.MinimumDimensionsForIndex > len(capabilityDimensions) || c.MinimumFormalDimensions < c.MinimumDimensionsForIndex ||
		c.MinimumFormalDimensions > len(capabilityDimensions) || !unitInterval(c.FormalCoverageThreshold) || c.FormalCoverageThreshold <= 0 ||
		c.BootstrapSamples <= 0 || !finiteNumber(c.ConfidenceLevel) || c.ConfidenceLevel <= 0 || c.ConfidenceLevel >= 1 || c.MinimumScoredInstancesForInterval < 2 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力聚合最低样本、维度或覆盖率要求无效")
	}
	return nil
}

type CapabilityTaskCoveragePlan struct {
	Task             VersionedRef        `json:"task"`
	Dimension        CapabilityDimension `json:"dimension"`
	PlannedInstances int                 `json:"plannedInstances"`
}

func (p CapabilityTaskCoveragePlan) Validate() error {
	if err := p.Task.Validate("能力覆盖计划任务"); err != nil {
		return err
	}
	if !p.Dimension.Valid() || p.PlannedInstances <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力覆盖计划维度或实例数无效")
	}
	return nil
}

type CapabilityAggregationInput struct {
	Package    CapabilityTaskPackageSnapshot `json:"package"`
	Plan       []CapabilityTaskCoveragePlan  `json:"plan"`
	Results    []CapabilityTaskResult        `json:"results"`
	StopReason CapabilityRunStopReason       `json:"stopReason,omitempty"`
}

type CapabilityResultCounts struct {
	Planned         int `json:"planned"`
	Scored          int `json:"scored"`
	Skipped         int `json:"skipped"`
	Unsupported     int `json:"unsupported"`
	Indeterminate   int `json:"indeterminate"`
	ExecutionFailed int `json:"executionFailed"`
	ScoringFailed   int `json:"scoringFailed"`
	Missing         int `json:"missing"`
}

func (c CapabilityResultCounts) completed() int {
	return c.Scored + c.Skipped + c.Unsupported + c.Indeterminate + c.ExecutionFailed + c.ScoringFailed
}

func (c CapabilityResultCounts) Validate() error {
	if c.Planned < 0 || c.Scored < 0 || c.Skipped < 0 || c.Unsupported < 0 || c.Indeterminate < 0 ||
		c.ExecutionFailed < 0 || c.ScoringFailed < 0 || c.Missing < 0 || c.completed()+c.Missing != c.Planned {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力结果计数无效")
	}
	return nil
}

type CapabilityTaskScore struct {
	Task      VersionedRef           `json:"task"`
	Dimension CapabilityDimension    `json:"dimension"`
	Weight    float64                `json:"weight"`
	Score     *float64               `json:"score,omitempty"`
	Coverage  float64                `json:"coverage"`
	Counts    CapabilityResultCounts `json:"counts"`
	Reasons   []string               `json:"reasons,omitempty"`
}

func (s CapabilityTaskScore) Validate() error {
	if err := s.Task.Validate("能力任务分数"); err != nil {
		return err
	}
	if !s.Dimension.Valid() || !finiteNumber(s.Weight) || s.Weight <= 0 || !unitInterval(s.Coverage) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务分数维度、权重或覆盖率无效")
	}
	if s.Score != nil && (!finiteNumber(*s.Score) || *s.Score < 0 || *s.Score > 100) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务分数超出 0-100")
	}
	if err := s.Counts.Validate(); err != nil {
		return err
	}
	if math.Abs(s.Coverage-float64(s.Counts.Scored)/float64(s.Counts.Planned)) > 1e-9 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务覆盖率与计数不一致")
	}
	if (s.Score != nil) != (s.Counts.Scored > 0) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力任务分数与已判分计数不一致")
	}
	return nil
}

type CapabilityConfidenceInterval struct {
	Lower       float64 `json:"lower"`
	Upper       float64 `json:"upper"`
	Level       float64 `json:"level"`
	Resamples   int     `json:"resamples"`
	SampleCount int     `json:"sampleCount"`
	Method      string  `json:"method"`
}

func (i CapabilityConfidenceInterval) Validate() error {
	if !finiteNumber(i.Lower) || !finiteNumber(i.Upper) || i.Lower < 0 || i.Upper > 100 || i.Lower > i.Upper ||
		!finiteNumber(i.Level) || i.Level <= 0 || i.Level >= 1 || i.Resamples <= 0 || i.SampleCount < 2 || i.Method != "stratified_bootstrap_percentile" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力置信区间无效")
	}
	return nil
}

type CapabilityDimensionScore struct {
	Dimension CapabilityDimension           `json:"dimension"`
	Weight    float64                       `json:"weight"`
	Score     *float64                      `json:"score,omitempty"`
	Interval  *CapabilityConfidenceInterval `json:"interval,omitempty"`
	Coverage  float64                       `json:"coverage"`
	Formal    bool                          `json:"formal"`
	Counts    CapabilityResultCounts        `json:"counts"`
	Tasks     []CapabilityTaskScore         `json:"tasks"`
	Reasons   []string                      `json:"reasons,omitempty"`
}

func (s CapabilityDimensionScore) Validate() error {
	if !s.Dimension.Valid() || !unitInterval(s.Weight) || s.Weight <= 0 || !unitInterval(s.Coverage) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力维度权重或覆盖率无效")
	}
	if s.Score != nil && (!finiteNumber(*s.Score) || *s.Score < 0 || *s.Score > 100) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力维度分数超出 0-100")
	}
	if s.Interval != nil {
		if err := s.Interval.Validate(); err != nil {
			return err
		}
	}
	if s.Interval != nil && s.Score == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力维度区间不能脱离分数存在")
	}
	if s.Formal && (s.Score == nil || s.Interval == nil) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正式能力维度缺少分数或置信区间")
	}
	if err := s.Counts.Validate(); err != nil {
		return err
	}
	for _, task := range s.Tasks {
		if err := task.Validate(); err != nil {
			return err
		}
		if task.Dimension != s.Dimension {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力维度包含其他维度的任务")
		}
	}
	return nil
}

type CapabilityReport struct {
	Aggregator       VersionedRef                  `json:"aggregator"`
	Package          VersionedRef                  `json:"package"`
	PackageSHA256    string                        `json:"packageSha256"`
	Status           CapabilityAggregationStatus   `json:"status"`
	Formal           bool                          `json:"formal"`
	Index            *float64                      `json:"index,omitempty"`
	IndexInterval    *CapabilityConfidenceInterval `json:"indexInterval,omitempty"`
	Coverage         float64                       `json:"coverage"`
	ScoredDimensions int                           `json:"scoredDimensions"`
	FormalDimensions int                           `json:"formalDimensions"`
	Counts           CapabilityResultCounts        `json:"counts"`
	Dimensions       []CapabilityDimensionScore    `json:"dimensions"`
	Reasons          []string                      `json:"reasons,omitempty"`
	StopReason       CapabilityRunStopReason       `json:"stopReason,omitempty"`
}

func (r CapabilityReport) Validate() error {
	if err := r.Aggregator.Validate("能力聚合器"); err != nil {
		return err
	}
	if err := r.Package.Validate("能力报告任务包"); err != nil {
		return err
	}
	if !validSHA256(r.PackageSHA256) || !r.Status.Valid() || !unitInterval(r.Coverage) ||
		r.ScoredDimensions < 0 || r.FormalDimensions < 0 || r.FormalDimensions > r.ScoredDimensions {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力报告状态、覆盖率或维度计数无效")
	}
	if !r.StopReason.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力报告停止原因无效")
	}
	if r.Index != nil && (!finiteNumber(*r.Index) || *r.Index < 0 || *r.Index > 100) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力指数超出 0-100")
	}
	if r.IndexInterval != nil {
		if err := r.IndexInterval.Validate(); err != nil {
			return err
		}
	}
	if r.IndexInterval != nil && r.Index == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力总区间不能脱离总指数存在")
	}
	if r.Formal != (r.Status == CapabilityAggregationComplete) || (r.Formal && (r.Index == nil || r.IndexInterval == nil)) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力报告正式状态不一致")
	}
	if r.Status == CapabilityAggregationInsufficientEvidence && r.Index != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "证据不足能力报告不能携带总指数")
	}
	if r.Status == CapabilityAggregationProvisional && r.Index == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "临时能力报告必须携带可解释的临时总指数")
	}
	if (r.Status == CapabilityAggregationPartialBudget) != (r.StopReason == CapabilityRunStoppedByBudget) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力报告预算停止状态不一致")
	}
	if r.Status != CapabilityAggregationPartialBudget && r.StopReason != CapabilityRunStopNone {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "非预算部分报告不能携带停止原因")
	}
	if r.Status != CapabilityAggregationComplete && len(r.Reasons) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "非正式能力报告必须说明原因")
	}
	if r.Status == CapabilityAggregationComplete && len(r.Reasons) > 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正式能力报告不能保留证据不足原因")
	}
	if len(r.Dimensions) != len(capabilityDimensions) || r.ScoredDimensions > len(r.Dimensions) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力报告必须包含七维明细")
	}
	if err := r.Counts.Validate(); err != nil {
		return err
	}
	seen := make(map[CapabilityDimension]struct{}, len(r.Dimensions))
	weightTotal, coverage := 0.0, 0.0
	scoredDimensions, formalDimensions := 0, 0
	dimensionCounts := CapabilityResultCounts{}
	for _, dimension := range r.Dimensions {
		if err := dimension.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[dimension.Dimension]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力报告包含重复维度")
		}
		seen[dimension.Dimension] = struct{}{}
		weightTotal += dimension.Weight
		coverage += dimension.Weight * dimension.Coverage
		if dimension.Score != nil {
			scoredDimensions++
		}
		if dimension.Formal {
			formalDimensions++
		}
		addCapabilityCounts(&dimensionCounts, dimension.Counts)
	}
	if math.Abs(weightTotal-1) > 1e-9 || math.Abs(coverage-r.Coverage) > 1e-9 ||
		scoredDimensions != r.ScoredDimensions || formalDimensions != r.FormalDimensions || dimensionCounts != r.Counts {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力报告维度权重、覆盖率或计数不一致")
	}
	return nil
}

func AggregateCapability(config CapabilityAggregationConfig, input CapabilityAggregationInput) (CapabilityReport, error) {
	if err := config.validate(); err != nil {
		return CapabilityReport{}, err
	}
	if err := input.Package.Validate(); err != nil {
		return CapabilityReport{}, err
	}
	if !input.StopReason.Valid() {
		return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力聚合停止原因无效")
	}
	tasksByID := make(map[string]CapabilityTaskDefinition, len(input.Package.Package.Tasks))
	for _, task := range input.Package.Package.Tasks {
		tasksByID[task.Ref.ID] = task
	}
	type taskAccumulator struct {
		plan    CapabilityTaskCoveragePlan
		task    CapabilityTaskDefinition
		results []CapabilityTaskResult
	}
	accumulators := make(map[string]*taskAccumulator, len(input.Plan))
	for _, plan := range input.Plan {
		if err := plan.Validate(); err != nil {
			return CapabilityReport{}, err
		}
		task, found := tasksByID[plan.Task.ID]
		if !found || task.Ref != plan.Task || task.Dimension != plan.Dimension {
			return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力覆盖计划任务 %q 与任务包不一致", plan.Task.ID))
		}
		if plan.PlannedInstances > task.Repetitions {
			return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力覆盖计划任务 %q 的实例数超过任务重复上限", plan.Task.ID))
		}
		if _, duplicate := accumulators[plan.Task.ID]; duplicate {
			return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力覆盖计划任务 %q 重复", plan.Task.ID))
		}
		accumulators[plan.Task.ID] = &taskAccumulator{plan: plan, task: task}
	}
	if len(accumulators) == 0 {
		return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力覆盖计划不能为空")
	}
	seenInstances := make(map[string]struct{}, len(input.Results))
	for _, result := range input.Results {
		if err := result.Validate(); err != nil {
			return CapabilityReport{}, err
		}
		if _, duplicate := seenInstances[result.InstanceID]; duplicate {
			return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力结果实例 %q 重复", result.InstanceID))
		}
		seenInstances[result.InstanceID] = struct{}{}
		accumulator := accumulators[result.Task.ID]
		if accumulator == nil || result.Task != accumulator.plan.Task || result.Dimension != accumulator.plan.Dimension {
			return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力结果 %q 不在覆盖计划中", result.InstanceID))
		}
		if result.Status == CapabilityTaskScored && (result.Scorer == nil || *result.Scorer != accumulator.task.Scorer) {
			return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力结果 %q 的判分器版本与任务定义不一致", result.InstanceID))
		}
		accumulator.results = append(accumulator.results, result)
		if len(accumulator.results) > accumulator.plan.PlannedInstances {
			return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("任务 %q 的结果数超过计划实例数", result.Task.ID))
		}
	}

	taskScoresByDimension := make(map[CapabilityDimension][]CapabilityTaskScore, len(capabilityDimensions))
	scoredValuesByTask := make(map[string][]float64, len(accumulators))
	totalCounts := CapabilityResultCounts{}
	for _, accumulator := range accumulators {
		counts := CapabilityResultCounts{Planned: accumulator.plan.PlannedInstances}
		scoreTotal := 0.0
		taskReasons := make([]string, 0)
		for _, result := range accumulator.results {
			switch result.Status {
			case CapabilityTaskScored:
				counts.Scored++
				scoreTotal += *result.Score
				scoredValuesByTask[accumulator.task.Ref.ID] = append(scoredValuesByTask[accumulator.task.Ref.ID], *result.Score*100)
			case CapabilityTaskSkipped:
				counts.Skipped++
			case CapabilityTaskUnsupported:
				counts.Unsupported++
			case CapabilityTaskIndeterminate:
				counts.Indeterminate++
			case CapabilityTaskExecutionFailed:
				counts.ExecutionFailed++
				taskReasons = append(taskReasons, result.Reason)
			case CapabilityTaskScoringFailed:
				counts.ScoringFailed++
				taskReasons = append(taskReasons, result.Reason)
			}
		}
		counts.Missing = counts.Planned - counts.completed()
		var score *float64
		if counts.Scored > 0 {
			value := scoreTotal / float64(counts.Scored) * 100
			score = &value
		}
		taskScore := CapabilityTaskScore{
			Task: accumulator.task.Ref, Dimension: accumulator.task.Dimension, Weight: accumulator.task.Weight,
			Score: score, Coverage: float64(counts.Scored) / float64(counts.Planned), Counts: counts, Reasons: uniqueSortedStrings(taskReasons),
		}
		taskScoresByDimension[accumulator.task.Dimension] = append(taskScoresByDimension[accumulator.task.Dimension], taskScore)
		addCapabilityCounts(&totalCounts, counts)
	}
	for taskID := range scoredValuesByTask {
		sort.Float64s(scoredValuesByTask[taskID])
	}

	dimensions := make([]CapabilityDimensionScore, 0, len(capabilityDimensions))
	dimensionBootstrap := make(map[CapabilityDimension][]float64, len(capabilityDimensions))
	random := rand.New(rand.NewSource(config.BootstrapSeed))
	overallCoverage := 0.0
	indexWeighted, indexWeight := 0.0, 0.0
	scoredDimensions, formalDimensions := 0, 0
	for _, dimension := range capabilityDimensions {
		tasks := taskScoresByDimension[dimension]
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].Task.ID < tasks[j].Task.ID })
		counts := CapabilityResultCounts{}
		dimensionReasons := make([]string, 0)
		plannedTaskWeight, coveredTaskWeight := 0.0, 0.0
		scoreWeight, weightedScore := 0.0, 0.0
		for _, task := range tasks {
			addCapabilityCounts(&counts, task.Counts)
			plannedTaskWeight += task.Weight
			coveredTaskWeight += task.Weight * task.Coverage
			if task.Score != nil {
				scoreWeight += task.Weight
				weightedScore += task.Weight * *task.Score
			}
			dimensionReasons = append(dimensionReasons, task.Reasons...)
		}
		coverage := 0.0
		if plannedTaskWeight > 0 {
			coverage = coveredTaskWeight / plannedTaskWeight
		}
		var score *float64
		if counts.Scored >= config.MinimumScoredInstancesPerDimension && scoreWeight > 0 {
			value := weightedScore / scoreWeight
			score = &value
			scoredDimensions++
		}
		var interval *CapabilityConfidenceInterval
		if score != nil && counts.Scored >= config.MinimumScoredInstancesForInterval {
			samples := bootstrapCapabilityDimension(tasks, scoredValuesByTask, config.BootstrapSamples, random)
			dimensionBootstrap[dimension] = samples
			value := capabilityPercentileInterval(samples, config.ConfidenceLevel, counts.Scored)
			interval = &value
		}
		formal := score != nil && interval != nil && capabilityCoverageMeetsThreshold(coverage, config.FormalCoverageThreshold)
		if formal {
			formalDimensions++
		}
		weight := input.Package.Package.DimensionWeights[dimension]
		dimensionScore := CapabilityDimensionScore{
			Dimension: dimension, Weight: weight, Score: score, Interval: interval,
			Coverage: coverage, Formal: formal, Counts: counts, Tasks: tasks, Reasons: uniqueSortedStrings(dimensionReasons),
		}
		dimensions = append(dimensions, dimensionScore)
		overallCoverage += weight * coverage
		if score != nil {
			indexWeight += weight
			indexWeighted += weight * *score
		}
	}

	reasons := make([]string, 0)
	var index *float64
	var indexInterval *CapabilityConfidenceInterval
	status := CapabilityAggregationInsufficientEvidence
	if scoredDimensions >= config.MinimumDimensionsForIndex && indexWeight > 0 {
		value := indexWeighted / indexWeight
		index = &value
		status = CapabilityAggregationProvisional
	}
	if index != nil {
		canBootstrapIndex := true
		indexSampleCount := 0
		for _, dimension := range dimensions {
			if dimension.Score == nil {
				continue
			}
			if len(dimensionBootstrap[dimension.Dimension]) != config.BootstrapSamples {
				canBootstrapIndex = false
				break
			}
			indexSampleCount += dimension.Counts.Scored
		}
		if canBootstrapIndex {
			samples := make([]float64, config.BootstrapSamples)
			for sampleIndex := range samples {
				weighted, weight := 0.0, 0.0
				for _, dimension := range dimensions {
					values := dimensionBootstrap[dimension.Dimension]
					if len(values) == 0 {
						continue
					}
					weighted += dimension.Weight * values[sampleIndex]
					weight += dimension.Weight
				}
				samples[sampleIndex] = weighted / weight
			}
			value := capabilityPercentileInterval(samples, config.ConfidenceLevel, indexSampleCount)
			indexInterval = &value
		}
	}
	formal := index != nil && indexInterval != nil && formalDimensions >= config.MinimumFormalDimensions && capabilityCoverageMeetsThreshold(overallCoverage, config.FormalCoverageThreshold)
	if formal {
		status = CapabilityAggregationComplete
	} else {
		if scoredDimensions < config.MinimumDimensionsForIndex {
			reasons = append(reasons, fmt.Sprintf("有分数维度数 %d 低于最低要求 %d", scoredDimensions, config.MinimumDimensionsForIndex))
		}
		if formalDimensions < config.MinimumFormalDimensions {
			reasons = append(reasons, fmt.Sprintf("正式维度数 %d 低于最低要求 %d", formalDimensions, config.MinimumFormalDimensions))
		}
		if !capabilityCoverageMeetsThreshold(overallCoverage, config.FormalCoverageThreshold) {
			reasons = append(reasons, fmt.Sprintf("总体覆盖率 %.4f 低于正式阈值 %.4f", overallCoverage, config.FormalCoverageThreshold))
		}
		if index != nil && indexInterval == nil {
			reasons = append(reasons, "至少一个计分维度未达到置信区间最低样本数")
		}
	}
	if input.StopReason == CapabilityRunStoppedByBudget {
		if totalCounts.Missing == 0 {
			return CapabilityReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "预算停止报告必须存在未执行实例")
		}
		formal = false
		status = CapabilityAggregationPartialBudget
		reasons = append(reasons, "运行达到请求或 token 预算上限，报告仅包含部分结果")
	}
	if totalCounts.ExecutionFailed > 0 && !formal {
		failureReasons := make([]string, 0)
		for _, dimension := range dimensions {
			failureReasons = append(failureReasons, dimension.Reasons...)
		}
		reasons = append(reasons, fmt.Sprintf("%d 个能力样本执行失败", totalCounts.ExecutionFailed))
		reasons = append(reasons, failureReasons...)
	}
	if totalCounts.ScoringFailed > 0 && !formal {
		reasons = append(reasons, fmt.Sprintf("%d 个能力样本判分失败", totalCounts.ScoringFailed))
	}
	report := CapabilityReport{
		Aggregator: config.Aggregator, Package: input.Package.Package.Ref, PackageSHA256: input.Package.SHA256,
		Status: status, Formal: formal, Index: index, IndexInterval: indexInterval, Coverage: overallCoverage,
		ScoredDimensions: scoredDimensions, FormalDimensions: formalDimensions,
		Counts: totalCounts, Dimensions: dimensions, Reasons: uniqueSortedStrings(reasons), StopReason: input.StopReason,
	}
	if err := report.Validate(); err != nil {
		return CapabilityReport{}, err
	}
	return report, nil
}

func capabilityCoverageMeetsThreshold(coverage, threshold float64) bool {
	return coverage+capabilityCoverageEpsilon >= threshold
}

func bootstrapCapabilityDimension(tasks []CapabilityTaskScore, scoresByTask map[string][]float64, samples int, random *rand.Rand) []float64 {
	result := make([]float64, samples)
	for sampleIndex := 0; sampleIndex < samples; sampleIndex++ {
		weighted, weight := 0.0, 0.0
		for _, task := range tasks {
			values := scoresByTask[task.Task.ID]
			if len(values) == 0 {
				continue
			}
			total := 0.0
			for draw := 0; draw < len(values); draw++ {
				total += values[random.Intn(len(values))]
			}
			weighted += task.Weight * total / float64(len(values))
			weight += task.Weight
		}
		result[sampleIndex] = weighted / weight
	}
	return result
}

func capabilityPercentileInterval(samples []float64, confidenceLevel float64, sampleCount int) CapabilityConfidenceInterval {
	ordered := append([]float64(nil), samples...)
	sort.Float64s(ordered)
	tail := (1 - confidenceLevel) / 2
	return CapabilityConfidenceInterval{
		Lower: empiricalQuantile(ordered, tail), Upper: empiricalQuantile(ordered, 1-tail),
		Level: confidenceLevel, Resamples: len(samples), SampleCount: sampleCount, Method: "stratified_bootstrap_percentile",
	}
}

func addCapabilityCounts(target *CapabilityResultCounts, source CapabilityResultCounts) {
	target.Planned += source.Planned
	target.Scored += source.Scored
	target.Skipped += source.Skipped
	target.Unsupported += source.Unsupported
	target.Indeterminate += source.Indeterminate
	target.ExecutionFailed += source.ExecutionFailed
	target.ScoringFailed += source.ScoringFailed
	target.Missing += source.Missing
}
