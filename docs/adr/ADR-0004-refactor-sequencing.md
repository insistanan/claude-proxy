# ADR-0004：渐进重构节奏 —— 三阶段执行

- 状态：已接受
- 日期：2026-08-01
- 关联：[ADR-0001](ADR-0001-core-layer-convergence.md)、[ADR-0002](ADR-0002-protocol-registry.md)、[ADR-0003](ADR-0003-streaming-framework.md)
- 相关术语：见 [术语表](../glossary.md)

## 背景

收敛范围定为"前后端一起"（前端 `api.ts` 的 CRUD ×5、`testChannel` 两份 ~480 行拷贝、`stores/channel.ts` 的 5 路 if/else 重复同样严重）。一次性大重构回归面大、中途难以回退。需在不破坏"每阶段可编译可运行"的前提下推进。

## 决策

分三阶段执行，每阶段结束都是一个可编译、可运行、可回退的稳定点：

### 阶段 1：核心层落地（后端，收益最大）

- 新建 `internal/core/`：`channelcrud`、`pinger`、`metricsapi`、`routes`。
- 收敛 `scheduler.ChannelScheduler` 的 5 个 `MetricsManager` 字段 → `map[ChannelKind]*MetricsManager`。
- 收敛 `main.go` 的 ×5 段路由注册 → 协议注册表 + 循环。
- **不动**五个协议包的既有实现（阶段 2 才逐个替换），阶段 1 仅"抽出可复用件 + 让旧代码调用它们"，纯增量、低风险。

### 阶段 2：协议迁移到插槽（后端）

- 实现 `ProtocolAdapter` 接口 + `registry`（ADR-0002）。
- 逐个把 messages → responses → gemini → chat → images 的渠道管理 / 流式 / 主 handler 迁移到插槽，**每迁移一个协议就替换一个旧实现**。
- 引入 `core/streamer`（ADR-0003），流式泄漏修复在此阶段随协议迁移一并落地。

### 阶段 3：前端收敛 + bug 修复（前端）

- `api.ts`：CRUD ×5 → `generateChannelApi(kind)` 工厂；`testChannel` / `testChannelWithModel` 合并 → `buildTestRequest()` + 统一 SSE 解析器。
- `stores/channel.ts`：5 路 if/else → `Record<ApiTab, TabConfig>` 数据表 + 工厂函数。
- 图表定时器收敛为共享 `useAutoRefresh` composable；`ChannelCard` 复用 `ChannelStatusBadge`。
- **修复功能性 bug**：前端 `updateLoadBalance()` / `updateResponsesLoadBalance()` 调用的 `/loadbalance`、`/responses/loadbalance` 后端从未注册（Messages / Responses 负载均衡下拉必定 404），在收敛时一并对齐路由。

## 后果

**正面**：
- 阶段 1 先行落地并立即产生收益（消灭重复最重的渠道管理），且几乎零回归风险。
- 每阶段独立可回退，单点故障不影响全局。
- 流式泄漏修复与重构同步完成，避免旧结构上打补丁被重构推翻。

**负面 / 代价**：
- 三阶段整体周期长，阶段 2 迁移期新旧结构并存、存在临时重复。
- 阶段间衔接需要明确的"迁移完成"判定（旧实现删除、路由切换到新注册表），需在实现时用任务清单跟踪。

## 备选方案

- **一次性大重构**：工作集中但回归面大、中途难回退，弃用。
