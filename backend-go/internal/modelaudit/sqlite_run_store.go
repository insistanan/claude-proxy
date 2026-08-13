package modelaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type AuditRunListOptions struct {
	Page   AuditPageRequest `json:"page"`
	JobID  string           `json:"jobId,omitempty"`
	Status AuditRunStatus   `json:"status,omitempty"`
}

type AuditRunPage struct {
	Runs     []AuditRun `json:"runs"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type AuditRecoveryEntry struct {
	Job   AuditJob       `json:"job"`
	Run   AuditRun       `json:"run"`
	Lease *AuditRunLease `json:"lease,omitempty"`
}

func (s *AuditSQLiteStore) CommitRunStart(ctx context.Context, start AuditRunStart, maximumActiveRuns int) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := start.Job.Validate(); err != nil {
		return err
	}
	if err := start.Run.Validate(); err != nil {
		return err
	}
	if err := start.Lease.Validate(); err != nil {
		return err
	}
	if maximumActiveRuns <= 0 || start.Job.Status != AuditJobRunning || start.Run.Status != AuditRunPending ||
		start.Job.ID != start.Run.JobID || start.Lease.JobID != start.Job.ID || start.Lease.RunID != start.Run.ID ||
		start.Run.JobRevision == ^uint64(0) || start.Job.Revision != start.Run.JobRevision+1 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "原子提交的审计任务、运行或租约不一致")
	}
	jobJSON, err := json.Marshal(start.Job)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 running 审计任务失败", err)
	}
	runJSON, err := json.Marshal(start.Run)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码待运行实例失败", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "开始审计运行事务失败", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE audit_schema_meta SET value = value WHERE key = 'schema_version'`); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "锁定审计运行事务失败", err)
	}
	var activeRuns int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_runs WHERE status IN (?, ?)`, AuditRunPending, AuditRunRunning).Scan(&activeRuns); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "统计活动审计运行失败", err)
	}
	if activeRuns >= maximumActiveRuns {
		return contractError(ErrorCodeConcurrencyLimited, ErrorCategoryRequest, "审计运行已达到全局并发上限")
	}
	result, err := tx.ExecContext(ctx, `UPDATE audit_jobs SET revision = ?, status = ?, job_json = ?, updated_at = ?, deleted_at = ?
		WHERE id = ? AND revision = ? AND status = ?`,
		start.Job.Revision, start.Job.Status, string(jobJSON), auditUnixMillis(start.Job.UpdatedAt), nullableAuditTime(start.Job.DeletedAt),
		start.Job.ID, start.Run.JobRevision, AuditJobEnabled,
	)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "切换审计任务到 running 失败", err)
	}
	if err := requireOneAuditRow(result, "审计任务版本已变化或不再可运行"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_runs(
		id, job_id, job_revision, trigger, status, scheduled_for, run_json, created_at, started_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		start.Run.ID, start.Run.JobID, start.Run.JobRevision, start.Run.Trigger, start.Run.Status,
		nullableAuditTime(start.Run.ScheduledFor), string(runJSON), auditUnixMillis(start.Run.CreatedAt),
		nullableAuditTime(start.Run.StartedAt), nullableAuditTime(start.Run.FinishedAt),
	)
	if err != nil {
		if isSQLiteConstraint(err) {
			return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行 ID 或定时槽位已经存在", err)
		}
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入审计运行失败", err)
	}
	if err := upsertAuditLease(ctx, tx, start.Lease, start.Lease.AcquiredAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交审计运行事务失败", err)
	}
	return nil
}

func (s *AuditSQLiteStore) SaveRun(ctx context.Context, run AuditRun, expectedStatus AuditRunStatus, lease AuditRunLease, now time.Time) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := run.Validate(); err != nil {
		return err
	}
	if err := lease.Validate(); err != nil {
		return err
	}
	if !expectedStatus.Valid() || now.IsZero() || run.ID != lease.RunID || run.JobID != lease.JobID {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "保存审计运行的预期状态、租约或时间无效")
	}
	encoded, err := json.Marshal(run)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码审计运行失败", err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE audit_runs SET status = ?, run_json = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND status = ? AND EXISTS (
			SELECT 1 FROM audit_run_leases WHERE job_id = ? AND run_id = ? AND owner_id = ? AND token = ? AND expires_at > ?
		)`, run.Status, string(encoded), nullableAuditTime(run.StartedAt), nullableAuditTime(run.FinishedAt),
		run.ID, expectedStatus, lease.JobID, lease.RunID, lease.OwnerID, lease.Token, auditUnixMillis(now.UTC()))
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "保存审计运行失败", err)
	}
	return requireOneAuditRow(result, "审计运行状态已变化或租约已失效")
}

