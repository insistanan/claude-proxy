# 不变量与铁律

任何代码不得违反。机器能验证的已接门禁（`make check`），其余靠本文件兜底——AI 重构时看到"看似冗余"的校验/上限/顺序，先查这里，绝不擅自合理化删除。

## 仓库层

- 技术文档总是 放在 `docs/`；根目录绝不 新增 README / CHANGELOG / CLAUDE.md / AGENTS.md / LICENSE 之外的文档。
- 配置文件绝不 提交真实密钥；只提交 `*.example`。
- 发布产物总是 走版本注入构建（根 `make build` / CI release，注入 Version/BuildTime/GitCommit）；绝不 裸 `go build` 出 dist exe。
- 未经用户明确要求：绝不 建文档、跑测试、编译打包、执行 git commit/push/branch。

## 后端（backend-go）

- 认证：代理端点总是 经 `ProxyAuthMiddleware` 校验（在 `RunProxyRequest` 第一步）；除 `/health` 外绝不 存在匿名业务端点。生产环境必须设强 `PROXY_ACCESS_KEY`。
- 渠道与管理：五协议的渠道/key CRUD + Ping 总是 复用 `core/channelcrud`；绝不 在协议 handler 里再写一套渠道增删改。
- 调度：渠道选择总是 走 `ChannelScheduler`（顺序：对话路由覆盖 → 促销 → Trace 亲和 → 自适应 → 按优先级降级，过滤熔断/挂起渠道）；handler 绝不 自行挑选渠道。
- 内容安全：钩子管线总是 注入 messages/responses/chat/gemini 四协议；images 未接入是已知缺口——接入前，新增图片端点绝不 绕过管线直通。
- 指标：请求成败/用量总是 经 scheduler 的 `Record*` 入口记录；绝不 在 handler 里新开 `MetricsManager` 手算指标。
- 流式：上游流总是 经 `HandleStreamResponseCtx`（断连中止）+ `IdleTimeoutReader`（空闲超时）转发；绝不 裸 `io.Copy`。
- 视觉：图片请求总是 经 `visionlayer.PrepareRequest` 就地处理；绝不 绕过 `prepareRequestForUpstream` 把原始 base64 直接透传上游。
- 日志：总是 `[Component-Action]` 标签、绝不 使用 emoji；上游 key 绝不 完整输出，一律 `utils.MaskAPIKey` 脱敏（标签表见 `backend-go/CLAUDE.md`）。

## 前端（frontend）

- 图标：新 mdi 图标总是 先在 `src/plugins/vuetify.ts` 的 `iconMap` 注册再使用；绝不 直接写未注册名（`bun run check:icons` 机器拦截）。
- 渠道 API：总是 走 `services/api.ts` 的 `channelApiByType` 工厂；绝不 在组件里拼接裸 fetch 调后端。

## 完成定义（每个任务，全部满足才算完）

1. `make check` 全绿（后端 gofmt + vet + test；前端 type-check + 图标扫描）。
2. 本次变更影响的 docs 已同步：新概念 → `docs/glossary.md`；新能力 → `docs/capabilities.md`；新铁律 → 本文件；链路变化 → `docs/flows.md`。
3. 没有新增重复代码：`docs/capabilities.md` 已登记的能力一律复用。