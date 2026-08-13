package modelaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type AuditModAnalysisListOptions struct {
	Page     AuditPageRequest       `json:"page"`
	RunID    string                 `json:"runId"`
	TargetID string                 `json:"targetId,omitempty"`
	Status   AuditModAnalysisStatus `json:"status,omitempty"`
}

type AuditModAnalysisPage struct {
	Analyses []AuditModAnalysisRecord `json:"analyses"`
	Total    int64                    `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"pageSize"`
}

func (s *AuditSQLiteStore) SaveModVersion(ctx context.Context, bundle AuditModBundle) (AuditModStoredVersion, error) {
	if err := s.lockOpen(); err != nil {
		return AuditModStoredVersion{}, err
	}
	defer s.mu.RUnlock()
	version, err := NewAuditModStoredVersion(bundle)
	if err != nil {
		return AuditModStoredVersion{}, err
	}
	encoded, err := json.Marshal(version)
	if err != nil {
		return AuditModStoredVersion{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 存储版本失败", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_mod_versions(mod_id, version, content_sha256, version_json, loaded_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(mod_id, content_sha256) DO NOTHING`,
		version.Reference.ID, version.Reference.Version, version.Reference.ContentSHA256, string(encoded), auditUnixMillis(version.LoadedAt))
	if err != nil {
		return AuditModStoredVersion{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "保存 Mod 版本失败", err)
	}
	return version, nil
}

func (s *AuditSQLiteStore) GetModVersion(ctx context.Context, reference AuditModReference) (AuditModStoredVersion, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditModStoredVersion{}, false, err
	}
	defer s.mu.RUnlock()
	if err := reference.Validate(); err != nil {
		return AuditModStoredVersion{}, false, err
	}
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT version_json FROM audit_mod_versions WHERE mod_id = ? AND content_sha256 = ?`,
		reference.ID, reference.ContentSHA256).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditModStoredVersion{}, false, nil
	}
	if err != nil {
		return AuditModStoredVersion{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取 Mod 版本失败", err)
	}
	version, err := decodeAuditModStoredVersion(encoded)
	if err != nil {
		return AuditModStoredVersion{}, false, err
	}
	if version.Reference != reference {
		return AuditModStoredVersion{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 版本索引与快照不一致")
	}
	return version, true, nil
}

func (s *AuditSQLiteStore) ListModVersions(ctx context.Context) ([]AuditModStoredVersion, error) {
	if err := s.lockOpen(); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT version_json FROM audit_mod_versions ORDER BY loaded_at ASC, mod_id ASC, content_sha256 ASC`)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询 Mod 历史版本失败", err)
	}
	defer rows.Close()
	versions := make([]AuditModStoredVersion, 0)
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取 Mod 历史版本失败", err)
		}
		version, err := decodeAuditModStoredVersion(encoded)
		if err != nil {
			return nil, err
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "遍历 Mod 历史版本失败", err)
	}
	return versions, nil
}

func (s *AuditSQLiteStore) CreateModAnalysis(ctx context.Context, analysis AuditModAnalysisRecord) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := analysis.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(analysis)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 分析记录失败", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_mod_analyses(
		id, revision, run_id, target_id, mod_id, mod_sha256, mode, status, analysis_json, created_at, started_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, analysis.ID, analysis.Revision, analysis.RunID, analysis.TargetID,
		analysis.Mod.ID, analysis.Mod.ContentSHA256, analysis.Mode, analysis.Status, string(encoded), auditUnixMillis(analysis.CreatedAt),
		nullableAuditTime(analysis.StartedAt), nullableAuditTime(analysis.FinishedAt))
	if err != nil {
		if isSQLiteConstraint(err) {
			return contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 分析 ID 已存在或引用的运行/版本不存在", err)
		}
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "创建 Mod 分析记录失败", err)
	}
	return nil
}

