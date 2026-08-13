package modelaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type AuditJobDefinitionPreparer interface {
	PrepareJobDefinition(AuditJobDefinition) (AuditJobDefinition, error)
}

type AuditModAnalysisController interface {
	ImportManualModAnalysis(context.Context, string, json.RawMessage) (AuditModAnalysisRecord, error)
	RerunModAnalysis(context.Context, AuditJob, AuditRun, string, AuditModBundle, json.RawMessage) (AuditModAnalysisRecord, error)
}

type AuditRuntimeCatalogProvider interface {
	StrategyCatalog() []StrategyDescriptor
	CapabilityCatalog() AuditCapabilityAssetCatalog
}

type QuestionBankReloader interface {
	ReloadQuestionBanks() (QuestionBankCatalogSnapshot, error)
}

type AuditHTMLRenderer interface {
	Render(AuditHTMLReportDTO) ([]byte, error)
}

type AuditManagementService struct {
	store         *AuditSQLiteStore
	runController AuditRunController
	renderer      AuditHTMLRenderer
	mods          *AuditModManager
	now           func() time.Time
	newJobID      func() (string, error)
	newArtifactID func() (string, error)
}

func (s *AuditManagementService) SetModManager(manager *AuditModManager) error {
	if s == nil || manager == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计 Mod 管理器未初始化")
	}
	s.mods = manager
	return nil
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
	prepared, err := s.prepareJobDefinition(definition)
	if err != nil {
		return AuditJob{}, err
	}
	definition = prepared
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
	prepared, err := s.prepareJobDefinition(definition)
	if err != nil {
		return AuditJob{}, err
	}
	definition = prepared
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

func (s *AuditManagementService) RetryJob(ctx context.Context, jobID string, expectedRevision uint64) (AuditJob, AuditRun, error) {
	original, err := s.requireJob(ctx, jobID)
	if err != nil {
		return AuditJob{}, AuditRun{}, err
	}
	if original.Revision != expectedRevision {
		return AuditJob{}, AuditRun{}, auditRevisionConflict(original.Revision, expectedRevision)
	}
	if !original.Status.Valid() || original.Status == AuditJobDeleted || original.Status == AuditJobRunning {
		return AuditJob{}, AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, fmt.Sprintf("状态 %q 的审计任务不能重新运行", original.Status))
	}
	if s.runController == nil {
		return AuditJob{}, AuditRun{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "审计 Runner 尚未配置，不能重新运行")
	}
	now := s.now().UTC()
	definition := AuditJobDefinition{
		Name: original.Name, Workload: original.Workload, Targets: append([]AuditJobTarget(nil), original.Targets...),
		Schedule: cloneAuditSchedule(original.Schedule), Budget: original.Budget, Analyzer: cloneAuditAnalysisTarget(original.Analyzer),
	}
	definition.Schedule.StartAt = now
	definition.Schedule.EndAt = nil
	definition.Schedule.DurationMs = 0
	definition.Schedule.OneShot = true
	created, err := s.CreateJob(ctx, definition, AuditJobEnabled)
	if err != nil {
		return AuditJob{}, AuditRun{}, err
	}
	run, err := s.runController.StartManual(ctx, created)
	if err != nil {
		return created, AuditRun{}, err
	}
	return created, run, nil
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

func (s *AuditManagementService) PresentRun(ctx context.Context, run AuditRun) (AuditRunPresentation, error) {
	view, err := NewAuditRunPresentation(run)
	if err != nil {
		return AuditRunPresentation{}, err
	}
	if len(run.Targets) != 1 {
		return view, nil
	}
	result, found, err := s.store.GetSingleRunResult(ctx, run.ID)
	if err != nil {
		return AuditRunPresentation{}, err
	}
	if found {
		view.Result = &result
	}
	return view, view.Validate()
}

