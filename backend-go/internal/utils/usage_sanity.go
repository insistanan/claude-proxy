package utils

// 上游 usage 合理性校验。
//
// 背景：代理已有完整的「语义转换」框架（各家 usage 口径互转，见 converters / providers），
// 但没有任何一条路径校验过「上游回报的数字与代理实际发出去的请求是否对得上」。
// 实测中转渠道会双向乱报，同一分钟内相邻两次请求：
//
//	448KB / 340 条消息  →  上游报 input_tokens=26032   （少报约 5 倍）
//	 24KB /   2 条消息  →  上游报 input_tokens=209736  （多报约 30 倍）
//
// 客户端全都拿这个数字决定何时压缩上下文（Claude Code 约 95% 容量、Codex
// model_auto_compact_token_limit、OpenCode isOverflow、Cursor 200K），所以假值会直接
// 表现为「压缩后立刻又要压缩」（多报）或「撞满上限也不压缩」（少报）。
//
// Anthropic 官方文档亦记载过同类现象：cache_read_input_tokens 可能包含多次内部调用的
// 累积读取而非真实上下文，导致 SDK 过早触发压缩，官方建议是另行测量真实上下文长度。
// 本文件即扮演这个「另行测量」的角色——用本地估算做交叉验证，代替额外的 token 计数请求。

const (
	// usageSanityRatio 触发校正所需的偏差倍数。
	// 本地估算是字符数近似（CJK 1.5、其他 3.5 字符/token），对正常上游有 ±30% 误差，
	// 2 倍留足空间：正常渠道永远够不到这条线，只有数量级级别的错报才会被判定。
	usageSanityRatio = 2

	// usageSanityMinTokens 介入所需的最小量级。
	// 只处理「长上下文被错报」这一类——小对话本来就不会触发任何客户端的压缩阈值，
	// 错报也无害，不值得为它承担估算误差的风险。
	usageSanityMinTokens = 20000
)

// SanityCheckedInputTokens 交叉验证上游回报的上下文规模，返回应当下发给客户端的 input_tokens。
//
// estimatedTotal 是代理本地估算的真实上下文总规模（含缓存部分，即整个请求体的量）。
// upstreamInput 是上游回报的 input_tokens。
// upstreamCached 是上游**单独回报、且不包含在 input_tokens 内**的缓存 token 总量：
//   - Anthropic 语义（input_tokens 是未缓存余量）：传 cache_read + cache_creation 各档之和。
//   - OpenAI 语义（cached_tokens 已含在 input_tokens 内）：传 0，否则会把缓存算两遍。
//
// 第二个返回值为 false 表示上游数据可信，调用方必须原样保留上游值。
// 返回值按 Anthropic 语义给出（不含缓存部分），使 input + cached 之和回到真实规模，
// 不与调用方保留的缓存字段重叠。
func SanityCheckedInputTokens(estimatedTotal, upstreamInput, upstreamCached int) (int, bool) {
	if estimatedTotal <= 0 {
		return 0, false
	}
	if upstreamInput < 0 {
		upstreamInput = 0
	}
	if upstreamCached < 0 {
		upstreamCached = 0
	}
	upstreamTotal := upstreamInput + upstreamCached

	// 量级门槛按双边取：多报场景下真实上下文很小（估算够不到门槛），
	// 但上游报出的假大值本身就是需要拦截的对象，只看估算侧会漏掉整类问题。
	if estimatedTotal < usageSanityMinTokens && upstreamTotal < usageSanityMinTokens {
		return 0, false
	}

	// 双向判定：上游漏报（含完全没报）与上游虚报都会误导客户端的压缩决策，
	// 只挡一个方向等于只修一半。
	underReported := upstreamTotal == 0 || estimatedTotal > upstreamTotal*usageSanityRatio
	overReported := upstreamTotal > estimatedTotal*usageSanityRatio
	if !underReported && !overReported {
		return 0, false
	}

	corrected := estimatedTotal - upstreamCached
	if corrected < 0 {
		corrected = 0
	}
	if corrected == upstreamInput {
		return 0, false
	}
	return corrected, true
}
