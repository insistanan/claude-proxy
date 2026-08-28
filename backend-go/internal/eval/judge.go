package eval

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type JudgeInput struct {
	Probe          Probe
	RequestedModel string
	Samples        []Extracted
}

func Judge(input JudgeInput) (verdict string, excerpt string, detail map[string]interface{}) {
	detail = map[string]interface{}{}
	if len(input.Samples) == 0 {
		return VerdictInsufficient, "", map[string]interface{}{"reason": "没有抽出任何样本"}
	}
	first := input.Samples[0]
	excerpt = first.Text
	if first.SVG != "" {
		excerpt = first.SVG
	}
	if len(excerpt) > 800 {
		excerpt = excerpt[:800] + "..."
	}

	switch input.Probe.Judge.Kind {
	case JudgeExact:
		return judgeExact(input.Probe.Judge, first, excerpt)
	case JudgeRegex:
		return judgeRegex(input.Probe.Judge, first, excerpt)
	case JudgeNumeric:
		return judgeNumeric(input.Probe.Judge, first, excerpt)
	case JudgeProtocol:
		return judgeProtocol(input.Probe.Judge, first, input.RequestedModel, excerpt)
	case JudgeSVG:
		return judgeSVG(first, excerpt)
	case JudgeDistribution:
		return judgeDistribution(input.Probe.Judge, input.Samples, excerpt)
	case JudgeObserve:
		return judgeObserve(input.Probe.Judge, first, excerpt)
	case JudgeRubric:
		return VerdictError, excerpt, map[string]interface{}{"reason": "rubric 必须走信封自评，不能在本地判定"}
	default:
		return VerdictError, excerpt, map[string]interface{}{"reason": "未知判定方式 " + input.Probe.Judge.Kind}
	}
}

func judgeExact(spec JudgeSpec, sample Extracted, excerpt string) (string, string, map[string]interface{}) {
	got := strings.TrimSpace(sample.Text)
	want := strings.TrimSpace(spec.Expected)
	if spec.CaseInsensitive {
		got = strings.ToLower(got)
		want = strings.ToLower(want)
	}
	if got == want || strings.Contains(got, want) {
		return VerdictPass, excerpt, map[string]interface{}{"expected": spec.Expected, "got": sample.Text}
	}
	return VerdictFail, excerpt, map[string]interface{}{"expected": spec.Expected, "got": sample.Text}
}

func judgeRegex(spec JudgeSpec, sample Extracted, excerpt string) (string, string, map[string]interface{}) {
	pattern, err := regexp.Compile(spec.Pattern)
	if err != nil {
		return VerdictError, excerpt, map[string]interface{}{"reason": err.Error()}
	}
	if pattern.MatchString(sample.Text) {
		return VerdictPass, excerpt, map[string]interface{}{"pattern": spec.Pattern}
	}
	return VerdictFail, excerpt, map[string]interface{}{"pattern": spec.Pattern, "got": sample.Text}
}

func judgeNumeric(spec JudgeSpec, sample Extracted, excerpt string) (string, string, map[string]interface{}) {
	want := spec.ExpectedNumber
	if spec.Expected != "" {
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(spec.Expected), 64); err == nil {
			want = parsed
		}
	}
	if len(sample.Numbers) == 0 {
		return VerdictFail, excerpt, map[string]interface{}{"expected": want, "got": sample.Text}
	}
	for _, number := range sample.Numbers {
		if math.Abs(number-want) < 1e-6 {
			return VerdictPass, excerpt, map[string]interface{}{"expected": want, "got": number}
		}
	}
	return VerdictFail, excerpt, map[string]interface{}{"expected": want, "got": sample.Numbers}
}

func judgeProtocol(spec JudgeSpec, sample Extracted, requestedModel, excerpt string) (string, string, map[string]interface{}) {
	detail := map[string]interface{}{
		"model":        sample.Model,
		"contentTypes": sample.ContentTypes,
		"hasThinking":  sample.HasThinking,
		"hasSignature": sample.HasSignature,
		"hasUsage":     sample.HasUsage,
	}
	if spec.ExpectUsage && !sample.HasUsage {
		return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "响应缺少 usage")
	}
	if len(spec.ExpectContentTypes) > 0 {
		for _, expected := range spec.ExpectContentTypes {
			if !containsString(sample.ContentTypes, expected) {
				return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "缺少内容类型 "+expected)
			}
		}
	}
	if spec.ExpectModelEcho {
		if strings.TrimSpace(sample.Model) == "" {
			return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "响应没有 model 字段")
		}
		if requestedModel != "" && !strings.EqualFold(strings.TrimSpace(sample.Model), strings.TrimSpace(requestedModel)) {
			detail["requestedModel"] = requestedModel
			detail["reason"] = "回显模型与请求模型不一致"
			return VerdictSuspect, excerpt, detail
		}
	}
	switch spec.ExpectThinking {
	case "required":
		if !sample.HasThinking {
			return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "未观察到 thinking")
		}
	case "forbidden":
		if sample.HasThinking {
			return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "出现了未预期的 thinking")
		}
	}
	switch spec.ExpectSignature {
	case "present":
		if !sample.HasSignature {
			// 缺签名不等于假 Claude，只标存疑
			return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "thinking 块没有 signature")
		}
	case "absent":
		if sample.HasSignature {
			return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "出现了未预期的 signature")
		}
	}
	return VerdictPass, excerpt, detail
}

