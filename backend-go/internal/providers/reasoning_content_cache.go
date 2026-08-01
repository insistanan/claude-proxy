package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

// reasoningContentCache 保存 "assistant 文本指纹 → reasoning_content" 的进程内缓存。
//
// 背景：Chat 推理模型（如 DeepSeek）要求将 thinking 模式产生的 reasoning_content
// 原样随 assistant 历史消息回传，否则下一轮返回 400
// （"The reasoning_content in the thinking mode must be passed back to the API."，
// Claude 兼容网关则报 "The content[].thinking in the thinking mode must be passed back..."）。
//
// 代理把上游 reasoning_content 转成 Claude thinking 块返回客户端后，
// 客户端（Claude Code / Cursor）回传历史时往往把明文 thinking 转成 redacted_thinking
// 或直接丢弃，导致 messages→chat 转换时无法回传 reasoning_content。
// 该缓存把每条 assistant 响应的（文本指纹 → reasoning_content）暂存，
// 在下一轮转换 assistant 历史消息缺失明文 thinking 时自动补回。
type reasoningContentCache struct {
	mu      sync.RWMutex
	entries map[string]string
	order   []string // 插入顺序，用于容量淘汰（FIFO）
	max     int
}

// reasoningCache 全局缓存实例。GetProvider 每次返回新的 OpenAIProvider 实例，
// 因此缓存必须挂在包级，不能放在 provider 实例上。
var reasoningCache = newReasoningContentCache(512)

func newReasoningContentCache(max int) *reasoningContentCache {
	if max <= 0 {
		max = 512
	}
	return &reasoningContentCache{
		entries: make(map[string]string),
		max:     max,
	}
}

func (c *reasoningContentCache) put(key, value string) {
	if c == nil || key == "" || value == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		c.order = append(c.order, key)
	}
	c.entries[key] = value
	for len(c.order) > c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *reasoningContentCache) get(key string) string {
	if c == nil || key == "" {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.entries[key]
}

// reset 仅供测试清理全局状态。
func (c *reasoningContentCache) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]string)
	c.order = nil
}

// reasoningFingerprint 生成 assistant 文本的指纹，作为缓存键。
// 使用 sha256 前 24 位十六进制，避免用完整文本做键占用内存。
func reasoningFingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:24]
}

// storeReasoning 保存一条 assistant 文本与其 reasoning_content 的映射。
// reasoning 为空或文本为空时不保存（无法作为回传键）。
func storeReasoning(text, reasoning string) {
	if strings.TrimSpace(text) == "" || reasoning == "" {
		return
	}
	reasoningCache.put(reasoningFingerprint(text), reasoning)
}

// lookupReasoning 查询指定 assistant 文本对应的 reasoning_content。
// 仅用于客户端回传历史时缺失明文 thinking 的兜底补回。
func lookupReasoning(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return reasoningCache.get(reasoningFingerprint(text))
}

// clearReasoningContentCacheForTest 清空全局缓存，仅供测试使用。
func clearReasoningContentCacheForTest() {
	reasoningCache.reset()
}
