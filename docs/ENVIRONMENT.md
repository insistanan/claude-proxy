# 环境变量配置

## 后端（Go）

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | 3000 | 服务器端口 |
| `ENV` | development | 运行环境：`development` / `production` |
| `PROXY_ACCESS_KEY` | your-proxy-access-key | 访问密钥（生产环境必须修改） |
| `ENABLE_WEB_UI` | true | 是否启用管理界面 |
| `LOG_LEVEL` | info | 日志级别：`debug` / `info` / `warn` / `error` |
| `ENABLE_REQUEST_LOGS` | true | 记录请求日志 |
| `ENABLE_RESPONSE_LOGS` | true | 记录响应日志 |
| `QUIET_POLLING_LOGS` | true | 静默前端轮询日志 |
| `RAW_LOG_OUTPUT` | false | 原始日志输出（不缩进、不截断） |
| `SSE_DEBUG_LEVEL` | off | SSE 调试级别：`off` / `summary` / `full` |
| `REWRITE_RESPONSE_MODEL` | false | 改写响应 model 字段为请求 model |
| `REQUEST_TIMEOUT` | 300000 | 请求超时（毫秒） |
| `MAX_REQUEST_BODY_SIZE_MB` | 50 | 请求体最大大小 |
| `ENABLE_CORS` | true | 启用 CORS |
| `CORS_ORIGIN` | * | CORS 允许的源 |
| `METRICS_WINDOW_SIZE` | 10 | 熔断滑动窗口大小 |
| `METRICS_FAILURE_THRESHOLD` | 0.5 | 熔断失败率阈值（0-1） |
| `METRICS_PERSISTENCE_ENABLED` | true | 指标 SQLite 持久化 |
| `METRICS_RETENTION_DAYS` | 7 | 指标保留天数（3-30） |
| `RESPONSE_HEADER_TIMEOUT` | 60 | 等待响应头超时（秒） |
| `LOG_DIR` | logs | 日志目录 |
| `LOG_FILE` | app.log | 日志文件名 |
| `LOG_MAX_SIZE` | 100 | 单个日志文件最大大小（MB） |
| `LOG_MAX_BACKUPS` | 10 | 保留旧日志文件数 |
| `LOG_MAX_AGE` | 7 | 保留旧日志文件天数 |
| `LOG_COMPRESS` | true | 压缩旧日志文件 |
| `LOG_TO_CONSOLE` | false | 同时输出到控制台 |

## 前端（Vite）

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `VITE_BACKEND_URL` | http://localhost:3000 | 后端 URL |
| `VITE_FRONTEND_PORT` | 5173 | 前端开发端口 |
| `VITE_API_BASE_PATH` | /api | API 基础路径 |

## 推荐生产配置

``env
ENV=production
PROXY_ACCESS_KEY=<strong-random-key>
LOG_LEVEL=info
ENABLE_REQUEST_LOGS=true
ENABLE_RESPONSE_LOGS=false
``

## 推荐开发配置

``env
ENV=development
LOG_LEVEL=debug
ENABLE_REQUEST_LOGS=true
ENABLE_RESPONSE_LOGS=true
``
