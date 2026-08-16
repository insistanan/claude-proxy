# 能力注册表：我要 X → 去哪找

写任何"通用能力"前先查本表；有现成实现一律复用，没查表就重复实现 = 违规。发现系统里已有但本表没登记的实现，补登记。新建通用能力后必须登记本表，否则任务视为未完成。

> 历史存量说明：部分能力历史上有多份相似实现。以本表"现成实现"列为正宗入口；确属重复且已治理的见文末「已知重复与待治理」。改动非正宗实现请在备注标 deprecated。

## 后端（backend-go/internal）

| 需求 | 现成实现 | 备注 |
|---|---|---|
| 渠道 CRUD / key 管理 / Ping | `core/channelcrud` | 五协议共享单份实现；新增协议只需填 Ops 并注册 |
| 渠道路由注册 | `handlers.RegisterChannelRoutes` | main.go 按 `ChannelKind` 声明式注册 |
| 渠道选择（过滤熔断） | `scheduler.ChannelScheduler.SelectChannel` / `ReserveChannel` | 顺序：对话路由覆盖 → 促销 → Trace 亲和 → 自适应 → 按优先级降级（同优先级选 in-flight 最低）；绝不自行挑渠道 |
| 跨渠道 failover | `handlers/common.HandleMultiChannelFailover` | 候选渠道逐个尝试，含视觉渠道选择 |
| 单渠道内 key/URL/模型映射重试 | `handlers/common.TryUpstreamWithModelMappingFailover` | key 轮换 + 模型映射变更后重试同一循环 |
| 请求体读取 / 放回 | `common.ReadRequestBody` / `RestoreRequestBody` | 大小上限走 env `MAX_REQUEST_BODY_SIZE_MB` |
| 上游请求发送 | `common.SendRequest` | 统一超时/代理/认证头 |
| 上游适配（构建请求/解析响应/流处理） | `providers.GetProvider(serviceType)` | `Provider` 接口；serviceType 全集 {openai, gemini, claude, responses}（无 codex）；messages 与 visionlayer 共用。注意 visionlayer 的视觉描述走独立 `imageAdapterForService` |
| 协议格式双向转换 | `converters` 包（非单一工厂） | `factory.go` 仅 Claude 上游走工厂；Responses 主链路 `responses_protocol.go`；Gemini↔Claude/OpenAI 在 `gemini_converter.go`；Chat↔Responses 在 `chat_to_responses.go`/`responses_to_chat.go` |
| 流式事件合成（日志/Token 观测） | `utils.StreamSynthesizer` | 拉平为文本/工具调用序列，仅用于日志与 Token 统计，**不参与协议 SSE 转换**（协议转换走 `converters/responses_stream.go`） |
| 流挂起防护（空闲超时/断连中止） | `httpclient.NewIdleTimeoutReader` + `HandleStreamResponseCtx` | 默认 300s idle / 120s 首字节，env 可调 |
| 内容安全（敏感词/凭据/危险命令检测、拦截记录） | `sensitive` 包 + `handlers/common` HookPipeline | main.go 组建 ContentSafetyPipeline 注入 messages/responses/chat/gemini 四协议；**images 未接入（已知缺口，待治理）**；pre-request 在 vision 前后各跑一次 |
| 视觉处理（描述生成/缓存/并发去重/请求改写） | `visionlayer.PrepareRequest` | 发送上游前自动执行，绝不绕过 |
| 图片检测 / 指纹 | `utils/vision.go` | `DetectImageContent` / `ExtractImageFingerprints` |
| Responses 多轮会话 | `session.SessionManager` | `previous_response_id` 链，SQLite 持久化；默认保留 7 天、上限 5000（main.go 注入） |
| Trace 亲和（同用户绑同渠道） | `session.TraceAffinityManager` | 经 scheduler 的 `SetTraceAffinity*` 入口 |
| 对话注册 / 路由覆盖 | `conversation.Registry` | scheduler 注入；冲突校验 `ValidateFixedChannel`，调度时优先级最高 |
| 请求成败/用量记录 | `scheduler.RecordRequest*` / `RecordSuccess*` / `RecordFailure` | 按 kind 分发至对应 MetricsManager；勿直接建 |
| 熔断判定 | `metrics.MetricsManager.ShouldSuspendKey(baseURL, apiKey, kind)` | 滑动窗口失败率阈值；注意方法名是 `ShouldSuspendKey` |
| 多 URL 延迟排序 | `urlhealth.URLManager` | `scheduler.GetSortedURLsForChannel` 入口；默认冷却 30s、连续 3 次失败移末尾 |
| 模型别名解析 / `[1m]` 后缀剥离 | `config.ResolveUpstreamModel` | 兼容 Claude Code 1M 上下文后缀 |
| 模型目录 / 上游模型发现 | `modelcatalog` | `/v1/models` 聚合：静态别名 + 池匹配 + 上游发现；无独立路由，入口在 `handlers/messages/models.go` |
| Token 估算（计费/日志） | `utils.EstimateTokens` / `EstimateResponsesRequestTokens` | 估算值，仅用于观测 |
| 敏感信息脱敏（日志用） | `utils.MaskAPIKey` / `MaskSensitiveHeaders` / `FormatJSONBytesForLog` | 日志输出一律走这里 |
| gzip 解压 | `utils.DecompressGzipIfNeeded` | 上游响应 body |
| 客户端伪装 | 头伪装 `utils/headers.go`（`ApplyClaudeCodeDisguise` / `ApplyCodexDisguise` / `PrepareUpstreamHeaders`）；请求体伪装 `utils/claude_disguise.go`（`ApplyClaudeCodeBodyDisguise`） | 两个文件分工：头 vs body，勿混用 |
| 模型审计（能力/身份/变形审计、任务与报告） | `modelaudit` | 路由经 `modelaudit.RegisterRoutes` 注册 `/api/model-audit`（main.go 挂载）；前端 `Audit*View` 配套 |
| Pi Agent 配置管理 | `piagent` | settings/providers/credentials/backups；路由在 main.go 直接注册 `/api/settings/pi-agent/*`（16 端点） |

