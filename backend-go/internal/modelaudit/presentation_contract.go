package modelaudit

import (
	"math"
	"strings"
	"time"
)

type AuditReportResultStatus string

const (
	AuditReportComplete             AuditReportResultStatus = "complete"
	AuditReportPartial              AuditReportResultStatus = "partial"
	AuditReportFailed               AuditReportResultStatus = "failed"
	AuditReportUnsupported          AuditReportResultStatus = "unsupported"
	AuditReportInsufficientEvidence AuditReportResultStatus = "insufficient_evidence"
)

func (s AuditReportResultStatus) Valid() bool {
	return s == AuditReportComplete || s == AuditReportPartial || s == AuditReportFailed ||
		s == AuditReportUnsupported || s == AuditReportInsufficientEvidence
}

type AuditSummaryStatus string

const (
	AuditSummaryNotDetected          AuditSummaryStatus = "not_detected"
	AuditSummaryRunning              AuditSummaryStatus = "running"
	AuditSummaryFailed               AuditSummaryStatus = "failed"
	AuditSummaryPartial              AuditSummaryStatus = "partial"
	AuditSummaryUnsupported          AuditSummaryStatus = "unsupported"
	AuditSummaryInsufficientEvidence AuditSummaryStatus = "insufficient_evidence"
	AuditSummaryStale                AuditSummaryStatus = "stale"
	AuditSummaryComplete             AuditSummaryStatus = "complete"
)

func (s AuditSummaryStatus) Valid() bool {
	return s == AuditSummaryNotDetected || s == AuditSummaryRunning || s == AuditSummaryFailed ||
		s == AuditSummaryPartial || s == AuditSummaryUnsupported || s == AuditSummaryInsufficientEvidence ||
		s == AuditSummaryStale || s == AuditSummaryComplete
}

type AuditFreshnessState string

const (
	AuditFreshnessUnavailable AuditFreshnessState = "unavailable"
	AuditFreshnessFresh       AuditFreshnessState = "fresh"
	AuditFreshnessStale       AuditFreshnessState = "stale"
)

func (s AuditFreshnessState) Valid() bool {
	return s == AuditFreshnessUnavailable || s == AuditFreshnessFresh || s == AuditFreshnessStale
}

type AuditFreshnessPolicy struct {
	Ref      VersionedRef `json:"ref"`
	MaxAgeMs int64        `json:"maxAgeMs"`
}

func (p AuditFreshnessPolicy) Validate() error {
	if err := p.Ref.Validate("审计新鲜度策略"); err != nil {
		return err
	}
	if p.MaxAgeMs <= 0 || p.MaxAgeMs > math.MaxInt64/int64(time.Millisecond) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计新鲜度最大时长无效")
	}
	return nil
}

type AuditFreshness struct {
	State       AuditFreshnessState  `json:"state"`
	Policy      AuditFreshnessPolicy `json:"policy"`
	ReportedAt  time.Time            `json:"reportedAt"`
	StaleAt     time.Time            `json:"staleAt"`
	EvaluatedAt time.Time            `json:"evaluatedAt"`
}

func EvaluateAuditFreshness(policy AuditFreshnessPolicy, reportedAt, now time.Time) (AuditFreshness, error) {
	if err := policy.Validate(); err != nil {
		return AuditFreshness{}, err
	}
	if reportedAt.IsZero() || now.IsZero() || now.Before(reportedAt) {
		return AuditFreshness{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计新鲜度时间无效")
	}
	reportedAt = reportedAt.UTC()
	now = now.UTC()
	staleAt := reportedAt.Add(time.Duration(policy.MaxAgeMs) * time.Millisecond)
	state := AuditFreshnessFresh
	if !now.Before(staleAt) {
		state = AuditFreshnessStale
	}
	result := AuditFreshness{State: state, Policy: policy, ReportedAt: reportedAt, StaleAt: staleAt, EvaluatedAt: now}
	return result, result.Validate()
}

func (f AuditFreshness) Validate() error {
	if f.State != AuditFreshnessFresh && f.State != AuditFreshnessStale {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "已有审计报告的新鲜度状态无效")
	}
	if err := f.Policy.Validate(); err != nil {
		return err
	}
	if f.ReportedAt.IsZero() || f.StaleAt.IsZero() || f.EvaluatedAt.IsZero() || f.EvaluatedAt.Before(f.ReportedAt) ||
		!f.StaleAt.Equal(f.ReportedAt.Add(time.Duration(f.Policy.MaxAgeMs)*time.Millisecond)) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计新鲜度时间边界无效")
	}
	expected := AuditFreshnessFresh
	if !f.EvaluatedAt.Before(f.StaleAt) {
		expected = AuditFreshnessStale
	}
	if f.State != expected {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计新鲜度状态与时间不一致")
	}
	return nil
}

