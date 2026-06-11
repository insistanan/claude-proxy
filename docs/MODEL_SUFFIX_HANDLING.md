# 模型名称后缀处理

## 概述

本项目现已支持 Claude Code 的 `[1m]` 上下文窗口后缀，允许客户端指定使用 1M token 上下文窗口而非默认的 200K。

## 背景

Claude Code 使用模型名称后缀来标识上下文窗口大小：

- **无后缀**: 使用默认 200K token 上下文窗口
- **[1m] 后缀**: 使用 1M token 上下文窗口

例如：
- `opus` → 200K 上下文
- `opus[1m]` → 1M 上下文
- `claude-opus-4-8` → 200K 上下文
- `claude-opus-4-8[1m]` → 1M 上下文

根据 Claude Code 官方文档，客户端在发送请求到上游提供商之前应该**剥离**这个后缀，因为上游 API 不认识这个后缀格式。

## 实现细节

### 1. 新增 Fable 模型支持

在静态模型列表中添加了 `fable`：

```go
// backend-go/internal/modelcatalog/catalog.go
func staticFamilyModels() []ModelEntry {
    ids := []string{
        "opus",
        "sonnet",
        "haiku",
        "fable",  // 新增
        "gpt",
        "codex",
        "gemini",
    }
    // ...
}
```

### 2. 后缀剥离函数

在 `backend-go/internal/config/config_utils.go` 中添加了 `StripContextSuffix` 函数：

```go
// StripContextSuffix 剥离 Claude Code 的上下文窗口后缀（如 [1m]）
// 返回：(原始模型名, 是否有后缀)
func StripContextSuffix(model string) (string, bool) {
    model = strings.TrimSpace(model)
    if strings.HasSuffix(model, "[1m]") {
        return strings.TrimSuffix(model, "[1m]"), true
    }
    return model, false
}
```

### 3. 集成到模型解析流程

在 `ResolveUpstreamModel` 函数中集成后缀处理：

```go
func ResolveUpstreamModel(model string, upstream *UpstreamConfig) string {
    model = strings.TrimSpace(model)
    
    // 剥离 Claude Code 的 [1m] 后缀（防御性处理）
    strippedModel, hasSuffix := StripContextSuffix(model)
    if hasSuffix {
        log.Printf("[Model-Suffix] 检测到上下文窗口后缀: %s -> %s", model, strippedModel)
        model = strippedModel
    }
    
    // 后续模型映射逻辑...
}
```

## 使用场景

### 场景 1: Claude Code 配置

Claude Code 用户可以在配置中使用：

```bash
ANTHROPIC_MODEL=opus[1m]
```

代理会：
1. 接收 `opus[1m]`
2. 剥离后缀得到 `opus`
3. 根据 ModelMapping 映射到上游模型（如 `deepseek-v4-pro`）
4. 发送 `deepseek-v4-pro` 到上游 API

### 场景 2: 模型映射示例

配置文件：
```json
{
  "messagesUpstream": [
    {
      "name": "DeepSeek",
      "baseURLs": ["https://api.deepseek.com"],
      "apiKeys": ["sk-xxx"],
      "modelMapping": {
        "opus": "deepseek-v4-pro",
        "sonnet": "deepseek-chat"
      }
    }
  ]
}
```

客户端请求：
- `opus[1m]` → 后端处理为 `opus` → 映射到 `deepseek-v4-pro` → 发送到上游
- `sonnet[1m]` → 后端处理为 `sonnet` → 映射到 `deepseek-chat` → 发送到上游

### 场景 3: Cursor 客户端

Cursor 客户端同样可以使用带后缀的模型名称，代理会自动处理。

## 测试覆盖

测试文件：`backend-go/internal/config/config_utils_test.go`

包含测试场景：
- ✅ 基本后缀剥离（`opus[1m]` → `opus`）
- ✅ 完整模型名后缀（`claude-opus-4-8[1m]` → `claude-opus-4-8`）
- ✅ 无后缀模型（`opus` → `opus`）
- ✅ 带空格的输入处理
- ✅ 与 ModelMapping 的集成
- ✅ 与 DefaultModel 的集成
- ✅ Fable 模型支持

运行测试：
```bash
cd backend-go
go test -v ./internal/config -run TestStripContextSuffix
go test -v ./internal/config -run TestResolveUpstreamModelWithSuffix
```

## 注意事项

1. **防御性处理**: 虽然 Claude Code 文档说明客户端应该剥离后缀，但我们在服务端也做了处理，确保即使客户端遗漏也能正常工作。

2. **日志记录**: 当检测到后缀时会记录日志，便于调试和监控。

3. **向后兼容**: 不影响现有不带后缀的模型名称处理。

4. **未来扩展**: 当前只支持 `[1m]` 后缀，如果将来有新的后缀格式（如 `[2m]`），可以轻松扩展 `StripContextSuffix` 函数。

## 相关资源

- [Claude Code 模型配置文档](https://code.claude.com/docs/en/model-config.md)
- [Claude 上下文窗口文档](https://platform.claude.com/docs/en/build-with-claude/context-windows)
- [参考实现](https://github.com/raine/claude-code-proxy/commit/66f957a74816462d86a99ec6424916f9538ec5f5)
