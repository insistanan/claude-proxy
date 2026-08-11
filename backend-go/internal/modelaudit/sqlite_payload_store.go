package modelaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type AuditPayloadListOptions struct {
	Page     AuditPageRequest       `json:"page"`
	RunID    string                 `json:"runId"`
	TargetID string                 `json:"targetId,omitempty"`
	Kind     AuditStoredPayloadKind `json:"kind"`
}

type AuditPayloadPage struct {
	Payloads []AuditStoredPayload `json:"payloads"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

func (s *AuditSQLiteStore) SavePayload(ctx context.Context, payload AuditStoredPayload) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := payload.Validate(); err != nil {
		return err
	}
	table, err := auditPayloadTable(payload.Kind)
	if err != nil {
		return err
	}
	schemaJSON, err := json.Marshal(payload.Schema)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码审计载荷 Schema 失败", err)
	}
	if payload.Kind == AuditStoredReport && payload.Schema.ID == AuditTargetReportSchemaID {
		report, err := decodeAuditTargetReportPayload(payload)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "开始审计目标报告事务失败", err)
		}
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_reports(id, run_id, target_id, schema_id, schema_json, payload_json, payload_sha256, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, payload.ID, payload.RunID, payload.TargetID, payload.Schema.ID, string(schemaJSON),
			string(payload.Payload), payload.SHA256, auditUnixMillis(payload.CreatedAt))
		if err != nil {
			if isSQLiteConstraint(err) {
				return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计目标报告 ID 已存在或引用的运行不存在", err)
			}
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入审计目标报告失败", err)
		}
		requested := report.Target.Requested
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_report_index(
			report_id, run_id, job_id, target_id, channel_id, channel_kind, result_status, reported_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, report.ID, report.RunID, report.JobID, report.Target.TargetID,
			requested.ChannelID, requested.ChannelKind, report.ResultStatus, auditUnixMillis(report.CreatedAt))
		if err != nil {
			if isSQLiteConstraint(err) {
				return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计目标报告索引引用无效", err)
			}
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入审计目标报告索引失败", err)
		}
		if err := tx.Commit(); err != nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "提交审计目标报告事务失败", err)
		}
		return nil
	}
	query := `INSERT INTO ` + table + `(id, run_id, target_id, schema_id, schema_json, payload_json, payload_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, query, payload.ID, payload.RunID, payload.TargetID, payload.Schema.ID, string(schemaJSON),
		string(payload.Payload), payload.SHA256, auditUnixMillis(payload.CreatedAt))
	if err != nil {
		if isSQLiteConstraint(err) {
			return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计载荷 ID 已存在或引用的运行不存在", err)
		}
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入审计载荷失败", err)
	}
	return nil
}

func (s *AuditSQLiteStore) GetPayload(ctx context.Context, kind AuditStoredPayloadKind, id string) (AuditStoredPayload, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditStoredPayload{}, false, err
	}
	defer s.mu.RUnlock()
	table, err := auditPayloadTable(kind)
	if err != nil {
		return AuditStoredPayload{}, false, err
	}
	if !validAuditEntityID(id) {
		return AuditStoredPayload{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计载荷 ID 无效")
	}
	query := `SELECT id, run_id, target_id, schema_json, payload_json, payload_sha256, created_at FROM ` + table + ` WHERE id = ?`
	payload, err := scanAuditPayload(kind, s.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AuditStoredPayload{}, false, nil
	}
	if err != nil {
		return AuditStoredPayload{}, false, err
	}
	return payload, true, nil
}

func (s *AuditSQLiteStore) ListPayloads(ctx context.Context, options AuditPayloadListOptions) (AuditPayloadPage, error) {
	if err := s.lockOpen(); err != nil {
		return AuditPayloadPage{}, err
	}
	defer s.mu.RUnlock()
	page, err := options.Page.Normalize()
	if err != nil {
		return AuditPayloadPage{}, err
	}
	table, err := auditPayloadTable(options.Kind)
	if err != nil {
		return AuditPayloadPage{}, err
	}
	if !validAuditEntityID(options.RunID) {
		return AuditPayloadPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计载荷筛选运行 ID 无效")
	}
	where := " WHERE run_id = ?"
	args := []any{options.RunID}
	if options.TargetID != "" {
		if !validAuditEntityID(options.TargetID) {
			return AuditPayloadPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计载荷筛选目标 ID 无效")
		}
		where += " AND target_id = ?"
		args = append(args, options.TargetID)
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+where, args...).Scan(&total); err != nil {
		return AuditPayloadPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "统计审计载荷失败", err)
	}
	queryArgs := append(append([]any(nil), args...), page.PageSize, (page.Page-1)*page.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, target_id, schema_json, payload_json, payload_sha256, created_at FROM `+table+where+
		` ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return AuditPayloadPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询审计载荷失败", err)
	}
	defer rows.Close()
	payloads := make([]AuditStoredPayload, 0, page.PageSize)
	for rows.Next() {
		payload, err := scanAuditPayload(options.Kind, rows)
		if err != nil {
			return AuditPayloadPage{}, err
		}
		payloads = append(payloads, payload)
	}
	if err := rows.Err(); err != nil {
		return AuditPayloadPage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计载荷失败", err)
	}
	return AuditPayloadPage{Payloads: payloads, Total: total, Page: page.Page, PageSize: page.PageSize}, nil
}