type AuditIdentitySummary struct {
	Conclusion       IdentityConclusion `json:"conclusion"`
	Formal           bool               `json:"formal"`
	Confidence       float64            `json:"confidence"`
	ConsistencyScore float64            `json:"consistencyScore"`
	SampleCount      int                `json:"sampleCount"`
	Mixture          *MixtureEstimate   `json:"mixture,omitempty"`
}

func SummarizeAuditIdentity(report IdentityReport) (AuditIdentitySummary, error) {
	if err := report.Validate(); err != nil {
		return AuditIdentitySummary{}, err
	}
	summary := AuditIdentitySummary{
		Conclusion: report.Conclusion, Formal: report.Formal, Confidence: report.Confidence,
		ConsistencyScore: report.ConsistencyScore, SampleCount: report.SampleCount,
	}
	if report.Mixture != nil {
		mixture := *report.Mixture
		mixture.Components = append([]MixtureComponent(nil), report.Mixture.Components...)
		summary.Mixture = &mixture
	}
	return summary, summary.Validate()
}

func (s AuditIdentitySummary) Validate() error {
	if !s.Conclusion.Valid() || !unitInterval(s.Confidence) || !unitInterval(s.ConsistencyScore) || s.SampleCount < 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计身份摘要无效")
	}
	if s.Formal && (s.Conclusion == IdentityInsufficientEvidence || s.Conclusion == IdentityUnsupported) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "正式身份摘要不能是证据不足或不支持")
	}
	if s.Mixture != nil {
		if err := validateMixtureEstimate(*s.Mixture); err != nil {
			return err
		}
	}
	return nil
}

type AuditCapabilitySummary struct {
	Status           CapabilityAggregationStatus   `json:"status"`
	Formal           bool                          `json:"formal"`
	Index            *float64                      `json:"index,omitempty"`
	IndexInterval    *CapabilityConfidenceInterval `json:"indexInterval,omitempty"`
	Coverage         float64                       `json:"coverage"`
	ScoredDimensions int                           `json:"scoredDimensions"`
	Dimension        CapabilityDimension           `json:"dimension,omitempty"`
	Score            *float64                      `json:"score,omitempty"`
}

func SummarizeAuditCapability(report CapabilityReport) (AuditCapabilitySummary, error) {
	if err := report.Validate(); err != nil {
		return AuditCapabilitySummary{}, err
	}
	summary := AuditCapabilitySummary{
		Status: report.Status, Formal: report.Formal, Index: cloneFloatPointer(report.Index),
		Coverage: report.Coverage, ScoredDimensions: report.ScoredDimensions,
	}
	if report.IndexInterval != nil {
		interval := *report.IndexInterval
		summary.IndexInterval = &interval
	}
	if report.ScoredDimensions == 1 {
		for _, dimension := range report.Dimensions {
			if dimension.Score != nil {
				summary.Dimension = dimension.Dimension
				summary.Score = cloneFloatPointer(dimension.Score)
				break
			}
		}
	}
	return summary, summary.Validate()
}

