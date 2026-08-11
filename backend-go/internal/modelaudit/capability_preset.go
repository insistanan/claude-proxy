package modelaudit

import (
	"fmt"
	"math"
	"sort"
)

type CapabilityRunStopReason string

const (
	CapabilityRunStopNone        CapabilityRunStopReason = ""
	CapabilityRunStoppedByBudget CapabilityRunStopReason = "budget_exhausted"
)

func (r CapabilityRunStopReason) Valid() bool {
	return r == CapabilityRunStopNone || r == CapabilityRunStoppedByBudget
}

type CapabilityPresetMode string

const (
	CapabilityPresetQuick    CapabilityPresetMode = "quick"
	CapabilityPresetStandard CapabilityPresetMode = "standard"
	CapabilityPresetDeep     CapabilityPresetMode = "deep"
)

func (m CapabilityPresetMode) Valid() bool {
	return m == CapabilityPresetQuick || m == CapabilityPresetStandard || m == CapabilityPresetDeep
}

type CapabilityPresetTask struct {
	TaskID      string `json:"taskId"`
	Repetitions int    `json:"repetitions"`
}

type CapabilityBudgetLimits struct {
	Requests     int   `json:"requests"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

func (l CapabilityBudgetLimits) Validate() error {
	if l.Requests <= 0 || l.InputTokens <= 0 || l.OutputTokens <= 0 || l.TotalTokens <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力预算上限必须全部大于 0")
	}
	return nil
}

type CapabilityRunPreset struct {
	Ref            VersionedRef           `json:"ref"`
	Mode           CapabilityPresetMode   `json:"mode"`
	Package        VersionedRef           `json:"package"`
	FormalEligible bool                   `json:"formalEligible"`
	Tasks          []CapabilityPresetTask `json:"tasks"`
	Limits         CapabilityBudgetLimits `json:"limits"`
}

func (p CapabilityRunPreset) Validate() error {
	if err := p.Ref.Validate("能力运行预设"); err != nil {
		return err
	}
	if !p.Mode.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力运行预设模式无效")
	}
	if err := p.Package.Validate("能力运行预设任务包"); err != nil {
		return err
	}
	if (p.Mode == CapabilityPresetQuick) == p.FormalEligible {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "快速预设不能生成正式结论，标准或深入预设必须具备正式资格")
	}
	if len(p.Tasks) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力运行预设任务不能为空")
	}
	seen := make(map[string]struct{}, len(p.Tasks))
	for _, task := range p.Tasks {
		if !stableIDPattern.MatchString(task.TaskID) || task.Repetitions <= 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力运行预设任务 ID 或重复次数无效")
		}
		if _, duplicate := seen[task.TaskID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力运行预设任务 %q 重复", task.TaskID))
		}
		seen[task.TaskID] = struct{}{}
	}
	return p.Limits.Validate()
}

type CapabilityBudgetEstimate struct {
	Requests     int   `json:"requests"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

func (e CapabilityBudgetEstimate) Validate() error {
	if e.Requests <= 0 || e.InputTokens <= 0 || e.OutputTokens <= 0 || e.InputTokens > math.MaxInt64-e.OutputTokens ||
		e.TotalTokens != e.InputTokens+e.OutputTokens {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力预算估算无效")
	}
	return nil
}

type CapabilityRunPlan struct {
	Preset         VersionedRef                 `json:"preset"`
	Mode           CapabilityPresetMode         `json:"mode"`
	FormalEligible bool                         `json:"formalEligible"`
	Package        VersionedRef                 `json:"package"`
	PackageSHA256  string                       `json:"packageSha256"`
	Tasks          []CapabilityTaskCoveragePlan `json:"tasks"`
	Estimate       CapabilityBudgetEstimate     `json:"estimate"`
	Limits         CapabilityBudgetLimits       `json:"limits"`
}

func (p CapabilityRunPlan) Validate() error {
	if err := p.Preset.Validate("能力运行计划预设"); err != nil {
		return err
	}
	if !p.Mode.Valid() || (p.Mode == CapabilityPresetQuick) == p.FormalEligible {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力运行计划模式或正式资格无效")
	}
	if err := p.Package.Validate("能力运行计划任务包"); err != nil {
		return err
	}
	if !validSHA256(p.PackageSHA256) || len(p.Tasks) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力运行计划缺少任务包哈希或任务")
	}
	requestCount := 0
	seenTasks := make(map[string]struct{}, len(p.Tasks))
	covered := make(map[CapabilityDimension]bool, len(capabilityDimensions))
	for _, task := range p.Tasks {
		if err := task.Validate(); err != nil {
			return err
		}
		if _, duplicate := seenTasks[task.Task.ID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力运行计划包含重复任务")
		}
		seenTasks[task.Task.ID] = struct{}{}
		covered[task.Dimension] = true
		var err error
		requestCount, err = safeAddInt(requestCount, task.PlannedInstances)
		if err != nil {
			return err
		}
	}
	if p.FormalEligible {
		for _, dimension := range capabilityDimensions {
			if !covered[dimension] {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正式能力运行计划未覆盖全部七维")
			}
		}
	}
	if err := p.Estimate.Validate(); err != nil {
		return err
	}
	if err := p.Limits.Validate(); err != nil {
		return err
	}
	if requestCount != p.Estimate.Requests || !capabilityEstimateFits(p.Estimate, p.Limits) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力运行计划任务数或预算不一致")
	}
	return nil
}

