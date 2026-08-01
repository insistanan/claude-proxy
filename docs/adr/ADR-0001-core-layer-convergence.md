# ADR-0001：核心层收敛 —— 分层收敛 + 轻量适配

- 状态：已接受
- 日期：2026-08-01
- 关联：[ADR-0002](ADR-0002-protocol-registry.md)、[ADR-0003](ADR-0003-streaming-framework.md)、[ADR-0004](ADR-0004-refactor-sequencing.md)
- 相关术语：见 [术语表](../glossary.md)

## 背景

项目先实现 Messages API，后逐步补齐 Responses / Gemini / Chat / Images 四种协议。每次新增协议时，渠道管理层被整体复制一份，形成五套平行实现：

- **渠道 CRUD + Ping**：`messages/responses/chat/gemini/images` 五个 `channels.go`（合计约 2900 行）中 `GetUpstreams / AddUpstream / UpdateUpstream / DeleteUpstream / AddApiKey / DeleteApiKey / MoveApiKeyToTop/Bottom / SetChannelStatus / ReorderChannels / SetChannelPromotion / Ping*` 逻辑几乎逐字相同，仅 `cfg.Upstream` 与 `cfg.ResponsesUpstream` 等切片字段不同。
- **API Key 管理**：`GetNextAPIKey` / `MoveAPIKeyToBottom` 的"跳过熔断 key / 轮询 / 失败降级"在 `config` 五个文件中各自实现。
- **路由注册**：`main.go` 中 messages / responses / gemini / chat / images 五段 `apiGroup.*` 注册几乎逐行对称；metrics manager 初始化与性能画像同步也各自 ×5。
- **URL 版本号拼接**：`/v\d+[a-z]*$` 版本号检测与 `#` 后缀约定在 9 处重复，其中 6 处在函数体内每次请求 `regexp.MustCompile`。

同时存在抽象腐烂：`providers.Provider` 接口实际上只服务 Messages 一个入口（Responses 用的 `ResponsesProvider` 甚至不在 `GetProvider()` 工厂里）；`converters` 的工厂 `NewConverter` 是死代码，真实分发是 `responses_protocol.go` 里手写的 `switch serviceType`。

## 决策

采用**分层收敛 + 轻量适配**，以 `scheduler.ChannelKind`（已是调度器的一等公民）为主轴：

1. 新增 `internal/core/` 协议无关核心层，吸收所有与协议无关的重复逻辑：
   - `channelcrud`：渠道 CRUD + key 管理 + reorder/status/promotion/pools，取代五个 `channels.go`（实测净删约 2700 行）。
   - `pinger`：多 URL 并发选最快，并入 `channelcrud` 单份实现。
   - `streamer`：统一流式框架（见 ADR-0003）。**实际落地**：因 Provider 接口被多份代码依赖，未新建独立包，而是为 `Provider` 接口新增带 ctx 的 `HandleStreamResponseCtx` 方法（旧签名保留兼容），5 个 provider 的生产 goroutine 发送事件时 select ctx.Done()。
   - `metricsapi`：指标 handler 收敛（`WithConfig` / `WithKind` / Gemini 变体 → 一个实现），Gemini 3 个重复版本改为委托。
   - `routes`：路由注册辅助，取代 `main.go` 的 ×5 段手写。**实际落地**：因 pools/dashboard 依赖 handlers 层，`RegisterChannelRoutes` 放在 `internal/handlers/channel_routes.go`（而非 core/routes），避免 core→handlers 反向依赖。5 段路由收敛为 5 次调用，净删约 250 行。
2. 协议差异收敛为 `internal/protocols/<kind>/` 适配插槽，每个协议只实现四个插槽（请求构建、响应解析、流式解码、特殊端点），见 ADR-0002。
3. 收敛 `scheduler.ChannelScheduler`：把 5 个独立 `MetricsManager` 字段替换为 `map[ChannelKind]*MetricsManager`。
4. 承认 `Provider` 接口失败：不强行治愈，而是废弃伪抽象，由 `ProtocolAdapter` 取代其真实职责。

## 后果

**正面**：
- 新增一个上游协议只需实现 `ProtocolAdapter` + 注册一行，不再复制一整套渠道管理（接入成本从约 15–25 个文件降到约 5 个）。
- 渠道管理的领域逻辑改动（加字段、改错误处理）只需改一处，消除 5 处人肉同步。
- 消除热路径上每次请求的正则编译。

**负面 / 代价**：
- 重构回归面大：8800 行平行代码收敛为核心层 + 适配器，必须分阶段迁移并在每阶段保持可编译可运行（见 ADR-0004）。
- 协议差异中有一部分是"历史分叉"而非本质差异（如 `loadbalance` 端点、dashboard 字段），收敛时需要逐个判断归属，工作量大。
- `ChannelKind` 成为跨层核心概念，要求所有新协议从一开始就声明自己的 kind。

## 备选方案

- **重设计统一接口**：让五种协议都实现一个带 context、统一形态的大接口。因 Gemini / Chat 原生协议与 Responses 协议形态差异过大，硬套统一形态反而别扭，弃用。
- **删除伪抽象但不收敛**：只删死代码不合并重复，五平行世界依旧，未解决主要矛盾，弃用。
