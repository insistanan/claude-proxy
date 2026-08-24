package scheduler

import (
	"context"
	"fmt"
	"strings"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

// SelectVisionChannel 通过稳定渠道 ID 解析图片理解层指定的原生图片理解渠道，
// 并为这次内部调用预留在途负载，避免其从渠道调度与实时指标中消失。
func (s *ChannelScheduler) SelectVisionChannel(ctx context.Context, kind ChannelKind, channelID string, ownerPoolID string) (*SelectionResult, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	channelID = strings.TrimSpace(channelID)
	ownerPoolID = strings.TrimSpace(ownerPoolID)
	if ownerPoolID == "" {
		ownerPoolID = config.DefaultChannelPoolID
	}
	if channelID == "" {
		return nil, fmt.Errorf("图片理解层未指定图片理解渠道")
	}

	cfg := s.configManager.GetConfig()
	var upstreams []config.UpstreamConfig
	switch kind {
	case ChannelKindMessages:
		upstreams = cfg.Upstream
	case ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case ChannelKindChat:
		upstreams = cfg.ChatUpstream
	case ChannelKindImages:
		upstreams = cfg.ImagesUpstream
	default:
		return nil, fmt.Errorf("不支持的图片理解渠道类型: %s", kind)
	}

	for index := range upstreams {
		upstream := &upstreams[index]
		if upstream.ID != channelID {
			continue
		}
		if !upstream.VisionCapable {
			return nil, fmt.Errorf("渠道 %q 未标记为支持图片理解", upstream.Name)
		}
		targetPoolID := strings.TrimSpace(upstream.PoolID)
		if targetPoolID == "" {
			targetPoolID = config.DefaultChannelPoolID
		}
		if !upstream.ExcludeFromConversation && targetPoolID != ownerPoolID {
			return nil, fmt.Errorf("图片理解渠道 %q 不在当前分组或公用纯图片理解池", upstream.Name)
		}
		if config.GetChannelStatus(upstream) != config.ChannelStatusActive || len(upstream.APIKeys) == 0 {
			return nil, fmt.Errorf("图片理解渠道 %q 当前不可用", upstream.Name)
		}
		metricsManager := s.getMetricsManager(kind)
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys, index) {
			return nil, fmt.Errorf("图片理解渠道 %q 当前不健康", upstream.Name)
		}
		return s.reserveAndReturn(&SelectionResult{
			Upstream:     upstream.Clone(),
			ChannelIndex: index,
			Reason:       "vision_layer",
		}, kind), nil
	}

	return nil, fmt.Errorf("图片理解渠道 %q 不存在", channelID)
}

// ListFallbackVisionChannels 返回公共图片理解渠道池中可用的渠道列表，
// 排除已尝试过的渠道 ID。用于图片理解渠道失败后的自动回退。
// 返回的渠道按健康度排序（健康优先）。
func (s *ChannelScheduler) ListFallbackVisionChannels(ctx context.Context, kind ChannelKind, excludeChannelID string, ownerPoolID string) []*SelectionResult {
	if ctx == nil {
		return nil
	}
	channelID := strings.TrimSpace(excludeChannelID)
	ownerPoolID = strings.TrimSpace(ownerPoolID)
	if ownerPoolID == "" {
		ownerPoolID = config.DefaultChannelPoolID
	}

	cfg := s.configManager.GetConfig()
	var upstreams []config.UpstreamConfig
	switch kind {
	case ChannelKindMessages:
		upstreams = cfg.Upstream
	case ChannelKindResponses:
		upstreams = cfg.ResponsesUpstream
	case ChannelKindGemini:
		upstreams = cfg.GeminiUpstream
	case ChannelKindChat:
		upstreams = cfg.ChatUpstream
	case ChannelKindImages:
		upstreams = cfg.ImagesUpstream
	default:
		return nil
	}

	var results []*SelectionResult
	for index := range upstreams {
		upstream := &upstreams[index]
		if upstream.ID == channelID {
			continue
		}
		// 必须标记为支持图片理解
		if !upstream.VisionCapable {
			continue
		}
		// 必须是公共图片理解渠道（ExcludeFromConversation=true）或同池渠道
		targetPoolID := strings.TrimSpace(upstream.PoolID)
		if targetPoolID == "" {
			targetPoolID = config.DefaultChannelPoolID
		}
		if !upstream.ExcludeFromConversation && targetPoolID != ownerPoolID {
			continue
		}
		// 必须可用且健康
		if config.GetChannelStatus(upstream) != config.ChannelStatusActive || len(upstream.APIKeys) == 0 {
			continue
		}
		metricsManager := s.getMetricsManager(kind)
		if !metricsManager.IsChannelHealthyWithKeys(upstream.BaseURL, upstream.APIKeys, index) {
			continue
		}
		results = append(results, &SelectionResult{
			Upstream:     upstream.Clone(),
			ChannelIndex: index,
			Reason:       "vision_layer_fallback",
		})
	}
	return results
}
