// Package common 提供 handlers 模块的公共功能
package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/urlhealth"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/BenedictKing/claude-proxy/internal/visionlayer"
	"github.com/gin-gonic/gin"
)

// isClientSideError 判断错误是否由客户端明确取消（不应计入渠道失败）
// 仅识别 context.Canceled，broken pipe/connection reset 视为连接故障需要 failover
func isClientSideError(err error) bool {
	if err == nil {
		return false
	}
	// 只有 context.Canceled 才是明确的客户端取消意图
	return errors.Is(err, context.Canceled)
}

// NextAPIKeyFunc 返回下一个可用 API key（按 failover 策略）
type NextAPIKeyFunc func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error)

// BuildRequestFunc 构建上游请求（upstreamCopy.BaseURL 已写入当前尝试的 BaseURL）
type BuildRequestFunc func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error)

// DeprioritizeKeyFunc 对 quota 相关失败的 key 做降级（实现可选择是否记录日志）
type DeprioritizeKeyFunc func(apiKey string)

// HandleSuccessFunc 处理成功响应（负责写回客户端），并返回 usage（可为 nil）
// 注意：实现方需要自行关闭 resp.Body（与现有 handlers 保持一致）。
type HandleSuccessFunc func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error)

// RetrySameCandidateError 表示响应已成功到达代理，但模型暂时无法产出内容。
// 这类错误属于模型容量状态，不应污染 API Key、BaseURL 或渠道的失败指标。
type RetrySameCandidateError struct {
	err error
}

func (e *RetrySameCandidateError) Error() string {
	if e == nil || e.err == nil {
		return "upstream model is temporarily unavailable"
	}
	return e.err.Error()
}

