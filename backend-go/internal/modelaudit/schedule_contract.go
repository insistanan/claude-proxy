package modelaudit

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	AuditDefaultInterval = 24 * time.Hour
	AuditMinimumInterval = 6 * time.Hour
	AuditMaximumInterval = 30 * 24 * time.Hour
)

type AuditWorkloadKind string

const (
	AuditWorkloadIdentity   AuditWorkloadKind = "identity"
	AuditWorkloadCapability AuditWorkloadKind = "capability"
)

func (k AuditWorkloadKind) Valid() bool {
	return k == AuditWorkloadIdentity || k == AuditWorkloadCapability
}

type AuditWorkload struct {
	Kind               AuditWorkloadKind   `json:"kind"`
	StrategySetVersion string              `json:"strategySetVersion,omitempty"`
	Strategies         []StrategySelection `json:"strategies,omitempty"`
	CapabilityPreset   *VersionedRef       `json:"capabilityPreset,omitempty"`
}

func (w AuditWorkload) Validate() error {
	if !w.Kind.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务工作负载类型无效")
	}
	switch w.Kind {
	case AuditWorkloadIdentity:
		if !semanticVersionPattern.MatchString(w.StrategySetVersion) || len(w.Strategies) == 0 || w.CapabilityPreset != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份审计工作负载缺少策略集或混入能力预设")
		}
		seen := make(map[string]struct{}, len(w.Strategies))
		enabled := 0
		for _, selection := range w.Strategies {
			if !stableIDPattern.MatchString(selection.StrategyID) {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份审计策略 ID 无效")
			}
			if _, duplicate := seen[selection.StrategyID]; duplicate {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("身份审计策略 %q 重复", selection.StrategyID))
			}
			seen[selection.StrategyID] = struct{}{}
			canonical, _, err := canonicalJSONObject(selection.Config)
			if err != nil || string(canonical) != string(selection.Config) {
				return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份审计策略配置必须是规范化 JSON 对象", err)
			}
			if selection.Enabled {
				enabled++
			}
		}
		if enabled == 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份审计工作负载至少启用一个策略")
		}
	case AuditWorkloadCapability:
		if w.StrategySetVersion != "" || len(w.Strategies) > 0 || w.CapabilityPreset == nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "能力审计工作负载缺少预设或混入身份策略")
		}
		if err := w.CapabilityPreset.Validate("审计任务能力预设"); err != nil {
			return err
		}
	}
	return nil
}

type AuditJobTarget struct {
	ID             string        `json:"id"`
	ChannelID      string        `json:"channelId"`
	ChannelKind    ChannelKind   `json:"channelKind"`
	Protocol       Protocol      `json:"protocol"`
	Model          string        `json:"model,omitempty"`
	Thinking       ThinkingLevel `json:"thinking,omitempty"`
	RequestProfile string        `json:"requestProfile"`
}

func (t AuditJobTarget) Validate() error {
	if !validAuditEntityID(t.ID) || !validAuditEntityID(t.ChannelID) || !t.ChannelKind.Valid() || !t.Protocol.Valid() ||
		t.Protocol.ChannelKind() != t.ChannelKind || !t.Thinking.Valid() || !stableIDPattern.MatchString(t.RequestProfile) ||
		strings.TrimSpace(t.Model) != t.Model {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务目标 ID、渠道、协议、模型、思考档位或请求轮廓无效")
	}
	return nil
}

type AuditSchedule struct {
	StartAt    time.Time  `json:"startAt"`
	EndAt      *time.Time `json:"endAt,omitempty"`
	DurationMs int64      `json:"durationMs,omitempty"`
	IntervalMs int64      `json:"intervalMs"`
	TimeZone   string     `json:"timeZone"`
	JitterMs   int64      `json:"jitterMs"`
}

func (s AuditSchedule) Validate() error {
	if s.StartAt.IsZero() || strings.TrimSpace(s.TimeZone) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度缺少开始时间或时区")
	}
	if _, err := time.LoadLocation(s.TimeZone); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度时区无效", err)
	}
	if s.EndAt != nil && s.DurationMs != 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度结束时间和持续时间只能选择一种")
	}
	if s.DurationMs < 0 || s.DurationMs > math.MaxInt64/int64(time.Millisecond) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度持续时间无效")
	}
	if s.EndAt != nil && !s.EndAt.After(s.StartAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度结束时间必须晚于开始时间")
	}
	minimumIntervalMs := int64(AuditMinimumInterval / time.Millisecond)
	maximumIntervalMs := int64(AuditMaximumInterval / time.Millisecond)
	if s.IntervalMs < minimumIntervalMs || s.IntervalMs > maximumIntervalMs {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度间隔必须在 6 小时至 30 天之间")
	}
	if s.JitterMs < 0 || s.JitterMs >= s.IntervalMs {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度抖动必须非负且小于执行间隔")
	}
	return nil
}

