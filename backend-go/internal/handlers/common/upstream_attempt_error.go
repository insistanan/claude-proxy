// 本文件收拢"上游失败/响应处理失败该怎么办"的错误类型与分类决策：
// decideUpstreamFailure 把状态码与响应体交给同包 failover.go 的谓词
// （ShouldRetryWithNextKey / shouldFailoverToNextChannel，底层为 utils.ClassifyUpstreamStatus），
// classifyResponseProcessingError 把写回阶段的 error 归类；本文件只做"谓词结果 -> 本层动作"的映射。
package common

import (
	"context"
	"errors"
	"net/http"
	"strings"
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