func (e *RetrySameCandidateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func NewRetrySameCandidateError(err error) error {
	return &RetrySameCandidateError{err: err}
}

func isRetrySameCandidateError(err error) bool {
	var target *RetrySameCandidateError
	return errors.As(err, &target)
}

// IsUpstreamModelCapacityError 仅匹配已知的明确容量错误，避免用裸 capacity
// 误判业务正文或其他含义不同的错误。
func IsUpstreamModelCapacityError(body []byte) bool {
	message := strings.ToLower(string(body))
	return strings.Contains(message, "selected model is at capacity") ||
		strings.Contains(message, "model is at capacity")
}

type AttemptLogContext struct {
	ChannelIndex                      int
	Model                             string
	ConversationID                    string
	LogStore                          *metrics.ChannelLogStore
	RequestLogStore                   *metrics.RequestLogStore
	AllowContentPolicyChannelFailover bool
}

var attemptLogCounter uint64
var profileRequestCounter uint64

const requestLogFirstTokenAtKey = "__request_log_first_token_at"

// nextProfileRequestID 生成唯一请求 ID 用于性能画像追踪
func nextProfileRequestID() uint64 {
	return atomic.AddUint64(&profileRequestCounter, 1)
}

func ResetRequestLogFirstToken(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(requestLogFirstTokenAtKey, time.Time{})
}

func MarkRequestLogFirstToken(c *gin.Context) {
	if c == nil {
		return
	}
	if value, ok := c.Get(requestLogFirstTokenAtKey); ok {
		if markedAt, ok := value.(time.Time); ok && !markedAt.IsZero() {
			return
		}
	}
	c.Set(requestLogFirstTokenAtKey, time.Now())
}

// UpstreamAttempt 聚合一次上游故障转移所需的依赖、目标和协议回调。
// Context、回调和观测字段属于单次请求生命周期，不应被长期保存。
type UpstreamAttempt struct {
	Context            *gin.Context
	EnvConfig          *config.EnvConfig
	ConfigManager      *config.ConfigManager
	ChannelScheduler   *scheduler.ChannelScheduler
	Kind               scheduler.ChannelKind
	APIType            string
	MetricsManager     *metrics.MetricsManager
	Upstream           *config.UpstreamConfig
	RequestedModel     string
	AllowModelFailover bool
	URLResults         []urlhealth.URLLatencyResult
	RequestBody        []byte
	IsStream           bool
	NextAPIKey         NextAPIKeyFunc
	BuildRequest       BuildRequestFunc
	DeprioritizeKey    DeprioritizeKeyFunc
	MarkURLFailure     func(url string)
	MarkURLSuccess     func(url string)
	HandleSuccess      HandleSuccessFunc
	LogContext         AttemptLogContext
}

// UpstreamAttemptResult 是一次上游尝试的命名结果，避免调用方依赖位置返回值。
type UpstreamAttemptResult struct {
	Handled           bool
	SuccessKey        string
	SuccessBaseURLIdx int
	FailoverError     *FailoverError
	Usage             *types.Usage
	LastError         error
}

// candidateRetryState 收拢单个 BaseURL 生命周期内的 Key 失败和同候选重试状态。
// 每进入一个 BaseURL 都必须创建新的实例，避免候选状态跨 BaseURL 泄漏。
type candidateRetryState struct {
	failedKeys              map[string]bool
	sameCandidateRetries    map[string]int
	maxSameCandidateRetries int
	maxRetries              int
}

type upstreamFailureAction uint8

const (
	upstreamFailureRespond upstreamFailureAction = iota
	upstreamFailureTryNextKey
	upstreamFailureTryNextChannel
)

type upstreamFailureDecision struct {
	action       upstreamFailureAction
	quotaRelated bool
	errorType    string
}

type responseProcessingAction uint8

const (
	responseProcessingClientCancelled responseProcessingAction = iota
	responseProcessingContentSafety
	responseProcessingContentSafetyHook
	responseProcessingRetryCandidate
	responseProcessingChannelFailure
)

type responseProcessingDecision struct {
	action    responseProcessingAction
	safetyErr *ContentSafetyError
	hookErr   *ContentSafetyHookError
}

func newCandidateRetryState(apiKeyCount int) candidateRetryState {
	const maxSameCandidateRetries = 1

	return candidateRetryState{
		failedKeys:              make(map[string]bool),
		sameCandidateRetries:    make(map[string]int),
		maxSameCandidateRetries: maxSameCandidateRetries,
		maxRetries:              apiKeyCount * (maxSameCandidateRetries + 1),
	}
}

func (s *candidateRetryState) markKeyFailed(apiKey string) {
	if s == nil {
		return
	}
	s.failedKeys[apiKey] = true
}

func (s *candidateRetryState) canRetryCandidate(candidate string, ctx context.Context) bool {
	if s == nil || s.sameCandidateRetries[candidate] >= s.maxSameCandidateRetries {
		return false
	}
	return ctx != nil && ctx.Err() == nil
}

func (s *candidateRetryState) recordCandidateRetry(candidate string) int {
	if s == nil {
		return 0
	}
	s.sameCandidateRetries[candidate]++
	return s.sameCandidateRetries[candidate]
}

func (a UpstreamAttempt) decideUpstreamFailure(statusCode int, body []byte) upstreamFailureDecision {
	if a.LogContext.AllowContentPolicyChannelFailover && shouldFailoverToNextChannel(
		body,
		a.ChannelScheduler.GetActiveChannelCountForModel(a.Kind, a.LogContext.Model),
	) {
		return upstreamFailureDecision{
			action:    upstreamFailureTryNextChannel,
			errorType: "content_policy",
		}
	}

	shouldFailover, quotaRelated := ShouldRetryWithNextKey(
		statusCode,
		body,
		a.ConfigManager.GetFuzzyModeEnabled(),
		a.APIType,
	)
	if shouldFailover {
		return upstreamFailureDecision{
			action:       upstreamFailureTryNextKey,
			quotaRelated: quotaRelated,
			errorType:    classifyUpstreamError(statusCode, quotaRelated),
		}
	}

	return upstreamFailureDecision{
		action:    upstreamFailureRespond,
		errorType: classifyUpstreamError(statusCode, false),
	}
}

func classifyResponseProcessingError(err error, responseWritten bool) responseProcessingDecision {
	if isClientSideError(err) {
		return responseProcessingDecision{action: responseProcessingClientCancelled}
	}
	if safetyErr := contentSafetyError(err); safetyErr != nil {
		return responseProcessingDecision{
			action:    responseProcessingContentSafety,
			safetyErr: safetyErr,
		}
	}
	if hookErr := contentSafetyHookError(err); hookErr != nil {
		return responseProcessingDecision{
			action:  responseProcessingContentSafetyHook,
			hookErr: hookErr,
		}
	}
	if isRetrySameCandidateError(err) && !responseWritten {
		return responseProcessingDecision{action: responseProcessingRetryCandidate}
	}
	return responseProcessingDecision{action: responseProcessingChannelFailure}
}

func (r UpstreamAttemptResult) legacyValues() (bool, string, int, *FailoverError, *types.Usage, error) {
	return r.Handled, r.SuccessKey, r.SuccessBaseURLIdx, r.FailoverError, r.Usage, r.LastError
}

func newUpstreamAttemptResult(
	handled bool,
	successKey string,
	successBaseURLIdx int,
	failoverErr *FailoverError,
	usage *types.Usage,
	lastError error,
) UpstreamAttemptResult {
	return UpstreamAttemptResult{
		Handled:           handled,
		SuccessKey:        successKey,
		SuccessBaseURLIdx: successBaseURLIdx,
		FailoverError:     failoverErr,
		Usage:             usage,
		LastError:         lastError,
	}
}

// TryWithModelMappingFailover 按模型映射执行上游故障转移。
func (a UpstreamAttempt) TryWithModelMappingFailover() UpstreamAttemptResult {
	return a.tryWithModelMappingFailover()
}

// TryWithAllKeys 按当前上游的所有 Key 和 BaseURL 执行故障转移。
func (a UpstreamAttempt) TryWithAllKeys() UpstreamAttemptResult {
	return a.tryWithAllKeys()
}

// TryUpstreamWithModelMappingFailover 保留长参数入口，供现有外部调用方兼容使用。
func TryUpstreamWithModelMappingFailover(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	apiType string,
	metricsManager *metrics.MetricsManager,
	upstream *config.UpstreamConfig,
	requestedModel string,
	allowModelFailover bool,
	urlResults []urlhealth.URLLatencyResult,
	requestBody []byte,
	isStream bool,
	nextAPIKey NextAPIKeyFunc,
	buildRequest BuildRequestFunc,
	deprioritizeKey DeprioritizeKeyFunc,
	markURLFailure func(url string),
	markURLSuccess func(url string),
	handleSuccess HandleSuccessFunc,
	logCtx AttemptLogContext,
) (handled bool, successKey string, successBaseURLIdx int, failoverErr *FailoverError, usage *types.Usage, lastError error) {
	return UpstreamAttempt{
		Context:            c,
		EnvConfig:          envCfg,
		ConfigManager:      cfgManager,
		ChannelScheduler:   channelScheduler,
		Kind:               kind,
		APIType:            apiType,
		MetricsManager:     metricsManager,
		Upstream:           upstream,
		RequestedModel:     requestedModel,
		AllowModelFailover: allowModelFailover,
		URLResults:         urlResults,
		RequestBody:        requestBody,
		IsStream:           isStream,
		NextAPIKey:         nextAPIKey,
		BuildRequest:       buildRequest,
		DeprioritizeKey:    deprioritizeKey,
		MarkURLFailure:     markURLFailure,
		MarkURLSuccess:     markURLSuccess,
		HandleSuccess:      handleSuccess,
		LogContext:         logCtx,
	}.TryWithModelMappingFailover().legacyValues()
}

// tryWithModelMappingFailover 在模型映射级别进行 failover。
// 非 Fuzzy 模式下仅尝试首个映射模型，避免跨模型故障转移。
func (a UpstreamAttempt) tryWithModelMappingFailover() UpstreamAttemptResult {
	// 获取该模型的映射列表
	targetModels := config.ResolveUpstreamModelList(a.RequestedModel, a.Upstream)

	if len(targetModels) == 0 {
		// 没有映射，直接使用原始模型
		targetModels = []string{a.RequestedModel}
	}
	if !a.AllowModelFailover && len(targetModels) > 1 {
		targetModels = targetModels[:1]
	} else {
		targetModels = rankTargetModelsForChannel(a.ChannelScheduler, a.Upstream, targetModels, a.URLResults, a.LogContext.ChannelIndex)
	}

	// 如果只有一个目标模型，直接调用原有逻辑
	if len(targetModels) == 1 {
		return a.tryWithAllKeys()
	}

	// 多个目标模型：依次尝试
	log.Printf("[%s-ModelMapping] 模型 %s 映射到 %d 个备选: %v", a.APIType, a.RequestedModel, len(targetModels), targetModels)

	var lastFailoverError *FailoverError
	var lastErr error

	for modelIdx, targetModel := range targetModels {
		// 检查客户端是否已取消
		select {
		case <-a.Context.Request.Context().Done():
			log.Printf("[%s-Cancel] 客户端已取消，停止模型 failover", a.APIType)
			return newUpstreamAttemptResult(true, "", 0, nil, nil, context.Canceled)
		default:
		}

		log.Printf("[%s-ModelMapping] 尝试备选模型 %d/%d: %s -> %s",
			a.APIType, modelIdx+1, len(targetModels), a.RequestedModel, targetModel)

		// 创建上游副本，临时覆盖模型映射为当前尝试的单一模型
		upstreamCopy := a.Upstream.Clone()
		upstreamCopy.ModelMapping = map[string][]string{
			a.RequestedModel: {targetModel},
		}

		modelAttempt := a
		modelAttempt.Upstream = upstreamCopy
		result := modelAttempt.tryWithAllKeys()

		if result.Handled {
			if result.SuccessKey != "" {
				// 成功
				log.Printf("[%s-ModelMapping] 模型 %s (备选 %d/%d) 请求成功",
					a.APIType, targetModel, modelIdx+1, len(targetModels))
				return result
			}
			// handled=true 但 successKey 为空：非 failover 错误（如客户端取消、参数错误等）
			return result
		}

		// 未处理（failover 错误），保存错误信息并尝试下一个模型
		if result.FailoverError != nil {
			lastFailoverError = result.FailoverError
		}
		if result.LastError != nil {
			lastErr = result.LastError
		}

		log.Printf("[%s-ModelMapping] 模型 %s (备选 %d/%d) 失败，尝试下一个备选模型",
			a.APIType, targetModel, modelIdx+1, len(targetModels))
	}

	// 所有模型都失败
	log.Printf("[%s-ModelMapping] 所有 %d 个备选模型都失败", a.APIType, len(targetModels))
	return newUpstreamAttemptResult(false, "", 0, lastFailoverError, nil, lastErr)
}

func rankTargetModelsForChannel(
	channelScheduler *scheduler.ChannelScheduler,
	upstream *config.UpstreamConfig,
	targetModels []string,
	urlResults []urlhealth.URLLatencyResult,
	channelIndex int,
) []string {
	if len(targetModels) <= 1 || channelScheduler == nil || upstream == nil {
		return targetModels
	}
	pm := channelScheduler.GetProfileManager()
	if pm == nil {
		return targetModels
	}

	baseURLs := make([]string, 0, len(urlResults))
	for _, result := range urlResults {
		if strings.TrimSpace(result.URL) != "" {
			baseURLs = append(baseURLs, result.URL)
		}
	}
	if len(baseURLs) == 0 {
		baseURLs = upstream.GetAllBaseURLs()
	}

	type rankedModel struct {
		model          string
		originalIndex  int
		healthScore    float64
		activeRequests int64
	}

	ranked := make([]rankedModel, 0, len(targetModels))
	for i, model := range targetModels {
		snapshot := pm.GetAggregateProfileSnapshot(baseURLs, upstream.APIKeys, model, channelIndex)
		ranked = append(ranked, rankedModel{
			model:          model,
			originalIndex:  i,
			healthScore:    snapshot.HealthScore,
			activeRequests: snapshot.ActiveRequests,
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].healthScore == ranked[j].healthScore {
			if ranked[i].activeRequests == ranked[j].activeRequests {
				return ranked[i].originalIndex < ranked[j].originalIndex
			}
			return ranked[i].activeRequests < ranked[j].activeRequests
		}
		return ranked[i].healthScore > ranked[j].healthScore
	})

	ordered := make([]string, 0, len(ranked))
	for _, item := range ranked {
		ordered = append(ordered, item.model)
	}
	return ordered
}

func preferConversationBaseURL(
	channelScheduler *scheduler.ChannelScheduler,
	envCfg *config.EnvConfig,
	apiType string,
	logCtx AttemptLogContext,
	urlResults []urlhealth.URLLatencyResult,
) []urlhealth.URLLatencyResult {
	if channelScheduler == nil || logCtx.ConversationID == "" {
		return urlResults
	}
	preferredBaseURL, ok := channelScheduler.GetPreferredBaseURL(logCtx.ConversationID)
	if !ok {
		return urlResults
	}
	if envCfg != nil && envCfg.ShouldLog("info") {
		log.Printf("[%s-BaseURL-Affinity] conversation sticky prefer %s", apiType, preferredBaseURL)
	}
	return scheduler.PreferBaseURLInResults(urlResults, preferredBaseURL)
}

func (a UpstreamAttempt) buildPreparedAttemptRequest(
	upstreamCopy *config.UpstreamConfig,
	apiKey string,
) (*http.Request, string, error) {
	req, err := a.BuildRequest(a.Context, upstreamCopy, apiKey)
	if err != nil {
		return nil, "build_request", err
	}
	prepareStage, err := prepareRequestForUpstream(
		a.Context,
		a.EnvConfig,
		a.ConfigManager,
		a.ChannelScheduler,
		a.Kind,
		upstreamCopy,
		a.LogContext.Model,
		a.LogContext.ConversationID,
		req,
		a.APIType,
	)
	if err != nil {
		return req, prepareStage, err
	}
	return req, "", nil
}

type compatibilityRetryResult struct {
	Response         *http.Response
	Body             []byte
	Succeeded        bool
	PreparationStage string
	Err              error
}

type attemptObservation struct {
	ResolvedModel    string
	ProfileRequestID uint64
	RequestID        uint64
}

// attemptLifecycle 绑定单次候选尝试的观测身份和终结责任。
// 该对象只存在于当前请求的单次尝试范围内，不跨请求保存。
type attemptLifecycle struct {
	attempt     UpstreamAttempt
	baseURL     string
	apiKey      string
	observation attemptObservation
}

type upstreamCapabilityState struct {
	disablePromptCacheKey   bool
	requireReasoningContent bool
}

// upstreamFailoverState 保存一次故障转移请求跨 BaseURL 的结果状态。
// Key 失败和同候选重试状态仍由 candidateRetryState 按 BaseURL 独立维护。
type upstreamFailoverState struct {
	lastError              error
	lastFailoverError      *FailoverError
	deprioritizeCandidates map[string]bool
}

func newUpstreamFailoverState() upstreamFailoverState {
	return upstreamFailoverState{
		deprioritizeCandidates: make(map[string]bool),
	}
}

func newUpstreamCapabilityState(upstream *config.UpstreamConfig) upstreamCapabilityState {
	if upstream == nil {
		return upstreamCapabilityState{}
	}
	return upstreamCapabilityState{
		disablePromptCacheKey:   upstream.DisablePromptCacheKey,
		requireReasoningContent: upstream.RequireReasoningContent,
	}
}

func (a UpstreamAttempt) prepareCandidateUpstream(
	baseURL string,
	capabilities upstreamCapabilityState,
) *config.UpstreamConfig {
	upstreamCopy := a.Upstream.Clone()
	upstreamCopy.BaseURL = baseURL
	upstreamCopy.DisablePromptCacheKey = capabilities.disablePromptCacheKey
	upstreamCopy.RequireReasoningContent = capabilities.requireReasoningContent
	recordConversationAttempt(a.ChannelScheduler, a.Kind, a.Upstream, a.LogContext, a.IsStream)
	return upstreamCopy
}

func (a UpstreamAttempt) startAttemptObservation(baseURL, apiKey string) attemptObservation {
	a.ChannelScheduler.RecordRequestStart(baseURL, apiKey, a.LogContext.ChannelIndex, a.Kind)
	resolvedModel := config.ResolveUpstreamModel(a.LogContext.Model, a.Upstream)
	profileRequestID := nextProfileRequestID()
	if pm := a.ChannelScheduler.GetProfileManager(); pm != nil {
		pm.StartRequest(baseURL, a.Upstream.APIKeys, resolvedModel, a.LogContext.ChannelIndex, profileRequestID)
	}
	requestID := a.MetricsManager.RecordRequestConnected(baseURL, apiKey, a.LogContext.ChannelIndex, a.LogContext.Model)
	return attemptObservation{
		ResolvedModel:    resolvedModel,
		ProfileRequestID: profileRequestID,
		RequestID:        requestID,
	}
}

func (a UpstreamAttempt) startAttemptLifecycle(baseURL, apiKey string) attemptLifecycle {
	return attemptLifecycle{
		attempt:     a,
		baseURL:     baseURL,
		apiKey:      apiKey,
		observation: a.startAttemptObservation(baseURL, apiKey),
	}
}

func (a UpstreamAttempt) retrySameCandidateRequest(
	upstreamCopy *config.UpstreamConfig,
	apiKey string,
	proxyURL string,
) compatibilityRetryResult {
	req, preparationStage, err := a.buildPreparedAttemptRequest(upstreamCopy, apiKey)
	if err != nil {
		if preparationStage != "build_request" && req != nil && req.Body != nil {
			_ = req.Body.Close()
		}
		return compatibilityRetryResult{
			PreparationStage: preparationStage,
			Err:              err,
		}
	}

	resp, err := SendRequest(req, upstreamCopy, a.EnvConfig, a.IsStream, a.APIType, proxyURL)
	if err != nil {
		return compatibilityRetryResult{PreparationStage: "send_request", Err: err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return compatibilityRetryResult{Response: resp, Succeeded: true}
	}

	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		// 读取中断：响应不完整，按网络故障处理，不返回截断 body。
		return compatibilityRetryResult{PreparationStage: "read_body", Err: fmt.Errorf("读取重试响应体失败: %w", readErr)}
	}
	body = utils.DecompressGzipIfNeeded(resp, body)
	return compatibilityRetryResult{Response: resp, Body: body}
}

func (l attemptLifecycle) finalizeNeutral() {
	l.attempt.MetricsManager.RecordRequestFinalizeNeutral(l.baseURL, l.apiKey, l.attempt.LogContext.ChannelIndex, l.observation.RequestID)
	l.attempt.ChannelScheduler.RecordRequestEnd(l.baseURL, l.apiKey, l.attempt.LogContext.ChannelIndex, l.attempt.Kind)
	if pm := l.attempt.ChannelScheduler.GetProfileManager(); pm != nil {
		pm.EndRequestNeutral(l.baseURL, l.attempt.Upstream.APIKeys, l.observation.ResolvedModel, l.attempt.LogContext.ChannelIndex, l.observation.ProfileRequestID)
	}
}

func (l attemptLifecycle) finalizeClientCancelled() {
	l.attempt.MetricsManager.RecordRequestFinalizeClientCancel(l.baseURL, l.apiKey, l.attempt.LogContext.ChannelIndex, l.observation.RequestID)
	l.attempt.ChannelScheduler.RecordRequestEnd(l.baseURL, l.apiKey, l.attempt.LogContext.ChannelIndex, l.attempt.Kind)
	if pm := l.attempt.ChannelScheduler.GetProfileManager(); pm != nil {
		pm.EndRequestNeutral(l.baseURL, l.attempt.Upstream.APIKeys, l.observation.ResolvedModel, l.attempt.LogContext.ChannelIndex, l.observation.ProfileRequestID)
	}
}

func (l attemptLifecycle) finalizeFailed() {
	l.attempt.MetricsManager.RecordRequestFinalizeFailure(l.baseURL, l.apiKey, l.attempt.LogContext.ChannelIndex, l.observation.RequestID)
	l.attempt.ChannelScheduler.RecordRequestEnd(l.baseURL, l.apiKey, l.attempt.LogContext.ChannelIndex, l.attempt.Kind)
	if pm := l.attempt.ChannelScheduler.GetProfileManager(); pm != nil {
		pm.EndRequest(l.baseURL, l.attempt.Upstream.APIKeys, l.observation.ResolvedModel, l.attempt.LogContext.ChannelIndex, l.observation.ProfileRequestID, false, 0)
	}
}

func (l attemptLifecycle) finalizeSuccessful(usage *types.Usage) {
	l.attempt.MetricsManager.RecordRequestFinalizeSuccess(l.baseURL, l.apiKey, l.attempt.LogContext.ChannelIndex, l.observation.RequestID, usage)
	l.attempt.ChannelScheduler.RecordRequestEnd(l.baseURL, l.apiKey, l.attempt.LogContext.ChannelIndex, l.attempt.Kind)
	if pm := l.attempt.ChannelScheduler.GetProfileManager(); pm != nil {
		var outputTokens int64
		if usage != nil {
			outputTokens = int64(usage.OutputTokens)
		}
		pm.EndRequest(l.baseURL, l.attempt.Upstream.APIKeys, l.observation.ResolvedModel, l.attempt.LogContext.ChannelIndex, l.observation.ProfileRequestID, true, outputTokens)
	}
}

func prepareRequestForUpstream(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	upstream *config.UpstreamConfig,
	model string,
	conversationID string,
	req *http.Request,
	apiType string,
) (string, error) {
	payloadProtocol := payloadProtocolForServiceType(upstream.ServiceType, apiType)
	if err := runAttachedPreRequestHooks(c.Request.Context(), c, req, upstream.Name, payloadProtocol); err != nil {
		return "content_safety", err
	}
	if err := visionlayer.PrepareRequest(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		kind,
		upstream,
		model,
		conversationID,
		req,
	); err != nil {
		return "vision_layer", err
	}
	if err := runAttachedPreRequestHooks(c.Request.Context(), c, req, upstream.Name, payloadProtocol); err != nil {
		return "content_safety", err
	}
	return "", nil
}

func handleContentSafetyPreparationError(c *gin.Context, apiType string, err error) int {
	if safetyErr := contentSafetyError(err); safetyErr != nil {
		if writeErr := WriteAttachedContentSafetyError(c, safetyErr); writeErr != nil {
			log.Printf("[%s-ContentSafety] 协议错误写出失败: %v", apiType, writeErr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": writeErr.Error(), "code": "CONTENT_SAFETY_RESPONSE_ERROR"})
			return http.StatusInternalServerError
		}
		return http.StatusForbidden
	}
	c.JSON(http.StatusInternalServerError, gin.H{
		"error": err.Error(),
		"code":  "CONTENT_SAFETY_HOOK_ERROR",
	})
	return http.StatusInternalServerError
}

// 返回:
//   - handled: 是否已向客户端写回响应（成功或非 failover 错误）
//   - successKey: 成功的 key（仅 handled=true 且成功时有值）
//   - successBaseURLIdx: 成功 BaseURL 的原始索引（用于指标记录）
//   - failoverErr: 最后一次可故障转移的上游错误（用于多渠道聚合错误）
//   - usage: usage 统计（可能为 nil）
func TryUpstreamWithAllKeys(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	kind scheduler.ChannelKind,
	apiType string,
	metricsManager *metrics.MetricsManager,
	upstream *config.UpstreamConfig,
	urlResults []urlhealth.URLLatencyResult,
	requestBody []byte,
	isStream bool,
	nextAPIKey NextAPIKeyFunc,
	buildRequest BuildRequestFunc,
	deprioritizeKey DeprioritizeKeyFunc,
	markURLFailure func(url string),
	markURLSuccess func(url string),
	handleSuccess HandleSuccessFunc,
	logCtx AttemptLogContext,
) (handled bool, successKey string, successBaseURLIdx int, failoverErr *FailoverError, usage *types.Usage, lastError error) {
	return UpstreamAttempt{
		Context:          c,
		EnvConfig:        envCfg,
		ConfigManager:    cfgManager,
		ChannelScheduler: channelScheduler,
		Kind:             kind,
		APIType:          apiType,
		MetricsManager:   metricsManager,
		Upstream:         upstream,
		URLResults:       urlResults,
		RequestBody:      requestBody,
		IsStream:         isStream,
		NextAPIKey:       nextAPIKey,
		BuildRequest:     buildRequest,
		DeprioritizeKey:  deprioritizeKey,
		MarkURLFailure:   markURLFailure,
		MarkURLSuccess:   markURLSuccess,
		HandleSuccess:    handleSuccess,
		LogContext:       logCtx,
	}.TryWithAllKeys().legacyValues()
}

type upstreamAttemptPreflight struct {
	proxyURL string
}

func (a UpstreamAttempt) prepareAllKeys() (upstreamAttemptPreflight, bool, error) {
	if a.Upstream == nil || len(a.Upstream.APIKeys) == 0 {
		return upstreamAttemptPreflight{}, false, nil
	}
	if a.MetricsManager == nil {
		return upstreamAttemptPreflight{}, false, nil
	}
	if a.NextAPIKey == nil || a.BuildRequest == nil || a.HandleSuccess == nil {
		return upstreamAttemptPreflight{}, false, nil
	}
	if len(a.URLResults) == 0 {
		return upstreamAttemptPreflight{}, false, nil
	}

	proxyURL, err := a.ConfigManager.ResolveUpstreamProxyURL(a.Upstream)
	if err != nil {
		return upstreamAttemptPreflight{}, false, err
	}
	return upstreamAttemptPreflight{proxyURL: proxyURL}, true, nil
}

func (a UpstreamAttempt) tryWithAllKeys() UpstreamAttemptResult {
	c := a.Context
	envCfg := a.EnvConfig
	cfgManager := a.ConfigManager
	channelScheduler := a.ChannelScheduler
	kind := a.Kind
	apiType := a.APIType
	metricsManager := a.MetricsManager
	upstream := a.Upstream
	urlResults := a.URLResults
	requestBody := a.RequestBody
	isStream := a.IsStream
	logCtx := a.LogContext

	var usage *types.Usage

	preflight, valid, err := a.prepareAllKeys()
	if err != nil {
		return newUpstreamAttemptResult(false, "", 0, nil, nil, err)
	}
	if !valid {
		return newUpstreamAttemptResult(false, "", 0, nil, nil, nil)
	}
	proxyURL := preflight.proxyURL

	urlResults = preferConversationBaseURL(channelScheduler, envCfg, apiType, logCtx, urlResults)

	failoverState := newUpstreamFailoverState()
	requestLogID := nextAttemptLogID("req")
	capabilities := newUpstreamCapabilityState(upstream)

	// 强制探测模式：基于本次优先尝试的 BaseURL 判断（避免 BaseURL/BaseURLs 不一致导致误判）
	forceProbeMode := AreAllKeysSuspended(metricsManager, urlResults[0].URL, upstream.APIKeys, logCtx.ChannelIndex)
	if forceProbeMode {
		log.Printf("[%s-ForceProbe] 渠道 %s 所有 Key 都被熔断，启用强制探测模式", apiType, upstream.Name)
	}

	for urlIdx, urlResult := range urlResults {
		currentBaseURL := urlResult.URL
		originalIdx := urlResult.OriginalIdx // 原始索引用于指标记录
		retryState := newCandidateRetryState(len(upstream.APIKeys))

		for attempt := 0; attempt < retryState.maxRetries; attempt++ {
			RestoreRequestBody(c, requestBody)
			ResetRequestLogFirstToken(c)
			attemptStart := time.Now()

			apiKey, err := a.NextAPIKey(upstream, retryState.failedKeys)
			if err != nil {
				failoverState.lastError = err
				break // 当前 BaseURL 没有可用 Key，尝试下一个 BaseURL
			}

			// 检查熔断状态
			if !forceProbeMode && metricsManager.ShouldSuspendKey(currentBaseURL, apiKey, logCtx.ChannelIndex) {
				retryState.markKeyFailed(apiKey)
				log.Printf("[%s-Circuit] 跳过熔断中的 Key: %s", apiType, utils.MaskAPIKey(apiKey))
				continue
			}

			if envCfg.ShouldLog("info") {
				log.Printf("[%s-Key] 使用API密钥: %s (BaseURL %d/%d, 尝试 %d/%d)",
					apiType, utils.MaskAPIKey(apiKey), urlIdx+1, len(urlResults), attempt+1, retryState.maxRetries)
			}

			// 使用深拷贝避免并发修改问题
			upstreamCopy := a.prepareCandidateUpstream(currentBaseURL, capabilities)

			req, prepareStage, err := a.buildPreparedAttemptRequest(upstreamCopy, apiKey)
			if err != nil && prepareStage == "build_request" {
				failoverState.lastError = err
				retryState.markKeyFailed(apiKey)
				channelScheduler.RecordFailure(currentBaseURL, apiKey, logCtx.ChannelIndex, kind)
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", 0, false, attemptStart, "build_request", err.Error(), true, isStream, nil)
				continue
			}
			if err != nil {
				_ = req.Body.Close()
				if prepareStage == "content_safety" {
					status := handleContentSafetyPreparationError(c, apiType, err)
					attemptStatus := "failed"
					if status == http.StatusForbidden {
						attemptStatus = "blocked"
					}
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, attemptStatus, status, false, attemptStart, prepareStage, err.Error(), false, isStream, nil)
					return newUpstreamAttemptResult(true, "", 0, nil, nil, err)
				}
				status, code := visionlayer.ErrorResponse(err)
				payload := gin.H{
					"error": err.Error(),
					"code":  code,
				}
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", status, false, attemptStart, "vision_layer", err.Error(), true, isStream, nil)
				if channelScheduler.GetActiveChannelCountForModel(kind, logCtx.Model) > 1 {
					body, _ := json.Marshal(payload)
					return newUpstreamAttemptResult(false, "", 0, &FailoverError{Status: status, Body: body}, nil, err)
				}
				c.JSON(status, payload)
				return newUpstreamAttemptResult(true, "", 0, nil, nil, err)
			}
			// 请求准备成功后再启动请求画像和活跃度观测。
			lifecycle := a.startAttemptLifecycle(currentBaseURL, apiKey)
			finishRetryContentSafety := func(preparationErr error) {
				status := handleContentSafetyPreparationError(c, apiType, preparationErr)
				lifecycle.finalizeNeutral()
				attemptStatus := "failed"
				if status == http.StatusForbidden {
					attemptStatus = "blocked"
				}
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, attemptStatus, status, false, attemptStart, "content_safety", preparationErr.Error(), false, isStream, nil)
			}

			resp, err := SendRequest(req, upstream, envCfg, isStream, apiType, proxyURL)
			if err != nil {
				failoverState.lastError = err
				// 区分客户端取消和真实渠道故障（统一口径）
				if isClientSideError(err) {
					// 客户端取消：不计入失败，不触发 failover
					lifecycle.finalizeClientCancelled()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "cancelled", 0, false, attemptStart, "client_cancelled", err.Error(), false, isStream, nil)
					log.Printf("[%s-Cancel] 请求已取消（SendRequest 阶段）", apiType)
					return newUpstreamAttemptResult(true, "", 0, nil, nil, err)
				}
				// 真实渠道故障：计入失败，继续 failover
				retryState.markKeyFailed(apiKey)
				cfgManager.MarkKeyAsFailed(apiKey, apiType)
				lifecycle.finalizeFailed()
				if a.MarkURLFailure != nil {
					a.MarkURLFailure(currentBaseURL)
				}
				recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", 0, false, attemptStart, "network", err.Error(), true, isStream, nil)
				log.Printf("[%s-Key] 警告: API密钥失败: %v", apiType, err)
				continue
			}

			// 收到响应头，性能画像记录首字节时间
			if pm := channelScheduler.GetProfileManager(); pm != nil {
				pm.RecordFirstByte(lifecycle.observation.ProfileRequestID)
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				respBodyBytes, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil {
					// 响应体读取中断：连接已不可靠，按渠道网络故障处理，
					// 不把截断 body 用于错误分类或回给客户端。
					failoverState.lastError = fmt.Errorf("读取上游错误响应体失败: %w", readErr)
					retryState.markKeyFailed(apiKey)
					cfgManager.MarkKeyAsFailed(apiKey, apiType)
					lifecycle.finalizeFailed()
					if a.MarkURLFailure != nil {
						a.MarkURLFailure(currentBaseURL)
					}
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "read_body", readErr.Error(), true, isStream, nil)
					log.Printf("[%s-Key] 警告: 读取上游错误响应体失败 (状态: %d)，尝试下一个密钥: %v", apiType, resp.StatusCode, readErr)
					continue
				}
				respBodyBytes = utils.DecompressGzipIfNeeded(resp, respBodyBytes)
				retrySucceeded := false

				if IsUpstreamModelCapacityError(respBodyBytes) {
					lifecycle.finalizeNeutral()

					failoverState.lastError = fmt.Errorf("上游模型暂时满载")
					failoverState.lastFailoverError = &FailoverError{Status: resp.StatusCode, Body: respBodyBytes}
					candidate := currentBaseURL + "\x00" + apiKey
					canRetry := retryState.canRetryCandidate(candidate, c.Request.Context())
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "model_capacity", string(respBodyBytes), canRetry, isStream, nil)
					if canRetry {
						retryCount := retryState.recordCandidateRetry(candidate)
						log.Printf("[%s-Capacity] 模型暂时满载，使用同一 BaseURL 和 Key 重试 (%d/%d)", apiType, retryCount, retryState.maxSameCandidateRetries)
						continue
					}
					retryState.markKeyFailed(apiKey)
					continue
				}

				// 兼容部分严格的 OpenAI 协议网关：首次明确拒绝 prompt_cache_key 时，
				// 使用同一渠道、BaseURL 和 API key 移除该字段后重试一次，并记住能力。
				if !upstreamCopy.DisablePromptCacheKey && IsPromptCacheKeyUnsupported(resp.StatusCode, respBodyBytes) {
					log.Printf("[%s-ChannelCapability] 渠道 %s 不支持 prompt_cache_key，移除后使用同一 key 重试一次", apiType, upstream.Name)
					capabilities.disablePromptCacheKey = true
					upstreamCopy.DisablePromptCacheKey = true
					if err := cfgManager.MarkPromptCacheKeyUnsupported(string(kind), upstream.ID); err != nil {
						log.Printf("[%s-ChannelCapability] 持久化 prompt_cache_key 能力失败: %v", apiType, err)
					}

					RestoreRequestBody(c, requestBody)
					retryResult := a.retrySameCandidateRequest(upstreamCopy, apiKey, proxyURL)
					if retryResult.Err != nil {
						if retryResult.PreparationStage == "content_safety" {
							finishRetryContentSafety(retryResult.Err)
							return newUpstreamAttemptResult(true, "", 0, nil, nil, retryResult.Err)
						}
						if retryResult.PreparationStage == "build_request" {
							log.Printf("[%s-ChannelCapability] 重建无 prompt_cache_key 请求失败: %v", apiType, retryResult.Err)
						} else if retryResult.PreparationStage == "send_request" {
							log.Printf("[%s-ChannelCapability] 无 prompt_cache_key 重试失败: %v", apiType, retryResult.Err)
						} else {
							log.Printf("[%s-ChannelCapability] 准备无 prompt_cache_key 请求失败: %v", apiType, retryResult.Err)
						}
					} else {
						resp = retryResult.Response
						if retryResult.Succeeded {
							retrySucceeded = true
						} else {
							respBodyBytes = retryResult.Body
						}
					}
				}

				// 部分 DeepSeek 兼容渠道会在 thinking 模式下要求每条 assistant
				// 历史都带 reasoning_content。Cursor 等客户端可能已将该字段
				// 隐藏；首次收到明确 400 后，用同一渠道立即强制补齐并重试，
				// 同时持久化能力，避免之后的每轮请求都先失败一次。
				if !retrySucceeded && kind == scheduler.ChannelKindMessages && upstreamCopy.ServiceType == "openai" && !upstreamCopy.RequireReasoningContent &&
					IsReasoningContentRequired(resp.StatusCode, respBodyBytes) {
					log.Printf("[%s-ChannelCapability] 渠道 %s 要求完整 reasoning_content，补齐后使用同一 key 重试一次", apiType, upstream.Name)
					capabilities.requireReasoningContent = true
					upstreamCopy.RequireReasoningContent = true
					if err := cfgManager.MarkReasoningContentRequired(string(kind), upstream.ID); err != nil {
						log.Printf("[%s-ChannelCapability] 持久化 reasoning_content 能力失败: %v", apiType, err)
					}

					RestoreRequestBody(c, requestBody)
					retryResult := a.retrySameCandidateRequest(upstreamCopy, apiKey, proxyURL)
					if retryResult.Err != nil {
						if retryResult.PreparationStage == "content_safety" {
							finishRetryContentSafety(retryResult.Err)
							return newUpstreamAttemptResult(true, "", 0, nil, nil, retryResult.Err)
						}
						if retryResult.PreparationStage == "build_request" {
							log.Printf("[%s-ChannelCapability] 重建 reasoning_content 兼容请求失败: %v", apiType, retryResult.Err)
						} else if retryResult.PreparationStage == "send_request" {
							log.Printf("[%s-ChannelCapability] reasoning_content 兼容重试失败: %v", apiType, retryResult.Err)
						} else {
							log.Printf("[%s-ChannelCapability] 准备 reasoning_content 兼容请求失败: %v", apiType, retryResult.Err)
						}
					} else {
						resp = retryResult.Response
						if retryResult.Succeeded {
							retrySucceeded = true
						} else {
							respBodyBytes = retryResult.Body
						}
					}
				}

				// 某些 Responses 中转会扫描原始 JSON 字节，中文业务文本或历史中的审核错误码
				// 可能被稳定误判。保持 JSON 解码值不变，改用 Unicode 转义后重试一次。
				if !retrySucceeded && kind == scheduler.ChannelKindResponses && upstreamCopy.ServiceType == "responses" && isContentPolicyError(respBodyBytes) {
					escapedBody, changed, escapeErr := buildContentPolicyCompatibilityBody(requestBody)
					if escapeErr != nil {
						log.Printf("[%s-ContentPolicy] 构建等价 JSON 转义请求失败: %v", apiType, escapeErr)
					} else if changed {
						log.Printf("[%s-ContentPolicy] 使用等价 JSON Unicode 转义在同一渠道重试一次", apiType)
						RestoreRequestBody(c, escapedBody)
						retryResult := a.retrySameCandidateRequest(upstreamCopy, apiKey, proxyURL)
						if retryResult.Err != nil {
							if retryResult.PreparationStage == "content_safety" {
								finishRetryContentSafety(retryResult.Err)
								return newUpstreamAttemptResult(true, "", 0, nil, nil, retryResult.Err)
							}
							if retryResult.PreparationStage == "build_request" {
								log.Printf("[%s-ContentPolicy] 重建转义请求失败: %v", apiType, retryResult.Err)
							} else if retryResult.PreparationStage == "send_request" {
								log.Printf("[%s-ContentPolicy] 转义请求重试失败: %v", apiType, retryResult.Err)
							} else {
								log.Printf("[%s-ContentPolicy] 准备转义请求失败: %v", apiType, retryResult.Err)
							}
						} else {
							resp = retryResult.Response
							if retryResult.Succeeded {
								retrySucceeded = true
								log.Printf("[%s-ContentPolicy] 等价 JSON 转义重试成功", apiType)
							} else {
								respBodyBytes = retryResult.Body
							}
						}
						RestoreRequestBody(c, requestBody)
					}
				}

				if !retrySucceeded {
					decision := a.decideUpstreamFailure(resp.StatusCode, respBodyBytes)
					switch decision.action {
					case upstreamFailureTryNextChannel:
						// 内容审核与当前请求正文、上游策略相关。不要换 Key，也不要计入渠道故障；
						// 多渠道可用时把原始错误交给外层继续选渠。
						lifecycle.finalizeNeutral()

						failoverState.lastError = fmt.Errorf("上游内容审核拒绝请求")
						failoverState.lastFailoverError = &FailoverError{Status: resp.StatusCode, Body: respBodyBytes}
						recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, decision.errorType, string(respBodyBytes), true, isStream, nil)
						log.Printf("[%s-ContentPolicy] 渠道 %s 拒绝请求，跳过当前渠道", apiType, upstream.Name)
						return newUpstreamAttemptResult(false, "", 0, failoverState.lastFailoverError, nil, failoverState.lastError)

					case upstreamFailureTryNextKey:
						failoverState.lastError = fmt.Errorf("上游错误: %d", resp.StatusCode)
						retryState.markKeyFailed(apiKey)
						cfgManager.MarkKeyAsFailed(apiKey, apiType)
						lifecycle.finalizeFailed()
						if a.MarkURLFailure != nil {
							a.MarkURLFailure(currentBaseURL)
						}
						log.Printf("[%s-Key] 警告: API密钥失败 (状态: %d)，尝试下一个密钥", apiType, resp.StatusCode)

						failoverState.lastFailoverError = &FailoverError{
							Status: resp.StatusCode,
							Body:   respBodyBytes,
						}

						if decision.quotaRelated {
							failoverState.deprioritizeCandidates[apiKey] = true
						}
						recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, decision.errorType, string(respBodyBytes), true, isStream, nil)
						continue

					case upstreamFailureRespond:
						// 非 failover 错误，记录失败指标后返回（请求已处理）
						lifecycle.finalizeFailed()
						recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, decision.errorType, string(respBodyBytes), false, isStream, nil)
						c.Data(resp.StatusCode, "application/json", respBodyBytes)
						return newUpstreamAttemptResult(true, "", 0, nil, nil, nil)
					}
				}
			}

			// 成功响应：处理 quota key 降级
			if a.DeprioritizeKey != nil && len(failoverState.deprioritizeCandidates) > 0 {
				for key := range failoverState.deprioritizeCandidates {
					a.DeprioritizeKey(key)
				}
			}

			usage, err = a.HandleSuccess(c, resp, upstreamCopy, apiKey)
			if err != nil {
				failoverState.lastError = err
				decision := classifyResponseProcessingError(err, c.Writer.Written())
				switch decision.action {
				case responseProcessingClientCancelled:
					// 客户端取消/断开：计入总请求数但不计入失败
					lifecycle.finalizeClientCancelled()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "cancelled", resp.StatusCode, false, attemptStart, "client_cancelled", err.Error(), false, isStream, usage)
					log.Printf("[%s-Cancel] 请求已取消，停止渠道 failover", apiType)

				case responseProcessingContentSafety:
					lifecycle.finalizeNeutral()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "blocked", http.StatusForbidden, false, attemptStart, "content_safety", decision.safetyErr.Error(), false, isStream, usage)
					if c.Writer.Written() {
						if writeErr := WriteAttachedStreamError(c, decision.safetyErr); writeErr != nil {
							log.Printf("[%s-ContentSafety] 流错误写出失败: %v", apiType, writeErr)
						}
					} else {
						if writeErr := WriteAttachedContentSafetyError(c, decision.safetyErr); writeErr != nil {
							log.Printf("[%s-ContentSafety] 协议错误写出失败: %v", apiType, writeErr)
							c.JSON(http.StatusInternalServerError, gin.H{"error": writeErr.Error(), "code": "CONTENT_SAFETY_RESPONSE_ERROR"})
						}
					}

				case responseProcessingContentSafetyHook:
					lifecycle.finalizeNeutral()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", http.StatusInternalServerError, false, attemptStart, "content_safety_hook", decision.hookErr.Error(), false, isStream, usage)
					log.Printf("[%s-ContentSafety] 本地响应检查失败: %v", apiType, decision.hookErr)
					if c.Writer.Written() {
						if writeErr := WriteAttachedStreamHookError(c, decision.hookErr); writeErr != nil {
							log.Printf("[%s-ContentSafety] 流内部错误写出失败: %v", apiType, writeErr)
						}
					} else {
						c.JSON(http.StatusInternalServerError, gin.H{
							"error": "内容安全检查失败",
							"code":  "CONTENT_SAFETY_HOOK_ERROR",
						})
					}

				case responseProcessingRetryCandidate:
					lifecycle.finalizeNeutral()

					candidate := currentBaseURL + "\x00" + apiKey
					canRetry := retryState.canRetryCandidate(candidate, c.Request.Context())
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "model_capacity", err.Error(), canRetry, isStream, usage)
					if canRetry {
						retryCount := retryState.recordCandidateRetry(candidate)
						log.Printf("[%s-Capacity] 上游未产出内容，使用同一 BaseURL 和 Key 重试 (%d/%d)", apiType, retryCount, retryState.maxSameCandidateRetries)
						continue
					}

					// 仅在本次请求内排除该 Key，继续其他候选；不写入 Key 熔断和失败率。
					retryState.markKeyFailed(apiKey)
					failoverState.lastFailoverError = &FailoverError{Status: http.StatusServiceUnavailable, Body: []byte(err.Error())}
					continue

				case responseProcessingChannelFailure:
					// 真实渠道故障：计入失败指标
					cfgManager.MarkKeyAsFailed(apiKey, apiType)
					lifecycle.finalizeFailed()
					shouldRetryResponseProcessing := !c.Writer.Written()
					recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "failed", resp.StatusCode, false, attemptStart, "response_processing", err.Error(), shouldRetryResponseProcessing, isStream, usage)
					log.Printf("[%s-Key] 警告: 响应处理失败: %v", apiType, err)
					if shouldRetryResponseProcessing {
						retryState.markKeyFailed(apiKey)
						if a.MarkURLFailure != nil {
							a.MarkURLFailure(currentBaseURL)
						}
						failoverState.lastFailoverError = &FailoverError{
							Status: resp.StatusCode,
							Body:   []byte(err.Error()),
						}
						continue
					}
				}
				return newUpstreamAttemptResult(true, "", 0, nil, usage, err)
			}

			if a.MarkURLSuccess != nil {
				a.MarkURLSuccess(currentBaseURL)
			}

			lifecycle.finalizeSuccessful(usage)
			recordAttemptLog(c, logCtx, upstream, apiType, requestLogID, currentBaseURL, apiKey, "completed", resp.StatusCode, true, attemptStart, "", "", false, isStream, usage)
			// 记录会话 BaseURL 粘滞，后续请求优先同 URL 以利 prompt cache
			if channelScheduler != nil && logCtx.ConversationID != "" {
				channelScheduler.SetPreferredBaseURL(logCtx.ConversationID, currentBaseURL)
			}
			return newUpstreamAttemptResult(true, apiKey, originalIdx, nil, usage, nil)
		}

		// 当前 BaseURL 的所有 Key 都失败，记录并尝试下一个 BaseURL
		if envCfg.ShouldLog("info") && urlIdx < len(urlResults)-1 {
			log.Printf("[%s-BaseURL] BaseURL %d/%d 所有 Key 失败，切换到下一个 BaseURL", apiType, urlIdx+1, len(urlResults))
		}
	}

	return newUpstreamAttemptResult(false, "", 0, failoverState.lastFailoverError, nil, failoverState.lastError)
}

