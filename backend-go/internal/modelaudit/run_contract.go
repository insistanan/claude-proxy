package modelaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"time"
)

type AuditRunTrigger string

const (
	AuditRunManual    AuditRunTrigger = "manual"
	AuditRunScheduled AuditRunTrigger = "scheduled"
)

func (t AuditRunTrigger) Valid() bool {
	return t == AuditRunManual || t == AuditRunScheduled
}

type AuditRunStatus string

const (
	AuditRunPending   AuditRunStatus = "pending"
	AuditRunRunning   AuditRunStatus = "running"
	AuditRunCompleted AuditRunStatus = "completed"
	AuditRunPartial   AuditRunStatus = "partial"
	AuditRunFailed    AuditRunStatus = "failed"
	AuditRunCancelled AuditRunStatus = "cancelled"
)

func (s AuditRunStatus) Valid() bool {
	switch s {
	case AuditRunPending, AuditRunRunning, AuditRunCompleted, AuditRunPartial, AuditRunFailed, AuditRunCancelled:
		return true
	default:
		return false
	}
}

func (s AuditRunStatus) Terminal() bool {
	return s == AuditRunCompleted || s == AuditRunPartial || s == AuditRunFailed || s == AuditRunCancelled
}

type AuditRunStopReason string

const (
	AuditRunStopNone        AuditRunStopReason = ""
	AuditRunStopBudget      AuditRunStopReason = "budget_exhausted"
	AuditRunStopCancelled   AuditRunStopReason = "cancelled"
	AuditRunStopFailure     AuditRunStopReason = "execution_failed"
	AuditRunStopInterrupted AuditRunStopReason = "interrupted"
)

func (r AuditRunStopReason) Valid() bool {
	return r == AuditRunStopNone || r == AuditRunStopBudget || r == AuditRunStopCancelled ||
		r == AuditRunStopFailure || r == AuditRunStopInterrupted
}

