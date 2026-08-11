package modelaudit

import (
	"fmt"
	"math"
	"time"
)

func ActivateAuditRun(run AuditRun, job AuditJob, targets []AuditRunTargetSnapshot, startedAt time.Time) (AuditRun, error) {
	if err := run.Validate(); err != nil {
		return AuditRun{}, err
	}
	if err := job.Validate(); err != nil {
		return AuditRun{}, err
	}
	if run.Status != AuditRunPending || job.Status != AuditJobRunning || run.JobID != job.ID ||
		run.JobRevision == math.MaxUint64 || run.JobRevision+1 != job.Revision {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "待运行实例与 running 任务不一致")
	}
	if startedAt.IsZero() || startedAt.Before(run.CreatedAt) || len(targets) != len(job.Targets) {
		return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行启动时间或目标数量无效")
	}
	requestedByID := make(map[string]AuditJobTarget, len(job.Targets))
	for _, target := range job.Targets {
		requestedByID[target.ID] = target
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if err := target.Validate(); err != nil {
			return AuditRun{}, err
		}
		requested, exists := requestedByID[target.TargetID]
		if !exists || requested != target.Requested {
			return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("运行目标 %q 不属于冻结任务", target.TargetID))
		}
		if _, duplicate := seen[target.TargetID]; duplicate {
			return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行启动包含重复目标")
		}
		seen[target.TargetID] = struct{}{}
	}
	activated := run
	activated.Status = AuditRunRunning
	startedAt = startedAt.UTC()
	activated.StartedAt = &startedAt
	activated.Targets = cloneAuditRunTargetSnapshots(targets)
	if err := activated.Validate(); err != nil {
		return AuditRun{}, err
	}
	return activated, nil
}

type AuditBudgetLimit string

const (
	AuditBudgetRequestLimit AuditBudgetLimit = "request_limit"
	AuditBudgetInputLimit   AuditBudgetLimit = "input_token_limit"
	AuditBudgetOutputLimit  AuditBudgetLimit = "output_token_limit"
	AuditBudgetTotalLimit   AuditBudgetLimit = "total_token_limit"
)

func (l AuditBudgetLimit) Valid() bool {
	return l == AuditBudgetRequestLimit || l == AuditBudgetInputLimit || l == AuditBudgetOutputLimit || l == AuditBudgetTotalLimit
}

type AuditRequestBudget struct {
	MaximumInputTokens  int64 `json:"maximumInputTokens"`
	MaximumOutputTokens int64 `json:"maximumOutputTokens"`
}

func (b AuditRequestBudget) Validate() error {
	if b.MaximumInputTokens <= 0 || b.MaximumOutputTokens <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "下一审计请求的 token 上限无效")
	}
	return nil
}

type AuditBudgetDecision struct {
	Allowed   bool               `json:"allowed"`
	Reasons   []AuditBudgetLimit `json:"reasons,omitempty"`
	Projected AuditRunUsage      `json:"projected"`
}

func (d AuditBudgetDecision) Validate() error {
	if err := d.Projected.Validate(); err != nil {
		return err
	}
	if d.Allowed != (len(d.Reasons) == 0) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计预算决策与原因不一致")
	}
	seen := make(map[AuditBudgetLimit]struct{}, len(d.Reasons))
	for _, reason := range d.Reasons {
		if !reason.Valid() {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计预算决策包含无效原因")
		}
		if _, duplicate := seen[reason]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计预算决策包含重复原因")
		}
		seen[reason] = struct{}{}
	}
	return nil
}

func CheckAuditRunBudget(run AuditRun, next AuditRequestBudget) (AuditBudgetDecision, error) {
	if err := run.Validate(); err != nil {
		return AuditBudgetDecision{}, err
	}
	if run.Status != AuditRunRunning {
		return AuditBudgetDecision{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "只有运行中的审计任务可以启动下一请求")
	}
	if err := next.Validate(); err != nil {
		return AuditBudgetDecision{}, err
	}
	projectedRequests, err := safeAuditAddInt(run.Usage.Requests, 1)
	if err != nil {
		return AuditBudgetDecision{}, err
	}
	projectedInput, err := safeAuditAddInt64(run.Usage.InputTokens, next.MaximumInputTokens)
	if err != nil {
		return AuditBudgetDecision{}, err
	}
	projectedOutput, err := safeAuditAddInt64(run.Usage.OutputTokens, next.MaximumOutputTokens)
	if err != nil {
		return AuditBudgetDecision{}, err
	}
	projectedTotal, err := safeAuditAddInt64(projectedInput, projectedOutput)
	if err != nil {
		return AuditBudgetDecision{}, err
	}
	projected := AuditRunUsage{
		Requests: projectedRequests, InputTokens: projectedInput, OutputTokens: projectedOutput, TotalTokens: projectedTotal,
	}
	reasons := make([]AuditBudgetLimit, 0, 4)
	if projected.Requests > run.Budget.MaxRequests {
		reasons = append(reasons, AuditBudgetRequestLimit)
	}
	if projected.InputTokens > run.Budget.MaxInputTokens {
		reasons = append(reasons, AuditBudgetInputLimit)
	}
	if projected.OutputTokens > run.Budget.MaxOutputTokens {
		reasons = append(reasons, AuditBudgetOutputLimit)
	}
	if projected.TotalTokens > run.Budget.MaxTotalTokens {
		reasons = append(reasons, AuditBudgetTotalLimit)
	}
	decision := AuditBudgetDecision{Allowed: len(reasons) == 0, Reasons: reasons, Projected: projected}
	if err := decision.Validate(); err != nil {
		return AuditBudgetDecision{}, err
	}
	return decision, nil
}

