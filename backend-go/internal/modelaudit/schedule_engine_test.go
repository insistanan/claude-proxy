package modelaudit

import (
	"testing"
	"time"
)

func TestNextAuditScheduleSlotSkipsMissedCycles(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	schedule := AuditSchedule{StartAt: start, IntervalMs: AuditDefaultInterval.Milliseconds(), TimeZone: "UTC"}
	after := start.Add(10*AuditDefaultInterval + time.Hour)
	slot, err := NextAuditScheduleSlot("job-1", schedule, after)
	if err != nil {
		t.Fatal(err)
	}
	if slot == nil || slot.Ordinal != 11 || !slot.ScheduledAt.Equal(start.Add(11*AuditDefaultInterval)) {
		t.Fatalf("错过周期后的下一槽位 = %#v", slot)
	}
}

func TestNextAuditScheduleSlotUsesStableBoundedJitter(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	schedule := AuditSchedule{
		StartAt: start, IntervalMs: AuditDefaultInterval.Milliseconds(), TimeZone: "Asia/Shanghai", JitterMs: (2 * time.Hour).Milliseconds(),
	}
	first, err := NextAuditScheduleSlot("job-stable", schedule, start.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NextAuditScheduleSlot("job-stable", schedule, start.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || second == nil || *first != *second || first.JitterMs < 0 || first.JitterMs > schedule.JitterMs {
		t.Fatalf("稳定抖动槽位: first=%#v second=%#v", first, second)
	}
	local, err := first.LocalScheduledAt()
	if err != nil {
		t.Fatal(err)
	}
	if local.Location().String() != "Asia/Shanghai" {
		t.Fatalf("槽位本地时区 = %q", local.Location())
	}
}

func TestNextAuditScheduleSlotIsStrictlyFuture(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	schedule := AuditSchedule{StartAt: start, IntervalMs: AuditMinimumInterval.Milliseconds(), TimeZone: "UTC"}
	slot, err := NextAuditScheduleSlot("job-1", schedule, start)
	if err != nil {
		t.Fatal(err)
	}
	if slot == nil || slot.Ordinal != 1 || !slot.ScheduledAt.After(start) {
		t.Fatalf("严格未来槽位 = %#v", slot)
	}
}

func TestNextAuditScheduleSlotStopsAtEnd(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	end := start.Add(2 * AuditMinimumInterval)
	schedule := AuditSchedule{
		StartAt: start, EndAt: &end, IntervalMs: AuditMinimumInterval.Milliseconds(), TimeZone: "UTC",
	}
	slot, err := NextAuditScheduleSlot("job-1", schedule, start.Add(AuditMinimumInterval))
	if err != nil {
		t.Fatal(err)
	}
	if slot != nil {
		t.Fatalf("结束时间处不应创建新运行: %#v", slot)
	}
}

func TestManualRunDoesNotAffectScheduledSlot(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	schedule := AuditSchedule{StartAt: start, IntervalMs: AuditDefaultInterval.Milliseconds(), TimeZone: "UTC", JitterMs: time.Hour.Milliseconds()}
	now := start.Add(2 * time.Hour)
	beforeManual, err := NextAuditScheduleSlot("job-1", schedule, now)
	if err != nil {
		t.Fatal(err)
	}
	afterManual, err := NextAuditScheduleSlot("job-1", schedule, now)
	if err != nil {
		t.Fatal(err)
	}
	if beforeManual == nil || afterManual == nil || *beforeManual != *afterManual {
		t.Fatalf("手动运行前后定时槽位发生变化: before=%#v after=%#v", beforeManual, afterManual)
	}
}

func TestAuditScheduleSlotRejectsForgedStableJitter(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	schedule := AuditSchedule{StartAt: start, IntervalMs: AuditDefaultInterval.Milliseconds(), TimeZone: "UTC", JitterMs: time.Hour.Milliseconds()}
	slot, err := NextAuditScheduleSlot("job-1", schedule, start.Add(-time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	forged := *slot
	forged.JitterMs = (forged.JitterMs + 1) % (schedule.JitterMs + 1)
	forged.ScheduledAt = forged.BaseAt.Add(time.Duration(forged.JitterMs) * time.Millisecond)
	if err := forged.ValidateForJob("job-1", schedule); err == nil {
		t.Fatal("伪造的稳定抖动应被拒绝")
	}
}
