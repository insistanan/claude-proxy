# ADR-0003：流式框架 —— ctx 中止 + 首字节/空闲超时

- 状态：已接受
- 日期：2026-08-01
- 关联：[ADR-0001](ADR-0001-core-layer-convergence.md)
- 相关术语：见 [术语表](../glossary.md)

## 背景

审计确认三类流式健壮性缺陷：

1. **客户端断连后 goroutine/连接泄漏**：
   - Messages：`providers/claude.go`、`providers/openai.go` 的 provider goroutine 用无依赖缓冲的 `eventChan <- event`（缓冲 100）推送 SSE，且不监听 context/done。客户端断开后，`handlers/common/stream.go` 直接 `return`，无人再消费 `eventChan`；上游若继续产出 >100 个事件，goroutine 将**永久阻塞在 channel send**（`resp.Body.Close()` 无法解除 send 阻塞）→ 每个被抛弃的长流泄漏一个 goroutine + 一个上游连接。
   - Responses：读循环在 `clientGone` 后不检查 `c.Request.Context().Done()`，继续读上游直到 EOF。
   - Gemini：三个流循环同样不检查 client done，`fmt.Fprintf` 写错误被忽略。
2. **流式上游无任何读超时**：`httpclient/client.go` 的流式 client `Timeout: 0`、无 `ResponseHeaderTimeout`。上游挂起（TCP 通但永不发数据）时 handler 无限挂起，叠加上述泄漏可耗尽连接池。
3. 设计根因：旧 `Provider.HandleStreamResponse` 接口返回裸 `<-chan string`，没有 context/done 参数，结构上无法中止。

## 决策

按"先重构再修"的顺序，将流式修复**直接融入重构**（不单独在旧结构上打补丁）。**实际落地**：因 `Provider` 接口被 messages handler、common/stream、visionlayer 及测试多处依赖，未新建独立 `core/streamer` 包，而是：

1. **接口扩展**：`Provider` 接口新增 `HandleStreamResponseCtx(ctx, body)`，旧 `HandleStreamResponse(body)` 保留为兼容包装（委托 ctx 版本 + `context.Background()`）。5 个 provider（Claude/OpenAI/Gemini/MessagesResponses/Responses）全部实现。
2. **断连中止**：所有 provider 的生产 goroutine 在向 `eventChan` 发送事件时改为 `select { case eventChan <- event: case <-ctx.Done(): return }`（Claude 用 `trySend`，OpenAI/Gemini 用 `send`，扫描循环内再加 `select ctx.Done()` 提前退出）。客户端断连后立即停止读取上游并退出，杜绝"缓冲(100)写满后永久阻塞在 channel send"的 goroutine/上游连接泄漏。`errChan` 发送也改为 select ctx.Done() 防阻塞。
3. **消费端切换**：`handlers/common/stream.go` 的 `HandleStreamResponse` 改调 `HandleStreamResponseCtx(c.Request.Context(), ...)`，让 gin 请求上下文（客户端断连即取消）真正传到 provider。

**首字节/空闲超时（已落地）**：
- **首字节超时**：`GetStreamClient` 的 transport 增加 `ResponseHeaderTimeout`（复用 `RESPONSE_HEADER_TIMEOUT`，默认 120s，可配置 30-300s），限制"发出请求到收到响应头"的时间，挡住"连接建立但永不响应"的上游。**响应体读取不受此限制**（`Timeout: 0` 保持），避免思考模型长静默期被整体超时误杀。
- **空闲超时**：新增 `STREAM_IDLE_TIMEOUT` 配置（默认 300s=5 分钟，范围 30-3600s）。`handlers/common/request.go` 的 `SendRequest` 流式分支将 `resp.Body` 包装为 `httpclient.IdleTimeoutReader`：每次 Read 阻塞期间若超过 idleTimeout 无数据，关闭底层连接并返回 `ErrStreamIdleTimeout`，检测"TCP 通但流中挂起"的上游。长流持续有数据不受影响；已用单测验证挂起触发/正常透传/禁用直通三种行为。

## 后续待办

- 无（首字节 + 空闲超时已落地，流式泄漏三层防护完整）。

## 后果

**正面**：
- 消除 goroutine / 上游连接泄漏，被抛弃的长流不再累计资源占用。
- 上游挂起可被检测并回收，不再无限占用 handler。
- 统一超时语义，配置一处生效于所有协议。

**负面 / 代价**：
- 需修改流式接口签名（引入 ctx / done），破坏旧调用点——这正是安排在重构期一并完成的原因。
- 空闲超时需经验调参：默认 5 分钟偏保守，若实测发现频繁误杀可下调。
- 首字节超时对慢启动上游（冷启动超 15s）需通过配置放宽。

## 备选方案

- **仅 ctx.Done（最小改动）**：只止断连泄漏，不处理上游挂起，修复不完整，弃用。
- **仅首字节超时 + ctx**：挡得住"连上不响应"和"断连"，但流中挂起仍无解，弃用。