func RecordAuditRunUsage(run AuditRun, requestUsage AuditRunUsage) (AuditRun, error) {
	if err := run.Validate(); err != nil {
		return AuditRun{}, err
	}
	if run.Status != AuditRunRunning {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "只有运行中的审计任务可以记录请求用量")
	}
	if err := requestUsage.Validate(); err != nil {
		return AuditRun{}, err
	}
	if requestUsage.Requests != 1 {
		return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "单次审计请求用量的请求数必须为 1")
	}
	requests, err := safeAuditAddInt(run.Usage.Requests, requestUsage.Requests)
	if err != nil {
		return AuditRun{}, err
	}
	inputTokens, err := safeAuditAddInt64(run.Usage.InputTokens, requestUsage.InputTokens)
	if err != nil {
		return AuditRun{}, err
	}
	outputTokens, err := safeAuditAddInt64(run.Usage.OutputTokens, requestUsage.OutputTokens)
	if err != nil {
		return AuditRun{}, err
	}
	totalTokens, err := safeAuditAddInt64(inputTokens, outputTokens)
	if err != nil {
		return AuditRun{}, err
	}
	updated := run
	updated.Usage = AuditRunUsage{Requests: requests, InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens}
	if !updated.Usage.Fits(updated.Budget) {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计请求实际用量超过冻结运行预算")
	}
	if err := updated.Validate(); err != nil {
		return AuditRun{}, err
	}
	return updated, nil
}

func CompleteAuditRun(run AuditRun, finishedAt time.Time) (AuditRun, error) {
	return finishAuditRun(run, AuditRunCompleted, AuditRunStopNone, "", finishedAt)
}

func StopAuditRun(run AuditRun, reason AuditRunStopReason, failure string, finishedAt time.Time) (AuditRun, error) {
	if reason == AuditRunStopNone {
		return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "停止审计运行必须提供原因")
	}
	switch reason {
	case AuditRunStopBudget:
		if run.Status != AuditRunRunning {
			return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "只有运行中的审计任务可以因预算停止")
		}
		return finishAuditRun(run, AuditRunPartial, reason, "", finishedAt)
	case AuditRunStopCancelled:
		status := AuditRunCancelled
		if run.Usage != (AuditRunUsage{}) {
			status = AuditRunPartial
		}
		return finishAuditRun(run, status, reason, "", finishedAt)
	case AuditRunStopFailure:
		if run.Status != AuditRunRunning || failure == "" {
			return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "失败停止要求运行中状态和失败原因")
		}
		return finishAuditRun(run, AuditRunFailed, reason, failure, finishedAt)
	case AuditRunStopInterrupted:
		if run.Status != AuditRunRunning {
			return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "只有运行中的审计任务可以按中断恢复")
		}
		if run.Usage == (AuditRunUsage{}) {
			if failure == "" {
				return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "无已保存请求的中断运行必须说明失败原因")
			}
			return finishAuditRun(run, AuditRunFailed, reason, failure, finishedAt)
		}
		return finishAuditRun(run, AuditRunPartial, reason, "", finishedAt)
	default:
		return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行停止原因无效")
	}
}

func finishAuditRun(run AuditRun, status AuditRunStatus, reason AuditRunStopReason, failure string, finishedAt time.Time) (AuditRun, error) {
	if err := run.Validate(); err != nil {
		return AuditRun{}, err
	}
	if run.Status != AuditRunPending && run.Status != AuditRunRunning {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行已经处于终态")
	}
	if run.Status == AuditRunPending && status != AuditRunCancelled {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "待运行实例只能在启动前取消")
	}
	if finishedAt.IsZero() || finishedAt.Before(run.CreatedAt) || (run.StartedAt != nil && finishedAt.Before(*run.StartedAt)) {
		return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行结束时间无效")
	}
	finished := run
	finished.Status = status
	finished.StopReason = reason
	finished.Failure = failure
	finishedAt = finishedAt.UTC()
	finished.FinishedAt = &finishedAt
	if err := finished.Validate(); err != nil {
		return AuditRun{}, err
	}
	return finished, nil
}

func cloneAuditRunTargetSnapshots(values []AuditRunTargetSnapshot) []AuditRunTargetSnapshot {
	cloned := make([]AuditRunTargetSnapshot, len(values))
	for index, value := range values {
		cloned[index] = value
		if value.Resolved != nil {
			target := *value.Resolved
			if value.Resolved.ThinkingMap != nil {
				mapping := *value.Resolved.ThinkingMap
				if mapping.BudgetTokens != nil {
					budget := *mapping.BudgetTokens
					mapping.BudgetTokens = &budget
				}
				target.ThinkingMap = &mapping
			}
			cloned[index].Resolved = &target
		}
	}
	return cloned
}

func safeAuditAddInt(left, right int) (int, error) {
	if left < 0 || right < 0 || left > math.MaxInt-right {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计请求计数溢出")
	}
	return left + right, nil
}

func safeAuditAddInt64(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计 token 计数溢出")
	}
	return left + right, nil
}