## 前端（frontend/src）

| 需求 | 现成实现 | 备注 |
|---|---|---|
| 渠道 API 调用 | `services/api.ts`（`channelApiByType` 工厂） | 五协议共用；store 层调用 |
| SSE 解析 | `utils/sse.ts` | 流式输出 |
| 自动刷新定时器 | `composables/useAutoRefresh.ts` | 跨视图复用 |
| 主题切换 | `composables/useTheme.ts` + `plugins/vuetify.ts` | — |
| 快捷测试输入解析 | `utils/quickInputParser.ts` | 有 vitest 单测 |
| mdi 图标 | `plugins/vuetify.ts` 的 `iconMap` | 先查表；未注册先注册，`check:icons` 把关 |
| 版本信息 | `services/version.ts` | UI 展示构建注入的版本 |

## 已知重复与待治理（不新增，治理前先核实再动）

以下为审查登记的历史重复，**合并/删除前必须跑 `make check` 验证**；新增代码一律走本表"现成实现"列，不扩写这些重复。

| 重复组 | 位置 | 说明 |
|---|---|---|
| Responses 流解析两套状态机 | `converters/responses_stream.go`（协议 SSE）vs `utils/stream_synthesizer.go:processResponses`（日志合成） | 同解析 `type` 字段事件流，输出用途不同；合并风险高，暂缓 |
| finish_reason/stop_reason 映射 | `converters/converter.go`（Anthropic↔OpenAI + →Responses）vs `converters/gemini_converter.go`（Gemini 四向） | 已核查：域对不重叠（7 个映射各自唯一），非 1:1 重复；如需收敛为公共语义层属重构，暂缓 |
| 图片 base64/dataURL 解析 | `utils/vision.go`、`visionlayer`、`providers/gemini.go`、`converters/responses_gemini.go` | 至少 4 处；跨层差异未核清，合并前先对齐行为 |
| 工具（tools）转换 | `converters/responses_claude.go`（ResponsesToolsToClaudeTools）、`converters/openai_converter.go`（responsesToolsToOpenAIChatTools）、`providers/responses_messages.go`（claudeToolsToResponsesTools）及多个 `build*ToolSearchTool` | 各自目标域不同，非 1:1 重复；暂缓 |
| 缓存键 canonical JSON | `providers/responses_messages.go` 与 `converters/responses_protocol.go` 各一份 | **已合并**到 `utils.CanonicalJSON`（见治理记录） |
| 死代码候选 | `providers/responses.go` 的 `ResponsesProvider` 与 `MessagesResponsesProvider` 并存，前者流方法标"暂不实现"；`images/handler.go` 的 `chatVersionPattern` 疑为误复制未使用 | 删除前先 grep 确认无生产引用 |

## 治理记录

- **v3.0.0**：canonical JSON 两份实现合并为 `utils.CanonicalJSON`（`providers/responses_messages.go`、`providers/openai.go`、`converters/responses_protocol.go` 改调用）。

## 规则

1. 写任何通用能力前先查本表；有就复用，没有才允许新建。
2. 新建能力后必须登记本表，并同步 `docs/glossary.md`（新概念）、`docs/flows.md`（链路变化）。
3. 发现本表与代码不符（入口改名/废弃），当场修正本表，不攒着。
4. 治理历史重复时：先 grep 全部引用 → 改动 → 跑 `make check` 全绿才算完。
