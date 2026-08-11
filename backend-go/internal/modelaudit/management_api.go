package modelaudit

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

type AuditJobResponse struct {
	Job AuditJob `json:"job"`
}

type AuditRunResponse struct {
	Run AuditRunPresentation `json:"run"`
}

type AuditJobPageResponse struct {
	Page AuditJobPage `json:"page"`
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
	Available bool                            `json:"available"`
	Reason    string                          `json:"reason,omitempty"`
	Packages  []CapabilityTaskPackageSnapshot `json:"packages"`
	Presets   []CapabilityRunPreset           `json:"presets"`
}

type AuditStrategyCatalogResponse struct {
	Available  bool                 `json:"available"`
	Reason     string               `json:"reason,omitempty"`
	Strategies []StrategyDescriptor `json:"strategies"`
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