func (s *AuditSQLiteStore) SaveArtifact(ctx context.Context, artifact AuditArtifact) error {
	if err := s.lockOpen(); err != nil {
		return err
	}
	defer s.mu.RUnlock()
	if err := artifact.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码审计报告制品失败", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_artifacts(id, run_id, report_id, artifact_json, created_at)
		VALUES (?, ?, ?, ?, ?)`, artifact.ID, artifact.RunID, artifact.ReportID, string(encoded), auditUnixMillis(artifact.CreatedAt))
	if err != nil {
		if isSQLiteConstraint(err) {
			return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计报告制品 ID 已存在或报告引用无效", err)
		}
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "写入审计报告制品失败", err)
	}
	return nil
}

func (s *AuditSQLiteStore) GetArtifact(ctx context.Context, id string) (AuditArtifact, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditArtifact{}, false, err
	}
	defer s.mu.RUnlock()
	if !validAuditEntityID(id) {
		return AuditArtifact{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计报告制品 ID 无效")
	}
	var encoded string
	err := s.db.QueryRowContext(ctx, `SELECT artifact_json FROM audit_artifacts WHERE id = ?`, id).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditArtifact{}, false, nil
	}
	if err != nil {
		return AuditArtifact{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计报告制品失败", err)
	}
	var artifact AuditArtifact
	if err := json.Unmarshal([]byte(encoded), &artifact); err != nil {
		return AuditArtifact{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码审计报告制品失败", err)
	}
	if err := artifact.Validate(); err != nil {
		return AuditArtifact{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化审计报告制品合同无效", err)
	}
	return artifact, true, nil
}

func (s *AuditSQLiteStore) DeleteTerminalRunsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if err := s.lockOpen(); err != nil {
		return 0, err
	}
	defer s.mu.RUnlock()
	if cutoff.IsZero() || limit < 1 || limit > 1000 {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计历史清理截止时间或批量上限无效")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM audit_runs WHERE id IN (
		SELECT id FROM audit_runs
		WHERE status IN (?, ?, ?, ?) AND finished_at < ?
		ORDER BY finished_at ASC, id ASC LIMIT ?
	)`, AuditRunCompleted, AuditRunPartial, AuditRunFailed, AuditRunCancelled, auditUnixMillis(cutoff.UTC()), limit)
	if err != nil {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "清理审计历史运行失败", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计历史清理数量失败", err)
	}
	return deleted, nil
}

func auditPayloadTable(kind AuditStoredPayloadKind) (string, error) {
	switch kind {
	case AuditStoredSample:
		return "audit_samples", nil
	case AuditStoredStrategyResult:
		return "audit_strategy_results", nil
	case AuditStoredReport:
		return "audit_reports", nil
	default:
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计持久化载荷类型无效")
	}
}

func scanAuditPayload(kind AuditStoredPayloadKind, scanner auditSQLScanner) (AuditStoredPayload, error) {
	var payload AuditStoredPayload
	var schemaJSON, payloadJSON string
	var createdAt int64
	if err := scanner.Scan(&payload.ID, &payload.RunID, &payload.TargetID, &schemaJSON, &payloadJSON, &payload.SHA256, &createdAt); err != nil {
		return AuditStoredPayload{}, err
	}
	payload.Kind = kind
	payload.CreatedAt = time.UnixMilli(createdAt).UTC()
	if err := json.Unmarshal([]byte(schemaJSON), &payload.Schema); err != nil {
		return AuditStoredPayload{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码审计载荷 Schema 失败", err)
	}
	payload.Payload = json.RawMessage(payloadJSON)
	if err := payload.Validate(); err != nil {
		return AuditStoredPayload{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化审计载荷合同无效", err)
	}
	return payload, nil
}
