package modelaudit

import (
	"fmt"
	"math"
	"time"
)

type AuditRunStartLimits struct {
	MaximumActiveRuns int   `json:"maximumActiveRuns"`
	LeaseDurationMs   int64 `json:"leaseDurationMs"`
}

func (l AuditRunStartLimits) Validate() error {
	if l.MaximumActiveRuns <= 0 || l.LeaseDurationMs <= 0 || l.LeaseDurationMs > math.MaxInt64/int64(time.Millisecond) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行活动上限或租约时长无效")
	}
	return nil
}

type AuditRunStartInput struct {
	Job              AuditJob            `json:"job"`
	ExpectedRevision uint64              `json:"expectedRevision"`
	RunID            string              `json:"runId"`
	Trigger          AuditRunTrigger     `json:"trigger"`
	ScheduledSlot    *AuditScheduleSlot  `json:"scheduledSlot,omitempty"`
	OwnerID          string              `json:"ownerId"`
	LeaseToken       string              `json:"leaseToken"`
	Now              time.Time           `json:"now"`
	ActiveRuns       int                 `json:"activeRuns"`
	Limits           AuditRunStartLimits `json:"limits"`
	ExistingLease    *AuditRunLease      `json:"existingLease,omitempty"`
}

type AuditRunStart struct {
	Job   AuditJob      `json:"job"`
	Run   AuditRun      `json:"run"`
	Lease AuditRunLease `json:"lease"`
}