func (s *AuditSQLiteStore) CommitRunFinish(
	ctx context.Context,
	coordination AuditRunFinishCoordination,
	run AuditRun,
	lease AuditRunLease,
	expectedJobRevision uint64,
) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := coordination.Job.Validate(); err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if err := lease.Validate(); err != nil {
		return err
	}
	if !run.Status.Terminal() || coordination.ReleaseToken != lease.Token || run.ID != lease.RunID || run.JobID != lease.JobID ||
		coordination.Job.ID != run.JobID || expectedJobRevision == ^uint64(0) || coordination.Job.Revision != expectedJobRevision+1 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "完成审计运行的任务、运行、租约或版本不一致")
	}
	jobJSON, err := json.Marshal(coordination.Job)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码完成后的审计任务失败", err)
	}
	runJSON, err := json.Marshal(run)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码终态审计运行失败", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "开始审计运行完成事务失败", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE audit_runs SET status = ?, run_json = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND status IN (?, ?)`, run.Status, string(runJSON), nullableAuditTime(run.StartedAt), nullableAuditTime(run.FinishedAt),
		run.ID, AuditRunPending, AuditRunRunning)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入终态审计运行失败", err)
	}
	if err := requireOneAuditRow(result, "审计运行已经完成或不存在"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE audit_jobs SET revision = ?, status = ?, job_json = ?, updated_at = ?, deleted_at = ?
		WHERE id = ? AND revision = ? AND status = ?`, coordination.Job.Revision, coordination.Job.Status, string(jobJSON),
		auditUnixMillis(coordination.Job.UpdatedAt), nullableAuditTime(coordination.Job.DeletedAt), coordination.Job.ID, expectedJobRevision, AuditJobRunning)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "恢复审计任务调度状态失败", err)
	}
	if err := requireOneAuditRow(result, "running 审计任务版本已变化"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `DELETE FROM audit_run_leases
		WHERE job_id = ? AND run_id = ? AND owner_id = ? AND token = ? AND expires_at > ?`,
		lease.JobID, lease.RunID, lease.OwnerID, lease.Token, auditUnixMillis(run.FinishedAt.UTC()))
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "释放审计运行租约失败", err)
	}
	if err := requireOneAuditRow(result, "审计运行租约已经变化"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交审计运行完成事务失败", err)
	}
	return nil
}

func (s *AuditSQLiteStore) GetRun(ctx context.Context, id string) (AuditRun, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditRun{}, false, err
	}
	defer s.mu.RUnlock()
	if !validAuditEntityID(id) {
		return AuditRun{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行 ID 无效")
	}
	run, err := scanAuditRun(s.db.QueryRowContext(ctx, `SELECT status, run_json FROM audit_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AuditRun{}, false, nil
	}
	if err != nil {
		return AuditRun{}, false, err
	}
	return run, true, nil
}