func (s AuditCapabilitySummary) Validate() error {
	if !s.Status.Valid() || !unitInterval(s.Coverage) || s.ScoredDimensions < 0 || s.ScoredDimensions > len(capabilityDimensions) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计能力摘要状态、覆盖率或维度数无效")
	}
	if s.Index != nil && (!finiteNumber(*s.Index) || *s.Index < 0 || *s.Index > 100) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计能力摘要指数无效")
	}
	if s.Dimension != "" && !s.Dimension.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计能力摘要维度无效")
	}
	if s.Score != nil && (s.Dimension == "" || !finiteNumber(*s.Score) || *s.Score < 0 || *s.Score > 100) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计能力摘要单项分数无效")
	}
	if s.IndexInterval != nil {
		if err := s.IndexInterval.Validate(); err != nil {
			return err
		}
	}
	if s.IndexInterval != nil && s.Index == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计能力摘要区间不能脱离指数")
	}
	if s.Formal != (s.Status == CapabilityAggregationComplete) || (s.Formal && (s.Index == nil || s.IndexInterval == nil)) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计能力摘要正式状态不一致")
	}
	return nil
}

type AuditTargetReport struct {
	Schema            VersionedRef            `json:"schema"`
	ID                string                  `json:"id"`
	RunID             string                  `json:"runId"`
	JobID             string                  `json:"jobId"`
	JobRevision       uint64                  `json:"jobRevision"`
	JobSnapshotSHA256 string                  `json:"jobSnapshotSha256"`
	Target            AuditRunTargetSnapshot  `json:"target"`
	ResultStatus      AuditReportResultStatus `json:"resultStatus"`
	RunStatus         AuditRunStatus          `json:"runStatus"`
	StopReason        AuditRunStopReason      `json:"stopReason,omitempty"`
	Usage             AuditRunUsage           `json:"usage"`
	SampleCount       int                     `json:"sampleCount"`
	Identity          *IdentityReport         `json:"identity,omitempty"`
	Capability        *CapabilityReport       `json:"capability,omitempty"`
	Evidence          []EvidenceReference     `json:"evidence,omitempty"`
	FreshnessPolicy   AuditFreshnessPolicy    `json:"freshnessPolicy"`
	CreatedAt         time.Time               `json:"createdAt"`
}

const (
	AuditTargetReportSchemaID      = "audit.target-report"
	AuditTargetReportSchemaVersion = "1.0.0"
)

func (r AuditTargetReport) Validate() error {
	if err := r.Schema.Validate("审计目标报告 Schema"); err != nil {
		return err
	}
	if r.Schema.ID != AuditTargetReportSchemaID || r.Schema.SemanticVersion != AuditTargetReportSchemaVersion {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计目标报告 Schema 不受支持")
	}
	if !validAuditEntityID(r.ID) || !validAuditEntityID(r.RunID) || !validAuditEntityID(r.JobID) ||
		r.JobRevision == 0 || !validSHA256(r.JobSnapshotSHA256) || !r.ResultStatus.Valid() || !r.RunStatus.Terminal() ||
		!r.StopReason.Valid() || r.SampleCount < 0 || r.CreatedAt.IsZero() ||
		!r.CreatedAt.Equal(time.UnixMilli(r.CreatedAt.UnixMilli()).UTC()) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计目标报告身份、运行状态或时间无效")
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if err := r.Usage.Validate(); err != nil {
		return err
	}
	if err := r.FreshnessPolicy.Validate(); err != nil {
		return err
	}
	if r.Target.Resolved != nil && r.CreatedAt.Before(r.Target.Resolved.CapturedAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计目标报告早于目标解析快照")
	}
	if err := validateAuditReportRunResult(r); err != nil {
		return err
	}
	formal := false
	if r.Identity != nil {
		if err := r.Identity.Validate(); err != nil {
			return err
		}
		formal = formal || r.Identity.Formal
	}
	if r.Capability != nil {
		if err := r.Capability.Validate(); err != nil {
			return err
		}
		formal = formal || r.Capability.Formal
	}
	if r.ResultStatus == AuditReportComplete && !formal {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "完整审计报告至少需要一个正式结论")
	}
	if r.ResultStatus == AuditReportUnsupported && (r.Identity == nil || r.Identity.Conclusion != IdentityUnsupported || r.Capability != nil) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "不支持审计报告必须由身份不支持结论构成")
	}
	if r.ResultStatus == AuditReportInsufficientEvidence && formal {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "证据不足审计报告不能包含正式结论")
	}
	if r.Identity == nil && r.Capability == nil && r.ResultStatus != AuditReportFailed && r.ResultStatus != AuditReportPartial {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计目标报告缺少身份或能力结论")
	}
	for _, evidence := range r.Evidence {
		if !validAuditEntityID(evidence.ID) || strings.TrimSpace(evidence.Kind) == "" || !validSHA256(evidence.SHA256) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计目标报告包含无效证据引用")
		}
	}
	return nil
}