type AuditRunUsage struct {
	Requests     int   `json:"requests"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	TotalTokens  int64 `json:"totalTokens"`
}

func (u AuditRunUsage) Validate() error {
	if u.Requests < 0 || u.InputTokens < 0 || u.OutputTokens < 0 || u.TotalTokens < 0 ||
		u.InputTokens > math.MaxInt64-u.OutputTokens || u.TotalTokens != u.InputTokens+u.OutputTokens {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行预算用量无效")
	}
	return nil
}

func (u AuditRunUsage) Fits(budget AuditRunBudget) bool {
	return u.Requests <= budget.MaxRequests && u.InputTokens <= budget.MaxInputTokens &&
		u.OutputTokens <= budget.MaxOutputTokens && u.TotalTokens <= budget.MaxTotalTokens
}

type AuditRunTargetSnapshot struct {
	TargetID        string          `json:"targetId"`
	Requested       AuditJobTarget  `json:"requested"`
	Resolved        *TargetSnapshot `json:"resolved,omitempty"`
	ResolutionError string          `json:"resolutionError,omitempty"`
}

func (s AuditRunTargetSnapshot) Validate() error {
	if !validAuditEntityID(s.TargetID) || s.TargetID != s.Requested.ID {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行目标 ID 与任务目标不一致")
	}
	if err := s.Requested.Validate(); err != nil {
		return err
	}
	if (s.Resolved == nil) == (strings.TrimSpace(s.ResolutionError) == "") {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行目标必须且只能包含解析结果或解析错误")
	}
	if s.Resolved == nil {
		return nil
	}
	target := s.Resolved
	if target.ChannelID != s.Requested.ChannelID || target.ChannelKind != s.Requested.ChannelKind ||
		target.Protocol != s.Requested.Protocol || target.RequestedModel != s.Requested.Model ||
		target.Thinking != s.Requested.Thinking || target.RequestProfile != s.Requested.RequestProfile ||
		!target.WireProtocol.Valid() || strings.TrimSpace(target.ResolvedModel) == "" || target.CapturedAt.IsZero() ||
		(target.RequestedModel == "" && strings.TrimSpace(target.DefaultModel) == "") {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行目标解析快照与任务目标不一致")
	}
	if (target.Thinking == ThinkingUnset) != (target.ThinkingMap == nil) ||
		(target.ThinkingMap != nil && target.ThinkingMap.Level != target.Thinking) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行目标思考档位与映射不一致")
	}
	return nil
}

type AuditRun struct {
	ID                string                   `json:"id"`
	JobID             string                   `json:"jobId"`
	JobRevision       uint64                   `json:"jobRevision"`
	JobSnapshotSHA256 string                   `json:"jobSnapshotSha256"`
	Trigger           AuditRunTrigger          `json:"trigger"`
	ScheduledFor      *time.Time               `json:"scheduledFor,omitempty"`
	Status            AuditRunStatus           `json:"status"`
	StopReason        AuditRunStopReason       `json:"stopReason,omitempty"`
	Failure           string                   `json:"failure,omitempty"`
	Budget            AuditRunBudget           `json:"budget"`
	Usage             AuditRunUsage            `json:"usage"`
	Targets           []AuditRunTargetSnapshot `json:"targets,omitempty"`
	CreatedAt         time.Time                `json:"createdAt"`
	StartedAt         *time.Time               `json:"startedAt,omitempty"`
	FinishedAt        *time.Time               `json:"finishedAt,omitempty"`
}

func (r AuditRun) Validate() error {
	if !validAuditEntityID(r.ID) || !validAuditEntityID(r.JobID) || r.JobRevision == 0 || !validSHA256(r.JobSnapshotSHA256) ||
		!r.Trigger.Valid() || !r.Status.Valid() || !r.StopReason.Valid() || r.CreatedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行 ID、任务快照、触发方式、状态或创建时间无效")
	}
	if (r.Trigger == AuditRunScheduled) != (r.ScheduledFor != nil) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "定时审计运行必须且只能保存计划触发时间")
	}
	if r.ScheduledFor != nil && r.ScheduledFor.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "定时审计运行的计划触发时间无效")
	}
	if err := r.Budget.Validate(); err != nil {
		return err
	}
	if err := r.Usage.Validate(); err != nil {
		return err
	}
	if !r.Usage.Fits(r.Budget) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行预算用量超过冻结上限")
	}
	seenTargets := make(map[string]struct{}, len(r.Targets))
	for _, target := range r.Targets {
		if err := target.Validate(); err != nil {
			return err
		}
		if _, duplicate := seenTargets[target.TargetID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行包含重复目标")
		}
		seenTargets[target.TargetID] = struct{}{}
		if r.StartedAt != nil && target.Resolved != nil && target.Resolved.CapturedAt.Before(*r.StartedAt) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行目标解析时间早于运行开始时间")
		}
	}
	if r.Status != AuditRunPending && r.Status != AuditRunCancelled && len(r.Targets) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "已启动审计运行必须保存目标解析快照")
	}
	switch r.Status {
	case AuditRunPending:
		if r.StartedAt != nil || r.FinishedAt != nil || r.StopReason != AuditRunStopNone || r.Failure != "" || len(r.Targets) > 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "待运行审计任务包含不应存在的启动或终态信息")
		}
	case AuditRunRunning:
		if r.StartedAt == nil || r.FinishedAt != nil || r.StopReason != AuditRunStopNone || r.Failure != "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "运行中审计任务的时间或停止信息无效")
		}
	default:
		if r.FinishedAt == nil || r.FinishedAt.Before(r.CreatedAt) ||
			(r.Status != AuditRunCancelled && r.StartedAt == nil) ||
			(r.StartedAt != nil && r.FinishedAt.Before(*r.StartedAt)) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "终态审计运行缺少有效开始或结束时间")
		}
		switch r.Status {
		case AuditRunCompleted:
			if r.StopReason != AuditRunStopNone || r.Failure != "" {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "已完成审计运行不能携带停止原因或失败")
			}
		case AuditRunPartial:
			if (r.StopReason != AuditRunStopBudget && r.StopReason != AuditRunStopCancelled && r.StopReason != AuditRunStopInterrupted) || r.Failure != "" {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "部分审计运行必须由预算或取消停止")
			}
		case AuditRunFailed:
			if (r.StopReason != AuditRunStopFailure && r.StopReason != AuditRunStopInterrupted) || strings.TrimSpace(r.Failure) == "" {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "失败审计运行缺少失败原因")
			}
		case AuditRunCancelled:
			if r.StopReason != AuditRunStopCancelled || r.Failure != "" || r.Usage != (AuditRunUsage{}) ||
				((r.StartedAt == nil) != (len(r.Targets) == 0)) {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "取消状态只适用于尚未产生请求的运行")
			}
		}
	}
	if r.StartedAt != nil && r.StartedAt.Before(r.CreatedAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行开始时间早于创建时间")
	}
	return nil
}

type AuditRunLease struct {
	JobID      string    `json:"jobId"`
	RunID      string    `json:"runId"`
	OwnerID    string    `json:"ownerId"`
	Token      string    `json:"token"`
	AcquiredAt time.Time `json:"acquiredAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func (l AuditRunLease) Validate() error {
	if !validAuditEntityID(l.JobID) || !validAuditEntityID(l.RunID) || !validAuditEntityID(l.OwnerID) ||
		!validAuditEntityID(l.Token) || l.AcquiredAt.IsZero() || !l.ExpiresAt.After(l.AcquiredAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行租约无效")
	}
	return nil
}

