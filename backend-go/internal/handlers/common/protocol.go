package common

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/middleware"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

// ProtocolSpec 描述一种代理协议的差异点。
// 五个协议共有的流程（认证/读体/会话观测/渠道选择/failover/日志）由 RunProxyRequest 承担，
// 协议只需填这张表。
type ProtocolSpec struct {
	Kind    scheduler.ChannelKind
	LogName string // 日志前缀，如 "Messages" / "Chat"

	// PreRoute 可选：协议特有的前置路由（如 chat 的 modelcatalog 路由）。
	// 返回 true 表示请求已被处理，主流程应直接返回。
	PreRoute func(c *gin.Context, body []byte, model string, userID string, startTime time.Time) bool

	// ParseRequest 解析请求体，返回本次请求的模型名、是否流式、提示词列表。
	// 解析失败时应自行写出 4xx 响应并返回 ok=false。
	ParseRequest func(c *gin.Context, body []byte) (model string, stream bool, prompts []string, ok bool)

	// BuildUpstreamRequest 构造发往上游的 http.Request（含协议特有的请求体清洗/模型映射）。
	BuildUpstreamRequest func(c *gin.Context, up *config.UpstreamConfig, apiKey string, body []byte) (*http.Request, error)

	// HandleSuccess 处理上游 2xx 响应（含流式与非流式分派）。
	HandleSuccess func(c *gin.Context, resp *http.Response, up *config.UpstreamConfig, apiKey string, body []byte, startTime time.Time) (*types.Usage, error)

	// HandleAllFailed 可选：所有渠道都失败时的错误响应格式。
	// 若为 nil，使用默认 HandleAllChannelsFailed（gin.H 格式）。
	// Gemini 协议需要覆盖此选项以返回 types.GeminiError 格式。
	HandleAllFailed func(c *gin.Context, failoverErr *FailoverError, lastError error)

	// HandleAllKeysFailed 可选：单渠道模式下所有 Key 都失败时的错误响应格式。
	// 若为 nil，使用默认 HandleAllKeysFailed（gin.H 格式）。
	HandleAllKeysFailed func(c *gin.Context, fuzzyMode bool, failoverErr *FailoverError, lastError error)
}

// RunProxyRequest 是一个可复用的代理请求处理主流程。
// 它实现了五个协议共有的骨架：
//
//	认证 → 读 body → 解析 → 会话观测 → 日志 → 指定渠道分支 → PreRoute → 多渠道/单渠道分派
//
// 协议特有的逻辑（解析、构建请求、处理响应、前置路由）通过 ProtocolSpec 注入。
// 调用方通常这样使用：
//
//	func Handler(envCfg, cfgManager, channelScheduler) gin.HandlerFunc {
//	    spec := ProtocolSpec{...}
//	    return func(c *gin.Context) {
//	        RunProxyRequest(c, envCfg, cfgManager, channelScheduler, spec)
//	    }
//	}
func RunProxyRequest(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	spec ProtocolSpec,
) {
	// 1. 认证
	middleware.ProxyAuthMiddleware(envCfg)(c)
	if c.IsAborted() {
		return
	}

	startTime := time.Now()

	// 2. 读取请求体
	bodyBytes, err := ReadRequestBody(c, envCfg.MaxRequestBodySize)
	if err != nil {
		return
	}

	// 3. 解析请求
	model, stream, prompts, ok := spec.ParseRequest(c, bodyBytes)
	if !ok {
		return
	}

	// 4. 会话观测
	userID := ObserveConversationRequest(
		channelScheduler,
		spec.Kind,
		ResolveConversationIdentity(c, bodyBytes),
		BuildConversationTranscript(string(spec.Kind), bodyBytes),
		model,
		prompts,
		utils.ExtractImageFingerprints(bodyBytes),
		stream,
	)
	defer MarkConversationComplete(channelScheduler, userID, spec.Kind)

	// 5. 记录原始请求
	LogOriginalRequest(c, bodyBytes, envCfg, spec.LogName)

	// 6. 指定渠道分支
	requestedChannelIndex, hasRequestedChannel, err := ExtractRequestedChannelIndex(bodyBytes)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
			"code":  "INVALID_CHANNEL_INDEX",
		})
		return
	}
	if hasRequestedChannel {
		upstream, channelIndex, err := ResolveRequestedUpstream(cfgManager, spec.Kind, requestedChannelIndex)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": err.Error(),
				"code":  "INVALID_CHANNEL_INDEX",
			})
			return
		}
		handleSingleChannelProxy(c, envCfg, cfgManager, channelScheduler, spec, bodyBytes, model, stream, userID, upstream, channelIndex, startTime)
		return
	}

	// 7. PreRoute（协议特有前置路由，如 chat 的 modelcatalog 路由）
	if spec.PreRoute != nil {
		if spec.PreRoute(c, bodyBytes, model, userID, startTime) {
			return
		}
	}

	// 8. 多渠道/单渠道分派
	if channelScheduler.IsMultiChannelModeForModel(spec.Kind, model) {
		hasImage := utils.DetectImageContent(bodyBytes)
		handleMultiChannelProxy(c, envCfg, cfgManager, channelScheduler, spec, bodyBytes, model, stream, userID, hasImage, startTime)
	} else {
		handleSingleChannelProxy(c, envCfg, cfgManager, channelScheduler, spec, bodyBytes, model, stream, userID, nil, 0, startTime)
	}
}

