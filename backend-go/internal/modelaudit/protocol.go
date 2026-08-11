package modelaudit

import (
	"fmt"
	"sort"
)

type SupportLevel string

const (
	SupportOfficial       SupportLevel = "official"
	SupportChannelProfile SupportLevel = "channel_profile"
	SupportUnsupported    SupportLevel = "unsupported"
)

type FeatureSupport struct {
	Level SupportLevel `json:"level"`
	Note  string       `json:"note,omitempty"`
}

type ThinkingMapping struct {
	Level        ThinkingLevel `json:"level"`
	Support      SupportLevel  `json:"support"`
	Parameter    string        `json:"parameter"`
	Mode         string        `json:"mode,omitempty"`
	Effort       string        `json:"effort,omitempty"`
	BudgetTokens *int          `json:"budgetTokens,omitempty"`
}

type ThinkingCapability struct {
	Support  SupportLevel      `json:"support"`
	Mappings []ThinkingMapping `json:"mappings"`
	Note     string            `json:"note,omitempty"`
}

type RequestProfileDescriptor struct {
	ID      string       `json:"id"`
	Support SupportLevel `json:"support"`
	Source  string       `json:"source"`
}

type ProtocolDescriptor struct {
	Protocol          Protocol                   `json:"protocol"`
	ChannelKind       ChannelKind                `json:"channelKind"`
	Streaming         FeatureSupport             `json:"streaming"`
	Tools             FeatureSupport             `json:"tools"`
	Images            FeatureSupport             `json:"images"`
	MultiTurn         FeatureSupport             `json:"multiTurn"`
	Thinking          ThinkingCapability         `json:"thinking"`
	Profiles          []RequestProfileDescriptor `json:"profiles"`
	CompletionSignals []string                   `json:"completionSignals"`
}