func (s *AuditSQLiteStore) UpdateModAnalysis(ctx context.Context, analysis AuditModAnalysisRecord, expectedPreviousRevision uint64) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := analysis.Validate(); err != nil {
		return err
	}
	if expectedPreviousRevision == ^uint64(0) || analysis.Revision != expectedPreviousRevision+1 {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 分析记录版本不连续")
	}
	current, err := scanAuditModAnalysis(s.db.QueryRowContext(ctx, `SELECT revision, status, analysis_json FROM audit_mod_analyses WHERE id = ?`, analysis.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 分析记录不存在")
	}
	if err != nil {
		return err
	}
	if current.Revision != expectedPreviousRevision || !validAuditModAnalysisTransition(current.Status, analysis.Status) ||
		current.RunID != analysis.RunID || current.TargetID != analysis.TargetID || current.Mod != analysis.Mod || current.Mode != analysis.Mode ||
		current.InputSHA256 != analysis.InputSHA256 || !current.CreatedAt.Equal(analysis.CreatedAt) {
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "Mod 分析记录状态、版本或冻结输入已经变化")
	}
	encoded, err := json.Marshal(analysis)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码 Mod 分析更新失败", err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE audit_mod_analyses SET revision = ?, status = ?, analysis_json = ?, started_at = ?, finished_at = ?
		WHERE id = ? AND revision = ? AND run_id = ? AND target_id = ? AND mod_id = ? AND mod_sha256 = ? AND mode = ?`,
		analysis.Revision, analysis.Status, string(encoded), nullableAuditTime(analysis.StartedAt), nullableAuditTime(analysis.FinishedAt),
		analysis.ID, expectedPreviousRevision, analysis.RunID, analysis.TargetID, analysis.Mod.ID, analysis.Mod.ContentSHA256, analysis.Mode)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "更新 Mod 分析记录失败", err)
	}
	return requireOneAuditRow(result, "Mod 分析记录版本已变化或记录不存在")
}

func (s *AuditSQLiteStore) GetModAnalysis(ctx context.Context, id string) (AuditModAnalysisRecord, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditModAnalysisRecord{}, false, err
	}
	defer s.mu.RUnlock()
	if !validAuditEntityID(id) {
		return AuditModAnalysisRecord{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析 ID 无效")
	}
	analysis, err := scanAuditModAnalysis(s.db.QueryRowContext(ctx, `SELECT revision, status, analysis_json FROM audit_mod_analyses WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AuditModAnalysisRecord{}, false, nil
	}
	if err != nil {
		return AuditModAnalysisRecord{}, false, err
	}
	return analysis, true, nil
}

func (s *AuditSQLiteStore) ListModAnalyses(ctx context.Context, options AuditModAnalysisListOptions) (AuditModAnalysisPage, error) {
	if err := s.lockOpen(); err != nil {
		return AuditModAnalysisPage{}, err
	}
	defer s.mu.RUnlock()
	page, err := options.Page.Normalize()
	if err != nil {
		return AuditModAnalysisPage{}, err
	}
	if !validAuditEntityID(options.RunID) || options.Status != "" && !options.Status.Valid() {
		return AuditModAnalysisPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "Mod 分析筛选条件无效")
	}
	where := " WHERE run_id = ?"
	args := []any{options.RunID}
	if options.TargetID != "" {
		where += " AND target_id = ?"
		args = append(args, options.TargetID)
	}
	if options.Status != "" {
		where += " AND status = ?"
		args = append(args, options.Status)
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_mod_analyses`+where, args...).Scan(&total); err != nil {
		return AuditModAnalysisPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "统计 Mod 分析记录失败", err)
	}
	queryArgs := append(append([]any(nil), args...), page.PageSize, (page.Page-1)*page.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT revision, status, analysis_json FROM audit_mod_analyses`+where+
		` ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return AuditModAnalysisPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询 Mod 分析记录失败", err)
	}
	defer rows.Close()
	analyses := make([]AuditModAnalysisRecord, 0, page.PageSize)
	for rows.Next() {
		analysis, err := scanAuditModAnalysis(rows)
		if err != nil {
			return AuditModAnalysisPage{}, err
		}
		analyses = append(analyses, analysis)
	}
	if err := rows.Err(); err != nil {
		return AuditModAnalysisPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取 Mod 分析记录失败", err)
	}
	return AuditModAnalysisPage{Analyses: analyses, Total: total, Page: page.Page, PageSize: page.PageSize}, nil
}

func decodeAuditModStoredVersion(encoded string) (AuditModStoredVersion, error) {
	var version AuditModStoredVersion
	if err := json.Unmarshal([]byte(encoded), &version); err != nil {
		return AuditModStoredVersion{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码 Mod 存储版本失败", err)
	}
	if err := version.Validate(); err != nil {
		return AuditModStoredVersion{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化 Mod 版本合同无效", err)
	}
	return version, nil
}

func scanAuditModAnalysis(scanner auditSQLScanner) (AuditModAnalysisRecord, error) {
	var revision uint64
	var status AuditModAnalysisStatus
	var encoded string
	if err := scanner.Scan(&revision, &status, &encoded); err != nil {
		return AuditModAnalysisRecord{}, err
	}
	var analysis AuditModAnalysisRecord
	if err := json.Unmarshal([]byte(encoded), &analysis); err != nil {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码 Mod 分析记录失败", err)
	}
	if analysis.Revision != revision || analysis.Status != status {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "Mod 分析索引列与快照不一致")
	}
	if err := analysis.Validate(); err != nil {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化 Mod 分析合同无效", err)
	}
	return analysis, nil
}

func auditModAnalysisTime(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}
