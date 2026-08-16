# 术语表（Glossary）

本项目核心领域术语的正宗定义。写码前先查这里，用既有术语，禁止发明平行概念/新表/新名词。架构演进背景见 [docs/ARCHITECTURE.md](ARCHITECTURE.md)。

## 后端核心概念

| 术语 | 定义 | 代码位置 |
|------|------|------|
| **上游（Upstream）** | 被代理的实际 AI 服务（Claude / OpenAI / Gemini 等）。一个上游包含 baseURL、API Keys、模型映射。 | `internal/config` |
| **渠道（Channel）** | 上游的编排单元，封装一个上游实例及其健康状态。五种类型：messages / responses / gemini / chat / images。 | `internal/scheduler` |
| **ChannelKind** | 渠道类型枚举，跨层一等公民：驱动调度、指标、路由按类型分发。 | `internal/scheduler` |
| **渠道 CRUD 收敛** | 五协议的渠道增删改查 / key 管理 / Ping 由单份实现提供，经 `RegisterChannelRoutes` 声明式注册（替代历史五组重复实现）。 | `internal/core/channelcrud`、`internal/handlers/channel_routes.go` |
| **API Key** | 上游认证密钥。渠道可挂多个 key，按优先级轮询，失败降级（`MoveAPIKeyToBottomForKind`）。 | `internal/config` |
| **熔断（Circuit Breaker）** | 滑动窗口失败率超阈值后挂起渠道/key，暂停参与调度，可自动恢复也可手动重置。默认：窗口 10 次、阈值 50%、恢复 15 分钟、最小请求数保护 `max(3, windowSize/2)`。 | `internal/metrics/channel_metrics.go` |
| **故障转移（Failover）** | key / URL / 渠道失败后按策略切换到下一候选。分两级：单渠道内重试、跨渠道转移。 | `internal/handlers/common/upstream_failover.go`、`multi_channel_failover.go` |
| **Fuzzy 模式** | 对所有非 2xx 错误都触发 failover 的宽松错误处理模式。 | `config.GetFuzzyModeEnabled` |
| **Trace 亲和性** | 同一用户/会话绑定同一渠道，保证多轮对话上下文连贯。实现按 `userID + kind` 绑定 `channelIndex`。 | `internal/session/trace_affinity.go` |
| **促销渠道（Promotion）** | 促销期内优先调度的渠道，带调用次数配额，用完自动失效。判定顺序先于 Trace 亲和（完整调度顺序见 `docs/flows.md` F1）。 | `internal/scheduler` |
| **渠道池（Channel Pool）** | 渠道的逻辑分组，支持按池调度与拖拽排序布局。 | `internal/config`、`main.go` |
| **性能画像 / 自适应调度** | 按 baseURL+模型统计性能表现，负载均衡时取向性能最优渠道。画像为进程内存，重启丢失。 | `internal/metrics/performance_profile.go`、`internal/scheduler/adaptive_scheduler.go` |
| **对话路由覆盖（Route Override）** | 对指定对话强制绑定渠道的覆盖规则，调度时优先级最高，冲突时返回 409 拒绝。 | `internal/conversation`、`scheduler.ValidateFixedChannel` |
| **内容安全管道** | pre-request / post-response / stream 钩子管线，内置敏感词、凭据、危险命令检测，拦截写 blocked_store。注入 messages / responses / chat / gemini 四协议 handler；**images 尚未接入（已知缺口，见 capabilities 待治理）**。pre-request 在 vision 前后各跑一次。 | `internal/handlers/common/hook_pipeline.go`、`internal/sensitive` |
| **视觉分流（Vision Layer）** | 图片请求发送上游前转为分析描述文本：检测 → 描述生成 → 两级缓存（内存+持久）→ 并发去重 → 就地改写请求。视觉渠道选择走独立 `SelectVisionChannel`。 | `internal/visionlayer` |
| **模型审计（ModelAudit）** | 对渠道/模型执行能力、身份、变形审计：任务、计划、证据、报告。路由经 `modelaudit.RegisterRoutes` 注册于 `/api/model-audit`（main.go 挂载）。 | `internal/modelaudit` |
| **URL 健康排序** | 多 BaseURL 渠道按延迟与失败冷却动态排序选路。默认：失败冷却 30s、连续 3 次失败移末尾（main.go 注入）。 | `internal/urlhealth` |
| **Pi Agent 配置管理** | 管理 Pi Coding Agent 的 providers / credentials / model-settings / backups：原子写（临时文件+rename）、写前备份、跨进程锁、revision 冲突检测。凭据文件 0600。配置目录 `~/.pi/agent`（可 `PI_AGENT_CONFIG_DIR` 覆盖）。路由在 main.go 直接注册 `/api/settings/pi-agent/*`。 | `internal/piagent`、`internal/handlers/pi_agent.go` |
| **模型目录（modelcatalog）** | `/v1/models` 聚合层：静态别名 + 渠道池匹配 + 上游 `/models` 发现，内存目录 + 后台刷新。无独立路由，聚合入口挂在 messages 包 handler 下。 | `internal/modelcatalog`、`internal/handlers/messages/models.go` |

