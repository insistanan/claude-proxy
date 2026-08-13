package modelaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type AuditEvidenceMetadata struct {
	ID        string                 `json:"id"`
	RunID     string                 `json:"runId"`
	TargetID  string                 `json:"targetId"`
	Kind      AuditStoredPayloadKind `json:"kind"`
	Schema    VersionedRef           `json:"schema"`
	SHA256    string                 `json:"sha256"`
	CreatedAt time.Time              `json:"createdAt"`
}

func (m AuditEvidenceMetadata) Validate() error {
	if !validAuditEntityID(m.ID) || !validAuditEntityID(m.RunID) || !validAuditEntityID(m.TargetID) ||
		(m.Kind != AuditStoredSample && m.Kind != AuditStoredStrategyResult) || !validSHA256(m.SHA256) || m.CreatedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计证据元数据无效")
	}
	return m.Schema.Validate("审计证据 Schema")
}

type AuditEvidenceListOptions struct {
	Page     AuditPageRequest       `json:"page"`
	RunID    string                 `json:"runId"`
	TargetID string                 `json:"targetId"`
	Kind     AuditStoredPayloadKind `json:"kind"`
}

type AuditEvidencePage struct {
	Evidence []AuditEvidenceMetadata `json:"evidence"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"pageSize"`
}

func (p AuditEvidencePage) Validate(kind AuditStoredPayloadKind, runID, targetID string) error {
	if p.Total < 0 || p.Page < 1 || p.PageSize < 1 || p.PageSize > AuditMaximumPageSize || len(p.Evidence) > p.PageSize {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计证据分页元数据无效")
	}
	for _, evidence := range p.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
		if evidence.Kind != kind || evidence.RunID != runID || evidence.TargetID != targetID {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计证据分页包含其他运行或目标")
		}
	}
	return nil
}

type AuditReportDetailOptions struct {
	Now             time.Time        `json:"now"`
	Samples         AuditPageRequest `json:"samples"`
	StrategyResults AuditPageRequest `json:"strategyResults"`
}

type AuditReportDetailResult struct {
	Detail          AuditReportDetail `json:"detail"`
	Samples         AuditEvidencePage `json:"samples"`
	StrategyResults AuditEvidencePage `json:"strategyResults"`
}

func (r AuditReportDetailResult) Validate() error {
	if err := r.Detail.Validate(); err != nil {
		return err
	}
	report := r.Detail.Report
	if err := r.Samples.Validate(AuditStoredSample, report.RunID, report.Target.TargetID); err != nil {
		return err
	}
	return r.StrategyResults.Validate(AuditStoredStrategyResult, report.RunID, report.Target.TargetID)
}

func (s *AuditSQLiteStore) SaveTargetReport(ctx context.Context, report AuditTargetReport) (AuditStoredPayload, error) {
	if err := report.Validate(); err != nil {
		return AuditStoredPayload{}, err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return AuditStoredPayload{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码审计目标报告失败", err)
	}
	payload, err := NewAuditStoredPayload(
		report.ID,
		report.RunID,
		report.Target.TargetID,
		AuditStoredReport,
		report.Schema,
		encoded,
		report.CreatedAt,
	)
	if err != nil {
		return AuditStoredPayload{}, err
	}
	if err := s.SavePayload(ctx, payload); err != nil {
		return AuditStoredPayload{}, err
	}
	return payload, nil
}

func (s *AuditSQLiteStore) GetTargetReport(ctx context.Context, id string) (AuditTargetReport, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditTargetReport{}, false, err
	}
	defer s.mu.RUnlock()
	return s.getTargetReportLocked(ctx, id)
}

