package modelaudit

import "encoding/json"

type CreateAuditJobRequest struct {
	Definition    AuditJobDefinition `json:"definition"`
	InitialStatus AuditJobStatus     `json:"initialStatus"`
}

type UpdateAuditJobRequest struct {
	ExpectedRevision uint64             `json:"expectedRevision"`
	Definition       AuditJobDefinition `json:"definition"`
}

type TransitionAuditJobRequest struct {
	ExpectedRevision uint64         `json:"expectedRevision"`
	Status           AuditJobStatus `json:"status"`
}

type StartManualAuditRunRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision"`
}

type RetryAuditJobResponse struct {
	Job AuditJob             `json:"job"`
	Run AuditRunPresentation `json:"run"`
}

type AuditJobResponse struct {
	Job AuditJob `json:"job"`
}

type AuditRunResponse struct {
	Run AuditRunPresentation `json:"run"`
}

type AuditJobPageResponse struct {
	Page       AuditJobPage                    `json:"page"`
	LatestRuns map[string]AuditRunPresentation `json:"latestRuns"`
}

type AuditRunPageResponse struct {
	Page AuditRunPresentationPage `json:"page"`
}

type AuditRunPresentationPage struct {
	Runs     []AuditRunPresentation `json:"runs"`
	Total    int64                  `json:"total"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"pageSize"`
}

type AuditCapabilityAssetCatalog struct {
	Available         bool                            `json:"available"`
	Reason            string                          `json:"reason,omitempty"`
	Packages          []CapabilityTaskPackageSnapshot `json:"packages"`
	Presets           []CapabilityRunPreset           `json:"presets"`
	EvaluationOptions []CapabilityEvaluationOption    `json:"evaluationOptions"`
	QuestionBanks     QuestionBankCatalogSnapshot     `json:"questionBanks"`
}

// CapabilityEvaluationOption is the stable UI-facing contract for a one-shot
// evaluation. Presets remain the internal execution contract and historical
// jobs can continue resolving their frozen preset references.
type CapabilityEvaluationOption struct {
	ID            string                 `json:"id"`
	BankID        string                 `json:"bankId"`
	BankName      string                 `json:"bankName"`
	Label         string                 `json:"label"`
	Dimension     CapabilityDimension    `json:"dimension,omitempty"`
	QuestionCount int                    `json:"questionCount"`
	Preset        VersionedRef           `json:"preset"`
	Limits        CapabilityBudgetLimits `json:"limits"`
}

type AuditStrategyCatalogResponse struct {
	Available  bool                 `json:"available"`
	Reason     string               `json:"reason,omitempty"`
	Strategies []StrategyDescriptor `json:"strategies"`
}

type AuditIdentityPresetCatalogResponse struct {
	Presets []IdentityPresetDescriptor `json:"presets"`
}

type AuditCapabilityCatalogResponse struct {
	Catalog AuditCapabilityAssetCatalog `json:"catalog"`
}

type AuditChannelSummaryResponse struct {
	Summary AuditChannelSummary `json:"summary"`
}

type AuditReportDetailResponse struct {
	Result AuditReportDetailResult `json:"result"`
}

type AuditModCatalogResponse struct {
	Catalog AuditModCatalogSnapshot `json:"catalog"`
}

type AuditModResponse struct {
	Mod AuditModEditable `json:"mod"`
}

type AuditModAnalysisPageResponse struct {
	Page AuditModAnalysisPage `json:"page"`
}

type AuditModAnalysisResponse struct {
	Analysis AuditModAnalysisRecord `json:"analysis"`
}

type ImportManualAuditModAnalysisRequest struct {
	Result json.RawMessage `json:"result"`
}
