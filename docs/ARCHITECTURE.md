# 项目架构

一体化架构：Go 后端（Gin）嵌入前端构建产物，单二进制部署。

## 目录结构

```
claude-proxy/
├── backend-go/              # Go 后端（主程序）
│   ├── main.go              # 入口：初始化各组件、注册路由、优雅关闭
│   └── internal/
│       ├── core/            # 协议无关核心层（重构收敛产物）
│       │   └── channelcrud/ # 渠道 CRUD + key 管理 + Ping（单份实现取代五组 ~2900 行重复）
│       ├── handlers/        # HTTP 处理器（按 API 协议拆分）
│       │   ├── channel_routes.go  # 渠道管理路由注册（五协议收敛为 RegisterChannelRoutes）
│       │   ├── messages/    # Anthropic Messages API
│       │   ├── responses/   # OpenAI Responses API（含 compact）
│       │   ├── chat/        # OpenAI Chat Completions API
│       │   ├── gemini/      # Google Gemini API
│       │   ├── images/      # OpenAI Images API
│       │   └── common/      # 多渠道 failover、流式处理、对话管理
│       ├── providers/        # 上游适配器（Claude/OpenAI/Gemini/Responses）
│       │                    # HandleStreamResponseCtx 新增 ctx 中止防泄漏
│       ├── converters/       # 双向协议转换器（工厂模式）
│       ├── scheduler/        # 多渠道调度：亲和 > 促销 > 优先级 + 熔断降级
│       ├── session/          # Responses API 会话管理 + Trace 亲和性
│       ├── metrics/          # 渠道健康指标（滑动窗口、SQLite 持久化）
│       ├── urlhealth/        # 多端点 URL 健康检测与动态排序
│       ├── config/           # 配置管理（fsnotify 热重载、JSON 持久化）
│       ├── conversation/     # 对话上下文注册与路由覆盖
│       ├── modelcatalog/     # 模型目录（别名解析、后缀剥离）
│       ├── middleware/       # 认证、CORS、日志过滤、Web UI 门控
│       ├── httpclient/       # HTTP 客户端 (含 IdleTimeoutReader 流式空闲超时)
│       ├── utils/            # 共享工具（脱敏、token 估算、流合成、客户端伪装）
│       ├── visionlayer/      # 视觉分流（图片描述生成、缓存、请求改写）
│       ├── sensitive/        # 内容安全检测（敏感词/凭据/命令）
│       ├── modelaudit/       # 模型审计（能力/身份/变形审计）
│       ├── piagent/          # Pi Agent 配置管理
│       ├── logger/           # 日志封装
│       └── types/            # 共享类型定义
├── frontend/                 # Vue 3 + Vuetify 3 管理界面
│   └── src/
│       ├── composables/     # Vue 组合式函数（useAutoRefresh 等共享逻辑）
│       ├── components/      # Vue 组件
│       └── services/        # API 封装（channelApiByType 工厂收敛五渠道 CRUD）
├── docs/                     # 技术文档（glossary / capabilities / invariants / flows + 指南类文档）
├── .config/                  # 运行时配置（热重载）
└── dist/                     # 发布构建产物
```

## 五类渠道池

| 渠道类型 | API 端点 | 上游协议 |
|---------|---------|---------|
| Messages | POST /v1/messages | Anthropic Claude |
| Responses | POST /v1/responses | OpenAI Responses |
| Chat | POST /v1/chat/completions | OpenAI Chat |
| Gemini | POST /v1beta/models/* | Google Gemini |
| Images | POST /v1/images/* | OpenAI Images |

每类渠道拥有独立的指标管理器和渠道池，由 ChannelScheduler 通过 `map[ChannelKind]*MetricsManager` 统一调度（收敛前为 5 个独立字段）。

## 架构设计模式

### 1. Provider 模式
上游适配器统一实现 `Provider` 接口（`internal/providers/`）。Messages 入口和 visionlayer 通过 `GetProvider(serviceType)` 获取对应适配器。

### 2. 渠道管理收敛模式
渠道层的 CRUD、key 管理、Ping 通过 `core/channelcrud` 单份实现消除重复：各协议包导出 `Crud(cfgManager, sch)` 工厂返回 `*channelcrud.Handlers`，main.go 通过 `handlers.RegisterChannelRoutes` 声明式注册全部路由。新增协议 = 填 Ops + 导出工厂 + 注册一行。

### 3. 流式防护三层防护
1. **断连中止**：`HandleStreamResponseCtx` 生产 goroutine 在 send 时 `select ctx.Done()`，客户端断连立即中止
2. **首字节超时**：`RESPONSE_HEADER_TIMEOUT`（默认 120s），限制"连接到响应头"时间
3. **空闲超时**：`STREAM_IDLE_TIMEOUT`（默认 300s），`IdleTimeoutReader` 检测流中挂起

### 4. Failover 模式
`handlers/common/upstream_failover.go` 把 key 轮换、URL failover、性能画像、日志收敛成一个通用函数，是 handlers 层最接近可复用的部分。
`ShouldRetryWithNextKey` 按 HTTP 状态码 + 错误消息关键词分类，支持 Fuzzy 模式。

### 5. Session / Conversation
- `session/`：基于 `previous_response_id` 的多轮对话跟踪，默认 7 天保留、5000 上限
- `conversation/`：对话上下文注册与路由覆盖，支持对话级别渠道绑定

### 6. 前端收敛模式
前端通过 `channelApiByType(type)` 工厂获取统一渠道 API 方法集，`stores/channel.ts` 用 `channelsDataMap + tabApi()` 查表消除 5 路 if/else。图表定时器复用 `useAutoRefresh` composable。

## 调度优先级

1. 会话亲和（Trace Affinity）— 同一用户绑定之前成功渠道
2. 促销渠道 — 仅在无亲和或亲和渠道失败后生效
3. 优先级遍历 — 按 priority 字段升序
4. 降级选择 — 失败率最低的可用渠道

亲和策略精细化：仅在新会话、续期、或原亲和渠道在本次请求中失败时才重建亲和。

## 协议转换

converters/ 实现 Responses API 与各上游协议之间的双向转换：

- **Responses -> OpenAI Chat**：custom tool 降级为 function（嵌入原始定义）；namespace tool 拍平为 parent__child；web_search 降级为 function；无 tools 时移除 tool_choice/parallel_tool_calls
- **OpenAI Chat -> Responses**：从原始请求恢复 custom_tool_call / tool_search_call 类型；按类型构建不同 output 结构（arguments vs input vs execution）
- **Responses -> Claude**：custom_tool_call 使用 {"input": ...} 作为 tool_use 输入
- **Channels -> Gemini**：无 tools 时不发送 toolConfig

## 指标与熔断

- 滑动窗口（默认最近 10 次请求）计算失败率
- 失败率 >= 50% 触发熔断，15 分钟后自动恢复
- 成功请求立即清除熔断状态
- 支持 SQLite 持久化（可配置保留天数）
- 缓存命中率零值也序列化，确保前端正确显示

## 会话管理

Responses API 通过 previous_response_id 实现多轮对话，由 SessionManager 维护会话历史（默认 24h 过期、最多 100 条消息、100k tokens）。ConversationRegistry 基于 conversation_id / fallback_key 建立对话路由，支持对话级别路由覆盖。

## 模型后缀

代理支持 [1m] 后缀（如 opus[1m]），自动剥离后发送到上游。详见 config.ResolveUpstreamModel。