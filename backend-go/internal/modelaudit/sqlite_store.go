package modelaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"
)

const (
	AuditDefaultPageSize = 50
	AuditMaximumPageSize = 200
)

type AuditPageRequest struct {
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

func (r AuditPageRequest) Normalize() (AuditPageRequest, error) {
	if r.Page == 0 {
		r.Page = 1
	}
	if r.PageSize == 0 {
		r.PageSize = AuditDefaultPageSize
	}
	if r.Page < 1 || r.PageSize < 1 || r.PageSize > AuditMaximumPageSize {
		return AuditPageRequest{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计分页页码或每页数量无效")
	}
	if r.Page-1 > math.MaxInt/r.PageSize {
		return AuditPageRequest{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计分页偏移量超出支持范围")
	}
	return r, nil
}

type AuditJobListOptions struct {
	Page           AuditPageRequest  `json:"page"`
	IncludeDeleted bool              `json:"includeDeleted"`
	WorkloadKind   AuditWorkloadKind `json:"workloadKind,omitempty"`
}

type AuditJobPage struct {
	Jobs     []AuditJob `json:"jobs"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type AuditSQLiteStore struct {
	db        *sql.DB
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
}

func NewAuditSQLiteStore(path string) (*AuditSQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计数据库路径不能为空")
	}
	if path != ":memory:" {
		directory := filepath.Dir(path)
		if err := os.MkdirAll(directory, 0755); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建审计数据库目录失败", err)
		}
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if path == ":memory:" {
		dsn = "file:model_audit?mode=memory&cache=shared&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "打开审计数据库失败", err)
	}
	// WAL 模式支持并发读，提高连接数以避免所有 RLock 查询串行化。
	// 写操作仍由 store 内部的 mu 互斥锁串行化，连接数仅影响读并发。
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	store := &AuditSQLiteStore{db: db}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *AuditSQLiteStore) initSchema() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS audit_schema_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`INSERT INTO audit_schema_meta(key, value) VALUES ('schema_version', '1')
			ON CONFLICT(key) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS audit_jobs (
			id TEXT PRIMARY KEY,
			revision INTEGER NOT NULL,
			status TEXT NOT NULL,
			job_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			deleted_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS audit_job_targets (
			job_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			channel_kind TEXT NOT NULL,
			position INTEGER NOT NULL,
			target_json TEXT NOT NULL,
			PRIMARY KEY(job_id, target_id),
			FOREIGN KEY(job_id) REFERENCES audit_jobs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS audit_runs (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			job_revision INTEGER NOT NULL,
			trigger TEXT NOT NULL,
			status TEXT NOT NULL,
			scheduled_for INTEGER,
			run_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			started_at INTEGER,
			finished_at INTEGER,
			FOREIGN KEY(job_id) REFERENCES audit_jobs(id),
			UNIQUE(job_id, scheduled_for)
		)`,
		`CREATE TABLE IF NOT EXISTS audit_run_leases (
			job_id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL UNIQUE,
			owner_id TEXT NOT NULL,
			token TEXT NOT NULL,
			acquired_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			FOREIGN KEY(job_id) REFERENCES audit_jobs(id),
			FOREIGN KEY(run_id) REFERENCES audit_runs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS audit_samples (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			schema_id TEXT NOT NULL,
			schema_json TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			payload_sha256 TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(run_id) REFERENCES audit_runs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS audit_strategy_results (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			schema_id TEXT NOT NULL,
			schema_json TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			payload_sha256 TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(run_id) REFERENCES audit_runs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS audit_reports (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			schema_id TEXT NOT NULL,
			schema_json TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			payload_sha256 TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(run_id) REFERENCES audit_runs(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS audit_report_index (
			report_id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			job_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			channel_kind TEXT NOT NULL,
			result_status TEXT NOT NULL,
			reported_at INTEGER NOT NULL,
			FOREIGN KEY(report_id) REFERENCES audit_reports(id) ON DELETE CASCADE,
			FOREIGN KEY(run_id) REFERENCES audit_runs(id) ON DELETE CASCADE,
			FOREIGN KEY(job_id) REFERENCES audit_jobs(id)
		)`,
		`CREATE TABLE IF NOT EXISTS audit_artifacts (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			report_id TEXT NOT NULL,
			artifact_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(run_id) REFERENCES audit_runs(id) ON DELETE CASCADE,
			FOREIGN KEY(report_id) REFERENCES audit_reports(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS audit_mod_versions (
			mod_id TEXT NOT NULL,
			version TEXT NOT NULL,
			content_sha256 TEXT NOT NULL,
			version_json TEXT NOT NULL,
			loaded_at INTEGER NOT NULL,
			PRIMARY KEY(mod_id, content_sha256)
		)`,
		`CREATE TABLE IF NOT EXISTS audit_mod_analyses (
			id TEXT PRIMARY KEY,
			revision INTEGER NOT NULL,
			run_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			mod_id TEXT NOT NULL,
			mod_sha256 TEXT NOT NULL,
			mode TEXT NOT NULL,
			status TEXT NOT NULL,
			analysis_json TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			started_at INTEGER,
			finished_at INTEGER,
			FOREIGN KEY(run_id) REFERENCES audit_runs(id) ON DELETE CASCADE,
			FOREIGN KEY(mod_id, mod_sha256) REFERENCES audit_mod_versions(mod_id, content_sha256)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_jobs_status_updated ON audit_jobs(status, updated_at DESC, id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_job_targets_channel ON audit_job_targets(channel_kind, channel_id, job_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_runs_job_created ON audit_runs(job_id, created_at DESC, id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_runs_status_created ON audit_runs(status, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_samples_run_created ON audit_samples(run_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_strategy_results_run_created ON audit_strategy_results(run_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_reports_run_created ON audit_reports(run_id, created_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_report_index_channel ON audit_report_index(channel_kind, channel_id, reported_at DESC, report_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_mod_versions_loaded ON audit_mod_versions(mod_id, loaded_at DESC, content_sha256)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_mod_analyses_run ON audit_mod_analyses(run_id, target_id, created_at ASC, id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_mod_analyses_status ON audit_mod_analyses(status, created_at ASC, id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "初始化审计数据库 Schema 失败", err)
		}
	}
	var schemaVersion string
	if err := s.db.QueryRow(`SELECT value FROM audit_schema_meta WHERE key = 'schema_version'`).Scan(&schemaVersion); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计数据库 Schema 版本不兼容", err)
	}
	if schemaVersion == "1" {
		result, err := s.db.Exec(`UPDATE audit_schema_meta SET value = '2' WHERE key = 'schema_version' AND value = '1'`)
		if err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "迁移审计数据库 Schema 失败", err)
		}
		if err := requireOneAuditRow(result, "审计数据库 Schema 版本迁移冲突"); err != nil {
			return err
		}
		schemaVersion = "2"
	}
	if schemaVersion == "2" {
		result, err := s.db.Exec(`UPDATE audit_schema_meta SET value = '3' WHERE key = 'schema_version' AND value = '2'`)
		if err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "迁移 Mod 审计数据库 Schema 失败", err)
		}
		if err := requireOneAuditRow(result, "Mod 审计数据库 Schema 版本迁移冲突"); err != nil {
			return err
		}
		schemaVersion = "3"
	}
	if schemaVersion != "3" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计数据库 Schema 版本不兼容")
	}
	return nil
}

func (s *AuditSQLiteStore) CreateJob(ctx context.Context, job AuditJob) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := job.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码审计任务失败", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "开始审计任务创建事务失败", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_jobs(id, revision, status, job_json, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, job.ID, job.Revision, job.Status, string(encoded), auditUnixMillis(job.CreatedAt), auditUnixMillis(job.UpdatedAt), nullableAuditTime(job.DeletedAt))
	if err != nil {
		if isSQLiteConstraint(err) {
			return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计任务 ID 已存在", err)
		}
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入审计任务失败", err)
	}
	if err := replaceAuditJobTargets(ctx, tx, job); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交审计任务创建事务失败", err)
	}
	return nil
}

func (s *AuditSQLiteStore) UpdateJob(ctx context.Context, job AuditJob, expectedPreviousRevision uint64) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := job.Validate(); err != nil {
		return err
	}
	if expectedPreviousRevision == ^uint64(0) || job.Revision != expectedPreviousRevision+1 {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计任务更新版本不连续")
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码审计任务失败", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "开始审计任务更新事务失败", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE audit_jobs SET revision = ?, status = ?, job_json = ?, updated_at = ?, deleted_at = ?
		WHERE id = ? AND revision = ?`, job.Revision, job.Status, string(encoded), auditUnixMillis(job.UpdatedAt), nullableAuditTime(job.DeletedAt), job.ID, expectedPreviousRevision)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "更新审计任务失败", err)
	}
	if err := requireOneAuditRow(result, "审计任务版本冲突或任务不存在"); err != nil {
		return err
	}
	if err := replaceAuditJobTargets(ctx, tx, job); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交审计任务更新事务失败", err)
	}
	return nil
}

func (s *AuditSQLiteStore) GetJob(ctx context.Context, id string) (AuditJob, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditJob{}, false, err
	}
	defer s.mu.RUnlock()
	if !validAuditEntityID(id) {
		return AuditJob{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务 ID 无效")
	}
	job, err := scanAuditJob(s.db.QueryRowContext(ctx, `SELECT revision, status, job_json FROM audit_jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AuditJob{}, false, nil
	}
	if err != nil {
		return AuditJob{}, false, err
	}
	return job, true, nil
}

func (s *AuditSQLiteStore) ListJobs(ctx context.Context, options AuditJobListOptions) (AuditJobPage, error) {
	if err := s.lockOpen(); err != nil {
		return AuditJobPage{}, err
	}
	defer s.mu.RUnlock()
	page, err := options.Page.Normalize()
	if err != nil {
		return AuditJobPage{}, err
	}
	where := " WHERE status <> ?"
	args := []any{AuditJobDeleted}
	if options.IncludeDeleted {
		where = ""
		args = nil
	}
	if options.WorkloadKind != "" {
		if !options.WorkloadKind.Valid() {
			return AuditJobPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计任务工作负载筛选无效")
		}
		clause := "json_extract(job_json, '$.workload.kind') = ?"
		if where == "" {
			where = " WHERE " + clause
		} else {
			where += " AND " + clause
		}
		args = append(args, options.WorkloadKind)
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_jobs`+where, args...).Scan(&total); err != nil {
		return AuditJobPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "统计审计任务失败", err)
	}
	queryArgs := append(append([]any(nil), args...), page.PageSize, (page.Page-1)*page.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT revision, status, job_json FROM audit_jobs`+where+`
		ORDER BY updated_at DESC, id ASC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return AuditJobPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询审计任务失败", err)
	}
	defer rows.Close()
	jobs := make([]AuditJob, 0, page.PageSize)
	for rows.Next() {
		job, err := scanAuditJob(rows)
		if err != nil {
			return AuditJobPage{}, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return AuditJobPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计任务失败", err)
	}
	return AuditJobPage{Jobs: jobs, Total: total, Page: page.Page, PageSize: page.PageSize}, nil
}

func (s *AuditSQLiteStore) ListEnabledJobsAfter(ctx context.Context, afterID string, limit int) ([]AuditJob, error) {
	if err := s.lockOpen(); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	if (afterID != "" && !validAuditEntityID(afterID)) || limit <= 0 || limit > AuditMaximumPageSize {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度任务游标或分页大小无效")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT revision, status, job_json FROM audit_jobs
		WHERE status = ? AND id > ? ORDER BY id ASC LIMIT ?`, AuditJobEnabled, afterID, limit)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询启用的审计调度任务失败", err)
	}
	defer rows.Close()
	jobs := make([]AuditJob, 0, limit)
	for rows.Next() {
		job, err := scanAuditJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取启用的审计调度任务失败", err)
	}
	return jobs, nil
}

func (s *AuditSQLiteStore) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		closeErr = s.db.Close()
		s.mu.Unlock()
	})
	return closeErr
}

func (s *AuditSQLiteStore) lockOpen() error {
	if s == nil || s.db == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计数据库未初始化")
	}
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计数据库已关闭")
	}
	return nil
}

type auditSQLScanner interface {
	Scan(...any) error
}

func scanAuditJob(scanner auditSQLScanner) (AuditJob, error) {
	var revision uint64
	var status AuditJobStatus
	var encoded string
	if err := scanner.Scan(&revision, &status, &encoded); err != nil {
		return AuditJob{}, err
	}
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

func replaceAuditJobTargets(ctx context.Context, tx *sql.Tx, job AuditJob) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_job_targets WHERE job_id = ?`, job.ID); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "清理审计任务目标失败", err)
	}
	for position, target := range job.Targets {
		encoded, err := json.Marshal(target)
		if err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码审计任务目标失败", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_job_targets(job_id, target_id, channel_id, channel_kind, position, target_json)
			VALUES (?, ?, ?, ?, ?, ?)`, job.ID, target.ID, target.ChannelID, target.ChannelKind, position, string(encoded)); err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入审计任务目标失败", err)
		}
	}
	return nil
}

func requireOneAuditRow(result sql.Result, message string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计数据库写入结果失败", err)
	}
	if rows != 1 {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, message)
	}
	return nil
}

func auditUnixMillis(value time.Time) int64 {
	return value.UnixMilli()
}

func nullableAuditTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixMilli()
}

func isSQLiteConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19
}