func validateAuditReportRunResult(report AuditTargetReport) error {
	switch report.RunStatus {
	case AuditRunCompleted:
		if report.StopReason != AuditRunStopNone ||
			(report.ResultStatus != AuditReportComplete && report.ResultStatus != AuditReportUnsupported && report.ResultStatus != AuditReportInsufficientEvidence) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "完成运行与审计报告结果不一致")
		}
	case AuditRunPartial:
		if report.ResultStatus != AuditReportPartial ||
			(report.StopReason != AuditRunStopBudget && report.StopReason != AuditRunStopCancelled && report.StopReason != AuditRunStopInterrupted) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "部分运行与审计报告结果不一致")
		}
	case AuditRunFailed:
		if report.ResultStatus != AuditReportFailed || (report.StopReason != AuditRunStopFailure && report.StopReason != AuditRunStopInterrupted) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "失败运行与审计报告结果不一致")
		}
	case AuditRunCancelled:
		if report.ResultStatus != AuditReportPartial || report.StopReason != AuditRunStopCancelled || report.Usage != (AuditRunUsage{}) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "取消运行与审计报告结果不一致")
		}
	}
	return nil
}

func ResolveAuditSummaryStatus(activeRunID string, hasReport bool, result AuditReportResultStatus, freshness AuditFreshnessState) (AuditSummaryStatus, error) {
	if activeRunID != "" && !validAuditEntityID(activeRunID) {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计摘要活动运行 ID 无效")
	}
	if activeRunID != "" {
		return AuditSummaryRunning, nil
	}
	if !hasReport {
		if result != "" || freshness != AuditFreshnessUnavailable {
			return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未检测摘要不能携带报告结果或新鲜度")
		}
		return AuditSummaryNotDetected, nil
	}
	if !result.Valid() || (freshness != AuditFreshnessFresh && freshness != AuditFreshnessStale) {
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计摘要报告结果或新鲜度无效")
	}
	switch result {
	case AuditReportFailed:
		return AuditSummaryFailed, nil
	case AuditReportPartial:
		return AuditSummaryPartial, nil
	case AuditReportUnsupported:
		return AuditSummaryUnsupported, nil
	case AuditReportInsufficientEvidence:
		return AuditSummaryInsufficientEvidence, nil
	case AuditReportComplete:
		if freshness == AuditFreshnessStale {
			return AuditSummaryStale, nil
		}
		return AuditSummaryComplete, nil
	default:
		return "", contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计摘要状态无法解析")
	}
}