// GetSingleRunResult 仅为单目标运行返回结果，避免多目标审计报告的语义混淆。
func (s *AuditSQLiteStore) GetSingleRunResult(ctx context.Context, runID string) (AuditRunResult, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditRunResult{}, false, err
	}
	defer s.mu.RUnlock()
	if !validAuditEntityID(runID) {
		return AuditRunResult{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行 ID 无效")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT report_id FROM audit_report_index
		WHERE run_id = ? ORDER BY reported_at DESC, report_id ASC LIMIT 2`, runID)
	if err != nil {
		return AuditRunResult{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询审计运行报告失败", err)
	}
	defer rows.Close()
	reportIDs := make([]string, 0, 2)
	for rows.Next() {
		var reportID string
		if err := rows.Scan(&reportID); err != nil {
			return AuditRunResult{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计运行报告失败", err)
		}
		reportIDs = append(reportIDs, reportID)
	}
	if err := rows.Err(); err != nil {
		return AuditRunResult{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "遍历审计运行报告失败", err)
	}
	if len(reportIDs) != 1 {
		return AuditRunResult{}, false, nil
	}
	report, found, err := s.getTargetReportLocked(ctx, reportIDs[0])
	if err != nil || !found {
		return AuditRunResult{}, false, err
	}
	result, err := NewAuditRunResult(report)
	if err != nil {
		return AuditRunResult{}, false, err
	}
	return result, true, nil
}

func (s *AuditSQLiteStore) GetLatestChannelSummary(ctx context.Context, channelID string, channelKind ChannelKind, now time.Time) (AuditChannelSummary, error) {
	if err := s.lockOpen(); err != nil {
		return AuditChannelSummary{}, err
	}
	defer s.mu.RUnlock()
	if !validAuditEntityID(channelID) || !channelKind.Valid() || now.IsZero() {
		return AuditChannelSummary{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "查询渠道审计摘要的渠道或时间无效")
	}
	activeRunID, activeRunCount, err := s.activeChannelRunsLocked(ctx, channelID, channelKind)
	if err != nil {
		return AuditChannelSummary{}, err
	}
	var reportID string
	err = s.db.QueryRowContext(ctx, `SELECT report_id FROM audit_report_index
		WHERE channel_id = ? AND channel_kind = ?
		ORDER BY reported_at DESC, report_id ASC LIMIT 1`, channelID, channelKind).Scan(&reportID)
	if errors.Is(err, sql.ErrNoRows) {
		return buildAuditChannelSummary(channelID, channelKind, activeRunID, activeRunCount, nil, now)
	}
	if err != nil {
		return AuditChannelSummary{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询渠道最新审计报告失败", err)
	}
	report, found, err := s.getTargetReportLocked(ctx, reportID)
	if err != nil {
		return AuditChannelSummary{}, err
	}
	if !found {
		return AuditChannelSummary{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "渠道最新审计报告索引指向不存在的报告")
	}
	return buildAuditChannelSummary(channelID, channelKind, activeRunID, activeRunCount, &report, now)
}

func (s *AuditSQLiteStore) GetReportDetail(ctx context.Context, reportID string, options AuditReportDetailOptions) (AuditReportDetailResult, bool, error) {
	if err := s.lockOpen(); err != nil {
		return AuditReportDetailResult{}, false, err
	}
	defer s.mu.RUnlock()
	if options.Now.IsZero() {
		return AuditReportDetailResult{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "查询审计详情缺少当前时间")
	}
	report, found, err := s.getTargetReportLocked(ctx, reportID)
	if err != nil || !found {
		return AuditReportDetailResult{}, found, err
	}
	run, err := scanAuditRun(s.db.QueryRowContext(ctx, `SELECT status, run_json FROM audit_runs WHERE id = ?`, report.RunID))
	if errors.Is(err, sql.ErrNoRows) {
		return AuditReportDetailResult{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计详情报告引用的运行不存在")
	}
	if err != nil {
		return AuditReportDetailResult{}, false, err
	}
	job, err := scanAuditJob(s.db.QueryRowContext(ctx, `SELECT revision, status, job_json FROM audit_jobs WHERE id = ?`, report.JobID))
	if errors.Is(err, sql.ErrNoRows) {
		return AuditReportDetailResult{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计详情报告引用的任务不存在")
	}
	if err != nil {
		return AuditReportDetailResult{}, false, err
	}
	requested := report.Target.Requested
	activeRunID, activeRunCount, err := s.activeChannelRunsLocked(ctx, requested.ChannelID, requested.ChannelKind)
	if err != nil {
		return AuditReportDetailResult{}, false, err
	}
	summary, err := buildAuditChannelSummary(
		requested.ChannelID,
		requested.ChannelKind,
		activeRunID,
		activeRunCount,
		&report,
		options.Now,
	)
	if err != nil {
		return AuditReportDetailResult{}, false, err
	}
	runView, err := NewAuditRunPresentation(run)
	if err != nil {
		return AuditReportDetailResult{}, false, err
	}
	detail := AuditReportDetail{
		Schema:       VersionedRef{ID: "audit.report-detail", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		WorkloadKind: job.Workload.Kind,
		Summary:      summary,
		Run:          runView,
		Report:       report,
	}
	samples, err := s.listEvidenceMetadataLocked(ctx, AuditEvidenceListOptions{
		Page: options.Samples, RunID: report.RunID, TargetID: report.Target.TargetID, Kind: AuditStoredSample,
	})
	if err != nil {
		return AuditReportDetailResult{}, false, err
	}
	strategyResults, err := s.listEvidenceMetadataLocked(ctx, AuditEvidenceListOptions{
		Page: options.StrategyResults, RunID: report.RunID, TargetID: report.Target.TargetID, Kind: AuditStoredStrategyResult,
	})
	if err != nil {
		return AuditReportDetailResult{}, false, err
	}
	result := AuditReportDetailResult{Detail: detail, Samples: samples, StrategyResults: strategyResults}
	if err := result.Validate(); err != nil {
		return AuditReportDetailResult{}, false, err
	}
	return result, true, nil
}

func (s *AuditSQLiteStore) ListEvidenceMetadata(ctx context.Context, options AuditEvidenceListOptions) (AuditEvidencePage, error) {
	if err := s.lockOpen(); err != nil {
		return AuditEvidencePage{}, err
	}
	defer s.mu.RUnlock()
	return s.listEvidenceMetadataLocked(ctx, options)
}

func (s *AuditSQLiteStore) listEvidenceMetadataLocked(ctx context.Context, options AuditEvidenceListOptions) (AuditEvidencePage, error) {
	page, err := options.Page.Normalize()
	if err != nil {
		return AuditEvidencePage{}, err
	}
	if !validAuditEntityID(options.RunID) || !validAuditEntityID(options.TargetID) ||
		(options.Kind != AuditStoredSample && options.Kind != AuditStoredStrategyResult) {
		return AuditEvidencePage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计证据分页筛选无效")
	}
	table, err := auditPayloadTable(options.Kind)
	if err != nil {
		return AuditEvidencePage{}, err
	}
	args := []any{options.RunID, options.TargetID}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE run_id = ? AND target_id = ?`, args...).Scan(&total); err != nil {
		return AuditEvidencePage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "统计审计证据失败", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, target_id, schema_json, payload_sha256, created_at FROM `+table+`
		WHERE run_id = ? AND target_id = ? ORDER BY created_at ASC, id ASC LIMIT ? OFFSET ?`,
		options.RunID, options.TargetID, page.PageSize, (page.Page-1)*page.PageSize)
	if err != nil {
		return AuditEvidencePage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "查询审计证据失败", err)
	}
	defer rows.Close()
	evidence := make([]AuditEvidenceMetadata, 0, page.PageSize)
	for rows.Next() {
		var item AuditEvidenceMetadata
		var schemaJSON string
		var createdAt int64
		if err := rows.Scan(&item.ID, &item.RunID, &item.TargetID, &schemaJSON, &item.SHA256, &createdAt); err != nil {
			return AuditEvidencePage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计证据失败", err)
		}
		item.Kind = options.Kind
		item.CreatedAt = time.UnixMilli(createdAt).UTC()
		if err := json.Unmarshal([]byte(schemaJSON), &item.Schema); err != nil {
			return AuditEvidencePage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码审计证据 Schema 失败", err)
		}
		if err := item.Validate(); err != nil {
			return AuditEvidencePage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化审计证据元数据无效", err)
		}
		evidence = append(evidence, item)
	}
	if err := rows.Err(); err != nil {
		return AuditEvidencePage{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "遍历审计证据失败", err)
	}
	result := AuditEvidencePage{Evidence: evidence, Total: total, Page: page.Page, PageSize: page.PageSize}
	if err := result.Validate(options.Kind, options.RunID, options.TargetID); err != nil {
		return AuditEvidencePage{}, err
	}
	return result, nil
}

func (s *AuditSQLiteStore) getTargetReportLocked(ctx context.Context, id string) (AuditTargetReport, bool, error) {
	if !validAuditEntityID(id) {
		return AuditTargetReport{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计目标报告 ID 无效")
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		p.id, p.run_id, p.target_id, p.schema_json, p.payload_json, p.payload_sha256, p.created_at,
		i.run_id, i.job_id, i.target_id, i.channel_id, i.channel_kind, i.result_status, i.reported_at
		FROM audit_reports p JOIN audit_report_index i ON i.report_id = p.id WHERE p.id = ?`, id)
	var payload AuditStoredPayload
	var schemaJSON, payloadJSON string
	var payloadCreatedAt, indexReportedAt int64
	var indexRunID, indexJobID, indexTargetID, indexChannelID string
	var indexChannelKind ChannelKind
	var indexResultStatus AuditReportResultStatus
	err := row.Scan(
		&payload.ID, &payload.RunID, &payload.TargetID, &schemaJSON, &payloadJSON, &payload.SHA256, &payloadCreatedAt,
		&indexRunID, &indexJobID, &indexTargetID, &indexChannelID, &indexChannelKind, &indexResultStatus, &indexReportedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditTargetReport{}, false, nil
	}
	if err != nil {
		return AuditTargetReport{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取审计目标报告失败", err)
	}
	payload.Kind = AuditStoredReport
	payload.Payload = json.RawMessage(payloadJSON)
	payload.CreatedAt = time.UnixMilli(payloadCreatedAt).UTC()
	if err := json.Unmarshal([]byte(schemaJSON), &payload.Schema); err != nil {
		return AuditTargetReport{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码审计目标报告 Schema 失败", err)
	}
	if err := payload.Validate(); err != nil {
		return AuditTargetReport{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化审计目标报告载荷无效", err)
	}
	report, err := decodeAuditTargetReportPayload(payload)
	if err != nil {
		return AuditTargetReport{}, false, err
	}
	requested := report.Target.Requested
	if indexRunID != report.RunID || indexJobID != report.JobID || indexTargetID != report.Target.TargetID ||
		indexChannelID != requested.ChannelID || indexChannelKind != requested.ChannelKind || indexResultStatus != report.ResultStatus ||
		indexReportedAt != auditUnixMillis(report.CreatedAt) {
		return AuditTargetReport{}, false, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计目标报告索引与冻结报告不一致")
	}
	return report, true, nil
}

func decodeAuditTargetReportPayload(payload AuditStoredPayload) (AuditTargetReport, error) {
	if err := payload.Validate(); err != nil {
		return AuditTargetReport{}, err
	}
	if payload.Kind != AuditStoredReport || payload.Schema.ID != AuditTargetReportSchemaID ||
		payload.Schema.SemanticVersion != AuditTargetReportSchemaVersion {
		return AuditTargetReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计载荷不是受支持的目标报告")
	}
	var report AuditTargetReport
	if err := json.Unmarshal(payload.Payload, &report); err != nil {
		return AuditTargetReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码审计目标报告失败", err)
	}
	if err := report.Validate(); err != nil {
		return AuditTargetReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "持久化审计目标报告合同无效", err)
	}
	if report.Schema != payload.Schema || report.ID != payload.ID || report.RunID != payload.RunID ||
		report.Target.TargetID != payload.TargetID || !report.CreatedAt.Equal(payload.CreatedAt) {
		return AuditTargetReport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计目标报告与外层载荷不一致")
	}
	return report, nil
}

func (s *AuditSQLiteStore) activeChannelRunsLocked(ctx context.Context, channelID string, channelKind ChannelKind) (string, int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT r.id) FROM audit_runs r
		JOIN audit_job_targets t ON t.job_id = r.job_id
		WHERE t.channel_id = ? AND t.channel_kind = ? AND r.status IN (?, ?)`,
		channelID, channelKind, AuditRunPending, AuditRunRunning).Scan(&count); err != nil {
		return "", 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "统计渠道活动审计运行失败", err)
	}
	if count == 0 {
		return "", 0, nil
	}
	var runID string
	if err := s.db.QueryRowContext(ctx, `SELECT r.id FROM audit_runs r
		JOIN audit_job_targets t ON t.job_id = r.job_id
		WHERE t.channel_id = ? AND t.channel_kind = ? AND r.status IN (?, ?)
		GROUP BY r.id, r.created_at ORDER BY r.created_at DESC, r.id ASC LIMIT 1`,
		channelID, channelKind, AuditRunPending, AuditRunRunning).Scan(&runID); err != nil {
		return "", 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "读取渠道活动审计运行失败", err)
	}
	return runID, count, nil
}

func buildAuditChannelSummary(
	channelID string,
	channelKind ChannelKind,
	activeRunID string,
	activeRunCount int,
	report *AuditTargetReport,
	now time.Time,
) (AuditChannelSummary, error) {
	if !validAuditEntityID(channelID) || !channelKind.Valid() || now.IsZero() || activeRunCount < 0 ||
		((activeRunID == "") != (activeRunCount == 0)) {
		return AuditChannelSummary{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "构建渠道审计摘要的输入无效")
	}
	if report == nil {
		primary, err := ResolveAuditSummaryStatus(activeRunID, false, "", AuditFreshnessUnavailable)
		if err != nil {
			return AuditChannelSummary{}, err
		}
		summary := AuditChannelSummary{
			ChannelID: channelID, ChannelKind: channelKind, PrimaryStatus: primary,
			ActiveRunID: activeRunID, ActiveRunCount: activeRunCount,
		}
		return summary, summary.Validate()
	}
	if err := report.Validate(); err != nil {
		return AuditChannelSummary{}, err
	}
	requested := report.Target.Requested
	if requested.ChannelID != channelID || requested.ChannelKind != channelKind {
		return AuditChannelSummary{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "渠道审计报告索引到了其他渠道")
	}
	freshness, err := EvaluateAuditFreshness(report.FreshnessPolicy, report.CreatedAt, now)
	if err != nil {
		return AuditChannelSummary{}, err
	}
	primary, err := ResolveAuditSummaryStatus(activeRunID, true, report.ResultStatus, freshness.State)
	if err != nil {
		return AuditChannelSummary{}, err
	}
	summary := AuditChannelSummary{
		ChannelID: channelID, ChannelKind: channelKind, PrimaryStatus: primary,
		ActiveRunID: activeRunID, ActiveRunCount: activeRunCount, ReportID: report.ID, RunID: report.RunID,
		ResultStatus: report.ResultStatus, Protocol: requested.Protocol, Thinking: requested.Thinking,
		SampleCount: report.SampleCount, Freshness: &freshness, ReportedAt: cloneTimePointer(&report.CreatedAt),
	}
	if report.Target.Resolved != nil {
		summary.ResolvedModel = strings.TrimSpace(report.Target.Resolved.ResolvedModel)
	}
	if report.Identity != nil {
		identity, err := SummarizeAuditIdentity(*report.Identity)
		if err != nil {
			return AuditChannelSummary{}, err
		}
		summary.Identity = &identity
	}
	if report.Capability != nil {
		capability, err := SummarizeAuditCapability(*report.Capability)
		if err != nil {
			return AuditChannelSummary{}, err
		}
		summary.Capability = &capability
	}
	if err := summary.Validate(); err != nil {
		return AuditChannelSummary{}, err
	}
	return summary, nil
}
