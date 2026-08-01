# 术语表（Glossary）

本项目的核心领域术语定义。关联架构决策见 [docs/adr/](adr/)。

## 后端核心概念

| 术语 | 定义 | 关联 |
|------|------|------|
| **上游（Upstream）** | 被代理的实际 AI 服务提供方（Claude / OpenAI / Gemini 等）。一个上游包含 base URL、API Keys、模型映射等。 | `internal/config` |
| **渠道（Channel）** | 代理面对上游的编排单元，封装一个上游实例及其健康状态。项目支持五种渠道类型：messages / responses / gemini / chat / images。 | `internal/scheduler` |
| **ChannelKind** | 渠道类型的枚举，是跨层的一等公民，驱动调度、指标、路由的按类型分发。 | [ADR-0001](adr/ADR-0001-core-layer-convergence.md) |
| **API Key** | 上游服务的认证密钥。每个渠道可挂多个 key，按优先级轮询，支持置顶/置底。 | `internal/config` |
| **熔断（Circuit Breaker）** | 渠道连续失败达到阈值（默认滑动窗口失败率）后被挂起（suspended），暂停参与调度，可手动恢复。 | `internal/metrics` |
| **故障转移（Failover）** | 渠道/key 失败后按策略（优先/亲和/优先级/熔断过滤）切换到下一个可用候选。 | `internal/handlers/common/failover.go` |
| **Fuzzy 模式** | 对所有非 2xx 错误都触发 failover 的宽松错误处理模式，用于屏蔽错误类型误判。 | `internal/handlers/common/failover.go` |
| **Trace 亲和性** | 同一用户/会话绑定到同一渠道的调度策略，保证多轮对话上下文连贯。 | `internal/session` |
| **促销渠道（Promotion）** | 优先被选中的渠道（促销期内优先调度）。 | `internal/scheduler` |
| **渠道池（Channel Pool）** | 渠道的逻辑分组，支持按池调度与拖拽排序布局。 | `main.go`、`handlers` |
| **性能画像（Profile）** | 各 base URL + 模型的性能统计，驱动自适应负载均衡。 | `internal/metrics` |
| **自适应调度** | 基于性能画像的负载均衡，选择性能最优的渠道。 | `internal/scheduler` |
| **对话路由覆盖（Route Override）** | 对指定对话强制绑定渠道的覆盖规则。 | `internal/conversation` |

## 协议与流式

| 术语 | 定义 | 关联 |
|------|------|------|
| **协议适配器（ProtocolAdapter）** | 描述协议差异的插槽接口：请求构建、响应解析、流式解码、特殊端点。核心层通过注册表取用。 | [ADR-0002](adr/ADR-0002-protocol-registry.md) |
| **SSE（Server-Sent Events）** | 流式响应的传输格式（`data: ...` 行），代理转发时需按协议解析/重建。 | [ADR-0003](adr/ADR-0003-streaming-framework.md) |
| **首字节超时（ResponseHeaderTimeout）** | 从发出请求到收到响应头的最大等待，挡住"连接建立但永不响应"。 | [ADR-0003](adr/ADR-0003-streaming-framework.md) |
| **空闲超时（Idle Timeout）** | 流中两次数据事件之间的最大间隔，挡住"流中挂起"；默认保守避免误杀思考模型长流。 | [ADR-0003](adr/ADR-0003-streaming-framework.md) |
| **转换器（Converter）** | 协议格式双向转换（如 OpenAI Chat ↔ Claude Messages）。旧 `converters` 包与 `Provider` 接口为历史遗留，逐步被 `ProtocolAdapter` 取代。 | [ADR-0001](adr/ADR-0001-core-layer-convergence.md) |

## 前端

| 术语 | 定义 | 关联 |
|------|------|------|
| **ApiTab** | 前端渠道页签枚举（messages / responses / gemini / chat / images），与后端 ChannelKind 对应。 | `stores/channel.ts` |
| **Composable** | Vue 3 组合式函数，用于抽取跨组件复用逻辑（如自动刷新定时器）。 | 阶段 3（[ADR-0004](adr/ADR-0004-refactor-sequencing.md)） |
