# 环境变量配置

> 默认值以代码为准（`backend-go/internal/config/env.go` 的 `NewEnvConfig`）；`.env.example` 是推荐配置示例，与代码默认值允许不同（如 `ENABLE_CORS`、`ENABLE_REQUEST_LOGS` 示例值即与默认值相反）。未设置的环境变量走下表"代码默认值"。
>
> `.env` 由 `config.LoadDotEnv` 加载：先读可执行文件同目录，再读进程工作目录；已存在的环境变量不覆盖。`go run` / air 的二进制在临时目录，实际吃的是工作目录 `.env`。

## 后端（Go）

| 变量 | 代码默认值 | 说明 |
|------|-----------|------|
| `PORT` | 3000 | 服务器端口 |
| `ENV` | production（`NODE_ENV` 兼容） | 运行环境：`development` / `production`。未设置时默认 production。只影响 Gin 模式、`/admin/dev/info`、CORS localhost；不控制请求/响应体日志。本地开发显式写 `ENV=development` |
| `PROXY_ACCESS_KEY` | your-proxy-access-key | 访问密钥（生产环境必须修改） |
| `ENABLE_WEB_UI` | true | 是否启用 Web 管理界面 |
| `LOG_LEVEL` | info | 日志级别：`error` / `warn` / `info` / `debug` |
| `ENABLE_REQUEST_LOGS` | true | 记录请求日志（含请求体/头；`false` 才关闭）。不跟 `ENV` 走 |
| `ENABLE_RESPONSE_LOGS` | true | 记录响应日志（含响应体与流式合成内容）。不跟 `ENV` 走 |
| `QUIET_POLLING_LOGS` | true | 静默前端轮询 / 健康检查 / OPTIONS 预检日志；其余请求以请求块打印（`┌` 进入 / `└` 结束，含状态码与耗时）。设为 false 恢复 gin 标准单行访问日志 |
| `RAW_LOG_OUTPUT` | false | 原始日志输出（不缩进、不截断） |
| `SSE_DEBUG_LEVEL` | off | SSE 调试级别：`off` / `summary` / `full` |
| `REWRITE_RESPONSE_MODEL` | false | 改写响应 model 字段为请求 model（仅 Messages 流式响应） |
| `REQUEST_TIMEOUT` | 300000 | 请求超时（毫秒） |
| `MAX_REQUEST_BODY_SIZE_MB` | 50 | 请求体最大大小（MB） |
| `ENABLE_CORS` | true | 启用 CORS（`.env.example` 建议 false） |
| `CORS_ORIGIN` | * | CORS 允许的源 |
| `METRICS_WINDOW_SIZE` | 10 | 熔断滑动窗口大小（最小 3） |
| `METRICS_FAILURE_THRESHOLD` | 0.5 | 熔断失败率阈值（0-1） |
| `METRICS_PERSISTENCE_ENABLED` | true | 指标 SQLite 持久化 |
| `METRICS_RETENTION_DAYS` | 7 | 指标保留天数（3-30，超出自动 clamp） |
| `RESPONSE_HEADER_TIMEOUT` | 120 | 等待响应头超时（秒，30-300；默认 120，非流式请求） |
| `STREAM_IDLE_TIMEOUT` | 300 | 流式响应空闲超时（秒，30-3600；默认 300，即 5 分钟） |
| `FORCE_HTTP1` | -（已废弃） | 上游请求已固定使用 HTTP/1.1；该环境变量不再读取，保留仅为兼容说明。原因：HTTP/2 在部分代理/上游下会触发 `http2: timeout awaiting response headers` |
| `LOG_DIR` | logs | 日志目录 |
| `LOG_FILE` | app.log | 日志文件名 |
| `LOG_MAX_SIZE` | 100 | 单个日志文件最大大小（MB） |
| `LOG_MAX_BACKUPS` | 10 | 保留旧日志文件数 |
| `LOG_MAX_AGE` | 7 | 保留旧日志文件天数 |
| `LOG_COMPRESS` | true | 压缩旧日志文件 |
| `LOG_TO_CONSOLE` | false | 同时输出到控制台（终端黑框框） |
| `CORRECT_RESPONSES_INPUT_TOKENS` | true | 校正 Responses 透传分支中明显错报的 input_tokens（仅作用于客户端下发 usage） |
| `AFFINITY_DEBUG` | false | 开启 Trace / BaseURL 会话亲和性调度的调试日志 |
| `CLAUDE_CONFIG_DIR` | 自动推导 | 自定义 Claude Code 配置与技能目录 |
| `CODEX_HOME` | 自动推导 | 自定义 Codex 配置与技能目录 |
| `OPENCODE_CONFIG_DIR` | 自动推导 | 自定义 OpenCode 配置与技能目录 |
| `PI_AGENT_CONFIG_DIR` | 自动推导 | 自定义 Pi Agent 配置目录 |

## 前端（Vite）

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `VITE_BACKEND_URL` | http://localhost:3000 | 后端 URL |
| `VITE_FRONTEND_PORT` | 5173 | 前端开发端口 |
| `VITE_API_BASE_PATH` | /api | API 基础路径 |

## 推荐生产配置

```env
ENV=production
PROXY_ACCESS_KEY=<strong-random-key>
ENABLE_CORS=false
LOG_LEVEL=info
ENABLE_REQUEST_LOGS=true
ENABLE_RESPONSE_LOGS=false
```

## 推荐开发配置

```env
ENV=development
LOG_LEVEL=debug
ENABLE_REQUEST_LOGS=true
ENABLE_RESPONSE_LOGS=true
```
