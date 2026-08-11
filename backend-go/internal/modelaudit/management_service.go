package modelaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type AuditRunController interface {
	StartManual(context.Context, AuditJob) (AuditRun, error)
	CancelRun(context.Context, AuditRun) (AuditRun, error)
}

type AuditWorkloadValidator interface {
	ValidateWorkload(AuditWorkload) error
}

type AuditRuntimeCatalogProvider interface {
	StrategyCatalog() []StrategyDescriptor
	CapabilityCatalog() AuditCapabilityAssetCatalog
}

type AuditHTMLRenderer interface {
	Render(AuditHTMLReportDTO) ([]byte, error)
}

type AuditManagementService struct {
	store         *AuditSQLiteStore
	runController AuditRunController
	renderer      AuditHTMLRenderer
	now           func() time.Time
	newJobID      func() (string, error)
	newArtifactID func() (string, error)
}

type AuditHTMLExport struct {
	FileName string
	Content  []byte
	Artifact AuditArtifact
}

func NewAuditManagementService(
	store *AuditSQLiteStore,
	runController AuditRunController,
	renderer AuditHTMLRenderer,
) (*AuditManagementService, error) {
	if store == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计管理服务缺少持久化仓储")
	}
	return &AuditManagementService{
		store: store, runController: runController, renderer: renderer, now: time.Now,
		newJobID:      func() (string, error) { return NewAuditEntityID("audit-job") },
		newArtifactID: func() (string, error) { return NewAuditEntityID("audit-artifact") },
	}, nil
}

func (s *AuditManagementService) CreateJob(ctx context.Context, definition AuditJobDefinition, initialStatus AuditJobStatus) (AuditJob, error) {
	if err := s.validate(); err != nil {
		return AuditJob{}, err
	}
	if err := s.validateWorkload(definition.Workload); err != nil {
		return AuditJob{}, err
	}
	id, err := s.newJobID()
	if err != nil {
		return AuditJob{}, err
	}
	job, err := CreateAuditJob(id, definition, initialStatus, s.now().UTC())
	if err != nil {
		return AuditJob{}, err
	}
	if err := s.store.CreateJob(ctx, job); err != nil {
		return AuditJob{}, err
	}
	return job, nil
}

func (s *AuditManagementService) UpdateJob(ctx context.Context, id string, expectedRevision uint64, definition AuditJobDefinition) (AuditJob, error) {
	if err := s.validateWorkload(definition.Workload); err != nil {
		return AuditJob{}, err
	}
	job, err := s.requireJob(ctx, id)
	if err != nil {
		return AuditJob{}, err
	}
	updated, err := UpdateAuditJob(job, expectedRevision, definition, s.now().UTC())
	if err != nil {
		return AuditJob{}, err
	}
	if err := s.store.UpdateJob(ctx, updated, expectedRevision); err != nil {
		return AuditJob{}, err
	}
	return updated, nil
}

func (s *AuditManagementService) TransitionJob(ctx context.Context, id string, expectedRevision uint64, status AuditJobStatus) (AuditJob, error) {
	job, err := s.requireJob(ctx, id)
	if err != nil {
		return AuditJob{}, err
	}
	if status == AuditJobEnabled {
		if err := s.validateWorkload(job.Workload); err != nil {
			return AuditJob{}, err
		}
	}
	updated, err := TransitionAuditJob(job, expectedRevision, status, s.now().UTC())
	if err != nil {
		return AuditJob{}, err
	}
	if err := s.store.UpdateJob(ctx, updated, expectedRevision); err != nil {
		return AuditJob{}, err
	}
	return updated, nil
}

func (s *AuditManagementService) GetJob(ctx context.Context, id string) (AuditJob, error) {
	return s.requireJob(ctx, id)
}

func (s *AuditManagementService) ListJobs(ctx context.Context, options AuditJobListOptions) (AuditJobPage, error) {
	if err := s.validate(); err != nil {
		return AuditJobPage{}, err
	}
	return s.store.ListJobs(ctx, options)
}

func (s *AuditManagementService) StartManualRun(ctx context.Context, jobID string, expectedRevision uint64) (AuditRun, error) {
	job, err := s.requireJob(ctx, jobID)
	if err != nil {
		return AuditRun{}, err
	}
	if job.Revision != expectedRevision {
		return AuditRun{}, auditRevisionConflict(job.Revision, expectedRevision)
	}
	if s.runController == nil {
		return AuditRun{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "审计 Runner 尚未配置，不能启动手动运行")
	}
	return s.runController.StartManual(ctx, job)
}

func (s *AuditManagementService) CancelRun(ctx context.Context, runID string) (AuditRun, error) {
	run, err := s.requireRun(ctx, runID)
	if err != nil {
		return AuditRun{}, err
	}
	if run.Status.Terminal() {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "终态审计运行不能取消")
	}
	if s.runController == nil {
		return AuditRun{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "审计 Runner 尚未配置，不能取消运行")
	}
	return s.runController.CancelRun(ctx, run)
}

func (s *AuditManagementService) GetRun(ctx context.Context, id string) (AuditRun, error) {
	return s.requireRun(ctx, id)
}

func (s *AuditManagementService) ListRuns(ctx context.Context, options AuditRunListOptions) (AuditRunPage, error) {
	if err := s.validate(); err != nil {
		return AuditRunPage{}, err
	}
	return s.store.ListRuns(ctx, options)
}

func (s *AuditManagementService) GetStrategyCatalog() AuditStrategyCatalogResponse {
	response := AuditStrategyCatalogResponse{Available: false, Strategies: []StrategyDescriptor{}}
	provider, ok := s.runController.(AuditRuntimeCatalogProvider)
	if !ok {
		response.Reason = "审计 Runner 尚未配置"
		return response
	}
	response.Available = true
	response.Strategies = provider.StrategyCatalog()
	return response
}

