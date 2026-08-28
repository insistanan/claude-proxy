package eval

import "time"

const (
	CategoryAuthenticity = "authenticity"
	CategoryIQ           = "iq"

	ExtractText     = "text"
	ExtractUsage    = "usage"
	ExtractProtocol = "protocol"
	ExtractSVG      = "svg"
	ExtractNumbers  = "numbers"

	JudgeExact        = "exact"
	JudgeRegex        = "regex"
	JudgeNumeric      = "numeric"
	JudgeProtocol     = "protocol"
	JudgeSVG          = "svg"
	JudgeDistribution = "distribution"
	JudgeRubric       = "rubric"
	JudgeObserve      = "observe"

	VerdictPass         = "pass"
	VerdictSuspect      = "suspect"
	VerdictFail         = "fail"
	VerdictError        = "error"
	VerdictInsufficient = "insufficient"
	VerdictInapplicable = "inapplicable"

	TriggerManual = "manual"
	TriggerWatch  = "watch"

	RunQueued    = "queued"
	RunRunning   = "running"
	RunDone      = "done"
	RunCancelled = "cancelled"
	RunSkipped   = "skipped"

	Interval30m = "30m"
	Interval2h  = "2h"
	Interval1d  = "1d"

	ThinkingInherit = "inherit"
	ThinkingOff     = "off"
	ThinkingEnabled = "enabled"

	ServiceClaude    = "claude"
	ServiceOpenAI    = "openai"
	ServiceGemini    = "gemini"
	ServiceResponses = "responses"

	KindMessages  = "messages"
	KindResponses = "responses"
	KindGemini    = "gemini"
	KindChat      = "chat"
	KindImages    = "images"

	AggregatePass    = "pass"
	AggregateSuspect = "suspect"
	AggregateFail    = "fail"
	AggregateNeutral = "neutral"
	AggregateStale   = "stale"

	DefaultDBPath = ".config/eval.db"
	StaleAfter    = 7 * 24 * time.Hour

	// FingerprintTwinThreshold 随机数直方图余弦相似度高于此值就在结果里标注"指纹相近"。
	FingerprintTwinThreshold = 0.95

	ValidateCanAddDirectly    = "can_add_directly"
	ValidateInvalidFields     = "invalid_fields"
	ValidateUnsupportedGrader = "unsupported_grader"
	ValidateNeedsNewGrader    = "needs_new_grader"
)

// Stimulus 描述发给上游的提示。判定器读不到这里以外的字段。
type Stimulus struct {
	Prompt         string  `json:"prompt"`
	System         string  `json:"system,omitempty"`
	MaxTokens      int     `json:"maxTokens,omitempty"`
	Temperature    float64 `json:"temperature"`
	Thinking       string  `json:"thinking,omitempty"`
	ThinkingBudget int     `json:"thinkingBudget,omitempty"`
	ForceJSON      bool    `json:"forceJson,omitempty"`
}

// ExtractSpec 封闭的抽取方式。Kind 必须是已知枚举。
type ExtractSpec struct {
	Kind string `json:"kind"`
}

// JudgeSpec 封闭的判定方式。未知 Kind 在 validate 被拒绝。
type JudgeSpec struct {
	Kind string `json:"kind"`

	Expected        string  `json:"expected,omitempty"`
	ExpectedNumber  float64 `json:"expectedNumber,omitempty"`
	Pattern         string  `json:"pattern,omitempty"`
	CaseInsensitive bool    `json:"caseInsensitive,omitempty"`

	ExpectModelEcho    bool     `json:"expectModelEcho,omitempty"`
	ExpectContentTypes []string `json:"expectContentTypes,omitempty"`
	ExpectUsage        bool     `json:"expectUsage,omitempty"`
	ExpectThinking     string   `json:"expectThinking,omitempty"`  // optional | required | forbidden | observe
	ExpectSignature    string   `json:"expectSignature,omitempty"` // optional | present | absent | observe

	MinSamples         int     `json:"minSamples,omitempty"`
	FailIfIdentical    bool    `json:"failIfIdentical,omitempty"`
	SuspectUniqueRatio float64 `json:"suspectUniqueRatio,omitempty"`
	CompareHistogram   bool    `json:"compareHistogram,omitempty"`

	AnalysisPrompt string `json:"analysisPrompt,omitempty"`

	// ExpectThinkingUsage 要求观测到 > 0 的思考 token。
	// 只判"有没有"，不判占比：output 是否已含思考各家口径不一，占比阈值必然大量误报。
	ExpectThinkingUsage bool `json:"expectThinkingUsage,omitempty"`
}

