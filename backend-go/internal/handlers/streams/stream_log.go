// 本文件负责流式响应结束时的收尾：logStreamCompletion 输出完成/事件统计日志、
// 触发隐式缓存推断，并把 StreamContext 累积的 usage 组装成 *types.Usage 返回；
// logPartialResponse 处理上游中断时的部分内容日志；logSynthesizedContent 打印
// StreamSynthesizer 的合成文本（合成不可用时回退打印原始 LogBuffer）。
package streams

import (
	"log"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/types"
)

// logStreamCompletion 记录流完成日志
func logStreamCompletion(ctx *StreamContext, envCfg *config.EnvConfig, startTime time.Time) *types.Usage {
	if envCfg.EnableResponseLogs {
		log.Printf("[Messages-Stream] 流式响应完成: %dms", time.Since(startTime).Milliseconds())
	}

	// SSE 事件统计日志
	if envCfg.SSEDebugLevel == "full" || envCfg.SSEDebugLevel == "summary" {
		blockTypeSummary := make(map[string]int)
		for _, bt := range ctx.ContentBlockTypes {
			blockTypeSummary[bt]++
		}
		log.Printf("[Messages-Stream-Summary] 总事件数=%d, content_blocks=%d, 类型分布=%v",
			ctx.EventCount, ctx.ContentBlockCount, blockTypeSummary)
	}

	if envCfg.IsDevelopment() {
		logSynthesizedContent(ctx)
	}

	// 推断隐式缓存读取
	inferImplicitCacheRead(ctx, envCfg.EnableResponseLogs && envCfg.ShouldLog("debug"))

	// 将累积的 usage 数据转换为 *types.Usage
	var usage *types.Usage
	hasUsageData := ctx.CollectedUsage.InputTokens > 0 ||
		ctx.CollectedUsage.OutputTokens > 0 ||
		ctx.CollectedUsage.CacheCreationInputTokens > 0 ||
		ctx.CollectedUsage.CacheReadInputTokens > 0 ||
		ctx.CollectedUsage.CacheCreation5mInputTokens > 0 ||
		ctx.CollectedUsage.CacheCreation1hInputTokens > 0
	if hasUsageData {
		usage = &types.Usage{
			InputTokens:                ctx.CollectedUsage.InputTokens,
			OutputTokens:               ctx.CollectedUsage.OutputTokens,
			CacheCreationInputTokens:   ctx.CollectedUsage.CacheCreationInputTokens,
			CacheReadInputTokens:       ctx.CollectedUsage.CacheReadInputTokens,
			CacheCreation5mInputTokens: ctx.CollectedUsage.CacheCreation5mInputTokens,
			CacheCreation1hInputTokens: ctx.CollectedUsage.CacheCreation1hInputTokens,
			CacheTTL:                   ctx.CollectedUsage.CacheTTL,
		}
	}
	return usage
}

// logPartialResponse 记录部分响应日志
func logPartialResponse(ctx *StreamContext, envCfg *config.EnvConfig) {
	if envCfg.EnableResponseLogs && envCfg.IsDevelopment() {
		logSynthesizedContent(ctx)
	}
}

// logSynthesizedContent 记录合成内容
func logSynthesizedContent(ctx *StreamContext) {
	if ctx.Synthesizer != nil {
		content := ctx.Synthesizer.GetSynthesizedContent()
		if content != "" && !ctx.Synthesizer.IsParseFailed() {
			trimmed := strings.TrimSpace(content)

			// 仅在“明显是 JSON 续写”的情况下拼接预置前缀，避免出现 "{OK" 这类误导日志
			if ctx.LogPrefillText == "{" && !strings.HasPrefix(strings.TrimLeft(trimmed, " \t\r\n"), "{") {
				left := strings.TrimLeft(trimmed, " \t\r\n")
				if strings.HasPrefix(left, "\"") {
					trimmed = ctx.LogPrefillText + trimmed
				}
			}

			log.Printf("[Messages-Stream] 上游流式响应合成内容:\n%s", strings.TrimSpace(trimmed))
			return
		}
	}
	if ctx.LogBuffer.Len() > 0 {
		log.Printf("[Messages-Stream] 上游流式响应原始内容:\n%s", ctx.LogBuffer.String())
	}
}
