package modelaudit

import (
	"bytes"
	"fmt"
	"html/template"
	"time"
)

type SelfContainedAuditHTMLRenderer struct {
	template *template.Template
}

func NewSelfContainedAuditHTMLRenderer() (*SelfContainedAuditHTMLRenderer, error) {
	parsed, err := template.New("audit-report").Parse(auditHTMLTemplate)
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "初始化审计 HTML 模板失败", err)
	}
	return &SelfContainedAuditHTMLRenderer{template: parsed}, nil
}

func (r *SelfContainedAuditHTMLRenderer) Render(dto AuditHTMLReportDTO) ([]byte, error) {
	if r == nil || r.template == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计 HTML 渲染器未初始化")
	}
	if err := dto.Validate(); err != nil {
		return nil, err
	}
	view := newAuditHTMLView(dto)
	var output bytes.Buffer
	if err := r.template.Execute(&output, view); err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "渲染审计 HTML 报告失败", err)
	}
	return output.Bytes(), nil
}

type auditHTMLView struct {
	Title             string
	Status            string
	StatusClass       string
	GeneratedAt       string
	ReportedAt        string
	Freshness         string
	ChannelName       string
	ChannelID         string
	ChannelKind       string
	Protocol          string
	RequestedModel    string
	ResolvedModel     string
	Thinking          string
	RequestProfile    string
	RunID             string
	JobID             string
	RunStatus         string
	Trigger           string
	StopReason        string
	Requests          int
	InputTokens       int64
	OutputTokens      int64
	TotalTokens       int64
	SampleCount       int
	Identity          *auditHTMLIdentityView
	Capability        *auditHTMLCapabilityView
	Evidence          []auditHTMLEvidenceView
	Versions          []auditHTMLVersionView
	Limitations       []string
	RedactionExcluded []string
}

type auditHTMLIdentityView struct {
	Conclusion      string
	Formal          string
	Confidence      float64
	ConfidenceText  string
	Consistency     float64
	ConsistencyText string
	SampleCount     int
	Mixture         []auditHTMLMixtureView
	Signals         []auditHTMLSignalView
	Alternatives    []string
}

type auditHTMLMixtureView struct {
	Candidate      string
	Proportion     float64
	ProportionText string
	Interval       string
}

type auditHTMLSignalView struct {
	ID          string
	Kind        string
	Status      string
	Samples     int
	Reliability string
	Score       string
	Evidence    int
}

type auditHTMLCapabilityView struct {
	Status        string
	Formal        string
	Index         string
	IndexValue    float64
	IndexInterval string
	Coverage      string
	Dimensions    []auditHTMLDimensionView
}

type auditHTMLDimensionView struct {
	Dimension string
	Score     string
	Value     float64
	Coverage  string
	Formal    string
}

type auditHTMLEvidenceView struct {
	ID       string
	Kind     string
	SHA256   string
	Redacted string
}

type auditHTMLVersionView struct {
	Label          string
	ID             string
	Semantic       string
	Implementation string
}