// handleSingleChannelProxy 处理单渠道代理请求。
// 当 upstream 为 nil 时，从配置中获取当前可用渠道。
func handleSingleChannelProxy(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	spec ProtocolSpec,
	bodyBytes []byte,
	model string,
	stream bool,
	userID string,
	upstream *config.UpstreamConfig,
	channelIndex int,
	startTime time.Time,
) {
	// 未指定渠道时，从配置获取
	if upstream == nil {
		var err error
		upstream, channelIndex, err = cfgManager.GetCurrentUpstreamWithIndexForModelForKind(string(spec.Kind), model)
		if err != nil {
			c.JSON(503, gin.H{
				"error": err.Error(),
				"code":  "NO_UPSTREAM",
			})
			return
		}
	}

	// 无 API Key 检查
	if len(upstream.APIKeys) == 0 {
		c.JSON(503, gin.H{
			"error": "当前渠道未配置API密钥",
			"code":  "NO_API_KEYS",
		})
		return
	}

	// 对话路由冲突检查
	if err := channelScheduler.ValidateFixedChannel(userID, spec.Kind, channelIndex); err != nil {
		MarkConversationFailure(channelScheduler, userID, spec.Kind, err)
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "code": "CONVERSATION_ROUTE_OVERRIDE"})
		return
	}

	metricsManager := channelScheduler.MetricsManager(spec.Kind)
	baseURLs := upstream.GetAllBaseURLs()
	urlResults := BuildDefaultURLResults(baseURLs)

	handled, successKey, _, lastFailoverError, _, lastError := TryUpstreamWithModelMappingFailover(
		c,
		envCfg,
		cfgManager,
		channelScheduler,
		spec.Kind,
		spec.LogName,
		metricsManager,
		upstream,
		model,
		cfgManager.GetFuzzyModeEnabled(),
		urlResults,
		bodyBytes,
		stream,
		func(up *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
			return cfgManager.GetNextAPIKey(up, failedKeys, spec.LogName)
		},
		func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
			return spec.BuildUpstreamRequest(c, upstreamCopy, apiKey, bodyBytes)
		},
		func(apiKey string) {
			if err := cfgManager.MoveAPIKeyToBottomForKind(string(spec.Kind), channelIndex, apiKey); err != nil {
				log.Printf("[%s-Key] 警告: 密钥降级失败: %v", spec.LogName, err)
			}
		},
		nil, // markURLFailure — 单渠道模式不追踪 URL 失败
		nil, // markURLSuccess — 单渠道模式不追踪 URL 成功
		func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
			return spec.HandleSuccess(c, resp, upstreamCopy, apiKey, bodyBytes, startTime)
		},
		AttemptLogContext{
			ChannelIndex:    channelIndex,
			Model:           model,
			ConversationID:  userID,
			LogStore:        channelScheduler.GetChannelLogStore(spec.Kind),
			RequestLogStore: channelScheduler.GetRequestLogStore(),
		},
	)
	if handled {
		if successKey != "" {
			MarkConversationSuccess(channelScheduler, userID, spec.Kind, channelIndex, upstream.Name)
			channelScheduler.ConsumePromotionCount(channelIndex, spec.Kind)
		} else if lastError != nil && !errors.Is(lastError, context.Canceled) {
			MarkConversationFailure(channelScheduler, userID, spec.Kind, lastError)
		}
		return
	}

	log.Printf("[%s-Error] 所有API密钥都失败了", spec.LogName)
	MarkConversationFailure(channelScheduler, userID, spec.Kind, lastError)
	if spec.HandleAllKeysFailed != nil {
		spec.HandleAllKeysFailed(c, cfgManager.GetFuzzyModeEnabled(), lastFailoverError, lastError)
	} else {
		HandleAllKeysFailed(c, cfgManager.GetFuzzyModeEnabled(), lastFailoverError, lastError, spec.LogName)
	}
}

