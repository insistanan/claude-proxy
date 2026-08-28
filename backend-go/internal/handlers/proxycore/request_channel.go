package proxycore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// IsQuickTestRequest 检查请求是否来自快捷测试（通过请求头或 metadata.purpose 判断）。
func IsQuickTestRequest(c *gin.Context, bodyBytes []byte) bool {
	if c != nil {
		if purpose := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Proxy-Purpose"))); purpose == "quick_test" {
			return true
		}
		if purpose := strings.ToLower(strings.TrimSpace(c.GetHeader("X-Purpose"))); purpose == "quick_test" {
			return true
		}
	}
	if len(bodyBytes) == 0 {
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()

	var payload map[string]interface{}
	if err := decoder.Decode(&payload); err != nil {
		return false
	}

	rawMetadata, exists := payload["metadata"]
	if !exists || rawMetadata == nil {
		return false
	}

	metadata, ok := rawMetadata.(map[string]interface{})
	if !ok {
		return false
	}

	if purpose, ok := metadata["purpose"].(string); ok {
		if strings.ToLower(strings.TrimSpace(purpose)) == "quick_test" {
			return true
		}
	}

	return false
}

// AreAllKeysSuspended 检查渠道的所有 Key 是否都处于熔断状态
// 用于判断是否需要启用强制探测模式
func AreAllKeysSuspended(metricsManager *metrics.MetricsManager, baseURL string, apiKeys []string, channelIndex int) bool {
	if len(apiKeys) == 0 {
		return false
	}

	for _, apiKey := range apiKeys {
		if !metricsManager.ShouldSuspendKey(baseURL, apiKey, channelIndex) {
			return false
		}
	}
	return true
}

// ExtractRequestedChannelIndex 从请求体 metadata.channel_index 中提取显式指定的渠道索引。
// 仅当字段显式存在时才返回 ok=true；若字段类型非法则返回错误，避免静默回退到默认调度。
func ExtractRequestedChannelIndex(bodyBytes []byte) (int, bool, error) {
	if len(bodyBytes) == 0 {
		return 0, false, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(bodyBytes))
	decoder.UseNumber()

	var payload map[string]interface{}
	if err := decoder.Decode(&payload); err != nil {
		return 0, false, nil
	}

	rawMetadata, exists := payload["metadata"]
	if !exists || rawMetadata == nil {
		return 0, false, nil
	}

	metadata, ok := rawMetadata.(map[string]interface{})
	if !ok {
		return 0, false, fmt.Errorf("metadata 必须是对象")
	}

	rawChannelIndex, exists := metadata["channel_index"]
	if !exists || rawChannelIndex == nil {
		return 0, false, nil
	}

	number, ok := rawChannelIndex.(json.Number)
	if !ok {
		return 0, true, fmt.Errorf("metadata.channel_index 必须是整数")
	}

	channelIndex, err := number.Int64()
	if err != nil {
		return 0, true, fmt.Errorf("metadata.channel_index 必须是整数")
	}
	if channelIndex < 0 {
		return 0, true, fmt.Errorf("metadata.channel_index 不能小于 0")
	}

	return int(channelIndex), true, nil
}

// ResolveRequestedUpstream 解析显式指定的渠道。
// 这里允许演练/调试显式命中非默认渠道，但会拒绝 deleted 渠道和未配置 BaseURL 的渠道。
func ResolveRequestedUpstream(
	cfgManager *config.ConfigManager,
	kind scheduler.ChannelKind,
	channelIndex int,
) (*config.UpstreamConfig, int, error) {
	cfg := cfgManager.GetConfig()

	var upstreams []config.UpstreamConfig
	switch kind {
	case scheduler.ChannelKindMessages:
		upstreams = cfg.Upstream
	case scheduler.ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case scheduler.ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case scheduler.ChannelKindChat:
		upstreams = cfg.ChatUpstream
	case scheduler.ChannelKindImages:
		upstreams = cfg.ImagesUpstream
	default:
		return nil, -1, fmt.Errorf("不支持的渠道类型: %s", kind)
	}

	if len(upstreams) == 0 {
		return nil, -1, fmt.Errorf("当前未配置任何 %s 渠道", kind)
	}
	if channelIndex < 0 || channelIndex >= len(upstreams) {
		return nil, -1, fmt.Errorf("metadata.channel_index [%d] 超出 %s 渠道范围", channelIndex, kind)
	}

	upstream := upstreams[channelIndex].Clone()
	if upstream == nil {
		return nil, -1, fmt.Errorf("无法解析 %s 渠道 [%d]", kind, channelIndex)
	}
	if config.GetChannelStatus(upstream) == config.ChannelStatusDeleted {
		return nil, -1, fmt.Errorf("%s 渠道 [%d] 已删除", kind, channelIndex)
	}
	if kind != scheduler.ChannelKindImages && upstream.ExcludeFromConversation {
		return nil, -1, fmt.Errorf("%s 渠道 [%d] 已设置为不参与对话", kind, channelIndex)
	}
	if strings.TrimSpace(upstream.GetEffectiveBaseURL()) == "" {
		return nil, -1, fmt.Errorf("%s 渠道 [%d] 未配置 BaseURL", kind, channelIndex)
	}

	return upstream, channelIndex, nil
}
