package eval

import (
	"strings"
	"time"
)

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
	RunPartial   = "partial"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
	RunSkipped   = "skipped"

	Interval30m = "30m"
	Interval2h  = "2h"
	Interval1d  = "1d"

	ThinkingInherit = "inherit"
	ThinkingOff     = "off"
	ThinkingEnabled = "enabled"

	// 细粒度思考等级，按协议映射成 budget / reasoning_effort。
	// enabled 向后兼容等价于 medium；inherit 跟随题目 stimulus.thinking。
	ThinkingLow    = "low"
	ThinkingMedium = "medium"
	ThinkingHigh   = "high"
	ThinkingMax    = "max"

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
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Description 给人看的"这题在查什么"。内置题由 seed.go 提供，界面直接展示。
	Description            string      `json:"description,omitempty"`
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
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	ProbeIDs    []string `json:"probeIds"`
	Cheap       bool     `json:"cheap"`
	Builtin     bool     `json:"builtin"`
	CreatedAt   int64    `json:"createdAt"`
	UpdatedAt   int64    `json:"updatedAt"`
}

// Run 一次评测批次。
type Run struct {
	ID         string   `json:"id"`
	SuiteID    string   `json:"suiteId"`
	SuiteName  string   `json:"suiteName,omitempty"`
	Trigger    string   `json:"trigger"`
	Status     string   `json:"status"`
	ChannelIDs []string `json:"channelIds"`
	// Model 是所有渠道共用的模型覆盖；ChannelModels 按渠道逐个覆盖，优先于 Model。
	// 两者都空时各渠道用自己的 defaultModel。
	Model          string            `json:"model,omitempty"`
	ChannelModels  map[string]string `json:"channelModels,omitempty"`
	Thinking       string            `json:"thinking,omitempty"`
	EstimatedCalls int               `json:"estimatedCalls"`
	SkipReason     string            `json:"skipReason,omitempty"`
	Error          string            `json:"error,omitempty"`
	StartedAt      int64             `json:"startedAt"`
	FinishedAt     int64             `json:"finishedAt"`
	CreatedAt      int64             `json:"createdAt"`
	Results        []Result          `json:"results,omitempty"`
	// Tally 是批次的 verdict 聚合计数。GetRun 里有完整 Results 时由调用方填，
	// ListRuns 不返回 Results 但会填 Tally，供历史卡片画 mini 统计条而无需逐条拉结果。
	Tally *RunTally `json:"tally,omitempty"`
}

// RunTally 批次概览用的 verdict 分布计数。Total 等于已落库 result 数，
// 不含尚未跑的格子——历史列表只关心已出结论的分布，待跑格子不参与。
type RunTally struct {
	Pass         int `json:"pass"`
	Suspect      int `json:"suspect"`
	Fail         int `json:"fail"`
	Error        int `json:"error"`
	Insufficient int `json:"insufficient"`
	Inapplicable int `json:"inapplicable"`
	Total        int `json:"total"`
}

// ModelForChannel 返回指定渠道本次评测应使用的模型覆盖。
// 逐渠道覆盖（ChannelModels）优先于统一覆盖（Model）；都未指定则返回空，
// 调用方随后会用渠道自己的 defaultModel。
func (r Run) ModelForChannel(channelID string) string {
	if r.ChannelModels != nil {
		if model := strings.TrimSpace(r.ChannelModels[channelID]); model != "" {
			return model
		}
	}
	return strings.TrimSpace(r.Model)
}

// normalizeChannelModels 只保留当前批次选中的渠道，并对模型名做剥空白清洗。
// 防止前端提交了"已取消勾选渠道"的残留模型值。
func normalizeChannelModels(channelModels map[string]string, channelIDs []string) map[string]string {
	result := make(map[string]string, len(channelIDs))
	selected := make(map[string]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		selected[id] = struct{}{}
	}
	for id, model := range channelModels {
		if _, ok := selected[id]; !ok {
			continue
		}
		if model = strings.TrimSpace(model); model != "" {
			result[id] = model
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
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
	Enabled        bool              `json:"enabled"`
	SuiteID        string            `json:"suiteId"`
	Interval       string            `json:"interval"`
	ChannelIDs     []string          `json:"channelIds"`
	Model          string            `json:"model,omitempty"`
	ChannelModels  map[string]string `json:"channelModels,omitempty"`
	Thinking       string            `json:"thinking,omitempty"`
	LastRunAt      int64             `json:"lastRunAt"`
	NextRunAt      int64             `json:"nextRunAt"`
	LastSkipReason string            `json:"lastSkipReason,omitempty"`
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
	// ChannelModels 按渠道 UUID 指定模型，优先于 Model。前端的"逐渠道选模型"写这里。
	ChannelModels map[string]string `json:"channelModels,omitempty"`
	Thinking      string            `json:"thinking,omitempty"`
	// ProbeIDs 覆盖套件的题：非空时只跑这里列出的探针，空则跑套件全部。
	ProbeIDs []string `json:"probeIds,omitempty"`
	Trigger  string   `json:"trigger,omitempty"`
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