// handleMultiChannelProxy 处理多渠道代理请求。
func handleMultiChannelProxy(
	c *gin.Context,
	envCfg *config.EnvConfig,
	cfgManager *config.ConfigManager,
	channelScheduler *scheduler.ChannelScheduler,
	spec ProtocolSpec,
	bodyBytes []byte,
	model string,
	stream bool,
	userID string,
	hasImage bool,
	startTime time.Time,
) {
	metricsManager := channelScheduler.MetricsManager(spec.Kind)

	HandleMultiChannelFailover(
		c,
		envCfg,
		channelScheduler,
		spec.Kind,
		spec.LogName,
		userID,
		model,
		hasImage,
		cfgManager.GetFuzzyModeEnabled(),
		func(selection *scheduler.SelectionResult) MultiChannelAttemptResult {
			upstream := selection.Upstream
			channelIndex := selection.ChannelIndex

			if upstream == nil {
				return MultiChannelAttemptResult{}
			}

			baseURLs := upstream.GetAllBaseURLs()
			sortedURLResults := channelScheduler.GetSortedURLsForChannel(spec.Kind, channelIndex, baseURLs)

			handled, successKey, successBaseURLIdx, failoverErr, usage, lastErr := TryUpstreamWithModelMappingFailover(
				c,
				envCfg,
				cfgManager,
				channelScheduler,
				spec.Kind,
				spec.LogName,
				metricsManager,
				upstream,
				model,
				cfgManager.GetFuzzyModeEnabled(),
				sortedURLResults,
				bodyBytes,
				stream,
				func(up *config.UpstreamConfig, failedKeys map[string]bool) (string, error) {
					return cfgManager.GetNextAPIKey(up, failedKeys, spec.LogName)
				},
				func(c *gin.Context, upstreamCopy *config.UpstreamConfig, apiKey string) (*http.Request, error) {
					return spec.BuildUpstreamRequest(c, upstreamCopy, apiKey, bodyBytes)
				},
				func(apiKey string) {
					if err := cfgManager.MoveAPIKeyToBottom(channelIndex, apiKey); err != nil {
						log.Printf("[%s-Key] 警告: 密钥降级失败: %v", spec.LogName, err)
					}
				},
				func(url string) {
					channelScheduler.MarkURLFailure(spec.Kind, channelIndex, url)
				},
				func(url string) {
					channelScheduler.MarkURLSuccess(spec.Kind, channelIndex, url)
				},
				func(c *gin.Context, resp *http.Response, upstreamCopy *config.UpstreamConfig, apiKey string) (*types.Usage, error) {
					return spec.HandleSuccess(c, resp, upstreamCopy, apiKey, bodyBytes, startTime)
				},
				AttemptLogContext{
					ChannelIndex:    channelIndex,
					Model:           model,
					ConversationID:  userID,
					LogStore:        channelScheduler.GetChannelLogStore(spec.Kind),
					RequestLogStore: channelScheduler.GetRequestLogStore(),
				},
			)

			return MultiChannelAttemptResult{
				Handled:           handled,
				Attempted:         true,
				SuccessKey:        successKey,
				SuccessBaseURLIdx: successBaseURLIdx,
				FailoverError:     failoverErr,
				Usage:             usage,
				LastError:         lastErr,
			}
		},
		func(selection *scheduler.SelectionResult, result MultiChannelAttemptResult) {
			if selection == nil || selection.Upstream == nil {
				return
			}
			if result.SuccessKey != "" {
				MarkConversationSuccess(channelScheduler, userID, spec.Kind, selection.ChannelIndex, selection.Upstream.Name)
				return
			}
			if result.LastError != nil && !errors.Is(result.LastError, context.Canceled) {
				MarkConversationFailure(channelScheduler, userID, spec.Kind, result.LastError)
			}
		},
		func(ctx *gin.Context, failoverErr *FailoverError, lastError error) {
			MarkConversationFailure(channelScheduler, userID, spec.Kind, lastError)
			if spec.HandleAllFailed != nil {
				spec.HandleAllFailed(ctx, failoverErr, lastError)
			} else {
				HandleAllChannelsFailed(ctx, cfgManager.GetFuzzyModeEnabled(), failoverErr, lastError, spec.LogName)
			}
		},
	)
}