// Probe 一条可入库的检测题。
type Probe struct {
	ID                     string      `json:"id"`
	Slug                   string      `json:"slug"`
	Name                   string      `json:"name"`
	Category               string      `json:"category"`
	Stimulus               Stimulus    `json:"stimulus"`
	Extract                ExtractSpec `json:"extract"`
	Judge                  JudgeSpec   `json:"judge"`
	SampleCount            int         `json:"sampleCount"`
	ApplicableServiceTypes []string    `json:"applicableServiceTypes"`
	Cheap                  bool        `json:"cheap"`
	// Builtin 标记这条题由 seed.go 维护：每次启动按代码同步，不接受改与删。
	// 想定制就复制成自建题，免得引擎升级后题目与判定器对不上。
	Builtin   bool  `json:"builtin"`
	CreatedAt int64 `json:"createdAt"`
	UpdatedAt int64 `json:"updatedAt"`
}

// Suite 一组探针。值班套件必须 cheap。内置套件同样由 seed.go 维护。
type Suite struct {
	ID        string   `json:"id"`
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	ProbeIDs  []string `json:"probeIds"`
	Cheap     bool     `json:"cheap"`
	Builtin   bool     `json:"builtin"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
}

// Run 一次评测批次。
type Run struct {
	ID             string   `json:"id"`
	SuiteID        string   `json:"suiteId"`
	SuiteName      string   `json:"suiteName,omitempty"`
	Trigger        string   `json:"trigger"`
	Status         string   `json:"status"`
	ChannelIDs     []string `json:"channelIds"`
	Model          string   `json:"model,omitempty"`
	Thinking       string   `json:"thinking,omitempty"`
	EstimatedCalls int      `json:"estimatedCalls"`
	SkipReason     string   `json:"skipReason,omitempty"`
	Error          string   `json:"error,omitempty"`
	StartedAt      int64    `json:"startedAt"`
	FinishedAt     int64    `json:"finishedAt"`
	CreatedAt      int64    `json:"createdAt"`
	Results        []Result `json:"results,omitempty"`
}

// Result 单个渠道 × 探针的结论。
type Result struct {
	ID        string                 `json:"id"`
	RunID     string                 `json:"runId"`
	ChannelID string                 `json:"channelId"`
	ProbeID   string                 `json:"probeId"`
	ProbeName string                 `json:"probeName,omitempty"`
	Verdict   string                 `json:"verdict"`
	Excerpt   string                 `json:"excerpt,omitempty"`
	Detail    map[string]interface{} `json:"detail,omitempty"`
	LatencyMS int64                  `json:"latencyMs"`
	CreatedAt int64                  `json:"createdAt"`
}

// WatchConfig 全局唯一的值班配置。
type WatchConfig struct {
	Enabled        bool     `json:"enabled"`
	SuiteID        string   `json:"suiteId"`
	Interval       string   `json:"interval"`
	ChannelIDs     []string `json:"channelIds"`
	Model          string   `json:"model,omitempty"`
	Thinking       string   `json:"thinking,omitempty"`
	LastRunAt      int64    `json:"lastRunAt"`
	NextRunAt      int64    `json:"nextRunAt"`
	LastSkipReason string   `json:"lastSkipReason,omitempty"`
}

// ChannelLatest 渠道行芯片用的聚合结论。
type ChannelLatest struct {
	ChannelID  string `json:"channelId"`
	Aggregate  string `json:"aggregate"`
	Label      string `json:"label"`
	SuiteName  string `json:"suiteName,omitempty"`
	FinishedAt int64  `json:"finishedAt"`
	Watching   bool   `json:"watching"`
	RunID      string `json:"runId,omitempty"`
}

// StartRunRequest 发起一次评测。
type StartRunRequest struct {
	SuiteID    string   `json:"suiteId"`
	ChannelIDs []string `json:"channelIds"`
	Model      string   `json:"model,omitempty"`
	Thinking   string   `json:"thinking,omitempty"`
	Trigger    string   `json:"trigger,omitempty"`
}

// ValidateReport 配套 skill / 管理题目用的分流结果。
type ValidateReport struct {
	OK          bool     `json:"ok"`
	Mode        string   `json:"mode"` // can_add_directly | invalid_fields | unsupported_grader | needs_new_grader
	Errors      []string `json:"errors,omitempty"`
	ExtractKind string   `json:"extractKind,omitempty"`
	JudgeKind   string   `json:"judgeKind,omitempty"`
	Cheap       bool     `json:"cheap"`
}
