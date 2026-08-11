package modelaudit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

type ConfigProvider interface {
	GetConfig() config.Config
}

type TargetResolver interface {
	Resolve(context.Context, ExecutionSpec) (ResolvedTarget, error)
}

type ConfigTargetResolver struct {
	provider ConfigProvider
	now      func() time.Time
}

func NewConfigTargetResolver(provider ConfigProvider) (*ConfigTargetResolver, error) {
	if provider == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "配置提供者不能为空")
	}
	return &ConfigTargetResolver{provider: provider, now: time.Now}, nil
}

type ResolvedTarget struct {
	Snapshot     TargetSnapshot        `json:"snapshot"`
	ChannelIndex int                   `json:"-"`
	Upstream     config.UpstreamConfig `json:"-"`
}

func (r *ConfigTargetResolver) Resolve(ctx context.Context, spec ExecutionSpec) (ResolvedTarget, error) {
	if r == nil || r.provider == nil {
		return ResolvedTarget{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "目标解析器未初始化")
	}
	if ctx == nil {
		return ResolvedTarget{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "执行上下文不能为空")
	}
	if err := ctx.Err(); err != nil {
		return ResolvedTarget{}, contractError(ErrorCodeCancelled, ErrorCategoryRequest, "目标解析已取消", err)
	}
	return ResolveTarget(r.provider.GetConfig(), spec, r.now().UTC())
}

func ResolveTarget(cfg config.Config, spec ExecutionSpec, capturedAt time.Time) (ResolvedTarget, error) {
	if err := spec.Validate(); err != nil {
		return ResolvedTarget{}, err
	}
	if err := ValidateSpecCapabilities(spec); err != nil {
		return ResolvedTarget{}, err
	}

	upstreams, err := upstreamsForKind(cfg, spec.Target.ChannelKind)
	if err != nil {
		return ResolvedTarget{}, err
	}

	channelID := strings.TrimSpace(spec.Target.ChannelID)
	matchIndex := -1
	var match config.UpstreamConfig
	for i := range upstreams {
		candidateID := strings.TrimSpace(upstreams[i].ID)
		if candidateID == "" {
			continue
		}
		if candidateID != channelID {
			continue
		}
		if matchIndex >= 0 {
			return ResolvedTarget{}, contractError(
				ErrorCodeChannelIDAmbiguous,
				ErrorCategoryTarget,
				fmt.Sprintf("渠道类型 %q 中存在重复的稳定渠道 ID %q", spec.Target.ChannelKind, channelID),
			)
		}
		matchIndex = i
		match = *upstreams[i].Clone()
	}
	if matchIndex < 0 {
		return ResolvedTarget{}, contractError(
			ErrorCodeChannelNotFound,
			ErrorCategoryTarget,
			fmt.Sprintf("未找到渠道 %q（类型 %q）", channelID, spec.Target.ChannelKind),
		)
	}
	if strings.TrimSpace(match.ID) == "" {
		return ResolvedTarget{}, contractError(ErrorCodeChannelIDMissing, ErrorCategoryTarget, "目标渠道缺少稳定 ID")
	}
	if config.GetChannelStatus(&match) == config.ChannelStatusDeleted {
		return ResolvedTarget{}, contractError(ErrorCodeChannelNotFound, ErrorCategoryTarget, fmt.Sprintf("渠道 %q 已删除", channelID))
	}

	requestedModel := strings.TrimSpace(spec.Model)
	if requestedModel == "" && strings.TrimSpace(match.DefaultModel) == "" {
		return ResolvedTarget{}, contractError(
			ErrorCodeModelRequired,
			ErrorCategoryTarget,
			fmt.Sprintf("渠道 %q 未配置默认模型，请显式选择模型", channelID),
		)
	}
	resolvedModel := strings.TrimSpace(config.ResolveUpstreamModel(requestedModel, &match))
	if resolvedModel == "" {
		return ResolvedTarget{}, contractError(
			ErrorCodeModelRequired,
			ErrorCategoryTarget,
			fmt.Sprintf("渠道 %q 无法解析实际模型", channelID),
		)
	}
	wireProtocol, err := wireProtocolFor(spec.Target.ChannelKind, match.ServiceType)
	if err != nil {
		return ResolvedTarget{}, err
	}
	thinkingMapping, err := ResolveThinking(wireProtocol, spec.Thinking)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	} else {
		capturedAt = capturedAt.UTC()
	}

	snapshot := TargetSnapshot{
		ChannelID:      match.ID,
		ChannelKind:    spec.Target.ChannelKind,
		ChannelName:    match.Name,
		ChannelStatus:  config.GetChannelStatus(&match),
		ServiceType:    match.ServiceType,
		Protocol:       spec.Protocol,
		WireProtocol:   wireProtocol,
		RequestedModel: requestedModel,
		ResolvedModel:  resolvedModel,
		DefaultModel:   strings.TrimSpace(match.DefaultModel),
		Thinking:       spec.Thinking,
		ThinkingMap:    thinkingMapping,
		RequestProfile: spec.RequestProfile,
		CapturedAt:     capturedAt,
	}

	return ResolvedTarget{Snapshot: snapshot, ChannelIndex: matchIndex, Upstream: match}, nil
}

func wireProtocolFor(kind ChannelKind, serviceType string) (Protocol, error) {
	serviceType = strings.ToLower(strings.TrimSpace(serviceType))
	switch kind {
	case ChannelKindMessages:
		switch serviceType {
		case "", "claude":
			return ProtocolMessages, nil
		case "responses":
			return ProtocolResponses, nil
		case "openai":
			return ProtocolChat, nil
		case "gemini":
			return ProtocolGemini, nil
		}
	case ChannelKindResponses:
		switch serviceType {
		case "", "responses":
			return ProtocolResponses, nil
		case "claude":
			return ProtocolMessages, nil
		case "openai":
			return ProtocolChat, nil
		case "gemini":
			return ProtocolGemini, nil
		}
	case ChannelKindChat:
		return ProtocolChat, nil
	case ChannelKindGemini:
		return ProtocolGemini, nil
	case ChannelKindImages:
		return ProtocolImages, nil
	}
	return "", contractError(
		ErrorCodeUnsupported,
		ErrorCategoryUnsupported,
		fmt.Sprintf("渠道类型 %q 不支持服务类型 %q", kind, serviceType),
	)
}

func upstreamsForKind(cfg config.Config, kind ChannelKind) ([]config.UpstreamConfig, error) {
	switch kind {
	case ChannelKindMessages:
		return cfg.Upstream, nil
	case ChannelKindResponses:
		return cfg.ResponsesUpstream, nil
	case ChannelKindGemini:
		return cfg.GeminiUpstream, nil
	case ChannelKindChat:
		return cfg.ChatUpstream, nil
	case ChannelKindImages:
		return cfg.ImagesUpstream, nil
	default:
		return nil, contractError(ErrorCodeInvalidChannelKind, ErrorCategoryTarget, fmt.Sprintf("无效的渠道类型 %q", kind))
	}
}