func contentSafetyError(err error) *ContentSafetyError {
	var target *ContentSafetyError
	if errors.As(err, &target) {
		return target
	}
	return nil
}

func contentSafetyHookError(err error) *ContentSafetyHookError {
	var target *ContentSafetyHookError
	if errors.As(err, &target) {
		return target
	}
	return nil
}

func recordConversationAttempt(channelScheduler *scheduler.ChannelScheduler, kind scheduler.ChannelKind, upstream *config.UpstreamConfig, logCtx AttemptLogContext, isStream bool) {
	if channelScheduler == nil || strings.TrimSpace(logCtx.ConversationID) == "" || upstream == nil {
		return
	}
	channelScheduler.MarkConversationAttempt(
		logCtx.ConversationID,
		kind,
		logCtx.ChannelIndex,
		upstream.Name,
		logCtx.Model,
		config.ResolveUpstreamModel(logCtx.Model, upstream),
		isStream,
	)
}

func recordAttemptLog(
	c *gin.Context,
	logCtx AttemptLogContext,
	upstream *config.UpstreamConfig,
	apiType string,
	requestID string,
	baseURL string,
	apiKey string,
	status string,
	statusCode int,
	success bool,
	start time.Time,
	errorType string,
	errorMessage string,
	retried bool,
	isStream bool,
	usage *types.Usage,
) {
	if logCtx.LogStore == nil && logCtx.RequestLogStore == nil {
		return
	}

	channelName := ""
	resolvedModel := logCtx.Model
	if upstream != nil {
		channelName = upstream.Name
		resolvedModel = config.ResolveUpstreamModel(logCtx.Model, upstream)
	}
	transform := ""
	if strings.TrimSpace(logCtx.Model) != "" && strings.TrimSpace(resolvedModel) != "" && logCtx.Model != resolvedModel {
		transform = logCtx.Model + " -> " + resolvedModel
	}
	timestamp := time.Now().Format(time.RFC3339Nano)
	attemptID := nextAttemptLogID("attempt")
	durationMs := time.Since(start).Milliseconds()
	errorMessage = truncateLogMessage(errorMessage, 500)

	if logCtx.LogStore != nil {
		logCtx.LogStore.Record(&metrics.ChannelLog{
			RequestID:             requestID,
			AttemptID:             attemptID,
			Timestamp:             timestamp,
			Status:                status,
			StatusCode:            statusCode,
			Success:               success,
			DurationMs:            durationMs,
			APIType:               apiType,
			Model:                 logCtx.Model,
			InputTokens:           usageInputTokens(usage),
			OutputTokens:          usageOutputTokens(usage),
			CacheCreationTokens:   usageCacheCreationTokens(usage),
			CacheReadTokens:       usageCacheReadTokens(usage),
			CacheCreation5mTokens: usageCacheCreation5mTokens(usage),
			CacheCreation1hTokens: usageCacheCreation1hTokens(usage),
			ChannelIndex:          logCtx.ChannelIndex,
			ChannelName:           channelName,
			BaseURL:               baseURL,
			KeyMask:               utils.MaskAPIKey(apiKey),
			ErrorType:             errorType,
			ErrorMessage:          errorMessage,
			Retried:               retried,
			Stream:                isStream,
		})
	}
	if logCtx.RequestLogStore != nil {
		logCtx.RequestLogStore.Record(metrics.RequestLogEntry{
			RequestID:             requestID,
			AttemptID:             attemptID,
			Timestamp:             timestamp,
			APIType:               apiType,
			Status:                status,
			StatusCode:            statusCode,
			Success:               success,
			DurationMs:            durationMs,
			FirstTokenMs:          requestLogFirstTokenMs(c, start),
			Model:                 logCtx.Model,
			ResolvedModel:         resolvedModel,
			Transform:             transform,
			InputTokens:           usageInputTokens(usage),
			OutputTokens:          usageOutputTokens(usage),
			CacheCreationTokens:   usageCacheCreationTokens(usage),
			CacheReadTokens:       usageCacheReadTokens(usage),
			CacheCreation5mTokens: usageCacheCreation5mTokens(usage),
			CacheCreation1hTokens: usageCacheCreation1hTokens(usage),
			CacheTTL:              usageCacheTTL(usage),
			ChannelIndex:          logCtx.ChannelIndex,
			ChannelName:           channelName,
			BaseURL:               baseURL,
			KeyMask:               utils.MaskAPIKey(apiKey),
			ErrorType:             errorType,
			ErrorMessage:          errorMessage,
			Retried:               retried,
			Stream:                isStream,
			ConversationID:        logCtx.ConversationID,
		})
	}
}