func PrepareCapabilityRun(preset CapabilityRunPreset, snapshot CapabilityTaskPackageSnapshot) (CapabilityRunPlan, error) {
	if err := preset.Validate(); err != nil {
		return CapabilityRunPlan{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return CapabilityRunPlan{}, err
	}
	if preset.Package != snapshot.Package.Ref {
		return CapabilityRunPlan{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力预设引用的任务包版本与快照不一致")
	}
	tasks := make(map[string]CapabilityTaskDefinition, len(snapshot.Package.Tasks))
	for _, task := range snapshot.Package.Tasks {
		tasks[task.Ref.ID] = task
	}
	planTasks := make([]CapabilityTaskCoveragePlan, 0, len(preset.Tasks))
	covered := make(map[CapabilityDimension]bool, len(capabilityDimensions))
	estimate := CapabilityBudgetEstimate{}
	for _, selection := range preset.Tasks {
		task, found := tasks[selection.TaskID]
		if !found {
			return CapabilityRunPlan{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力预设任务 %q 不在任务包中", selection.TaskID))
		}
		if selection.Repetitions > task.Repetitions {
			return CapabilityRunPlan{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("能力预设任务 %q 重复次数超过任务上限", selection.TaskID))
		}
		var err error
		estimate.Requests, err = safeAddInt(estimate.Requests, selection.Repetitions)
		if err != nil {
			return CapabilityRunPlan{}, err
		}
		estimate.InputTokens, err = safeAddProduct(estimate.InputTokens, task.MaximumInputTokens, selection.Repetitions)
		if err != nil {
			return CapabilityRunPlan{}, err
		}
		estimate.OutputTokens, err = safeAddProduct(estimate.OutputTokens, task.MaximumOutputTokens, selection.Repetitions)
		if err != nil {
			return CapabilityRunPlan{}, err
		}
		covered[task.Dimension] = true
		planTasks = append(planTasks, CapabilityTaskCoveragePlan{Task: task.Ref, Dimension: task.Dimension, PlannedInstances: selection.Repetitions})
	}
	var err error
	estimate.TotalTokens, err = safeAddInt64(estimate.InputTokens, estimate.OutputTokens)
	if err != nil {
		return CapabilityRunPlan{}, err
	}
	if preset.FormalEligible {
		for _, dimension := range capabilityDimensions {
			if !covered[dimension] {
				return CapabilityRunPlan{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("正式能力预设未覆盖维度 %q", dimension))
			}
		}
	}
	if !capabilityEstimateFits(estimate, preset.Limits) {
		return CapabilityRunPlan{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力预设的最坏预算估算超过预设上限")
	}
	sort.Slice(planTasks, func(i, j int) bool { return planTasks[i].Task.ID < planTasks[j].Task.ID })
	plan := CapabilityRunPlan{
		Preset: preset.Ref, Mode: preset.Mode, FormalEligible: preset.FormalEligible,
		Package: snapshot.Package.Ref, PackageSHA256: snapshot.SHA256,
		Tasks: planTasks, Estimate: estimate, Limits: preset.Limits,
	}
	if err := plan.Validate(); err != nil {
		return CapabilityRunPlan{}, err
	}
	return plan, nil
}

type CapabilityBudgetUsage struct {
	Requests     int   `json:"requests"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

func (u CapabilityBudgetUsage) Validate() error {
	if u.Requests < 0 || u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 ||
		u.InputTokens > math.MaxInt64-u.OutputTokens || u.TotalTokens != u.InputTokens+u.OutputTokens {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力预算用量无效")
	}
	return nil
}

type CapabilityBudgetLimit string

const (
	CapabilityBudgetRequestLimit CapabilityBudgetLimit = "request_limit"
	CapabilityBudgetInputLimit   CapabilityBudgetLimit = "input_token_limit"
	CapabilityBudgetOutputLimit  CapabilityBudgetLimit = "output_token_limit"
	CapabilityBudgetTotalLimit   CapabilityBudgetLimit = "total_token_limit"
)

func (l CapabilityBudgetLimit) Valid() bool {
	return l == CapabilityBudgetRequestLimit || l == CapabilityBudgetInputLimit ||
		l == CapabilityBudgetOutputLimit || l == CapabilityBudgetTotalLimit
}

type CapabilityBudgetDecision struct {
	Allowed   bool                    `json:"allowed"`
	Reasons   []CapabilityBudgetLimit `json:"reasons,omitempty"`
	Projected CapabilityBudgetUsage   `json:"projected"`
}

func (d CapabilityBudgetDecision) Validate() error {
	if err := d.Projected.Validate(); err != nil {
		return err
	}
	if d.Allowed != (len(d.Reasons) == 0) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力预算决策状态与原因不一致")
	}
	seen := make(map[CapabilityBudgetLimit]struct{}, len(d.Reasons))
	for _, reason := range d.Reasons {
		if !reason.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力预算决策包含无效原因")
		}
		if _, duplicate := seen[reason]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力预算决策包含重复原因")
		}
		seen[reason] = struct{}{}
	}
	return nil
}

func CheckCapabilityBudget(plan CapabilityRunPlan, usage CapabilityBudgetUsage, nextTask CapabilityTaskDefinition) (CapabilityBudgetDecision, error) {
	if err := plan.Validate(); err != nil {
		return CapabilityBudgetDecision{}, err
	}
	if err := usage.Validate(); err != nil {
		return CapabilityBudgetDecision{}, err
	}
	if err := nextTask.Validate(); err != nil {
		return CapabilityBudgetDecision{}, err
	}
	selected := false
	for _, task := range plan.Tasks {
		if task.Task == nextTask.Ref {
			selected = true
			break
		}
	}
	if !selected {
		return CapabilityBudgetDecision{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "下一能力任务不在冻结运行计划中")
	}
	if usage.Requests > plan.Limits.Requests || usage.InputTokens > plan.Limits.InputTokens ||
		usage.OutputTokens > plan.Limits.OutputTokens || usage.TotalTokens > plan.Limits.TotalTokens {
		return CapabilityBudgetDecision{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "当前能力预算用量已经超过运行计划上限")
	}
	projectedRequests, err := safeAddInt(usage.Requests, 1)
	if err != nil {
		return CapabilityBudgetDecision{}, err
	}
	projectedInput, err := safeAddProduct(usage.InputTokens, nextTask.MaximumInputTokens, 1)
	if err != nil {
		return CapabilityBudgetDecision{}, err
	}
	projectedOutput, err := safeAddProduct(usage.OutputTokens, nextTask.MaximumOutputTokens, 1)
	if err != nil {
		return CapabilityBudgetDecision{}, err
	}
	projectedTotal, err := safeAddInt64(projectedInput, projectedOutput)
	if err != nil {
		return CapabilityBudgetDecision{}, err
	}
	projected := CapabilityBudgetUsage{Requests: projectedRequests, InputTokens: projectedInput, OutputTokens: projectedOutput, TotalTokens: projectedTotal}
	reasons := make([]CapabilityBudgetLimit, 0, 4)
	if projected.Requests > plan.Limits.Requests {
		reasons = append(reasons, CapabilityBudgetRequestLimit)
	}
	if projected.InputTokens > plan.Limits.InputTokens {
		reasons = append(reasons, CapabilityBudgetInputLimit)
	}
	if projected.OutputTokens > plan.Limits.OutputTokens {
		reasons = append(reasons, CapabilityBudgetOutputLimit)
	}
	if projected.TotalTokens > plan.Limits.TotalTokens {
		reasons = append(reasons, CapabilityBudgetTotalLimit)
	}
	decision := CapabilityBudgetDecision{Allowed: len(reasons) == 0, Reasons: reasons, Projected: projected}
	if err := decision.Validate(); err != nil {
		return CapabilityBudgetDecision{}, err
	}
	return decision, nil
}

func capabilityEstimateFits(estimate CapabilityBudgetEstimate, limits CapabilityBudgetLimits) bool {
	return estimate.Requests <= limits.Requests && estimate.InputTokens <= limits.InputTokens &&
		estimate.OutputTokens <= limits.OutputTokens && estimate.TotalTokens <= limits.TotalTokens
}

func safeAddInt(current, value int) (int, error) {
	if current < 0 || value < 0 || current > math.MaxInt-value {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力请求预算溢出")
	}
	return current + value, nil
}

func safeAddProduct(current int64, value, count int) (int64, error) {
	if current < 0 || value < 0 || count < 0 || (value > 0 && int64(count) > math.MaxInt64/int64(value)) {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力 token 预算溢出")
	}
	product := int64(value) * int64(count)
	if current > math.MaxInt64-product {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力 token 预算溢出")
	}
	return current + product, nil
}

func safeAddInt64(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力 token 预算溢出")
	}
	return left + right, nil
}
