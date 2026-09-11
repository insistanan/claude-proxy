// 本文件定义单次上游尝试的输入契约（UpstreamAttempt + 四个回调类型）与命名结果，
// 让调用方按字段构造/读取，而不依赖长参数列表和位置返回值。
package proxycore

import (
	"net/http"

	"github.com/BenedictKing/api-proxy/internal/config"
	"github.com/BenedictKing/api-proxy/internal/metrics"
	"github.com/BenedictKing/api-proxy/internal/scheduler"
	"github.com/BenedictKing/api-proxy/internal/types"
	"github.com/BenedictKing/api-proxy/internal/urlhealth"
	"github.com/gin-gonic/gin"
)

// NextAPIKeyFunc 返回下一个可用 API key（按 failover 策略）
type NextAPIKeyFunc func(upstream *config.UpstreamConfig, failedKeys map[string]bool) (string, error)

// BuildRequestFunc 构建上游请求（upstreamCopy.BaseURL 已写入当前尝试的 BaseURL）
type BuildRequestFunc func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error)

// DeprioritizeKeyFunc 对 quota 相关失败的 key 做降级（实现可选择是否记录日志）
type DeprioritizeKeyFunc func(apiKey string)

// HandleSuccessFunc 处理成功响应（负责写回客户端），并返回 usage（可为 nil）
// 注意：实现方需要自行关闭 resp.Body（与现有 handlers 保持一致）。
type HandleSuccessFunc func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error)

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
	// Handled 表示是否已向客户端写回响应（成功，或不值得故障转移的错误）。
	Handled bool
	// SuccessKey 是成功的 Key，仅 Handled=true 且确实成功时有值。
	SuccessKey string
	// SuccessBaseURLIdx 是成功 BaseURL 的原始索引，用于指标记录。
	SuccessBaseURLIdx int
	// FailoverError 是最后一次可故障转移的上游错误，供多渠道聚合错误使用。
	FailoverError *FailoverError
	// Usage 是本次尝试的用量统计，可能为 nil。
	Usage *types.Usage
	// LastError 是最后一次底层错误，用于日志与上层判断。
	LastError error
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

type AttemptLogContext struct {
	ChannelIndex                      int
	Model                             string
	ConversationID                    string
	LogStore                          *metrics.ChannelLogStore
	RequestLogStore                   *metrics.RequestLogStore
	AllowContentPolicyChannelFailover bool
}
