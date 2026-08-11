package common

import "strings"

func payloadProtocolForServiceType(serviceType, fallback string) string {
	fallback = strings.ToLower(strings.TrimSpace(fallback))
	// Chat 渠道始终透传 Chat Completions 载荷，不按 ServiceType 转换。
	if fallback == "chat" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(serviceType)) {
	case "claude":
		return "messages"
	case "openai":
		return "chat"
	case "responses":
		return "responses"
	case "gemini":
		return "gemini"
	default:
		return fallback
	}
}