func newAuditHTMLView(dto AuditHTMLReportDTO) auditHTMLView {
	detail := dto.Detail
	report := detail.Report
	target := report.Target
	requested := target.Requested
	resolvedModel := detail.Summary.ResolvedModel
	channelName := ""
	if target.Resolved != nil {
		channelName = target.Resolved.ChannelName
		resolvedModel = target.Resolved.ResolvedModel
	}
	view := auditHTMLView{
		Title: "渠道模型审计报告", Status: string(detail.Summary.PrimaryStatus), StatusClass: auditHTMLStatusClass(detail.Summary.PrimaryStatus),
		GeneratedAt: formatAuditHTMLTime(dto.GeneratedAt), ReportedAt: formatAuditHTMLTime(report.CreatedAt),
		Freshness: string(detail.Summary.Freshness.State), ChannelName: channelName,
		ChannelID: requested.ChannelID, ChannelKind: string(requested.ChannelKind), Protocol: string(requested.Protocol),
		RequestedModel: requested.Model, ResolvedModel: resolvedModel, Thinking: string(requested.Thinking), RequestProfile: requested.RequestProfile,
		RunID: report.RunID, JobID: report.JobID, RunStatus: string(report.RunStatus), Trigger: string(detail.Run.Trigger),
		StopReason: string(report.StopReason), Requests: report.Usage.Requests, InputTokens: report.Usage.InputTokens,
		OutputTokens: report.Usage.OutputTokens, TotalTokens: report.Usage.TotalTokens, SampleCount: report.SampleCount,
		Limitations: append([]string(nil), dto.Limitations...),
	}
	for _, excluded := range dto.Redaction.Excluded {
		view.RedactionExcluded = append(view.RedactionExcluded, string(excluded))
	}
	view.Versions = append(view.Versions,
		auditHTMLVersion("报告 Schema", report.Schema),
		auditHTMLVersion("详情 Schema", detail.Schema),
		auditHTMLVersion("导出 Schema", dto.Schema),
		auditHTMLVersion("新鲜度策略", report.FreshnessPolicy.Ref),
	)
	if report.Identity != nil {
		identity := report.Identity
		identityView := &auditHTMLIdentityView{
			Conclusion: string(identity.Conclusion), Formal: auditHTMLBoolean(identity.Formal),
			Confidence: identity.Confidence * 100, ConfidenceText: formatAuditHTMLPercent(identity.Confidence),
			Consistency: identity.ConsistencyScore * 100, ConsistencyText: formatAuditHTMLPercent(identity.ConsistencyScore),
			SampleCount: identity.SampleCount, Alternatives: append([]string(nil), identity.AlternativeExplanations...),
		}
		if identity.Mixture != nil {
			for _, component := range identity.Mixture.Components {
				identityView.Mixture = append(identityView.Mixture, auditHTMLMixtureView{
					Candidate: component.Candidate, Proportion: component.Proportion * 100,
					ProportionText: formatAuditHTMLPercent(component.Proportion),
					Interval:       fmt.Sprintf("%.1f%% - %.1f%%", component.Lower*100, component.Upper*100),
				})
			}
		}
		for _, signal := range identity.Signals {
			score := "-"
			if signal.Score != nil {
				score = formatAuditHTMLPercent(*signal.Score)
			}
			identityView.Signals = append(identityView.Signals, auditHTMLSignalView{
				ID: signal.ID, Kind: string(signal.Kind), Status: string(signal.Status), Samples: signal.SampleCount,
				Reliability: formatAuditHTMLPercent(signal.Reliability), Score: score, Evidence: len(signal.Evidence),
			})
		}
		view.Identity = identityView
		view.Versions = append(view.Versions, auditHTMLVersion("身份聚合器", identity.Aggregator))
	}
	if report.Capability != nil {
		capability := report.Capability
		capabilityView := &auditHTMLCapabilityView{
			Status: string(capability.Status), Formal: auditHTMLBoolean(capability.Formal), Coverage: formatAuditHTMLPercent(capability.Coverage),
			Index: "-", IndexInterval: "-",
		}
		if capability.Index != nil {
			capabilityView.IndexValue = *capability.Index
			capabilityView.Index = fmt.Sprintf("%.1f", *capability.Index)
		}
		if capability.IndexInterval != nil {
			capabilityView.IndexInterval = fmt.Sprintf("%.1f - %.1f (%.0f%%)", capability.IndexInterval.Lower,
				capability.IndexInterval.Upper, capability.IndexInterval.Level*100)
		}
		for _, dimension := range capability.Dimensions {
			score := "-"
			value := 0.0
			if dimension.Score != nil {
				value = *dimension.Score
				score = fmt.Sprintf("%.1f", *dimension.Score)
			}
			capabilityView.Dimensions = append(capabilityView.Dimensions, auditHTMLDimensionView{
				Dimension: string(dimension.Dimension), Score: score, Value: value,
				Coverage: formatAuditHTMLPercent(dimension.Coverage), Formal: auditHTMLBoolean(dimension.Formal),
			})
		}
		view.Capability = capabilityView
		view.Versions = append(view.Versions,
			auditHTMLVersion("能力聚合器", capability.Aggregator),
			auditHTMLVersion("能力任务包", capability.Package),
		)
	}
	for _, evidence := range report.Evidence {
		view.Evidence = append(view.Evidence, auditHTMLEvidenceView{
			ID: evidence.ID, Kind: evidence.Kind, SHA256: evidence.SHA256, Redacted: auditHTMLBoolean(evidence.Redacted),
		})
	}
	return view
}

