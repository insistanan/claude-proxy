# ADR-0002：协议注册机制 —— map 注册 + 注册函数

- 状态：已接受
- 日期：2026-08-01
- 关联：[ADR-0001](ADR-0001-core-layer-convergence.md)
- 相关术语：见 [术语表](../glossary.md)

## 背景

现状的协议分发是**中央 switch**：
- `providers.GetProvider(serviceType)` 的 `switch serviceType`（`provider.go`），只被 Messages 实际使用。
- `converters.NewConverter` / `NewConverterStrict` 的 `switch`（`factory.go`），是死代码，真实分发在 `responses_protocol.go` 的 `ConvertResponsesRequestToUpstream` 与 `ConvertUpstreamResponseToResponses` 里再次手写 `switch serviceType`。

每次添加新上游协议，都要在核心代码的多个 `switch` 中插入新分支，违反开闭原则；且 `switch` 与真实分发分处多处，容易失同步（现状 `factory.go` 对 gemini 返回"不支持"，实际却走 `responses_gemini.go` 分发，就是失同步的实证）。

## 决策

**实际落地：`channelcrud.Ops` 结构 + `Crud()` 工厂 + 声明式路由注册，替代最初设想的 `ProtocolAdapter` 接口 + `registry` map。**

重构过程中发现：渠道管理收敛（ADR-0001）已经把每个协议包的差异压缩成一个**纯数据配置 `channelcrud.Ops`**（13 个函数字段：List/LoadBalance/Add/Update/Remove/AddKey/RemoveKey/MoveKeyTop/MoveKeyBottom/Reorder/SetStatus/SetPromotion/SetLoadBalance）。此时再引入 `ProtocolAdapter` 接口 + 全局 registry map 属于过度设计——`Ops` 本身就是"协议差异的描述"，各协议包导出 `Crud(cfgManager, sch)` 工厂返回 `*channelcrud.Handlers` 即完成了"注册"。

实际机制：

```go
// internal/core/channelcrud/channelcrud.go
type Ops struct {
    Kind          scheduler.ChannelKind
    List          func() []config.UpstreamConfig
    LoadBalance   func() string
    Add           func(config.UpstreamConfig) (config.AddedUpstream, error)
    Update        func(int, config.UpstreamUpdate) (bool, error)
    // ... 其余协议差异点
}

// 各协议包导出 Crud 工厂（如 internal/handlers/messages/channels.go）
func Crud(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) *channelcrud.Handlers {
    return channelcrud.New(channelcrud.Ops{
        Kind: scheduler.ChannelKindMessages,
        List: func() []config.UpstreamConfig { return cfgManager.GetConfig().Upstream },
        Add:  cfgManager.AddUpstreamWithResult,
        // ...
    }, sch)
}

// handlers.RegisterChannelRoutes 声明式注册一个渠道类型的全部路由（main.go 调用）
handlers.RegisterChannelRoutes(apiGroup, scheduler.ChannelKindMessages, deps, handlers.ChannelRouteOptions{...})
```

## 与最初设计的差异

- **未实现** `ProtocolAdapter` 接口与 `registry` map：`Ops` 结构取代了它，功能等价（新增协议 = 填 Ops + 导出 Crud 工厂 + 一行 RegisterChannelRoutes），且更贴合实际代码（不引入额外抽象层）。
- **`providers.GetProvider` 保留**：Messages 入口和 visionlayer 仍通过它对 `Claude/OpenAI/Gemini/MessagesResponses` Provider 做协议适配（含流式），是有效的运行时分发，非死代码。

## 后果

**正面**：
- 新增协议 = 填一个 `channelcrud.Ops` + 导出 `Crud()` 工厂 + 一行 `RegisterChannelRoutes`，不触碰核心层任何代码。
- 协议注册点唯一、集中（各协议包 `channels.go`），消除多份 switch 失同步问题。
- 满足"后续支持更多协议"的扩展性诉求，与"收敛存量"并行不悖。
- 无全局可变注册表，测试无需隔离机制（比最初设想的 registry map 更简洁）。

**负面 / 代价**：
- `channelcrud.Ops` 的函数字段需与 `config.ConfigManager` 方法签名精确匹配（方法值绑定），新增配置操作需同步维护 Ops 接口。
- 路由注册依赖 `handlers.RegisterChannelRoutes` 的 `ChannelRouteOptions`（协议特有端点显式声明），协议特有端点需手动挂载，未完全自动化。

## 备选方案

- **保持 switch + 新增分支**：改动直接但每次加协议都要改核心层，违背 ADR-0001 的轻量适配初衷，弃用。
