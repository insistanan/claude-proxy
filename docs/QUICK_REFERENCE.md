# 快速参考：模型后缀支持

## 快速开始

### Claude Code 配置

```bash
# 设置使用 1M 上下文的模型
export ANTHROPIC_MODEL=opus[1m]
export ANTHROPIC_SMALL_FAST_MODEL=sonnet[1m]

# 或使用完整模型名
export ANTHROPIC_MODEL=claude-opus-4-8[1m]
```

### 代理配置示例

```json
{
  "messagesUpstream": [
    {
      "name": "DeepSeek",
      "baseURLs": ["https://api.deepseek.com"],
      "apiKeys": ["sk-xxx"],
      "modelMapping": {
        "opus": "deepseek-v4-pro",
        "sonnet": "deepseek-chat",
        "haiku": "deepseek-chat",
        "fable": "deepseek-reasoner"
      }
    }
  ]
}
```

## 工作流程

```
客户端请求: opus[1m]
    ↓
代理接收并剥离后缀: opus
    ↓
模型映射: opus → deepseek-v4-pro
    ↓
发送到上游: deepseek-v4-pro
```

## 支持的模型

- `opus[1m]` / `opus` - Claude Opus（高性能推理）
- `sonnet[1m]` / `sonnet` - Claude Sonnet（日常编码）
- `haiku[1m]` / `haiku` - Claude Haiku（快速高效）
- `fable[1m]` / `fable` - Claude Fable 5（复杂任务）
- `gpt` - OpenAI GPT 系列
- `codex` - OpenAI Codex 系列
- `gemini` - Google Gemini 系列

## API 请求示例

```bash
curl -X POST http://localhost:3000/v1/messages \
  -H "x-api-key: your-proxy-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "opus[1m]",
    "max_tokens": 1024,
    "messages": [
      {"role": "user", "content": "Hello!"}
    ]
  }'
```

## 日志

当检测到后缀时：

```
[Model-Suffix] 检测到上下文窗口后缀: opus[1m] -> opus
```

## 详细文档

- [完整文档](MODEL_SUFFIX_HANDLING.md) - 详细说明和高级用法
- [更新总结](UPDATE_SUMMARY.md) - 代码变更和测试覆盖

## 测试

```bash
cd backend-go
go test -v ./internal/config -run TestStripContextSuffix
go test -v ./internal/config -run TestResolveUpstreamModelWithSuffix
```

## 兼容性

✅ Claude Code  
✅ Cursor  
✅ 向后兼容（无后缀请求）  
✅ 所有现有模型