## 协议与流式

| 术语 | 定义 | 代码位置 |
|------|------|------|
| **ProtocolSpec** | 描述协议差异的插槽结构（ParseRequest / BuildUpstreamRequest / HandleSuccess / PreRoute / HookPipeline）。messages / chat / images / gemini 经 `RunProxyRequest` 通用骨架执行；responses 为独立链路。 | `internal/handlers/common/protocol.go` |
| **上游适配器（Provider）** | 按 ServiceType 实现的上游接入点：构建上游请求、解析响应、流式处理。`GetProvider(serviceType)` 全集 = `{openai, gemini, claude, responses}`（无 codex）。visionlayer 的视觉描述走独立 `imageAdapterForService`，不共用 Provider 注册表。 | `internal/providers/provider.go` |
| **转换器（Converter）** | 协议格式双向转换，分散在 converters 包多个文件，**非单一工厂**：`factory.go` 仅 Claude 上游走工厂（Resp/Gemini 直接分发）；Responses 主链路在 `responses_protocol.go`；Gemini↔Claude/OpenAI 在 `gemini_converter.go`；Chat↔Responses 在 `chat_to_responses.go` / `responses_to_chat.go`。 | `internal/converters` |
| **SSE（Server-Sent Events）** | 流式响应的 `data:` 行传输格式，转发时按协议解析/重建。 | `internal/handlers/common/stream.go` |
| **首字节超时** | `RESPONSE_HEADER_TIMEOUT`（默认 120s）：从发请求到收到响应头的最大等待。 | `internal/httpclient` |
| **空闲超时（Idle Timeout）** | `STREAM_IDLE_TIMEOUT`（默认 300s）：流中两次数据事件间最大间隔，挡"流中挂起"。 | `internal/httpclient/idle_timeout_reader.go` |
| **断连中止** | 流式转发 goroutine `select ctx.Done()`，客户端断连立即中止上游请求，防泄漏。`HandleStreamResponseCtx` 是 Provider 接口方法，五个 provider 均实现。 | `internal/providers`、`internal/handlers/common/stream.go` |
| **流式合成（StreamSynthesizer）** | 把各上游的流式事件拉平为文本/工具调用序列，**仅用于日志与 Token 观测**；协议 SSE 转换是独立状态机（`converters/responses_stream.go`）。两者为已知重复，见 capabilities 待治理。 | `internal/utils/stream_synthesizer.go` |
| **缓存键规范化（canonical JSON）** | 结构化 JSON 归一化后哈希作缓存键（键排序、稳定序列化）。当前有两处实现（`providers/responses_messages.go`、`converters/responses_protocol.go`），已知重复待治理。 | `internal/providers`、`internal/converters` |

## 前端

| 术语 | 定义 | 代码位置 |
|------|------|------|
| **ApiTab** | 前端渠道页签枚举（messages / responses / gemini / chat / images），与后端 ChannelKind 对应。 | `stores/channel.ts` |
| **渠道 API 工厂** | `channelApiByType`：五协议共享同一 API 调用封装。 | `services/api.ts` |
| **Composable** | Vue 3 组合式函数，抽取跨组件复用逻辑（自动刷新、主题、计数组件）。 | `composables/` |
| **图标注册表（iconMap）** | mdi 图标需先在 `iconMap` 注册才能使用；`bun run check:icons` 机器校验。 | `plugins/vuetify.ts` |
