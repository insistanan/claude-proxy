package modelaudit

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestCreateAndUpdateAuditJobUseStableIDsAndRevisions(t *testing.T) {
	jobID, err := NewAuditEntityID("audit.job")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(jobID, "audit.job.") || !stableIDPattern.MatchString(jobID) {
		t.Fatalf("生成的审计任务 ID 无效: %q", jobID)
	}
	fixture := auditJobFixture(t)
	definition := auditJobDefinitionFromFixture(fixture)
	job, err := CreateAuditJob(jobID, definition, AuditJobDraft, fixture.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != jobID || job.Revision != 1 || job.Status != AuditJobDraft || !job.CreatedAt.Equal(job.UpdatedAt) {
		t.Fatalf("创建的审计任务 = %#v", job)
	}

	definition.Name = "updated audit"
	definition.Targets[0].Model = "gpt-5.6-sol"
	updated, err := UpdateAuditJob(job, 1, definition, fixture.CreatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != job.ID || updated.Revision != 2 || updated.Name != "updated audit" || updated.Targets[0].Model != "gpt-5.6-sol" {
		t.Fatalf("更新的审计任务 = %#v", updated)
	}
	if _, err := UpdateAuditJob(updated, 1, definition, fixture.CreatedAt.Add(2*time.Minute)); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("过期版本更新错误 = %v", err)
	}
}

func TestUpdateAuditJobCannotRebindStableTargetOrModifyRunningJob(t *testing.T) {
	job := auditJobFixture(t)
	definition := auditJobDefinitionFromFixture(job)
	definition.Targets[0].ChannelID = "other-channel"
	if _, err := UpdateAuditJob(job, job.Revision, definition, job.UpdatedAt.Add(time.Minute)); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("目标改绑错误 = %v", err)
	}

	job.Status = AuditJobRunning
	definition = auditJobDefinitionFromFixture(job)
	if _, err := UpdateAuditJob(job, job.Revision, definition, job.UpdatedAt.Add(time.Minute)); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("运行中任务更新错误 = %v", err)
	}
}

func TestAuditJobStateTransitions(t *testing.T) {
	tests := []struct {
		from AuditJobStatus
		to   AuditJobStatus
		ok   bool
	}{
		{AuditJobDraft, AuditJobEnabled, true},
		{AuditJobDraft, AuditJobDeleted, true},
		{AuditJobEnabled, AuditJobRunning, true},
		{AuditJobEnabled, AuditJobPaused, true},
		{AuditJobEnabled, AuditJobExpired, true},
		{AuditJobRunning, AuditJobEnabled, true},
		{AuditJobRunning, AuditJobExpired, true},
		{AuditJobPaused, AuditJobEnabled, true},
		{AuditJobPaused, AuditJobDeleted, true},
		{AuditJobRunning, AuditJobDeleted, false},
		{AuditJobExpired, AuditJobEnabled, false},
		{AuditJobDeleted, AuditJobEnabled, false},
		{AuditJobEnabled, AuditJobEnabled, false},
	}
	for _, test := range tests {
		t.Run(string(test.from)+"_to_"+string(test.to), func(t *testing.T) {
			job := auditJobFixture(t)
			job.Status = test.from
			if test.from == AuditJobDeleted {
				deletedAt := job.UpdatedAt
				job.DeletedAt = &deletedAt
			}
			updated, err := TransitionAuditJob(job, job.Revision, test.to, job.UpdatedAt.Add(time.Minute))
			if test.ok && err != nil {
				t.Fatal(err)
			}
			if !test.ok && ErrorCodeOf(err) != ErrorCodeConflict {
				t.Fatalf("非法转换错误 = %v", err)
			}
			if test.ok && (updated.Revision != job.Revision+1 || updated.Status != test.to) {
				t.Fatalf("状态转换结果 = %#v", updated)
			}
			if test.ok && test.to == AuditJobDeleted && updated.DeletedAt == nil {
				t.Fatal("软删除任务必须记录删除时间")
			}
		})
	}
}

func TestAuditJobRevisionOverflowIsExplicitConflict(t *testing.T) {
	job := auditJobFixture(t)
	job.Revision = math.MaxUint64
	if _, err := TransitionAuditJob(job, job.Revision, AuditJobPaused, job.UpdatedAt.Add(time.Minute)); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("版本溢出错误 = %v", err)
	}
}

func TestCreateAuditJobRejectsRunningInitialState(t *testing.T) {
	fixture := auditJobFixture(t)
	if _, err := CreateAuditJob("job-created", auditJobDefinitionFromFixture(fixture), AuditJobRunning, fixture.CreatedAt); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("非法初始状态错误 = %v", err)
	}
}

func TestAuditJobCannotEnableAnEndedSchedule(t *testing.T) {
	job := auditJobFixture(t)
	endAt := job.Schedule.StartAt.Add(time.Minute)
	job.Schedule.EndAt = &endAt
	job.Status = AuditJobPaused
	if _, err := TransitionAuditJob(job, job.Revision, AuditJobEnabled, endAt); ErrorCodeOf(err) != ErrorCodeConflict {
		t.Fatalf("启用已结束调度错误 = %v", err)
	}
}

func auditJobDefinitionFromFixture(job AuditJob) AuditJobDefinition {
	return AuditJobDefinition{
		Name: job.Name, Workload: job.Workload, Targets: append([]AuditJobTarget(nil), job.Targets...),
		Schedule: job.Schedule, Budget: job.Budget,
	}
}