var protocolDescriptors = map[Protocol]ProtocolDescriptor{
	ProtocolMessages: {
		Protocol:    ProtocolMessages,
		ChannelKind: ChannelKindMessages,
		Streaming:   FeatureSupport{Level: SupportOfficial},
		Tools:       FeatureSupport{Level: SupportOfficial},
		Images:      FeatureSupport{Level: SupportOfficial},
		MultiTurn:   FeatureSupport{Level: SupportChannelProfile, Note: "由客户端历史或渠道兼容轮廓表达"},
		Thinking: ThinkingCapability{
			Support:  SupportOfficial,
			Mappings: budgetMappings("thinking", false),
			Note:     "档位映射为 Claude thinking.type 与 budget_tokens；渠道轮廓可以进一步收窄",
		},
		Profiles: []RequestProfileDescriptor{
			{ID: "messages.standard.v1", Support: SupportOfficial, Source: "anthropic_messages"},
		},
		CompletionSignals: []string{"message_stop", "error"},
	},
	ProtocolResponses: {
		Protocol:    ProtocolResponses,
		ChannelKind: ChannelKindResponses,
		Streaming:   FeatureSupport{Level: SupportOfficial},
		Tools:       FeatureSupport{Level: SupportOfficial},
		Images:      FeatureSupport{Level: SupportOfficial},
		MultiTurn:   FeatureSupport{Level: SupportOfficial, Note: "previous_response_id 或显式上下文重放"},
		Thinking: ThinkingCapability{
			Support:  SupportOfficial,
			Mappings: effortMappings("reasoning.effort"),
			Note:     "具体模型可用档位由渠道轮廓进一步限制",
		},
		Profiles: []RequestProfileDescriptor{
			{ID: "responses.standard.v1", Support: SupportOfficial, Source: "openai_responses"},
			{ID: "responses.codex_compatible.v1", Support: SupportChannelProfile, Source: "project_compatibility"},
			{ID: "responses.native_codex.v1", Support: SupportChannelProfile, Source: "project_compatibility"},
			{ID: "responses.multi_turn.v1", Support: SupportOfficial, Source: "openai_responses"},
		},
		CompletionSignals: []string{"response.completed", "response.failed", "response.incomplete"},
	},
	ProtocolChat: {
		Protocol:    ProtocolChat,
		ChannelKind: ChannelKindChat,
		Streaming:   FeatureSupport{Level: SupportOfficial},
		Tools:       FeatureSupport{Level: SupportOfficial},
		Images:      FeatureSupport{Level: SupportOfficial},
		MultiTurn:   FeatureSupport{Level: SupportChannelProfile, Note: "由客户端历史表达"},
		Thinking: ThinkingCapability{
			Support:  SupportChannelProfile,
			Mappings: effortMappings("reasoning_effort"),
			Note:     "OpenAI-compatible 渠道必须声明实际支持的 reasoning_effort 档位",
		},
		Profiles: []RequestProfileDescriptor{
			{ID: "chat.standard.v1", Support: SupportOfficial, Source: "openai_chat_completions"},
		},
		CompletionSignals: []string{"choices.finish_reason", "error"},
	},
	ProtocolGemini: {
		Protocol:    ProtocolGemini,
		ChannelKind: ChannelKindGemini,
		Streaming:   FeatureSupport{Level: SupportOfficial},
		Tools:       FeatureSupport{Level: SupportOfficial},
		Images:      FeatureSupport{Level: SupportOfficial},
		MultiTurn:   FeatureSupport{Level: SupportChannelProfile, Note: "contents 历史或渠道 interaction 轮廓"},
		Thinking: ThinkingCapability{
			Support:  SupportOfficial,
			Mappings: budgetMappings("generationConfig.thinkingConfig.thinkingBudget", true),
			Note:     "adaptive 映射为 thinkingBudget=-1；模型轮廓可以限制预算范围",
		},
		Profiles: []RequestProfileDescriptor{
			{ID: "gemini.standard.v1", Support: SupportOfficial, Source: "gemini_generate_content"},
		},
		CompletionSignals: []string{"candidates.finishReason", "error"},
	},
	ProtocolImages: {
		Protocol:    ProtocolImages,
		ChannelKind: ChannelKindImages,
		Streaming:   FeatureSupport{Level: SupportUnsupported},
		Tools:       FeatureSupport{Level: SupportUnsupported},
		Images:      FeatureSupport{Level: SupportOfficial, Note: "edit 和 variation 轮廓可包含源图片"},
		MultiTurn:   FeatureSupport{Level: SupportUnsupported},
		Thinking: ThinkingCapability{
			Support: SupportUnsupported,
			Note:    "Images 协议不接受思考档位",
		},
		Profiles: []RequestProfileDescriptor{
			{ID: "images.generation.v1", Support: SupportOfficial, Source: "openai_images"},
			{ID: "images.edit.v1", Support: SupportOfficial, Source: "openai_images"},
			{ID: "images.variation.v1", Support: SupportOfficial, Source: "openai_images"},
		},
		CompletionSignals: []string{"image_result", "error"},
	},
}

func budgetMappings(parameter string, adaptive bool) []ThinkingMapping {
	minimal := 1024
	low := 2048
	medium := 8192
	high := 16384
	xhigh := 32768
	mappings := []ThinkingMapping{
		{Level: ThinkingOff, Support: SupportOfficial, Parameter: parameter, Mode: "disabled"},
		{Level: ThinkingMinimal, Support: SupportChannelProfile, Parameter: parameter, Mode: "enabled", BudgetTokens: &minimal},
		{Level: ThinkingLow, Support: SupportChannelProfile, Parameter: parameter, Mode: "enabled", BudgetTokens: &low},
		{Level: ThinkingMedium, Support: SupportChannelProfile, Parameter: parameter, Mode: "enabled", BudgetTokens: &medium},
		{Level: ThinkingHigh, Support: SupportChannelProfile, Parameter: parameter, Mode: "enabled", BudgetTokens: &high},
		{Level: ThinkingXHigh, Support: SupportChannelProfile, Parameter: parameter, Mode: "enabled", BudgetTokens: &xhigh},
	}
	if adaptive {
		off := 0
		auto := -1
		mappings[0].Mode = "enabled"
		mappings[0].BudgetTokens = &off
		mappings = append(mappings, ThinkingMapping{
			Level: ThinkingAdaptive, Support: SupportOfficial, Parameter: parameter, Mode: "enabled", BudgetTokens: &auto,
		})
	}
	return mappings
}