type AuditChannelSummary struct {
	ChannelID      string                  `json:"channelId"`
	ChannelKind    ChannelKind             `json:"channelKind"`
	PrimaryStatus  AuditSummaryStatus      `json:"primaryStatus"`
	ActiveRunID    string                  `json:"activeRunId,omitempty"`
	ActiveRunCount int                     `json:"activeRunCount"`
	ReportID       string                  `json:"reportId,omitempty"`
	RunID          string                  `json:"runId,omitempty"`
	ResultStatus   AuditReportResultStatus `json:"resultStatus,omitempty"`
	Protocol       Protocol                `json:"protocol,omitempty"`
	ResolvedModel  string                  `json:"resolvedModel,omitempty"`
	Thinking       ThinkingLevel           `json:"thinking,omitempty"`
	SampleCount    int                     `json:"sampleCount"`
	Freshness      *AuditFreshness         `json:"freshness,omitempty"`
	Identity       *AuditIdentitySummary   `json:"identity,omitempty"`
	Capability     *AuditCapabilitySummary `json:"capability,omitempty"`
	ReportedAt     *time.Time              `json:"reportedAt,omitempty"`
}

func (s AuditChannelSummary) Validate() error {
	if !validAuditEntityID(s.ChannelID) || !s.ChannelKind.Valid() || !s.PrimaryStatus.Valid() || s.ActiveRunCount < 0 || s.SampleCount < 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计渠道摘要身份、状态或样本数无效")
	}
	if strings.TrimSpace(s.ResolvedModel) != s.ResolvedModel || (s.Protocol != "" && !s.Protocol.Valid()) || !s.Thinking.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计渠道摘要协议、模型或思考档位无效")
	}
	if s.ActiveRunID != "" && !validAuditEntityID(s.ActiveRunID) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计渠道摘要活动运行 ID 无效")
	}
	if (s.ActiveRunID == "") != (s.ActiveRunCount == 0) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计渠道摘要活动运行 ID 与数量不一致")
	}
	hasReport := s.ReportID != "" || s.RunID != "" || s.ReportedAt != nil || s.Freshness != nil
	if !hasReport {
		if s.ReportID != "" || s.RunID != "" || s.ResultStatus != "" || s.ReportedAt != nil || s.Freshness != nil ||
			s.Identity != nil || s.Capability != nil || s.SampleCount != 0 {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未检测渠道摘要包含报告字段")
		}
		expected, err := ResolveAuditSummaryStatus(s.ActiveRunID, false, "", AuditFreshnessUnavailable)
		if err != nil || s.PrimaryStatus != expected {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "未检测渠道摘要主状态不一致", err)
		}
		return nil
	}
	if !validAuditEntityID(s.ReportID) || !validAuditEntityID(s.RunID) || !s.ResultStatus.Valid() || s.ReportedAt == nil || s.ReportedAt.IsZero() || s.Freshness == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计渠道摘要报告引用不完整")
	}
	if err := s.Freshness.Validate(); err != nil {
		return err
	}
	if !s.Freshness.ReportedAt.Equal(*s.ReportedAt) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计渠道摘要报告时间与新鲜度不一致")
	}
	if s.Identity != nil {
		if err := s.Identity.Validate(); err != nil {
			return err
		}
	}
	if s.Capability != nil {
		if err := s.Capability.Validate(); err != nil {
			return err
		}
	}
	expected, err := ResolveAuditSummaryStatus(s.ActiveRunID, true, s.ResultStatus, s.Freshness.State)
	if err != nil || s.PrimaryStatus != expected {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计渠道摘要主状态不一致", err)
	}
	return nil
}

type AuditRunPresentation struct {
	ID             string             `json:"id"`
	JobID          string             `json:"jobId"`
	JobRevision    uint64             `json:"jobRevision"`
	Trigger        AuditRunTrigger    `json:"trigger"`
	ScheduledFor   *time.Time         `json:"scheduledFor,omitempty"`
	Status         AuditRunStatus     `json:"status"`
	StopReason     AuditRunStopReason `json:"stopReason,omitempty"`
	Budget         AuditRunBudget     `json:"budget"`
	Usage          AuditRunUsage      `json:"usage"`
	TargetCount    int                `json:"targetCount"`
	CreatedAt      time.Time          `json:"createdAt"`
	StartedAt      *time.Time         `json:"startedAt,omitempty"`
	FinishedAt     *time.Time         `json:"finishedAt,omitempty"`
	FailureHidden  bool               `json:"failureHidden"`
	FailureMessage string             `json:"failureMessage,omitempty"`
	Result         *AuditRunResult    `json:"result,omitempty"`
}

