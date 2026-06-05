package converters

import "fmt"

// ConverterFactory 转换器工厂
// 根据上游服务类型返回对应的转换器实例

// NewConverter 创建旧结构化转换器实例。
// 保留默认 openai 行为用于兼容既有单测和历史调用；Responses 主链路使用 responses_protocol.go 的协议分发。
// serviceType: "openai", "claude", "responses"
func NewConverter(serviceType string) ResponsesConverter {
	switch serviceType {
	case "openai":
		return &OpenAIChatConverter{}
	case "claude":
		return &ClaudeConverter{}
	case "responses":
		return &ResponsesPassthroughConverter{}
	default:
		// 默认使用 OpenAI Chat 转换器
		return &OpenAIChatConverter{}
	}
}

// NewConverterStrict 创建旧结构化转换器实例，未知 serviceType 显式返回错误。
func NewConverterStrict(serviceType string) (ResponsesConverter, error) {
	switch serviceType {
	case "openai":
		return &OpenAIChatConverter{}, nil
	case "claude":
		return &ClaudeConverter{}, nil
	case "responses":
		return &ResponsesPassthroughConverter{}, nil
	case "gemini":
		return nil, fmt.Errorf("Responses -> Gemini 使用 responses_protocol.go 直接分发，不支持旧结构化转换器")
	default:
		return nil, fmt.Errorf("Responses 上游 serviceType %q 不支持", serviceType)
	}
}