func (s *AuditManagementService) GetCapabilityCatalog() AuditCapabilityCatalogResponse {
	response := AuditCapabilityCatalogResponse{Catalog: AuditCapabilityAssetCatalog{
		Available: false, Reason: "审计 Runner 尚未配置", Packages: []CapabilityTaskPackageSnapshot{}, Presets: []CapabilityRunPreset{},
	}}
	provider, ok := s.runController.(AuditRuntimeCatalogProvider)
	if ok {
		response.Catalog = provider.CapabilityCatalog()
	}
	return response
}

func (s *AuditManagementService) GetChannelSummary(ctx context.Context, channelID string, channelKind ChannelKind) (AuditChannelSummary, error) {
	if err := s.validate(); err != nil {
		return AuditChannelSummary{}, err
	}
	return s.store.GetLatestChannelSummary(ctx, channelID, channelKind, s.now().UTC())
}

func (s *AuditManagementService) GetReportDetail(
	ctx context.Context,
	reportID string,
	samples AuditPageRequest,
	strategyResults AuditPageRequest,
) (AuditReportDetailResult, error) {
	if err := s.validate(); err != nil {
		return AuditReportDetailResult{}, err
	}
	result, found, err := s.store.GetReportDetail(ctx, reportID, AuditReportDetailOptions{
		Now: s.now().UTC(), Samples: samples, StrategyResults: strategyResults,
	})
	if err != nil {
		return AuditReportDetailResult{}, err
	}
	if !found {
		return AuditReportDetailResult{}, auditNotFound("报告", reportID)
	}
	return result, nil
}

func (s *AuditManagementService) ExportReportHTML(ctx context.Context, reportID string) (AuditHTMLExport, error) {
	if err := s.validate(); err != nil {
		return AuditHTMLExport{}, err
	}
	if s.renderer == nil {
		return AuditHTMLExport{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "审计 HTML 渲染器尚未配置")
	}
	generatedAt := s.now().UTC()
	result, found, err := s.store.GetReportDetail(ctx, reportID, AuditReportDetailOptions{
		Now: generatedAt, Samples: AuditPageRequest{Page: 1, PageSize: 1},
		StrategyResults: AuditPageRequest{Page: 1, PageSize: 1},
	})
	if err != nil {
		return AuditHTMLExport{}, err
	}
	if !found {
		return AuditHTMLExport{}, auditNotFound("报告", reportID)
	}
	dto := AuditHTMLReportDTO{
		Schema:      VersionedRef{ID: "audit.html-report", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		Detail:      result.Detail,
		Redaction:   DefaultAuditRedactionManifest(),
		Limitations: []string{"黑盒行为证据不是密码学身份证明。", "过期结果不代表渠道当前状态。"},
		GeneratedAt: generatedAt,
	}
	if err := dto.Validate(); err != nil {
		return AuditHTMLExport{}, err
	}
	content, err := s.renderer.Render(dto)
	if err != nil {
		return AuditHTMLExport{}, err
	}
	if len(content) == 0 {
		return AuditHTMLExport{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计 HTML 渲染器返回空内容")
	}
	artifactID, err := s.newArtifactID()
	if err != nil {
		return AuditHTMLExport{}, err
	}
	fileName := result.Detail.Report.ID + ".html"
	digest := sha256.Sum256(content)
	artifact := AuditArtifact{
		ID:            artifactID,
		RunID:         result.Detail.Report.RunID,
		ReportID:      result.Detail.Report.ID,
		FileName:      fileName,
		ContentSHA256: hex.EncodeToString(digest[:]),
		SizeBytes:     int64(len(content)),
		CreatedAt:     generatedAt,
	}
	if err := s.store.SaveArtifact(ctx, artifact); err != nil {
		return AuditHTMLExport{}, err
	}
	return AuditHTMLExport{FileName: fileName, Content: content, Artifact: artifact}, nil
}

func (s *AuditManagementService) validate() error {
	if s == nil || s.store == nil || s.now == nil || s.newJobID == nil || s.newArtifactID == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计管理服务未初始化")
	}
	return nil
}

func (s *AuditManagementService) validateWorkload(workload AuditWorkload) error {
	if validator, ok := s.runController.(AuditWorkloadValidator); ok {
		return validator.ValidateWorkload(workload)
	}
	return nil
}

func (s *AuditManagementService) requireJob(ctx context.Context, id string) (AuditJob, error) {
	if err := s.validate(); err != nil {
		return AuditJob{}, err
	}
	id = strings.TrimSpace(id)
	job, found, err := s.store.GetJob(ctx, id)
	if err != nil {
		return AuditJob{}, err
	}
	if !found {
		return AuditJob{}, auditNotFound("任务", id)
	}
	return job, nil
}

func (s *AuditManagementService) requireRun(ctx context.Context, id string) (AuditRun, error) {
	if err := s.validate(); err != nil {
		return AuditRun{}, err
	}
	id = strings.TrimSpace(id)
	run, found, err := s.store.GetRun(ctx, id)
	if err != nil {
		return AuditRun{}, err
	}
	if !found {
		return AuditRun{}, auditNotFound("运行", id)
	}
	return run, nil
}

func auditNotFound(entity, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return contractError(ErrorCodeAuditNotFound, ErrorCategoryRequest, fmt.Sprintf("审计%s ID 不能为空", entity))
	}
	return contractError(ErrorCodeAuditNotFound, ErrorCategoryRequest, fmt.Sprintf("审计%s %q 不存在", entity, id))
}