func auditHTMLVersion(label string, reference VersionedRef) auditHTMLVersionView {
	return auditHTMLVersionView{
		Label: label, ID: reference.ID, Semantic: reference.SemanticVersion, Implementation: reference.ImplementationVersion,
	}
}

func auditHTMLStatusClass(status AuditSummaryStatus) string {
	switch status {
	case AuditSummaryComplete:
		return "status-success"
	case AuditSummaryRunning:
		return "status-running"
	case AuditSummaryFailed:
		return "status-danger"
	case AuditSummaryPartial, AuditSummaryUnsupported, AuditSummaryInsufficientEvidence, AuditSummaryStale:
		return "status-warning"
	default:
		return "status-neutral"
	}
}

func formatAuditHTMLTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatAuditHTMLPercent(value float64) string {
	return fmt.Sprintf("%.1f%%", value*100)
}

func auditHTMLBoolean(value bool) string {
	if value {
		return "是"
	}
	return "否"
}

const auditHTMLTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} - {{.ChannelID}}</title>
  <style>
    :root{color-scheme:light;--bg:#f8fafc;--surface:#fff;--text:#0f172a;--muted:#475569;--border:#cbd5e1;--primary:#1e3a5f;--accent:#15803d;--warning:#a16207;--danger:#b91c1c;--track:#e9eef5}
    *{box-sizing:border-box}html{font-size:16px}body{margin:0;background:var(--bg);color:var(--text);font-family:"Segoe UI","Noto Sans SC","Microsoft YaHei",Arial,sans-serif;line-height:1.55;letter-spacing:0}
    main{width:min(1180px,calc(100% - 32px));margin:0 auto;padding:28px 0 48px}header{border-bottom:3px solid var(--primary);padding-bottom:18px;margin-bottom:22px}
    h1{font-size:1.75rem;line-height:1.2;margin:0 0 8px}h2{font-size:1.16rem;margin:0 0 14px}h3{font-size:1rem;margin:0 0 10px}.subtitle{color:var(--muted);margin:0}.status{display:inline-flex;align-items:center;min-height:30px;padding:4px 10px;border:1px solid;border-radius:4px;font-weight:700;font-size:.82rem}
    .status-success{color:#166534;background:#f0fdf4;border-color:#86efac}.status-running{color:#1e3a8a;background:#eff6ff;border-color:#93c5fd}.status-warning{color:#854d0e;background:#fefce8;border-color:#fde047}.status-danger{color:#991b1b;background:#fef2f2;border-color:#fca5a5}.status-neutral{color:#334155;background:#f1f5f9;border-color:#cbd5e1}
    section{padding:20px 0;border-bottom:1px solid var(--border)}.metrics{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}.metric{background:var(--surface);border:1px solid var(--border);border-radius:6px;padding:12px;min-width:0}.metric-label{display:block;color:var(--muted);font-size:.78rem}.metric-value{display:block;font-size:1.05rem;font-weight:700;overflow-wrap:anywhere;margin-top:3px}
    .grid-2{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px}.definition{display:grid;grid-template-columns:minmax(130px,.35fr) 1fr;gap:7px 14px;margin:0}.definition dt{color:var(--muted)}.definition dd{margin:0;overflow-wrap:anywhere}
    table{width:100%;border-collapse:collapse;background:var(--surface);font-size:.86rem}caption{text-align:left;font-weight:700;margin-bottom:8px}th,td{padding:8px 10px;border:1px solid var(--border);text-align:left;vertical-align:top;overflow-wrap:anywhere}th{background:#eef2f7;color:#1e293b}code{font-family:"Cascadia Mono",Consolas,monospace;font-size:.78rem;overflow-wrap:anywhere}
    .bar-row{display:grid;grid-template-columns:minmax(120px,.65fr) minmax(180px,1.5fr) 80px;gap:10px;align-items:center;margin:9px 0}.bar-label{overflow-wrap:anywhere}.bar-value{text-align:right;font-variant-numeric:tabular-nums}progress{width:100%;height:13px;accent-color:var(--primary);background:var(--track)}
    .note-list{margin:0;padding-left:20px}.note-list li+li{margin-top:6px}.meta{color:var(--muted);font-size:.78rem;margin-top:18px}.empty{color:var(--muted);font-style:italic}
    @media(max-width:760px){main{width:min(100% - 20px,1180px);padding-top:18px}.metrics,.grid-2{grid-template-columns:1fr 1fr}.bar-row{grid-template-columns:1fr}.bar-value{text-align:left}.definition{grid-template-columns:1fr}.definition dd{margin-bottom:6px}.table-wrap{overflow-x:auto}}
    @media(max-width:460px){.metrics,.grid-2{grid-template-columns:1fr}h1{font-size:1.45rem}}
    @media print{body{background:#fff}main{width:100%;padding:0}header{break-after:avoid}.metric,table{break-inside:avoid}section{break-inside:auto}.status{print-color-adjust:exact;-webkit-print-color-adjust:exact}}
  </style>
</head>
<body>
<main>
  <header>
    <span class="status {{.StatusClass}}">{{.Status}}</span>
    <h1>{{.Title}}</h1>
    <p class="subtitle">{{if .ChannelName}}{{.ChannelName}} · {{end}}{{.ChannelKind}} · {{.ChannelID}}</p>
  </header>

  <section aria-labelledby="summary-title">
    <h2 id="summary-title">摘要</h2>
    <div class="metrics">
      <div class="metric"><span class="metric-label">报告状态</span><span class="metric-value">{{.Status}}</span></div>
      <div class="metric"><span class="metric-label">新鲜度</span><span class="metric-value">{{.Freshness}}</span></div>
      <div class="metric"><span class="metric-label">样本数</span><span class="metric-value">{{.SampleCount}}</span></div>
      <div class="metric"><span class="metric-label">总 Token</span><span class="metric-value">{{.TotalTokens}}</span></div>
    </div>
  </section>

  <section aria-labelledby="target-title">
    <h2 id="target-title">目标与运行</h2>
    <div class="grid-2">
      <dl class="definition">
        <dt>协议</dt><dd>{{.Protocol}}</dd><dt>请求模型</dt><dd>{{if .RequestedModel}}{{.RequestedModel}}{{else}}使用渠道默认模型{{end}}</dd>
        <dt>实际模型</dt><dd>{{.ResolvedModel}}</dd><dt>思考档位</dt><dd>{{.Thinking}}</dd><dt>请求轮廓</dt><dd>{{.RequestProfile}}</dd>
      </dl>
      <dl class="definition">
        <dt>Run ID</dt><dd><code>{{.RunID}}</code></dd><dt>Job ID</dt><dd><code>{{.JobID}}</code></dd><dt>触发方式</dt><dd>{{.Trigger}}</dd>
        <dt>运行状态</dt><dd>{{.RunStatus}}</dd><dt>停止原因</dt><dd>{{if .StopReason}}{{.StopReason}}{{else}}-{{end}}</dd>
        <dt>报告时间</dt><dd>{{.ReportedAt}}</dd><dt>生成时间</dt><dd>{{.GeneratedAt}}</dd>
      </dl>
    </div>
    <div class="metrics" style="margin-top:14px">
      <div class="metric"><span class="metric-label">请求数</span><span class="metric-value">{{.Requests}}</span></div>
      <div class="metric"><span class="metric-label">输入 Token</span><span class="metric-value">{{.InputTokens}}</span></div>
      <div class="metric"><span class="metric-label">输出 Token</span><span class="metric-value">{{.OutputTokens}}</span></div>
      <div class="metric"><span class="metric-label">总 Token</span><span class="metric-value">{{.TotalTokens}}</span></div>
    </div>
  </section>

  {{with .Identity}}
  <section aria-labelledby="identity-title">
    <h2 id="identity-title">身份与混用</h2>
    <div class="metrics">
      <div class="metric"><span class="metric-label">结论</span><span class="metric-value">{{.Conclusion}}</span></div>
      <div class="metric"><span class="metric-label">正式结论</span><span class="metric-value">{{.Formal}}</span></div>
      <div class="metric"><span class="metric-label">置信度</span><span class="metric-value">{{.ConfidenceText}}</span></div>
      <div class="metric"><span class="metric-label">一致性</span><span class="metric-value">{{.ConsistencyText}}</span></div>
    </div>
    <h3 style="margin-top:18px">置信度与一致性</h3>
    <div class="bar-row"><span class="bar-label">置信度</span><progress max="100" value="{{.Confidence}}">{{.ConfidenceText}}</progress><span class="bar-value">{{.ConfidenceText}}</span></div>
    <div class="bar-row"><span class="bar-label">一致性</span><progress max="100" value="{{.Consistency}}">{{.ConsistencyText}}</progress><span class="bar-value">{{.ConsistencyText}}</span></div>
    {{if .Mixture}}<h3 style="margin-top:18px">混用比例估计</h3>{{range .Mixture}}<div class="bar-row"><span class="bar-label">{{.Candidate}}</span><progress max="100" value="{{.Proportion}}">{{.ProportionText}}</progress><span class="bar-value">{{.ProportionText}}<br><small>{{.Interval}}</small></span></div>{{end}}{{end}}
    {{if .Signals}}<div class="table-wrap" style="margin-top:18px"><table><caption>身份信号</caption><thead><tr><th>ID</th><th>类型</th><th>状态</th><th>样本</th><th>可靠度</th><th>分数</th><th>证据数</th></tr></thead><tbody>{{range .Signals}}<tr><td><code>{{.ID}}</code></td><td>{{.Kind}}</td><td>{{.Status}}</td><td>{{.Samples}}</td><td>{{.Reliability}}</td><td>{{.Score}}</td><td>{{.Evidence}}</td></tr>{{end}}</tbody></table></div>{{end}}
    {{if .Alternatives}}<h3 style="margin-top:18px">替代解释与限制</h3><ul class="note-list">{{range .Alternatives}}<li>{{.}}</li>{{end}}</ul>{{end}}
  </section>
  {{end}}

  {{with .Capability}}
  <section aria-labelledby="capability-title">
    <h2 id="capability-title">能力评分</h2>
    <div class="metrics">
      <div class="metric"><span class="metric-label">状态</span><span class="metric-value">{{.Status}}</span></div>
      <div class="metric"><span class="metric-label">能力指数</span><span class="metric-value">{{.Index}}</span></div>
      <div class="metric"><span class="metric-label">置信区间</span><span class="metric-value">{{.IndexInterval}}</span></div>
      <div class="metric"><span class="metric-label">覆盖率</span><span class="metric-value">{{.Coverage}}</span></div>
    </div>
    {{if .Dimensions}}<h3 style="margin-top:18px">维度明细</h3>{{range .Dimensions}}<div class="bar-row"><span class="bar-label">{{.Dimension}} · 覆盖 {{.Coverage}}</span><progress max="100" value="{{.Value}}">{{.Score}}</progress><span class="bar-value">{{.Score}}<br><small>正式：{{.Formal}}</small></span></div>{{end}}{{end}}
  </section>
  {{end}}

  <section aria-labelledby="evidence-title">
    <h2 id="evidence-title">证据引用</h2>
    {{if .Evidence}}<div class="table-wrap"><table><thead><tr><th>ID</th><th>类型</th><th>SHA-256</th><th>已脱敏</th></tr></thead><tbody>{{range .Evidence}}<tr><td><code>{{.ID}}</code></td><td>{{.Kind}}</td><td><code>{{.SHA256}}</code></td><td>{{.Redacted}}</td></tr>{{end}}</tbody></table></div>{{else}}<p class="empty">没有可导出的证据引用。</p>{{end}}
  </section>

  <section aria-labelledby="version-title">
    <h2 id="version-title">版本与脱敏</h2>
    <div class="table-wrap"><table><thead><tr><th>组件</th><th>ID</th><th>语义版本</th><th>实现版本</th></tr></thead><tbody>{{range .Versions}}<tr><td>{{.Label}}</td><td><code>{{.ID}}</code></td><td>{{.Semantic}}</td><td>{{.Implementation}}</td></tr>{{end}}</tbody></table></div>
    <h3 style="margin-top:18px">默认排除内容</h3><ul class="note-list">{{range .RedactionExcluded}}<li><code>{{.}}</code></li>{{end}}</ul>
  </section>

  <section aria-labelledby="limitations-title">
    <h2 id="limitations-title">限制声明</h2>
    <ul class="note-list">{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>
    <p class="meta">本报告由冻结 DTO 直接渲染，不在浏览器中重新计算结论或分数。</p>
  </section>
</main>
</body>
</html>`

var _ AuditHTMLRenderer = (*SelfContainedAuditHTMLRenderer)(nil)