type AuditRunResult struct {
	ReportID     string                  `json:"reportId"`
	ResultStatus AuditReportResultStatus `json:"resultStatus"`
	Capability   *AuditCapabilitySummary `json:"capability,omitempty"`
	ReportedAt   time.Time               `json:"reportedAt"`
}

func NewAuditRunResult(report AuditTargetReport) (AuditRunResult, error) {
	if err := report.Validate(); err != nil {
		return AuditRunResult{}, err
	}
	result := AuditRunResult{
		ReportID: report.ID, ResultStatus: report.ResultStatus, ReportedAt: report.CreatedAt,
	}
	if report.Capability != nil {
		capability, err := SummarizeAuditCapability(*report.Capability)
		if err != nil {
			return AuditRunResult{}, err
		}
		result.Capability = &capability
	}
	return result, result.Validate()
}

func (r AuditRunResult) Validate() error {
	if !validAuditEntityID(r.ReportID) || !r.ResultStatus.Valid() || r.ReportedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行结果摘要无效")
	}
	if r.Capability != nil {
		return r.Capability.Validate()
	}
	return nil
}

func NewAuditRunPresentation(run AuditRun) (AuditRunPresentation, error) {
	if err := run.Validate(); err != nil {
		return AuditRunPresentation{}, err
	}
	view := AuditRunPresentation{
		ID: run.ID, JobID: run.JobID, JobRevision: run.JobRevision, Trigger: run.Trigger,
		ScheduledFor: cloneTimePointer(run.ScheduledFor), Status: run.Status, StopReason: run.StopReason,
		Budget: run.Budget, Usage: run.Usage, TargetCount: len(run.Targets), CreatedAt: run.CreatedAt,
		StartedAt: cloneTimePointer(run.StartedAt), FinishedAt: cloneTimePointer(run.FinishedAt), FailureHidden: run.Failure != "",
		FailureMessage: strings.TrimSpace(run.Failure),
	}
	return view, view.Validate()
}

func (v AuditRunPresentation) Validate() error {
	if !validAuditEntityID(v.ID) || !validAuditEntityID(v.JobID) || v.JobRevision == 0 || !v.Trigger.Valid() ||
		!v.Status.Valid() || !v.StopReason.Valid() || v.TargetCount < 0 || v.CreatedAt.IsZero() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行展示视图无效")
	}
	if (v.Trigger == AuditRunScheduled) != (v.ScheduledFor != nil) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行展示视图的定时字段不一致")
	}
	if err := v.Budget.Validate(); err != nil {
		return err
	}
	if err := v.Usage.Validate(); err != nil {
		return err
	}
	if !v.Usage.Fits(v.Budget) || (v.StartedAt != nil && v.StartedAt.Before(v.CreatedAt)) ||
		(v.FinishedAt != nil && v.FinishedAt.Before(v.CreatedAt)) || (v.StartedAt != nil && v.FinishedAt != nil && v.FinishedAt.Before(*v.StartedAt)) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行展示视图的预算或时间无效")
	}
	if v.Status.Terminal() != (v.FinishedAt != nil) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行展示视图终态时间不一致")
	}
	if v.Result != nil {
		if err := v.Result.Validate(); err != nil {
			return err
		}
		if v.Result.ReportedAt.Before(v.CreatedAt) {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计运行结果早于运行创建时间")
		}
	}
	return nil
}

type AuditReportDetail struct {
	Schema       VersionedRef         `json:"schema"`
	WorkloadKind AuditWorkloadKind    `json:"workloadKind"`
	Summary      AuditChannelSummary  `json:"summary"`
	Run          AuditRunPresentation `json:"run"`
	Report       AuditTargetReport    `json:"report"`
}

