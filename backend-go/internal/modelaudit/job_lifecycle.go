package modelaudit

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

type AuditJobDefinition struct {
	Name     string           `json:"name"`
	Workload AuditWorkload    `json:"workload"`
	Targets  []AuditJobTarget `json:"targets"`
	Schedule AuditSchedule    `json:"schedule"`
	Budget   AuditRunBudget   `json:"budget"`
}

func (d AuditJobDefinition) Validate() error {
	if strings.TrimSpace(d.Name) == "" || strings.TrimSpace(d.Name) != d.Name || len(d.Targets) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务定义缺少名称或目标")
	}
	if err := d.Workload.Validate(); err != nil {
		return err
	}
	seenTargets := make(map[string]struct{}, len(d.Targets))
	for _, target := range d.Targets {
		if err := target.Validate(); err != nil {
			return err
		}
		if _, duplicate := seenTargets[target.ID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("审计任务目标 %q 重复", target.ID))
		}
		seenTargets[target.ID] = struct{}{}
	}
	if err := d.Schedule.Validate(); err != nil {
		return err
	}
	return d.Budget.Validate()
}

func NewAuditEntityID(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if !stableIDPattern.MatchString(prefix) {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计实体 ID 前缀无效")
	}
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "生成审计实体 ID 失败", err)
	}
	return prefix + "." + hex.EncodeToString(randomBytes), nil
}