func PrepareAuditRunStart(input AuditRunStartInput) (AuditRunStart, error) {
	if err := input.Job.Validate(); err != nil {
		return AuditRunStart{}, err
	}
	if input.ExpectedRevision != input.Job.Revision {
		return AuditRunStart{}, auditRevisionConflict(input.Job.Revision, input.ExpectedRevision)
	}
	if input.Job.Status != AuditJobEnabled {
		return AuditRunStart{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, fmt.Sprintf("状态 %q 的审计任务不能启动运行", input.Job.Status))
	}
	if !validAuditEntityID(input.RunID) || !validAuditEntityID(input.OwnerID) || !validAuditEntityID(input.LeaseToken) ||
		!input.Trigger.Valid() || input.Now.IsZero() || input.ActiveRuns < 0 {
		return AuditRunStart{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行启动参数无效")
	}
	if err := input.Limits.Validate(); err != nil {
		return AuditRunStart{}, err
	}
	if input.ActiveRuns >= input.Limits.MaximumActiveRuns {
		return AuditRunStart{}, contractError(ErrorCodeConcurrencyLimited, ErrorCategoryRequest, "审计运行已达到全局并发上限")
	}
	if (input.Trigger == AuditRunScheduled) != (input.ScheduledSlot != nil) {
		return AuditRunStart{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "定时触发必须且只能携带调度槽位")
	}
	if input.ScheduledSlot != nil {
		if err := input.ScheduledSlot.ValidateForJob(input.Job.ID, input.Job.Schedule); err != nil {
			return AuditRunStart{}, err
		}
		if input.ScheduledSlot.ScheduledAt.After(input.Now) {
			return AuditRunStart{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "定时审计槽位尚未到期")
		}
	}
	if err := validateAuditScheduleOpen(input.Job.Schedule, input.Now); err != nil {
		return AuditRunStart{}, err
	}
	jobSnapshotSHA256, err := AuditJobSnapshotSHA256(input.Job)
	if err != nil {
		return AuditRunStart{}, err
	}
	leaseExpiresAt := input.Now.Add(time.Duration(input.Limits.LeaseDurationMs) * time.Millisecond)
	candidateLease := AuditRunLease{
		JobID: input.Job.ID, RunID: input.RunID, OwnerID: input.OwnerID, Token: input.LeaseToken,
		AcquiredAt: input.Now.UTC(), ExpiresAt: leaseExpiresAt.UTC(),
	}
	lease, err := AcquireAuditRunLease(input.ExistingLease, candidateLease, input.Now)
	if err != nil {
		return AuditRunStart{}, err
	}
	var scheduledFor *time.Time
	if input.ScheduledSlot != nil {
		scheduledFor = cloneTimePointer(&input.ScheduledSlot.ScheduledAt)
	}
	run := AuditRun{
		ID: input.RunID, JobID: input.Job.ID, JobRevision: input.Job.Revision, JobSnapshotSHA256: jobSnapshotSHA256,
		Trigger: input.Trigger, ScheduledFor: scheduledFor, Status: AuditRunPending,
		Budget: input.Job.Budget, CreatedAt: input.Now.UTC(),
	}
	if err := run.Validate(); err != nil {
		return AuditRunStart{}, err
	}
	updatedJob, err := TransitionAuditJob(input.Job, input.ExpectedRevision, AuditJobRunning, input.Now)
	if err != nil {
		return AuditRunStart{}, err
	}
	return AuditRunStart{Job: updatedJob, Run: run, Lease: lease}, nil
}

func AcquireAuditRunLease(existing *AuditRunLease, candidate AuditRunLease, now time.Time) (AuditRunLease, error) {
	if err := candidate.Validate(); err != nil {
		return AuditRunLease{}, err
	}
	if now.IsZero() || !candidate.AcquiredAt.Equal(now.UTC()) {
		return AuditRunLease{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计租约获取时间与当前时间不一致")
	}
	if existing == nil {
		return candidate, nil
	}
	if err := existing.Validate(); err != nil {
		return AuditRunLease{}, err
	}
	if existing.JobID != candidate.JobID {
		return AuditRunLease{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "不能用其他任务的租约判断审计重入")
	}
	if existing.ExpiresAt.After(now) {
		return AuditRunLease{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, fmt.Sprintf("审计任务已有活动运行 %q", existing.RunID))
	}
	return candidate, nil
}

func RenewAuditRunLease(current AuditRunLease, ownerID, token string, now time.Time, durationMs int64) (AuditRunLease, error) {
	if err := current.Validate(); err != nil {
		return AuditRunLease{}, err
	}
	if !validAuditEntityID(ownerID) || !validAuditEntityID(token) || now.IsZero() || now.Before(current.AcquiredAt) || durationMs <= 0 ||
		durationMs > math.MaxInt64/int64(time.Millisecond) {
		return AuditRunLease{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计租约续期参数无效")
	}
	if current.OwnerID != ownerID || current.Token != token {
		return AuditRunLease{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计租约 owner 或 token 不匹配")
	}
	if !current.ExpiresAt.After(now) {
		return AuditRunLease{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计租约已经过期，不能续期")
	}
	renewed := current
	renewed.ExpiresAt = now.Add(time.Duration(durationMs) * time.Millisecond).UTC()
	if !renewed.ExpiresAt.After(current.ExpiresAt) {
		return AuditRunLease{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计租约续期后必须延长有效期")
	}
	if err := renewed.Validate(); err != nil {
		return AuditRunLease{}, err
	}
	return renewed, nil
}

func ValidateAuditRunLeaseRelease(current AuditRunLease, runID, ownerID, token string) error {
	if err := current.Validate(); err != nil {
		return err
	}
	if current.RunID != runID || current.OwnerID != ownerID || current.Token != token {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计租约释放的 run、owner 或 token 不匹配")
	}
	return nil
}

type AuditRunFinishCoordination struct {
	Job          AuditJob `json:"job"`
	ReleaseToken string   `json:"releaseToken"`
}

func PrepareAuditRunFinish(job AuditJob, expectedRevision uint64, run AuditRun, lease AuditRunLease, finishedAt time.Time) (AuditRunFinishCoordination, error) {
	if err := job.Validate(); err != nil {
		return AuditRunFinishCoordination{}, err
	}
	if expectedRevision != job.Revision {
		return AuditRunFinishCoordination{}, auditRevisionConflict(job.Revision, expectedRevision)
	}
	if job.Status != AuditJobRunning || !run.Status.Terminal() || run.JobID != job.ID || run.ID != lease.RunID || lease.JobID != job.ID {
		return AuditRunFinishCoordination{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行、任务与租约不能完成协调")
	}
	if run.JobRevision == math.MaxUint64 || run.JobRevision+1 != job.Revision {
		return AuditRunFinishCoordination{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行引用的任务版本与 running 状态版本不一致")
	}
	if err := run.Validate(); err != nil {
		return AuditRunFinishCoordination{}, err
	}
	if err := lease.Validate(); err != nil {
		return AuditRunFinishCoordination{}, err
	}
	if finishedAt.IsZero() || run.FinishedAt == nil || !run.FinishedAt.Equal(finishedAt) {
		return AuditRunFinishCoordination{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行完成时间不一致")
	}
	endAt, err := job.Schedule.EffectiveEndAt()
	if err != nil {
		return AuditRunFinishCoordination{}, err
	}
	nextStatus := AuditJobEnabled
	if job.Schedule.OneShot || endAt != nil && !endAt.After(finishedAt) {
		nextStatus = AuditJobExpired
	}
	updatedJob, err := TransitionAuditJob(job, expectedRevision, nextStatus, finishedAt)
	if err != nil {
		return AuditRunFinishCoordination{}, err
	}
	return AuditRunFinishCoordination{Job: updatedJob, ReleaseToken: lease.Token}, nil
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}
