package modelaudit

import (
	"testing"
	"time"
)

func TestAuditJobContractSupportsMultipleTargetsAndCanonicalWorkloads(t *testing.T) {
	job := auditJobFixture(t)
	job.Targets = append(job.Targets, AuditJobTarget{
		ID: "target-2", ChannelID: "channel-chat", ChannelKind: ChannelKindChat, Protocol: ProtocolChat,
		Model: "gpt-5.6-sol", Thinking: ThinkingMedium, RequestProfile: "chat.standard.v1",
	})
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(job.Targets) != 2 || string(job.Workload.Strategies[0].Config) != `{"sampleCount":2}` {
		t.Fatalf("多目标审计任务 = %#v", job)
	}

	job.Targets[1].ID = job.Targets[0].ID
	if err := job.Validate(); err == nil {
		t.Fatal("重复任务目标 ID 应被拒绝")
	}
}

func TestAuditWorkloadRequiresExactlyOneWorkloadFamily(t *testing.T) {
	identity := auditJobFixture(t).Workload
	preset := VersionedRef{ID: "capability.preset.standard", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"}
	identity.CapabilityPreset = &preset
	if err := identity.Validate(); err == nil {
		t.Fatal("身份策略与能力预设混用时应被拒绝")
	}

	capability := AuditWorkload{Kind: AuditWorkloadCapability, CapabilityPreset: &preset}
	if err := capability.Validate(); err != nil {
		t.Fatal(err)
	}
	capability.Strategies = []StrategySelection{{StrategyID: "identity.test", Enabled: true, Config: []byte(`{}`)}}
	if err := capability.Validate(); err == nil {
		t.Fatal("能力任务混入身份策略时应被拒绝")
	}
}

func TestAuditScheduleBoundariesAndEndModes(t *testing.T) {
	start := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	for _, interval := range []time.Duration{AuditMinimumInterval, AuditDefaultInterval, AuditMaximumInterval} {
		schedule := AuditSchedule{StartAt: start, IntervalMs: interval.Milliseconds(), TimeZone: "UTC", JitterMs: 1}
		if err := schedule.Validate(); err != nil {
			t.Fatalf("合法间隔 %s 被拒绝: %v", interval, err)
		}
	}
	for _, interval := range []time.Duration{AuditMinimumInterval - time.Millisecond, AuditMaximumInterval + time.Millisecond} {
		schedule := AuditSchedule{StartAt: start, IntervalMs: interval.Milliseconds(), TimeZone: "UTC"}
		if err := schedule.Validate(); err == nil {
			t.Fatalf("越界间隔 %s 应被拒绝", interval)
		}
	}

	duration := AuditSchedule{
		StartAt: start, DurationMs: (48 * time.Hour).Milliseconds(), IntervalMs: AuditDefaultInterval.Milliseconds(),
		TimeZone: "Asia/Shanghai", JitterMs: time.Hour.Milliseconds(),
	}
	end, err := duration.EffectiveEndAt()
	if err != nil {
		t.Fatal(err)
	}
	if end == nil || !end.Equal(start.Add(48*time.Hour)) {
		t.Fatalf("持续时间结束点 = %#v", end)
	}
	explicitEnd := start.Add(72 * time.Hour)
	duration.EndAt = &explicitEnd
	if err := duration.Validate(); err == nil {
		t.Fatal("结束时间与持续时间同时设置时应被拒绝")
	}
}

func TestAuditJobTargetAndBudgetRejectInvalidBoundaries(t *testing.T) {
	job := auditJobFixture(t)
	job.Targets[0].Protocol = ProtocolChat
	if err := job.Validate(); err == nil {
		t.Fatal("目标协议与渠道类型不一致时应被拒绝")
	}

	job = auditJobFixture(t)
	job.Budget.MaxConcurrentRequests = job.Budget.MaxRequests + 1
	if err := job.Validate(); err == nil {
		t.Fatal("并发上限超过请求预算时应被拒绝")
	}
}

func auditJobFixture(t *testing.T) AuditJob {
	t.Helper()
	workload, err := CanonicalizeAuditWorkload(AuditWorkload{
		Kind: AuditWorkloadIdentity, StrategySetVersion: "1.0.0",
		Strategies: []StrategySelection{{StrategyID: "identity.test", Enabled: true, Config: []byte(`{ "sampleCount": 2 }`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC)
	return AuditJob{
		ID: "job-1", Name: "nightly audit", Revision: 1, Status: AuditJobEnabled, Workload: workload,
		Targets: []AuditJobTarget{{
			ID: "target-1", ChannelID: "channel-responses", ChannelKind: ChannelKindResponses, Protocol: ProtocolResponses,
			Thinking: ThinkingMedium, RequestProfile: "responses.standard.v1",
		}},
		Schedule: AuditSchedule{
			StartAt: createdAt.Add(time.Hour), IntervalMs: AuditDefaultInterval.Milliseconds(), TimeZone: "UTC", JitterMs: time.Hour.Milliseconds(),
		},
		Budget: AuditRunBudget{
			MaxRequests: 100, MaxInputTokens: 100_000, MaxOutputTokens: 50_000, MaxTotalTokens: 150_000, MaxConcurrentRequests: 2,
		},
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}