func (s *AuditManagementService) ListLatestRunPresentations(ctx context.Context, jobIDs []string) (map[string]AuditRunPresentation, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	runs, err := s.store.ListLatestRunsForJobs(ctx, jobIDs)
	if err != nil {
		return nil, err
	}
	views := make(map[string]AuditRunPresentation, len(runs))
	for jobID, run := range runs {
		view, err := s.PresentRun(ctx, run)
		if err != nil {
			return nil, err
		}
		views[jobID] = view
	}
	return views, nil
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

func (s *AuditManagementService) ReloadQuestionBanks() (QuestionBankCatalogSnapshot, error) {
	reloader, ok := s.runController.(QuestionBankReloader)
	if !ok {
		return QuestionBankCatalogSnapshot{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力题库不支持重载")
	}
	return reloader.ReloadQuestionBanks()
}

func (s *AuditManagementService) GetModCatalog() (AuditModCatalogSnapshot, error) {
	if err := s.validateMods(); err != nil {
		return AuditModCatalogSnapshot{}, err
	}
	return s.mods.Snapshot(), nil
}

func (s *AuditManagementService) GetModExamples() ([]AuditModExample, error) {
	return BuiltinAuditModExamples()
}

func (s *AuditManagementService) ReloadMods(ctx context.Context) (AuditModCatalogSnapshot, error) {
	if err := s.validateMods(); err != nil {
		return AuditModCatalogSnapshot{}, err
	}
	return s.mods.Reload(ctx)
}

func (s *AuditManagementService) GetMod(id string) (AuditModEditable, error) {
	if err := s.validateMods(); err != nil {
		return AuditModEditable{}, err
	}
	return s.mods.Editable(id)
}

func (s *AuditManagementService) SaveMod(ctx context.Context, requestedID string, request AuditModWriteRequest) (AuditModEditable, error) {
	if err := s.validateMods(); err != nil {
		return AuditModEditable{}, err
	}
	return s.mods.Save(ctx, requestedID, request)
}

func (s *AuditManagementService) ImportMod(ctx context.Context, request AuditModImportRequest) (AuditModEditable, error) {
	if err := s.validateMods(); err != nil {
		return AuditModEditable{}, err
	}
	return s.mods.Import(ctx, request)
}

func (s *AuditManagementService) ListModAnalyses(ctx context.Context, options AuditModAnalysisListOptions) (AuditModAnalysisPage, error) {
	if err := s.validate(); err != nil {
		return AuditModAnalysisPage{}, err
	}
	return s.store.ListModAnalyses(ctx, options)
}

func (s *AuditManagementService) ImportManualModAnalysis(
	ctx context.Context,
	analysisID string,
	result json.RawMessage,
) (AuditModAnalysisRecord, error) {
	controller, ok := s.runController.(AuditModAnalysisController)
	if !ok {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "Mod 分析控制器尚未配置")
	}
	return controller.ImportManualModAnalysis(ctx, analysisID, result)
}

func (s *AuditManagementService) RerunModAnalysis(ctx context.Context, analysisID string) (AuditModAnalysisRecord, error) {
	if err := s.validateMods(); err != nil {
		return AuditModAnalysisRecord{}, err
	}
	previous, found, err := s.store.GetModAnalysis(ctx, strings.TrimSpace(analysisID))
	if err != nil {
		return AuditModAnalysisRecord{}, err
	}
	if !found {
		return AuditModAnalysisRecord{}, auditNotFound("Mod 分析", analysisID)
	}
	if !previous.Status.Terminal() {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "只有已完成或失败的 Mod 分析可以重新分析")
	}
	run, err := s.requireRun(ctx, previous.RunID)
	if err != nil {
		return AuditModAnalysisRecord{}, err
	}
	job, err := s.requireJob(ctx, run.JobID)
	if err != nil {
		return AuditModAnalysisRecord{}, err
	}
	if previous.Analyzer != nil {
		job.Analyzer = &AuditAnalysisTarget{
			ChannelID: previous.Analyzer.ChannelID, ChannelKind: previous.Analyzer.ChannelKind,
			Protocol: previous.Analyzer.Protocol, Model: previous.Analyzer.Model, Thinking: previous.Analyzer.Thinking,
			RequestProfile: previous.Analyzer.RequestProfile,
		}
	}
	bundle, found, err := s.mods.GetStored(ctx, previous.Mod)
	if err != nil {
		return AuditModAnalysisRecord{}, err
	}
	if !found {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "重新分析所需的冻结 Mod 版本不存在")
	}
	controller, ok := s.runController.(AuditModAnalysisController)
	if !ok {
		return AuditModAnalysisRecord{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "Mod 分析控制器尚未配置")
	}
	return controller.RerunModAnalysis(ctx, job, run, previous.TargetID, bundle, previous.Input)
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

func (s *AuditManagementService) validateMods() error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.mods == nil {
		return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "审计 Mod 管理器尚未配置")
	}
	return nil
}

func (s *AuditManagementService) validateWorkload(workload AuditWorkload) error {
	if validator, ok := s.runController.(AuditWorkloadValidator); ok {
		return validator.ValidateWorkload(workload)
	}
	return nil
}

func (s *AuditManagementService) prepareJobDefinition(definition AuditJobDefinition) (AuditJobDefinition, error) {
	if preparer, ok := s.runController.(AuditJobDefinitionPreparer); ok {
		return preparer.PrepareJobDefinition(definition)
	}
	if err := s.validateWorkload(definition.Workload); err != nil {
		return AuditJobDefinition{}, err
	}
	return definition, nil
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