func effortMappings(parameter string) []ThinkingMapping {
	return []ThinkingMapping{
		{Level: ThinkingOff, Support: SupportChannelProfile, Parameter: parameter, Effort: "none"},
		{Level: ThinkingMinimal, Support: SupportOfficial, Parameter: parameter, Effort: "minimal"},
		{Level: ThinkingLow, Support: SupportOfficial, Parameter: parameter, Effort: "low"},
		{Level: ThinkingMedium, Support: SupportOfficial, Parameter: parameter, Effort: "medium"},
		{Level: ThinkingHigh, Support: SupportOfficial, Parameter: parameter, Effort: "high"},
		{Level: ThinkingXHigh, Support: SupportChannelProfile, Parameter: parameter, Effort: "xhigh"},
	}
}

func ProtocolDescriptors() []ProtocolDescriptor {
	protocols := make([]Protocol, 0, len(protocolDescriptors))
	for protocol := range protocolDescriptors {
		protocols = append(protocols, protocol)
	}
	sort.Slice(protocols, func(i, j int) bool { return protocols[i] < protocols[j] })

	out := make([]ProtocolDescriptor, 0, len(protocols))
	for _, protocol := range protocols {
		descriptor := protocolDescriptors[protocol]
		descriptor.Profiles = append([]RequestProfileDescriptor(nil), descriptor.Profiles...)
		descriptor.CompletionSignals = append([]string(nil), descriptor.CompletionSignals...)
		descriptor.Thinking.Mappings = append([]ThinkingMapping(nil), descriptor.Thinking.Mappings...)
		out = append(out, descriptor)
	}
	return out
}

func DescribeProtocol(protocol Protocol) (ProtocolDescriptor, error) {
	descriptor, ok := protocolDescriptors[protocol]
	if !ok {
		return ProtocolDescriptor{}, contractError(
			ErrorCodeInvalidProtocol,
			ErrorCategoryRequest,
			fmt.Sprintf("无效的请求协议 %q", protocol),
		)
	}
	descriptor.Profiles = append([]RequestProfileDescriptor(nil), descriptor.Profiles...)
	descriptor.CompletionSignals = append([]string(nil), descriptor.CompletionSignals...)
	descriptor.Thinking.Mappings = append([]ThinkingMapping(nil), descriptor.Thinking.Mappings...)
	return descriptor, nil
}

func ResolveThinking(protocol Protocol, level ThinkingLevel) (*ThinkingMapping, error) {
	if level == ThinkingUnset {
		return nil, nil
	}
	descriptor, err := DescribeProtocol(protocol)
	if err != nil {
		return nil, err
	}
	for i := range descriptor.Thinking.Mappings {
		if descriptor.Thinking.Mappings[i].Level == level {
			mapping := descriptor.Thinking.Mappings[i]
			return &mapping, nil
		}
	}
	return nil, contractError(
		ErrorCodeUnsupported,
		ErrorCategoryUnsupported,
		fmt.Sprintf("协议 %q 不支持思考档位 %q", protocol, level),
	)
}

func ValidateSpecCapabilities(spec ExecutionSpec) error {
	descriptor, err := DescribeProtocol(spec.Protocol)
	if err != nil {
		return err
	}
	profileFound := false
	for _, profile := range descriptor.Profiles {
		if profile.ID == spec.RequestProfile {
			profileFound = true
			break
		}
	}
	if !profileFound {
		return contractError(
			ErrorCodeUnsupported,
			ErrorCategoryUnsupported,
			fmt.Sprintf("协议 %q 不支持请求轮廓 %q", spec.Protocol, spec.RequestProfile),
		)
	}
	if spec.Stream && descriptor.Streaming.Level == SupportUnsupported {
		return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, fmt.Sprintf("协议 %q 不支持流式执行", spec.Protocol))
	}
	if spec.Features.Tools && descriptor.Tools.Level == SupportUnsupported {
		return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, fmt.Sprintf("协议 %q 不支持工具调用", spec.Protocol))
	}
	if spec.Features.Images && descriptor.Images.Level == SupportUnsupported {
		return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, fmt.Sprintf("协议 %q 不支持图片输入", spec.Protocol))
	}
	if _, err := ResolveThinking(spec.Protocol, spec.Thinking); err != nil {
		return err
	}
	return nil
}