func (d AuditReportDetail) Validate() error {
	if err := d.Schema.Validate("审计详情 Schema"); err != nil {
		return err
	}
	if err := d.Summary.Validate(); err != nil {
		return err
	}
	if err := d.Run.Validate(); err != nil {
		return err
	}
	if err := d.Report.Validate(); err != nil {
		return err
	}
	requested := d.Report.Target.Requested
	if d.Summary.ReportID != d.Report.ID || d.Summary.RunID != d.Report.RunID || d.Run.ID != d.Report.RunID ||
		d.Run.JobID != d.Report.JobID || d.Run.JobRevision != d.Report.JobRevision || d.Run.Status != d.Report.RunStatus ||
		d.Run.StopReason != d.Report.StopReason || d.Run.Usage != d.Report.Usage || d.Summary.ChannelID != requested.ChannelID ||
		d.Summary.ChannelKind != requested.ChannelKind {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计详情的摘要、运行与报告引用不一致")
	}
	return nil
}

type AuditSensitiveFieldClass string

const (
	AuditSensitiveAPIKey          AuditSensitiveFieldClass = "api_key"
	AuditSensitiveAuthorization   AuditSensitiveFieldClass = "authorization"
	AuditSensitiveReservedAnswers AuditSensitiveFieldClass = "reserved_answers"
	AuditSensitiveUnselectedInput AuditSensitiveFieldClass = "unselected_input"
	AuditSensitiveEvidencePayload AuditSensitiveFieldClass = "raw_evidence_payload"
	AuditSensitiveFailureDetail   AuditSensitiveFieldClass = "failure_detail"
)

var defaultAuditExcludedFields = []AuditSensitiveFieldClass{
	AuditSensitiveAPIKey,
	AuditSensitiveAuthorization,
	AuditSensitiveReservedAnswers,
	AuditSensitiveUnselectedInput,
	AuditSensitiveEvidencePayload,
	AuditSensitiveFailureDetail,
}

type AuditRedactionManifest struct {
	Mode     string                     `json:"mode"`
	Excluded []AuditSensitiveFieldClass `json:"excluded"`
}

func DefaultAuditRedactionManifest() AuditRedactionManifest {
	return AuditRedactionManifest{Mode: "default", Excluded: append([]AuditSensitiveFieldClass(nil), defaultAuditExcludedFields...)}
}

func (m AuditRedactionManifest) Validate() error {
	if m.Mode != "default" || len(m.Excluded) != len(defaultAuditExcludedFields) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计导出脱敏模式或排除项无效")
	}
	for index, expected := range defaultAuditExcludedFields {
		if m.Excluded[index] != expected {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计导出脱敏排除项不可放宽或重排")
		}
	}
	return nil
}

type AuditHTMLReportDTO struct {
	Schema      VersionedRef           `json:"schema"`
	Detail      AuditReportDetail      `json:"detail"`
	Redaction   AuditRedactionManifest `json:"redaction"`
	Limitations []string               `json:"limitations"`
	GeneratedAt time.Time              `json:"generatedAt"`
}

func (d AuditHTMLReportDTO) Validate() error {
	if err := d.Schema.Validate("审计 HTML 报告 Schema"); err != nil {
		return err
	}
	if err := d.Detail.Validate(); err != nil {
		return err
	}
	if err := d.Redaction.Validate(); err != nil {
		return err
	}
	if d.GeneratedAt.IsZero() || d.GeneratedAt.Before(d.Detail.Report.CreatedAt) || len(d.Limitations) == 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计 HTML 报告生成时间或限制声明无效")
	}
	seen := make(map[string]struct{}, len(d.Limitations))
	for _, limitation := range d.Limitations {
		if strings.TrimSpace(limitation) == "" || strings.TrimSpace(limitation) != limitation {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计 HTML 报告包含无效限制声明")
		}
		if _, duplicate := seen[limitation]; duplicate {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计 HTML 报告包含重复限制声明")
		}
		seen[limitation] = struct{}{}
	}
	return nil
}

func cloneFloatPointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