func (s *AuditSQLiteStore) ListRuns(ctx context.Context, options AuditRunListOptions) (AuditRunPage, error) {
	if err := s.lockOpen(); err != nil {
		return AuditRunPage{}, err
	}
	defer s.mu.RUnlock()
	page, err := options.Page.Normalize()
	if err != nil {
		return AuditRunPage{}, err
	}
	where := " WHERE 1 = 1"
	args := make([]any, 0, 2)
	if options.JobID != "" {
		if !validAuditEntityID(options.JobID) {
			return AuditRunPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行筛选任务 ID 无效")
		}
		where += " AND job_id = ?"
		args = append(args, options.JobID)
	}
	if options.Status != "" {
		if !options.Status.Valid() {
			return AuditRunPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行筛选状态无效")
		}
		where += " AND status = ?"
		args = append(args, options.Status)
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_runs`+where, args...).Scan(&total); err != nil {
		return AuditRunPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "统计审计运行失败", err)
	}
	queryArgs := append(append([]any(nil), args...), page.PageSize, (page.Page-1)*page.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT status, run_json FROM audit_runs`+where+`
		ORDER BY created_at DESC, id ASC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return AuditRunPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询审计运行失败", err)
	}
	defer rows.Close()
	runs := make([]AuditRun, 0, page.PageSize)
	for rows.Next() {
		run, err := scanAuditRun(rows)
		if err != nil {
			return AuditRunPage{}, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return AuditRunPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计运行失败", err)
	}
	return AuditRunPage{Runs: runs, Total: total, Page: page.Page, PageSize: page.PageSize}, nil
}

func (s *AuditSQLiteStore) ListLatestRunsForJobs(ctx context.Context, jobIDs []string) (map[string]AuditRun, error) {
	if err := s.lockOpen(); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	result := make(map[string]AuditRun)
	unique := make([]string, 0, len(jobIDs))
	seen := make(map[string]struct{}, len(jobIDs))
	for _, jobID := range jobIDs {
		if !validAuditEntityID(jobID) {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务 ID 无效")
		}
		if _, exists := seen[jobID]; exists {
			continue
		}
		seen[jobID] = struct{}{}
		unique = append(unique, jobID)
	}
	if len(unique) == 0 {
		return result, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(unique)), ",")
	args := make([]any, len(unique))
	for index, jobID := range unique {
		args[index] = jobID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT status, run_json FROM audit_runs AS current
		WHERE job_id IN (`+placeholders+`) AND id = (
			SELECT id FROM audit_runs AS candidate WHERE candidate.job_id = current.job_id
			ORDER BY created_at DESC, id ASC LIMIT 1
		)`, args...)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询任务最新审计运行失败", err)
	}
	defer rows.Close()
	for rows.Next() {
		run, err := scanAuditRun(rows)
		if err != nil {
			return nil, err
		}
		result[run.JobID] = run
	}
	if err := rows.Err(); err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取任务最新审计运行失败", err)
	}
	return result, nil
}

func (s *AuditSQLiteStore) GetLease(ctx context.Context, jobID string) (AuditRunLease, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditRunLease{}, false, err
	}
	defer s.mu.RUnlock()
	if !validAuditEntityID(jobID) {
		return AuditRunLease{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计租约任务 ID 无效")
	}
	lease, err := scanAuditLease(s.db.QueryRowContext(ctx, `SELECT job_id, run_id, owner_id, token, acquired_at, expires_at
		FROM audit_run_leases WHERE job_id = ?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return AuditRunLease{}, false, nil
	}
	if err != nil {
		return AuditRunLease{}, false, err
	}
	return lease, true, nil
}

func (s *AuditSQLiteStore) ClaimRecoveryLease(ctx context.Context, candidate AuditRunLease, now time.Time) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := candidate.Validate(); err != nil {
		return err
	}
	if now.IsZero() || !candidate.AcquiredAt.Equal(now.UTC()) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "恢复租约的获取时间无效")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "开始恢复租约事务失败", err)
	}
	defer func() { _ = tx.Rollback() }()
	var status AuditRunStatus
	if err := tx.QueryRowContext(ctx, `SELECT status FROM audit_runs WHERE id = ? AND job_id = ?`, candidate.RunID, candidate.JobID).Scan(&status); err != nil {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "恢复的审计运行不存在", err)
	}
	if status != AuditRunPending {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "只有 pending 审计运行可以恢复租约")
	}
	if err := upsertAuditLease(ctx, tx, candidate, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交恢复租约事务失败", err)
	}
	return nil
}

func (s *AuditSQLiteStore) RenewLease(ctx context.Context, current, renewed AuditRunLease) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := current.Validate(); err != nil {
		return err
	}
	if err := renewed.Validate(); err != nil {
		return err
	}
	if current.JobID != renewed.JobID || current.RunID != renewed.RunID || current.OwnerID != renewed.OwnerID || current.Token != renewed.Token ||
		!renewed.ExpiresAt.After(current.ExpiresAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "续期前后的审计租约不一致")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE audit_run_leases SET expires_at = ?
		WHERE job_id = ? AND run_id = ? AND owner_id = ? AND token = ? AND expires_at = ?`,
		auditUnixMillis(renewed.ExpiresAt), current.JobID, current.RunID, current.OwnerID, current.Token, auditUnixMillis(current.ExpiresAt))
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "续期审计租约失败", err)
	}
	return requireOneAuditRow(result, "审计租约已经变化或不存在")
}

