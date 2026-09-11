// 本文件是单次上游尝试的状态载体：候选（BaseURL）级重试状态、单次尝试的观测与
// 生命周期收尾、以及跨候选累积的能力/故障转移状态。
// candidateRetryState 每进入一个 BaseURL 必须新建，避免状态跨候选泄漏。
package proxycore

import (
	"context"
	"net/http"

	"github.com/BenedictKing/api-proxy/internal/config"
	"github.com/BenedictKing/api-proxy/internal/types"
)

// candidateRetryState 收拢单个 BaseURL 生命周期内的 Key 失败和同候选重试状态。
// 每进入一个 BaseURL 都必须创建新的实例，避免候选状态跨 BaseURL 泄漏。
type candidateRetryState struct {
	failedKeys              map[string]bool
	sameCandidateRetries    map[string]int
	maxSameCandidateRetries int
	maxRetries              int
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
