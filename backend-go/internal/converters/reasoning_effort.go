package converters

import "fmt"

// normalizeReasoningEffortForConstrainedUpstream 将 Codex 专有的高等级映射到转换上游可表达的最高等级。
func normalizeReasoningEffortForConstrainedUpstream(effort string) (string, error) {
	switch effort {
	case "", "none", "auto", "minimal", "low", "medium", "high", "xhigh":
		return effort, nil
	case "max", "ultra":
		return "xhigh", nil
	default:
		return "", fmt.Errorf("不支持的 reasoning.effort=%q", effort)
	}
}