func requestLogFirstTokenMs(c *gin.Context, start time.Time) int64 {
	if c == nil {
		return 0
	}
	value, ok := c.Get(requestLogFirstTokenAtKey)
	if !ok {
		return 0
	}
	markedAt, ok := value.(time.Time)
	if !ok || markedAt.IsZero() || markedAt.Before(start) {
		return 0
	}
	return markedAt.Sub(start).Milliseconds()
}

func usageInputTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	if usage.InputTokens > 0 {
		return usage.InputTokens
	}
	return usage.PromptTokens
}

func usageOutputTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	if usage.OutputTokens > 0 {
		return usage.OutputTokens
	}
	return usage.CompletionTokens
}

func usageCacheCreationTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	if usage.CacheCreationInputTokens > 0 {
		return usage.CacheCreationInputTokens
	}
	return usage.CacheCreation5mInputTokens + usage.CacheCreation1hInputTokens
}

func usageCacheReadTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	return usage.CacheReadInputTokens
}

func usageCacheCreation5mTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	return usage.CacheCreation5mInputTokens
}

func usageCacheCreation1hTokens(usage *types.Usage) int {
	if usage == nil {
		return 0
	}
	return usage.CacheCreation1hInputTokens
}

func usageCacheTTL(usage *types.Usage) string {
	if usage == nil {
		return ""
	}
	return usage.CacheTTL
}

func nextAttemptLogID(prefix string) string {
	seq := atomic.AddUint64(&attemptLogCounter, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), seq)
}

func classifyUpstreamError(statusCode int, quotaRelated bool) string {
	if quotaRelated {
		return "quota"
	}
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "rate_limit"
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden:
		return "auth"
	case statusCode >= 500:
		return "upstream_5xx"
	case statusCode >= 400:
		return "upstream_4xx"
	default:
		return "upstream"
	}
}

func truncateLogMessage(message string, limit int) string {
	message = strings.TrimSpace(message)
	if limit <= 0 || len(message) <= limit {
		return message
	}
	return message[:limit] + "..."
}

// BuildDefaultURLResults 将 URLs 转为按原始顺序的结果列表（无动态排序）
func BuildDefaultURLResults(urls []string) []urlhealth.URLLatencyResult {
	results := make([]urlhealth.URLLatencyResult, len(urls))
	for i, url := range urls {
		results[i] = urlhealth.URLLatencyResult{
			URL:         url,
			OriginalIdx: i,
			Success:     true,
		}
	}
	return results
}