type AuditStoredPayloadKind string

const (
	AuditStoredSample         AuditStoredPayloadKind = "sample"
	AuditStoredStrategyResult AuditStoredPayloadKind = "strategy_result"
	AuditStoredReport         AuditStoredPayloadKind = "report"
)

func (k AuditStoredPayloadKind) Valid() bool {
	return k == AuditStoredSample || k == AuditStoredStrategyResult || k == AuditStoredReport
}

type AuditStoredPayload struct {
	ID        string                 `json:"id"`
	RunID     string                 `json:"runId"`
	TargetID  string                 `json:"targetId"`
	Kind      AuditStoredPayloadKind `json:"kind"`
	Schema    VersionedRef           `json:"schema"`
	Payload   json.RawMessage        `json:"payload"`
	SHA256    string                 `json:"sha256"`
	CreatedAt time.Time              `json:"createdAt"`
}

func NewAuditStoredPayload(id, runID, targetID string, kind AuditStoredPayloadKind, schema VersionedRef, payload json.RawMessage, createdAt time.Time) (AuditStoredPayload, error) {
	canonical, digest, err := canonicalJSONObject(payload)
	if err != nil {
		return AuditStoredPayload{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计持久化载荷必须是 JSON 对象", err)
	}
	record := AuditStoredPayload{
		ID: id, RunID: runID, TargetID: targetID, Kind: kind, Schema: schema,
		Payload: append(json.RawMessage(nil), canonical...), SHA256: digest, CreatedAt: createdAt.UTC(),
	}
	if err := record.Validate(); err != nil {
		return AuditStoredPayload{}, err
	}
	return record, nil
}

func (p AuditStoredPayload) Validate() error {
	if !validAuditEntityID(p.ID) || !validAuditEntityID(p.RunID) || !validAuditEntityID(p.TargetID) || !p.Kind.Valid() || p.CreatedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计持久化载荷元数据无效")
	}
	if err := p.Schema.Validate("审计持久化载荷 Schema"); err != nil {
		return err
	}
	canonical, digest, err := canonicalJSONObject(p.Payload)
	if err != nil || string(canonical) != string(p.Payload) || digest != p.SHA256 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计持久化载荷内容或哈希不一致", err)
	}
	return nil
}

type AuditArtifact struct {
	ID            string    `json:"id"`
	RunID         string    `json:"runId"`
	ReportID      string    `json:"reportId"`
	FileName      string    `json:"fileName"`
	ContentSHA256 string    `json:"contentSha256"`
	SizeBytes     int64     `json:"sizeBytes"`
	CreatedAt     time.Time `json:"createdAt"`
}

func (a AuditArtifact) Validate() error {
	if !validAuditEntityID(a.ID) || !validAuditEntityID(a.RunID) || !validAuditEntityID(a.ReportID) ||
		a.FileName == "" || strings.TrimSpace(a.FileName) != a.FileName || strings.ContainsAny(a.FileName, `/\\`) ||
		!validSHA256(a.ContentSHA256) || a.SizeBytes <= 0 || a.CreatedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计报告制品元数据无效")
	}
	return nil
}

func AuditJobSnapshotSHA256(job AuditJob) (string, error) {
	if err := job.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计任务快照编码失败", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