func judgeSVG(sample Extracted, excerpt string) (string, string, map[string]interface{}) {
	svg := sample.SVG
	if svg == "" {
		return VerdictFail, excerpt, map[string]interface{}{"reason": "没有 SVG"}
	}
	lower := strings.ToLower(svg)
	if !strings.Contains(lower, "<svg") || !strings.Contains(lower, "</svg>") {
		return VerdictFail, excerpt, map[string]interface{}{"reason": "SVG 标签不完整"}
	}
	return VerdictPass, excerpt, map[string]interface{}{"svg": svg}
}

func judgeDistribution(spec JudgeSpec, samples []Extracted, excerpt string) (string, string, map[string]interface{}) {
	var numbers []float64
	for _, sample := range samples {
		numbers = append(numbers, sample.Numbers...)
	}
	detail := map[string]interface{}{"numbers": numbers, "sampleCount": len(samples)}
	minSamples := spec.MinSamples
	if minSamples <= 0 {
		minSamples = 2
	}
	if len(numbers) < minSamples {
		return VerdictInsufficient, excerpt, mergeDetail(detail, "reason", fmt.Sprintf("样本不足：%d < %d", len(numbers), minSamples))
	}
	unique := uniqueFloats(numbers)
	detail["uniqueCount"] = len(unique)
	if spec.FailIfIdentical && len(unique) == 1 {
		return VerdictFail, excerpt, mergeDetail(detail, "reason", "全部样本是同一个数")
	}
	ratio := float64(len(unique)) / float64(len(numbers))
	detail["uniqueRatio"] = ratio
	threshold := spec.SuspectUniqueRatio
	if threshold <= 0 {
		threshold = 0.2
	}
	if ratio < threshold {
		return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "随机数过于集中")
	}
	return VerdictPass, excerpt, detail
}

// judgeObserve 观测思考用量。
// 只判"有没有思考 token"这个跨厂商都成立的信号，不判占比：
// output 含不含思考各家口径不一（Anthropic/OpenAI 含、xAI 不含、Gemini 文档自相矛盾），
// 中转还会改写 usage，用占比设阈值等于给自己造误报。占比只写进 detail 供人参考。
func judgeObserve(spec JudgeSpec, sample Extracted, excerpt string) (string, string, map[string]interface{}) {
	detail := map[string]interface{}{
		"outputTokens":     sample.OutputTokens,
		"thinkingTokens":   sample.ThinkingTokens,
		"hasUsage":         sample.HasUsage,
		"hasThinkingUsage": sample.HasThinkingUsage,
		"hasThinking":      sample.HasThinking,
	}
	if total := sample.OutputTokens + sample.ThinkingTokens; sample.HasThinkingUsage && total > 0 {
		detail["thinkingRatio"] = sample.ThinkingTokens / total
		detail["thinkingRatioNote"] = "分母是否已含思考随上游而异，仅供参考，不参与判定"
	}

	if !spec.ExpectThinkingUsage {
		return VerdictPass, excerpt, detail
	}
	if !sample.HasUsage {
		return VerdictInsufficient, excerpt, mergeDetail(detail, "reason", "响应没有 usage，拿不到思考用量")
	}
	if !sample.HasThinkingUsage {
		// 拿不到 ≠ 用量为 0。中转丢字段很常见，判存疑会误伤一大片。
		return VerdictInsufficient, excerpt, mergeDetail(detail, "reason", "上游没回报思考 token，无法核对")
	}
	if sample.ThinkingTokens <= 0 {
		return VerdictSuspect, excerpt, mergeDetail(detail, "reason", "请求开了思考，但思考用量为 0")
	}
	return VerdictPass, excerpt, detail
}

func HistogramSimilarity(left, right []float64) float64 {
	const bins = 100
	leftHist := histogram(left, bins)
	rightHist := histogram(right, bins)
	dot := 0.0
	leftNorm := 0.0
	rightNorm := 0.0
	for i := 0; i < bins; i++ {
		dot += leftHist[i] * rightHist[i]
		leftNorm += leftHist[i] * leftHist[i]
		rightNorm += rightHist[i] * rightHist[i]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func histogram(values []float64, bins int) []float64 {
	hist := make([]float64, bins)
	for _, value := range values {
		index := int(value) - 1
		if index < 0 {
			index = 0
		}
		if index >= bins {
			index = bins - 1
		}
		hist[index]++
	}
	return hist
}

func uniqueFloats(values []float64) []float64 {
	seen := map[float64]struct{}{}
	var unique []float64
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func mergeDetail(detail map[string]interface{}, key string, value interface{}) map[string]interface{} {
	detail[key] = value
	return detail
}