func (s *AuditSQLiteStore) LoadRecoveryState(ctx context.Context) ([]AuditRecoveryEntry, error) {
	if err := s.lockOpen(); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT j.revision, j.status, j.job_json, r.status, r.run_json,
		l.job_id, l.run_id, l.owner_id, l.token, l.acquired_at, l.expires_at
		FROM audit_runs r
		JOIN audit_jobs j ON j.id = r.job_id
		LEFT JOIN audit_run_leases l ON l.run_id = r.id
		WHERE r.status IN (?, ?)
		ORDER BY r.created_at ASC, r.id ASC`, AuditRunPending, AuditRunRunning)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询审计恢复状态失败", err)
	}
	defer rows.Close()
	entries := make([]AuditRecoveryEntry, 0)
	for rows.Next() {
		var jobRevision uint64
		var jobStatus AuditJobStatus
		var jobJSON string
		var runStatus AuditRunStatus
		var runJSON string
		var leaseJobID, leaseRunID, ownerID, token sql.NullString
		var acquiredAt, expiresAt sql.NullInt64
		if err := rows.Scan(&jobRevision, &jobStatus, &jobJSON, &runStatus, &runJSON,
			&leaseJobID, &leaseRunID, &ownerID, &token, &acquiredAt, &expiresAt); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计恢复状态失败", err)
		}
		job, err := decodeAuditJob(jobRevision, jobStatus, jobJSON)
		if err != nil {
			return nil, err
		}
		run, err := decodeAuditRun(runStatus, runJSON)
		if err != nil {
			return nil, err
		}
		if job.Status != AuditJobRunning || run.JobID != job.ID || run.JobRevision == ^uint64(0) || run.JobRevision+1 != job.Revision {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计恢复任务与活动运行不一致")
		}
		entry := AuditRecoveryEntry{Job: job, Run: run}
		if leaseJobID.Valid {
			if !leaseRunID.Valid || !ownerID.Valid || !token.Valid || !acquiredAt.Valid || !expiresAt.Valid {
				return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计恢复租约列不完整")
			}
			lease := AuditRunLease{
				JobID: leaseJobID.String, RunID: leaseRunID.String, OwnerID: ownerID.String, Token: token.String,
				AcquiredAt: time.UnixMilli(acquiredAt.Int64).UTC(), ExpiresAt: time.UnixMilli(expiresAt.Int64).UTC(),
			}
			if err := lease.Validate(); err != nil {
				return nil, err
			}
			entry.Lease = &lease
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "遍历审计恢复状态失败", err)
	}
	return entries, nil
}

func (s *AuditSQLiteStore) CommitInterruptedRecovery(ctx context.Context, decision AuditRecoveryDecision, recoveredAt time.Time) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := decision.Validate(); err != nil {
		return err
	}
	if decision.Action != AuditRecoveryFinalizeInterrupted || recoveredAt.IsZero() || decision.Run.FinishedAt == nil ||
		!decision.Run.FinishedAt.Equal(recoveredAt) || decision.Job.UpdatedAt.IsZero() || !decision.Job.UpdatedAt.Equal(recoveredAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "提交中断恢复的动作或时间不一致")
	}
	if decision.Job.Revision == 0 || decision.Run.JobRevision == ^uint64(0) || decision.Job.Revision != decision.Run.JobRevision+2 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "中断恢复的任务与运行版本不一致")
	}
	jobJSON, err := json.Marshal(decision.Job)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码中断恢复任务失败", err)
	}
	runJSON, err := json.Marshal(decision.Run)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码中断恢复运行失败", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "开始中断恢复事务失败", err)
	}
	defer func() { _ = tx.Rollback() }()
	var activeLeaseCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_run_leases
		WHERE job_id = ? AND run_id = ? AND expires_at > ?`, decision.Job.ID, decision.Run.ID, auditUnixMillis(recoveredAt.UTC())).Scan(&activeLeaseCount); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "检查中断恢复租约失败", err)
	}
	if activeLeaseCount > 0 {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行仍有活动租约，不能按中断收口")
	}
	result, err := tx.ExecContext(ctx, `UPDATE audit_runs SET status = ?, run_json = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND status = ?`, decision.Run.Status, string(runJSON), nullableAuditTime(decision.Run.StartedAt),
		nullableAuditTime(decision.Run.FinishedAt), decision.Run.ID, AuditRunRunning)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入中断恢复运行失败", err)
	}
	if err := requireOneAuditRow(result, "中断恢复运行已经变化或不存在"); err != nil {
		return err
	}
	expectedJobRevision := decision.Job.Revision - 1
	result, err = tx.ExecContext(ctx, `UPDATE audit_jobs SET revision = ?, status = ?, job_json = ?, updated_at = ?, deleted_at = ?
		WHERE id = ? AND revision = ? AND status = ?`, decision.Job.Revision, decision.Job.Status, string(jobJSON),
		auditUnixMillis(decision.Job.UpdatedAt), nullableAuditTime(decision.Job.DeletedAt), decision.Job.ID, expectedJobRevision, AuditJobRunning)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "恢复中断任务调度状态失败", err)
	}
	if err := requireOneAuditRow(result, "中断恢复任务版本已经变化"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_run_leases WHERE job_id = ? AND run_id = ? AND expires_at <= ?`,
		decision.Job.ID, decision.Run.ID, auditUnixMillis(recoveredAt.UTC())); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "清理中断恢复租约失败", err)
	}
	if err := tx.Commit(); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交中断恢复事务失败", err)
	}
	return nil
}

func scanAuditRun(scanner auditSQLScanner) (AuditRun, error) {
	var status AuditRunStatus
	var encoded string
	if err := scanner.Scan(&status, &encoded); err != nil {
		return AuditRun{}, err
	}
	return decodeAuditRun(status, encoded)
}

func decodeAuditRun(status AuditRunStatus, encoded string) (AuditRun, error) {
	var run AuditRun
	if err := json.Unmarshal([]byte(encoded), &run); err != nil {
		return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码审计运行失败", err)
	}
	if run.Status != status {
		return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计运行状态列与快照不一致")
	}
	if err := run.Validate(); err != nil {
		return AuditRun{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化审计运行合同无效", err)
	}
	return run, nil
}

func decodeAuditJob(revision uint64, status AuditJobStatus, encoded string) (AuditJob, error) {
	var job AuditJob
	if err := json.Unmarshal([]byte(encoded), &job); err != nil {
		return AuditJob{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码审计任务失败", err)
	}
	if job.Revision != revision || job.Status != status {
		return AuditJob{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计任务索引列与快照不一致")
	}
	if err := job.Validate(); err != nil {
		return AuditJob{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化审计任务合同无效", err)
	}
	return job, nil
}

func scanAuditLease(scanner auditSQLScanner) (AuditRunLease, error) {
	var lease AuditRunLease
	var acquiredAt, expiresAt int64
	if err := scanner.Scan(&lease.JobID, &lease.RunID, &lease.OwnerID, &lease.Token, &acquiredAt, &expiresAt); err != nil {
		return AuditRunLease{}, err
	}
	lease.AcquiredAt = time.UnixMilli(acquiredAt).UTC()
	lease.ExpiresAt = time.UnixMilli(expiresAt).UTC()
	if err := lease.Validate(); err != nil {
		return AuditRunLease{}, err
	}
	return lease, nil
}

func upsertAuditLease(ctx context.Context, tx *sql.Tx, lease AuditRunLease, now time.Time) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO audit_run_leases(job_id, run_id, owner_id, token, acquired_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			run_id = excluded.run_id,
			owner_id = excluded.owner_id,
			token = excluded.token,
			acquired_at = excluded.acquired_at,
			expires_at = excluded.expires_at
		WHERE audit_run_leases.expires_at <= ?`, lease.JobID, lease.RunID, lease.OwnerID, lease.Token,
		auditUnixMillis(lease.AcquiredAt), auditUnixMillis(lease.ExpiresAt), auditUnixMillis(now.UTC()))
	if err != nil {
		if isSQLiteConstraint(err) {
			return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行租约引用无效", err)
		}
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "获取审计运行租约失败", err)
	}
	if err := requireOneAuditRow(result, "审计任务已有未过期租约"); err != nil {
		return err
	}
	return nil
}