func (s AuditSchedule) EffectiveEndAt() (*time.Time, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if s.EndAt != nil {
		value := s.EndAt.UTC()
		return &value, nil
	}
	if s.DurationMs == 0 {
		return nil, nil
	}
	value := s.StartAt.Add(time.Duration(s.DurationMs) * time.Millisecond).UTC()
	return &value, nil
}

type AuditRunBudget struct {
	MaxRequests           int   `json:"maxRequests"`
	MaxInputTokens        int64 `json:"maxInputTokens"`
	MaxOutputTokens       int64 `json:"maxOutputTokens"`
	MaxTotalTokens        int64 `json:"maxTotalTokens"`
	MaxConcurrentRequests int   `json:"maxConcurrentRequests"`
}

func (b AuditRunBudget) Validate() error {
	if b.MaxRequests <= 0 || b.MaxInputTokens <= 0 || b.MaxOutputTokens <= 0 || b.MaxTotalTokens <= 0 ||
		b.MaxConcurrentRequests <= 0 || b.MaxConcurrentRequests > b.MaxRequests {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行预算和并发上限必须为有效正数")
	}
	return nil
}

type AuditJobStatus string

const (
	AuditJobDraft   AuditJobStatus = "draft"
	AuditJobEnabled AuditJobStatus = "enabled"
	AuditJobRunning AuditJobStatus = "running"
	AuditJobPaused  AuditJobStatus = "paused"
	AuditJobExpired AuditJobStatus = "expired"
	AuditJobDeleted AuditJobStatus = "deleted"
)

func (s AuditJobStatus) Valid() bool {
	switch s {
	case AuditJobDraft, AuditJobEnabled, AuditJobRunning, AuditJobPaused, AuditJobExpired, AuditJobDeleted:
		return true
	default:
		return false
	}
}

type AuditJob struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Revision  uint64           `json:"revision"`
	Status    AuditJobStatus   `json:"status"`
	Workload  AuditWorkload    `json:"workload"`
	Targets   []AuditJobTarget `json:"targets"`
	Schedule  AuditSchedule    `json:"schedule"`
	Budget    AuditRunBudget   `json:"budget"`
	CreatedAt time.Time        `json:"createdAt"`
	UpdatedAt time.Time        `json:"updatedAt"`
	DeletedAt *time.Time       `json:"deletedAt,omitempty"`
}

func (j AuditJob) Validate() error {
	if !validAuditEntityID(j.ID) || strings.TrimSpace(j.Name) == "" || strings.TrimSpace(j.Name) != j.Name ||
		j.Revision == 0 || !j.Status.Valid() || len(j.Targets) == 0 || j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() ||
		j.UpdatedAt.Before(j.CreatedAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务 ID、名称、版本、状态、目标或时间无效")
	}
	if (j.Status == AuditJobDeleted) != (j.DeletedAt != nil) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务删除状态与删除时间不一致")
	}
	if j.DeletedAt != nil && j.DeletedAt.Before(j.UpdatedAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务删除时间早于最后更新时间")
	}
	if err := j.Workload.Validate(); err != nil {
		return err
	}
	seenTargets := make(map[string]struct{}, len(j.Targets))
	for _, target := range j.Targets {
		if err := target.Validate(); err != nil {
			return err
		}
		if _, duplicate := seenTargets[target.ID]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("审计任务目标 %q 重复", target.ID))
		}
		seenTargets[target.ID] = struct{}{}
	}
	if err := j.Schedule.Validate(); err != nil {
		return err
	}
	return j.Budget.Validate()
}

func CanonicalizeAuditWorkload(workload AuditWorkload) (AuditWorkload, error) {
	cloned := workload
	cloned.Strategies = append([]StrategySelection(nil), workload.Strategies...)
	for index := range cloned.Strategies {
		canonical, _, err := canonicalJSONObject(cloned.Strategies[index].Config)
		if err != nil {
			return AuditWorkload{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "身份审计策略配置必须是 JSON 对象", err)
		}
		cloned.Strategies[index].Config = append(json.RawMessage(nil), canonical...)
	}
	if workload.CapabilityPreset != nil {
		value := *workload.CapabilityPreset
		cloned.CapabilityPreset = &value
	}
	if err := cloned.Validate(); err != nil {
		return AuditWorkload{}, err
	}
	return cloned, nil
}

func validAuditEntityID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