func CreateAuditJob(id string, definition AuditJobDefinition, initialStatus AuditJobStatus, createdAt time.Time) (AuditJob, error) {
	if !validAuditEntityID(id) || createdAt.IsZero() {
		return AuditJob{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "创建审计任务缺少稳定 ID 或时间")
	}
	if initialStatus != AuditJobDraft && initialStatus != AuditJobEnabled {
		return AuditJob{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计任务只能以 draft 或 enabled 状态创建")
	}
	canonicalWorkload, err := CanonicalizeAuditWorkload(definition.Workload)
	if err != nil {
		return AuditJob{}, err
	}
	definition.Workload = canonicalWorkload
	if err := definition.Validate(); err != nil {
		return AuditJob{}, err
	}
	createdAt = createdAt.UTC()
	if initialStatus == AuditJobEnabled {
		if err := validateAuditScheduleOpen(definition.Schedule, createdAt); err != nil {
			return AuditJob{}, err
		}
	}
	job := AuditJob{
		ID: id, Name: definition.Name, Revision: 1, Status: initialStatus,
		Workload: definition.Workload, Targets: append([]AuditJobTarget(nil), definition.Targets...),
		Schedule: cloneAuditSchedule(definition.Schedule), Budget: definition.Budget, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := job.Validate(); err != nil {
		return AuditJob{}, err
	}
	return job, nil
}

func UpdateAuditJob(current AuditJob, expectedRevision uint64, definition AuditJobDefinition, updatedAt time.Time) (AuditJob, error) {
	if err := current.Validate(); err != nil {
		return AuditJob{}, err
	}
	if expectedRevision != current.Revision {
		return AuditJob{}, auditRevisionConflict(current.Revision, expectedRevision)
	}
	if current.Status == AuditJobRunning || current.Status == AuditJobExpired || current.Status == AuditJobDeleted {
		return AuditJob{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, fmt.Sprintf("状态 %q 的审计任务不能修改定义", current.Status))
	}
	if updatedAt.IsZero() || updatedAt.Before(current.UpdatedAt) {
		return AuditJob{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务更新时间无效")
	}
	nextRevision, err := nextAuditRevision(current.Revision)
	if err != nil {
		return AuditJob{}, err
	}
	canonicalWorkload, err := CanonicalizeAuditWorkload(definition.Workload)
	if err != nil {
		return AuditJob{}, err
	}
	definition.Workload = canonicalWorkload
	if err := definition.Validate(); err != nil {
		return AuditJob{}, err
	}
	if current.Status == AuditJobEnabled {
		if err := validateAuditScheduleOpen(definition.Schedule, updatedAt); err != nil {
			return AuditJob{}, err
		}
	}
	if err := validateStableTargetIdentities(current.Targets, definition.Targets); err != nil {
		return AuditJob{}, err
	}
	updated := current
	updated.Name = definition.Name
	updated.Workload = definition.Workload
	updated.Targets = append([]AuditJobTarget(nil), definition.Targets...)
	updated.Schedule = cloneAuditSchedule(definition.Schedule)
	updated.Budget = definition.Budget
	updated.Revision = nextRevision
	updated.UpdatedAt = updatedAt.UTC()
	if err := updated.Validate(); err != nil {
		return AuditJob{}, err
	}
	return updated, nil
}

func TransitionAuditJob(current AuditJob, expectedRevision uint64, nextStatus AuditJobStatus, changedAt time.Time) (AuditJob, error) {
	if err := current.Validate(); err != nil {
		return AuditJob{}, err
	}
	if expectedRevision != current.Revision {
		return AuditJob{}, auditRevisionConflict(current.Revision, expectedRevision)
	}
	if !nextStatus.Valid() || !auditJobTransitionAllowed(current.Status, nextStatus) {
		return AuditJob{}, contractError(
			ErrorCodeConflict,
			ErrorCategoryRequest,
			fmt.Sprintf("审计任务状态不能从 %q 转换为 %q", current.Status, nextStatus),
		)
	}
	if changedAt.IsZero() || changedAt.Before(current.UpdatedAt) {
		return AuditJob{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务状态更新时间无效")
	}
	if nextStatus == AuditJobEnabled || nextStatus == AuditJobRunning {
		if err := validateAuditScheduleOpen(current.Schedule, changedAt); err != nil {
			return AuditJob{}, err
		}
	}
	nextRevision, err := nextAuditRevision(current.Revision)
	if err != nil {
		return AuditJob{}, err
	}
	updated := current
	updated.Status = nextStatus
	updated.Revision = nextRevision
	updated.UpdatedAt = changedAt.UTC()
	if nextStatus == AuditJobDeleted {
		deletedAt := changedAt.UTC()
		updated.DeletedAt = &deletedAt
	}
	if err := updated.Validate(); err != nil {
		return AuditJob{}, err
	}
	return updated, nil
}

func auditJobTransitionAllowed(current, next AuditJobStatus) bool {
	if current == next || current == AuditJobDeleted || current == AuditJobRunning && next == AuditJobDeleted {
		return false
	}
	if next == AuditJobDeleted {
		return current != AuditJobRunning
	}
	switch current {
	case AuditJobDraft:
		return next == AuditJobEnabled
	case AuditJobEnabled:
		return next == AuditJobRunning || next == AuditJobPaused || next == AuditJobExpired
	case AuditJobRunning:
		return next == AuditJobEnabled || next == AuditJobExpired
	case AuditJobPaused:
		return next == AuditJobEnabled || next == AuditJobExpired
	default:
		return false
	}
}

func validateStableTargetIdentities(current, updated []AuditJobTarget) error {
	currentByID := make(map[string]AuditJobTarget, len(current))
	for _, target := range current {
		currentByID[target.ID] = target
	}
	for _, target := range updated {
		previous, exists := currentByID[target.ID]
		if !exists {
			continue
		}
		if previous.ChannelID != target.ChannelID || previous.ChannelKind != target.ChannelKind {
			return contractError(ErrorCodeConflict, ErrorCategoryRequest, fmt.Sprintf("审计目标 %q 不能改绑到其他渠道", target.ID))
		}
	}
	return nil
}

func nextAuditRevision(current uint64) (uint64, error) {
	if current == math.MaxUint64 {
		return 0, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计任务版本已达到上限")
	}
	return current + 1, nil
}

func auditRevisionConflict(actual, expected uint64) error {
	return contractError(
		ErrorCodeConflict,
		ErrorCategoryRequest,
		fmt.Sprintf("审计任务版本冲突：当前为 %d，请求基于 %d", actual, expected),
	)
}

func validateAuditScheduleOpen(schedule AuditSchedule, at time.Time) error {
	endAt, err := schedule.EffectiveEndAt()
	if err != nil {
		return err
	}
	if endAt != nil && !endAt.After(at) {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计任务调度已经结束，应转换为 expired")
	}
	return nil
}

func cloneAuditSchedule(schedule AuditSchedule) AuditSchedule {
	cloned := schedule
	if schedule.EndAt != nil {
		value := *schedule.EndAt
		cloned.EndAt = &value
	}
	return cloned
}
