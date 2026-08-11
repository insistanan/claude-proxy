package modelaudit

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strconv"
	"time"
)

type AuditScheduleSlot struct {
	Ordinal     int64     `json:"ordinal"`
	BaseAt      time.Time `json:"baseAt"`
	JitterMs    int64     `json:"jitterMs"`
	ScheduledAt time.Time `json:"scheduledAt"`
	TimeZone    string    `json:"timeZone"`
}

func (s AuditScheduleSlot) Validate(schedule AuditSchedule) error {
	if err := schedule.Validate(); err != nil {
		return err
	}
	if s.Ordinal < 0 || s.BaseAt.IsZero() || s.ScheduledAt.IsZero() || s.TimeZone != schedule.TimeZone ||
		s.JitterMs < 0 || s.JitterMs > schedule.JitterMs || !s.ScheduledAt.Equal(s.BaseAt.Add(time.Duration(s.JitterMs)*time.Millisecond)) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度槽位无效")
	}
	expectedBaseMs, err := auditScheduleBaseMillis(schedule, s.Ordinal)
	if err != nil {
		return err
	}
	if !s.BaseAt.Equal(time.UnixMilli(expectedBaseMs).UTC()) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度槽位未锚定到任务开始时间")
	}
	return nil
}

func (s AuditScheduleSlot) ValidateForJob(jobID string, schedule AuditSchedule) error {
	if !validAuditEntityID(jobID) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度槽位缺少任务 ID")
	}
	if err := s.Validate(schedule); err != nil {
		return err
	}
	if s.JitterMs != stableAuditJitter(jobID, s.Ordinal, schedule.JitterMs) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度槽位的稳定抖动不匹配")
	}
	return nil
}

func (s AuditScheduleSlot) LocalScheduledAt() (time.Time, error) {
	location, err := time.LoadLocation(s.TimeZone)
	if err != nil {
		return time.Time{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度槽位时区无效", err)
	}
	return s.ScheduledAt.In(location), nil
}

func NextAuditScheduleSlot(jobID string, schedule AuditSchedule, after time.Time) (*AuditScheduleSlot, error) {
	if !validAuditEntityID(jobID) || after.IsZero() {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "计算审计调度缺少任务 ID 或当前时间")
	}
	if err := schedule.Validate(); err != nil {
		return nil, err
	}
	endAt, err := schedule.EffectiveEndAt()
	if err != nil {
		return nil, err
	}
	startMs := schedule.StartAt.UTC().UnixMilli()
	afterMs := after.UTC().UnixMilli()
	intervalMs := schedule.IntervalMs
	ordinal := int64(0)
	baseMs := startMs
	if afterMs >= startMs {
		delta, err := nonnegativeTimeDifference(afterMs, startMs)
		if err != nil {
			return nil, err
		}
		ordinal = delta / intervalMs
		baseMs = startMs + ordinal*intervalMs
	}
	for attempts := 0; attempts < 2; attempts++ {
		jitterMs := stableAuditJitter(jobID, ordinal, schedule.JitterMs)
		if baseMs > math.MaxInt64-jitterMs {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度时间超出支持范围")
		}
		scheduledAt := time.UnixMilli(baseMs + jitterMs).UTC()
		if scheduledAt.After(after) {
			if endAt != nil && !scheduledAt.Before(*endAt) {
				return nil, nil
			}
			slot := AuditScheduleSlot{
				Ordinal: ordinal, BaseAt: time.UnixMilli(baseMs).UTC(), JitterMs: jitterMs,
				ScheduledAt: scheduledAt, TimeZone: schedule.TimeZone,
			}
			if err := slot.ValidateForJob(jobID, schedule); err != nil {
				return nil, err
			}
			return &slot, nil
		}
		if ordinal == math.MaxInt64 || baseMs > math.MaxInt64-intervalMs {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度槽位超出支持范围")
		}
		ordinal++
		baseMs += intervalMs
	}
	return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计调度无法推进到未来槽位")
}

// DueAuditScheduleSlot returns only the latest slot in the current scan window.
// This deliberately skips older slots after a restart or a delayed scheduler loop.
func DueAuditScheduleSlot(jobID string, schedule AuditSchedule, after, now time.Time) (*AuditScheduleSlot, error) {
	if !validAuditEntityID(jobID) || after.IsZero() || now.IsZero() || now.Before(after) {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "计算到期审计槽位的任务 ID 或扫描窗口无效")
	}
	if err := schedule.Validate(); err != nil {
		return nil, err
	}
	endAt, err := schedule.EffectiveEndAt()
	if err != nil {
		return nil, err
	}
	if endAt != nil && !endAt.After(now) {
		return nil, nil
	}
	startMs := schedule.StartAt.UTC().UnixMilli()
	nowMs := now.UTC().UnixMilli()
	if nowMs < startMs {
		return nil, nil
	}
	delta, err := nonnegativeTimeDifference(nowMs, startMs)
	if err != nil {
		return nil, err
	}
	ordinal := delta / schedule.IntervalMs
	for attempts := 0; attempts < 2 && ordinal >= 0; attempts++ {
		baseMs, err := auditScheduleBaseMillis(schedule, ordinal)
		if err != nil {
			return nil, err
		}
		jitterMs := stableAuditJitter(jobID, ordinal, schedule.JitterMs)
		if baseMs > math.MaxInt64-jitterMs {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度时间超出支持范围")
		}
		scheduledAt := time.UnixMilli(baseMs + jitterMs).UTC()
		if !scheduledAt.After(now) {
			if !scheduledAt.After(after) {
				return nil, nil
			}
			slot := AuditScheduleSlot{
				Ordinal: ordinal, BaseAt: time.UnixMilli(baseMs).UTC(), JitterMs: jitterMs,
				ScheduledAt: scheduledAt, TimeZone: schedule.TimeZone,
			}
			if err := slot.ValidateForJob(jobID, schedule); err != nil {
				return nil, err
			}
			return &slot, nil
		}
		ordinal--
	}
	return nil, nil
}

func auditScheduleBaseMillis(schedule AuditSchedule, ordinal int64) (int64, error) {
	if ordinal < 0 || (schedule.IntervalMs > 0 && ordinal > math.MaxInt64/schedule.IntervalMs) {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度槽位序号超出支持范围")
	}
	offset := ordinal * schedule.IntervalMs
	startMs := schedule.StartAt.UTC().UnixMilli()
	if offset > 0 && startMs > math.MaxInt64-offset {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度槽位时间超出支持范围")
	}
	return startMs + offset, nil
}

func stableAuditJitter(jobID string, ordinal, maximumMs int64) int64 {
	if maximumMs == 0 {
		return 0
	}
	digest := sha256.Sum256([]byte(jobID + ":" + strconv.FormatInt(ordinal, 10)))
	return int64(binary.BigEndian.Uint64(digest[:8]) % uint64(maximumMs+1))
}

func nonnegativeTimeDifference(later, earlier int64) (int64, error) {
	if later < earlier {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计调度时间顺序无效")
	}
	if earlier < 0 && later > math.MaxInt64+earlier {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度时间跨度超出支持范围")
	}
	return later - earlier, nil
}
