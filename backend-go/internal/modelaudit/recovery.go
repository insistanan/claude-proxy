package modelaudit

import "time"

type AuditRecoveryAction string

const (
	AuditRecoveryWait                AuditRecoveryAction = "wait_for_lease"
	AuditRecoveryResumePending       AuditRecoveryAction = "resume_pending"
	AuditRecoveryFinalizeInterrupted AuditRecoveryAction = "finalize_interrupted"
)

func (a AuditRecoveryAction) Valid() bool {
	return a == AuditRecoveryWait || a == AuditRecoveryResumePending || a == AuditRecoveryFinalizeInterrupted
}

type AuditRecoveryDecision struct {
	Action AuditRecoveryAction `json:"action"`
	Job    AuditJob            `json:"job"`
	Run    AuditRun            `json:"run"`
}

func (d AuditRecoveryDecision) Validate() error {
	if !d.Action.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计恢复动作无效")
	}
	if err := d.Job.Validate(); err != nil {
		return err
	}
	if err := d.Run.Validate(); err != nil {
		return err
	}
	if d.Run.JobID != d.Job.ID {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计恢复任务与运行不一致")
	}
	if d.Action == AuditRecoveryFinalizeInterrupted {
		if !d.Run.Status.Terminal() || d.Run.StopReason != AuditRunStopInterrupted || d.Job.Status == AuditJobRunning {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计中断恢复没有生成一致的终态")
		}
	} else if d.Job.Status != AuditJobRunning || (d.Run.Status != AuditRunPending && d.Run.Status != AuditRunRunning) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "等待或继续恢复必须保留活动任务与运行")
	}
	return nil
}

func PlanAuditRecovery(entry AuditRecoveryEntry, now time.Time) (AuditRecoveryDecision, error) {
	if err := entry.Job.Validate(); err != nil {
		return AuditRecoveryDecision{}, err
	}
	if err := entry.Run.Validate(); err != nil {
		return AuditRecoveryDecision{}, err
	}
	if now.IsZero() || entry.Job.Status != AuditJobRunning || entry.Run.JobID != entry.Job.ID ||
		entry.Run.JobRevision == ^uint64(0) || entry.Run.JobRevision+1 != entry.Job.Revision ||
		(entry.Run.Status != AuditRunPending && entry.Run.Status != AuditRunRunning) {
		return AuditRecoveryDecision{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计恢复输入状态无效")
	}
	if entry.Lease != nil {
		if err := entry.Lease.Validate(); err != nil {
			return AuditRecoveryDecision{}, err
		}
		if entry.Lease.JobID != entry.Job.ID || entry.Lease.RunID != entry.Run.ID {
			return AuditRecoveryDecision{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计恢复租约与运行不一致")
		}
		if entry.Lease.ExpiresAt.After(now) {
			decision := AuditRecoveryDecision{Action: AuditRecoveryWait, Job: entry.Job, Run: entry.Run}
			return decision, decision.Validate()
		}
	}
	if entry.Run.Status == AuditRunPending {
		decision := AuditRecoveryDecision{Action: AuditRecoveryResumePending, Job: entry.Job, Run: entry.Run}
		return decision, decision.Validate()
	}
	terminalRun, err := StopAuditRun(
		entry.Run,
		AuditRunStopInterrupted,
		"服务重启前运行中断，且没有可确认的已完成请求",
		now,
	)
	if err != nil {
		return AuditRecoveryDecision{}, err
	}
	nextStatus := AuditJobEnabled
	endAt, err := entry.Job.Schedule.EffectiveEndAt()
	if err != nil {
		return AuditRecoveryDecision{}, err
	}
	if entry.Job.Schedule.OneShot || endAt != nil && !endAt.After(now) {
		nextStatus = AuditJobExpired
	}
	job, err := TransitionAuditJob(entry.Job, entry.Job.Revision, nextStatus, now)
	if err != nil {
		return AuditRecoveryDecision{}, err
	}
	decision := AuditRecoveryDecision{Action: AuditRecoveryFinalizeInterrupted, Job: job, Run: terminalRun}
	return decision, decision.Validate()
}
