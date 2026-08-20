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
| SSE `data:` 行判定 / 载荷提取 | `utils.ParseSSEDataLine` / `utils.SSEDataJSON` / `utils.ParseSSEDataLineBytes` | 五协议流式链路（providers / handlers / converters）的唯一出处。冒号后空格按 SSE 规范可选，**禁止再写 `HasPrefix(line, "data: ")`**——部分上游不带空格，严格匹配会静默丢整行。只要 JSON 载荷用 `SSEDataJSON`；需区分 `[DONE]`（如据此 break 读流循环）用 `ParseSSEDataLine` + `utils.SSEDoneMarker`；`[]byte` 热路径用 `ParseSSEDataLineBytes`。**输出**构造仍写规范的 `data: ` 前缀，不走本函数。契约由 `utils/sse_test.go` + `handlers/common/sse_tolerance_test.go` 锁定 |
| SSE 事件改写（拆行改载荷再拼回） | `utils.RewriteSSEDataLines` | token 修补 / usage 注入 / id-model 改写 / 整体替换的唯一骨架。**禁止再自行 `strings.Split(event, "\n")` 后逐行 `WriteString(line); WriteString("\n")`**——那样会给以 `\n\n` 结尾的事件多补一个换行（等于凭空插入一个空事件分隔）。回调返回 `changed=false` 的行与非 data 行**原样**写回（保留上游 `data:` / `data: ` 写法），无一行改写时返回值与入参逐字节相同；改写过的行统一写 `utils.SSEDataLinePrefix`。契约由 `utils/sse_test.go:TestRewriteSSEDataLines*` + `handlers/common/stream_rebuild_test.go` + `handlers/responses/stream_rebuild_test.go` 锁定 |
| 流式事件合成（日志/Token 观测） | `utils.StreamSynthesizer` | 拉平为文本/工具调用序列，仅用于日志与 Token 统计，**不参与协议 SSE 转换**（协议转换走 `converters/responses_stream.go`） |
| provider 流式泵（双通道/断连中止/SSE scanner） | `providers.streamPump`（`stream_pump.go`） | 四协议 `HandleStreamResponseCtx` 共用骨架；收尾约定 defer close 双通道；断连谓词 `isDisconnectLikeError` |
| 流挂起防护（空闲超时/断连中止） | `httpclient.NewIdleTimeoutReader` + `HandleStreamResponseCtx` | 默认 300s idle / 120s 首字节，env 可调 |
| 内容安全（敏感词/凭据/危险命令检测、拦截记录） | `sensitive` 包 + `handlers/common` HookPipeline | main.go 组建 ContentSafetyPipeline 注入 messages/responses/chat/gemini/images 五协议；pre-request 在 vision 前后各跑一次。协议提取器在 `content_safety_segments.go:extractSafetySegments` 登记（未登记显式报错）；载荷协议判定走 `payloadProtocolForServiceType`（chat/images 入口不按 ServiceType 转换）。images 只接 pre-request（含 multipart 分支），响应侧是图片不扫描 |
| multipart/form-data 请求体读写（boundary 判定 / 解析 / 重编码 / 只读文本字段） | `utils/multipart.go`：`MultipartBoundary` / `ParseMultipartParts` / `EncodeMultipartParts` / `ReadMultipartTextFields` | images 入口（模型映射、model/stream/prompt 观测）与内容安全 multipart 分支的唯一出处。**禁止再自行 `strings.HasPrefix(mediaType, "multipart/")` + `params["boundary"]`**；重编码后必须把返回的 Content-Type 同步到请求头（boundary 已变）。契约由 `utils/multipart_test.go` 锁定 |
| 非 JSON 载荷的观测 prompt 归一化 | `common.NormalizePromptTexts` | 与 `ExtractPrompts*` 系列同口径（清洗 + 去重 + 上限 3 条）；multipart 等入口取到裸文本后走它，别各自 TrimSpace |
| 视觉处理（描述生成/缓存/并发去重/请求改写） | `visionlayer.PrepareRequest` | 发送上游前自动执行，绝不绕过 |
| 图片检测 / 指纹 | `utils/vision.go` | `DetectImageContent` / `ExtractImageFingerprints` |
| 图片 data URL 解析 / 默认 mediaType | `utils.ParseImageDataURL` / `utils.DefaultImageMediaType` | 三处独立实现已收敛为单一出处（utils/providers.gemini/converters.responses_gemini）；行为契约由 `utils/vision_test.go:TestParseImageDataURL` 锁定 |
| Responses 多轮会话 | `session.SessionManager` | `previous_response_id` 链，SQLite 持久化；默认保留 7 天、上限 5000（main.go 注入） |
| Trace 亲和（同用户绑同渠道） | `session.TraceAffinityManager` | 经 scheduler 的 `SetTraceAffinityForKind` 入口（messages 专用旧入口与 Update 入口已作死代码删除） |
| 对话注册 / 路由覆盖 | `conversation.Registry` | scheduler 注入；冲突校验 `ValidateFixedChannel`，调度时优先级最高 |
| 请求成败/用量记录 | `scheduler.RecordRequest*` / `RecordSuccess*` / `RecordFailure` | 按 kind 分发至对应 MetricsManager；勿直接建 |
| 熔断判定 | `metrics.MetricsManager.ShouldSuspendKey(baseURL, apiKey, kind)` | 滑动窗口失败率阈值；注意方法名是 `ShouldSuspendKey` |
| 多 URL 延迟排序 | `urlhealth.URLManager` | `scheduler.GetSortedURLsForChannel` 入口；默认冷却 30s、连续 3 次失败移末尾 |
| 模型别名解析 / `[1m]` 后缀剥离 | `config.ResolveUpstreamModel` | 兼容 Claude Code 1M 上下文后缀 |
| 上游 URL 拼接（`#` 后缀跳过版本前缀 / 版本段检测） | `utils.BuildUpstreamURL(baseURL, defaultVersionPrefix, endpoint)` | providers 四处 + handlers chat/images 的唯一出处；`HasVersionSuffix` 供检测 |
| 模型目录 / 上游模型发现 | `modelcatalog` | `/v1/models` 聚合：静态别名 + 池匹配 + 上游发现；无独立路由，入口在 `handlers/messages/models.go` |
| Token 估算（计费/日志） | `utils.EstimateTokens` / `EstimateResponsesRequestTokens` | 估算值，仅用于观测 |
| 敏感信息脱敏（日志用） | `utils.MaskAPIKey` / `MaskSensitiveHeaders` / `FormatJSONBytesForLog` | 日志输出一律走这里 |
| gzip 解压 | `utils.DecompressGzipIfNeeded` | 上游响应 body |
| 客户端伪装 | 头伪装 `utils/headers.go`（`ApplyClaudeCodeDisguise` / `ApplyCodexDisguise` / `PrepareUpstreamHeaders`）；请求体伪装 `utils/claude_disguise.go`（`ApplyClaudeCodeBodyDisguise`） | 两个文件分工：头 vs body，勿混用 |
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
| ~~Responses 流解析两套状态机~~ | ~~`converters/responses_stream.go` vs `utils/stream_synthesizer.go:processResponses`~~ | **已证伪，非重复**：二者是 encoder/decoder 互逆对偶——`responses_stream.go` 读 Claude/Gemini 词汇表并**生成** `response.*`；`processResponses` **消费** `response.*`。且 `NewStreamSynthesizer(upstreamType)` 与 `ConvertUpstreamStreamLineToResponses(upstreamType)` 同源分派：`upstreamType=responses` 时 converters 走原样透传、`responsesStreamState` 根本不构造，其余取值时 synthesizer 走 `processClaude/processGemini/processOpenAI` → 两者从不解析同一事件流。核实日期 2026-08-20 |
| finish_reason/stop_reason 映射 | `converters/converter.go`（Anthropic↔OpenAI + →Responses）vs `converters/gemini_converter.go`（Gemini 四向） | 已核查：域对不重叠（7 个映射各自唯一），非 1:1 重复；如需收敛为公共语义层属重构，暂缓 |
| 图片 base64/dataURL 解析 | ~~`utils/vision.go`、`visionlayer`、`providers/gemini.go`、`converters/responses_gemini.go`~~ | **已收敛**：data URL 解析三份实现（`parseDataURL`/`parseGeminiDataURL`/`parseGeminiDataURI`）合并为 `utils.ParseImageDataURL`，默认 mediaType 收敛为 `utils.DefaultImageMediaType`（含 visionlayer）。各处**块结构提取**（`extractImageURL`/`extractGeminiInlineBase64`/`imageURIFromBlock`）目标类型不同（Claude block / Gemini map / GeminiInlineData 结构体），非 1:1 重复，保留 |
| 工具（tools）转换 | `converters/responses_claude.go`（ResponsesToolsToClaudeTools）、`converters/openai_converter.go`（responsesToolsToOpenAIChatTools）、`providers/responses_messages.go`（claudeToolsToResponsesTools）及多个 `build*ToolSearchTool` | 各自目标域不同，非 1:1 重复；暂缓 |
| 缓存键 canonical JSON | `providers/responses_messages.go` 与 `converters/responses_protocol.go` 各一份 | **已合并**到 `utils.CanonicalJSON`（见治理记录） |
| 死代码候选 | ~~`providers/responses.go` 的 `ResponsesProvider` 与 `MessagesResponsesProvider` 并存~~ **已核实非死代码**：`ResponsesProvider` 是 Responses 入口主链路生产组件（handlers/responses/handler.go:131/279 每请求构造，含流式处理），`MessagesResponsesProvider` 是 Messages 入口的 Responses 上游实现（provider.go:40 注册）；两者的差异是入口职责不同，非冗余。`images/handler.go` 的 `chatVersionPattern` 已在架构治理中消除 | 核实日期 2026-08-20；如再遇疑似死代码先 grep 生产引用 |

## 治理记录

- **v3.0.0**：canonical JSON 两份实现合并为 `utils.CanonicalJSON`（`providers/responses_messages.go`、`providers/openai.go`、`converters/responses_protocol.go` 改调用）。
- **架构治理**：上游 URL 拼接约定（`#`后缀跳过版本前缀 + 版本段检测）从 6 处独立实现收敛为 `utils.BuildUpstreamURL` 单一出处（providers/claude、gemini、openai、responses + handlers/chat、handlers/images）；顺带消除 chat/images 两包重复的 `chatVersionPattern`。`session.Session` 纯数据结构下沉至 `internal/types/session.go`，converters 不再 import session（session 包保留类型别名兼容既有引用）。
- **流式脚手架收敛**：四份 `HandleStreamResponseCtx`（claude/openai/gemini/responses_messages）的重复骨架（双通道构造、send/fail 断连中止、1MB SSE scanner、断连类错误判定）收敛为 `providers/stream_pump.go` 的 `streamPump`；顺带修复漂移：openai/gemini/responses_messages 恢复 `defer close(errChan)`（消费方 `ProcessStreamEvents` 对通道关闭有专门分支，close 不清除已缓冲错误），openai 注释"避免竞态条件"的理由经核实不成立。
- **调度器死代码清理**：`ChannelScheduler` 导出面调查（56 个方法）后删除 7 个零引用方法——`GetAdaptiveScheduler`、`GetURLManagerStats`、`InvalidateURLCache`、`ResetKeyMetrics`、`SetTraceAffinity`、`UpdateTraceAffinity`、`UpdateTraceAffinityForKind`。
- **跨层死代码清理**：上述调度器清理的连锁孤儿已一并删除——urlhealth 的 `InvalidateChannel`/`GetStats`、metrics 的 `ResetKey`（含指向它的 Deprecated 存根 `Reset`）、session 层 `TraceAffinityManager` 的 8 个零引用方法（`GetPreferredChannel`/`SetPreferredChannel`/`UpdateLastUsed`/`UpdateLastUsedForKind`/`Remove`/`RemoveByChannel`/`Size`/`GetAll`；生产链路仅用 `*ForKind` 变体 + `SizeForKind`/`GetTTL`）。BaseURL 亲和同族治理：`BaseURLAffinityManager` 的 `UpdateLastUsed`/`Remove`/`Size` 零引用删除；生产链路只用 `GetPreferredBaseURL`/`SetPreferredBaseURL`（TTL 续期靠 Set 时刷新，无需独立续期入口）。
- **metrics Deprecated 存根清理**：9 个仅返回固定值/打日志的零引用废弃方法（`IsChannelHealthy`/`CalculateFailureRate`/`CalculateSuccessRate`/`GetMetrics`/`GetAllMetrics`/`GetTimeWindowStats`/`GetAllTimeWindowStats`/`ShouldSuspend`，含此前连带的 `Reset`）全部删除；`ChannelMetrics` 类型保留（`GetChannelAggregatedMetrics` 仍在构造，handlers API 返回使用）。
- **converters 死链清理**：`OpenAICompletionsConverter`（factory 从未注册该 serviceType，Responses→Completions 链路未接线）连同其专属辅助 `ExtractTextFromResponses`、`OpenAICompletionsResponseToResponses` 及关联测试一并删除；`ReasoningEffortToGeminiThinkingLevel`（Gemini 3 thinkingLevel 映射，零引用）删除。
- **验证补强**：安装 mingw-w64 后全量 `go test -race -count=1 ./...` 首次在本机跑通（20 包全绿，零数据竞争）——覆盖切片①并发竞态修复、切片⑥流式泵收敛、优雅关闭幂等 Stop 等全部并发相关改动。
- **SSE `data:` 行判定收敛（含缺陷修复）**：判定散落 30+ 处、分裂成严格 `"data: "` 与宽松 `"data:"` 两派，收敛为 `utils/sse.go` 的 `ParseSSEDataLine`/`SSEDataJSON`/`ParseSSEDataLineBytes`。这不只是去重——严格派不符合 SSE 规范（冒号后单空格可选），遇不带空格的上游会**静默丢弃整行**，导致 usage 检测、token 修补、文本提取、内容安全钩子取文本失效。同一文件内此前已不一致：`handlers/chat/handler.go` 同一循环里 `extractChatUsageFromSSELine`（严格）与 `extractChatStreamSafetyFragments`（宽松）判定相反，不带空格上游丢 usage 但内容安全正常；`handlers/responses/handler.go` 有注释「有些上游不带空格」的容忍分支，同文件 `extractResponsesTextFromEvent` 却严格匹配。附带修复 `providers/openai.go` 用 `line == "data: [DONE]"` 精确比较导致无空格上游不触发 `finishStream()`；消除 `stream_synthesizer.go:ProcessLine` 每行重编译 `regexp.MustCompile` 的热路径浪费。**未动**输出构造点（`result.WriteString("data: ")` 等 9 处）与 `StripCacheFieldsFromClaudeSSE` 保留原前缀风格的逻辑——输出格式不变；`handlers/gemini/stream.go` 两处 `[DONE]` 的 `break` 语义用底层 `ParseSSEDataLine` 原样保留。反证验证：临时把 `sseDataPrefix` 改回 `"data: "` 后新增回归测试如期失败。
- **Responses 双状态机登记证伪**：「Responses 流解析两套状态机」为误登记。事实：`handlers/responses/handler.go:749` 用 `upstreamType` 构造 synthesizer，同一 `upstreamType` 又分派 `ConvertUpstreamStreamLineToResponses`（`responses_protocol.go:76`），且 synthesizer 吃的是**上游原始行**（handler.go:832）而非转换后事件——所以 `upstreamType=responses` 时 converters 原样透传、`responsesStreamState` 不构造，只有 `processResponses` 在跑；`upstreamType=claude/gemini/openai` 时 synthesizer 走对应的 `processClaude/processGemini/processOpenAI`，`responsesStreamState` 才工作但读的是 Claude/Gemini 词汇表。两者输入域互斥、方向互逆，无合并对象。真正同域消费 `response.*` 的是 `providers/responses_messages.go:processLine` 与 `handlers/responses/handler.go` 的若干抽取函数，但输出目标各异（Claude SSE / usage 结构 / 文本缓冲），合并属过度抽象，不在治理清单。
- **SSE 事件重建保真（含缺陷修复）**：六处「拆行改载荷再拼回」各自手写骨架（`handlers/common/stream.go` 的 `PatchTokensInEvent`/`PatchTokensInEventWithCache`/`ReplaceSSEData`/`PatchMessageStartEvent`，`handlers/responses/handler.go` 的 `injectResponsesUsageToCompletedEvent`/`patchResponsesCompletedEventUsage`）收敛为 `utils.RewriteSSEDataLines`；输出侧规范前缀收敛为 `utils.SSEDataLinePrefix`。修掉两个真实缺陷：①**多余尾换行**——六处都对每行补写 `"\n"`，而 `strings.Split("a\n\n", "\n")` 得 3 段，重建后变 `"a\n\n\n"`，等于在每个被改写的事件后凭空插入一个空的 SSE 事件分隔（改写路径覆盖 message_start 补 id / model 改写、message_delta token 修补、Responses usage 注入与修补，即绝大多数流式响应）；收敛后按 `Split` 的逆运算补分隔符，无一行改写时逐字节等于入参。②**兜底分支重复整段**——`injectResponsesUsageToCompletedEvent` 的多行 data 兜底里 `jsonEnd` 零值为 0，当 data 行区间后没有空行时，重建会从第 0 行起把整个事件再追加一遍，客户端收到「注入过 usage 的 data 行 + 一份原始重复事件」；该分支已提取为 `injectUsageIntoMultiLineDataEvent`，区间终点在遇到首个非 data 行时显式记录、缺失时取 `len(lines)`（尾部无行可补）。顺带把原兜底里「非 data 且非空行既不终止区间也不收集」的静默丢行改为按首个非 data 行终止。反证验证：把 `RewriteSSEDataLines` 改回逐行补换行的写法后，三个包的新增测试全部如期失败。
- **图片 data URL 解析收敛**：三份逐字符一致的 data URL 解析（`utils/vision.go:parseDataURL`、`providers/gemini.go:parseGeminiDataURL`、`converters/responses_gemini.go:parseGeminiDataURI`）合并为导出的 `utils.ParseImageDataURL`，`"image/png"` 默认值散落 6 处收敛为 `utils.DefaultImageMediaType`（含 visionlayer 一处）。行为差异核实：三者仅 `parseGeminiDataURI` 缺少 `data:` 前缀检查（依赖调用方 `HasPrefix` 前置判断），合并后前缀检查内置，调用点的前置判断随之删除，语义等价。新增 9 例表驱动测试 `TestParseImageDataURL` 固定契约（含空 mediaType、附加参数、非 base64 编码、缺逗号、空载荷等边界）。**未动**各处的块结构提取函数——其目标类型不同（Claude map block / Gemini map / `types.GeminiInlineData`），非重复。
- **images 接入内容安全（收口最后一个协议缺口）**：`docs` 四处标注的"images 未接入"已消除。三段接线：① `images/handler.go:Handler` 按 chat 的变参约定接受注入管道并填 `ProtocolSpec.HookPipeline`，main.go 三个 images 路由传入共享管道（带 BlockedLogRecorder，拦截可持久化）；② `extractSafetySegments` 登记 `case "images"` → `extractImagesSafetySegments`（只覆盖 `prompt`，含被拆成数组的写法；`model`/`n`/`size`/`quality`/`response_format` 是枚举或数值，`user` 是终端用户标识而非正文）；③ **修掉一个会让整次接入形同虚设的隐蔽缺陷**——`payloadProtocolForServiceType` 未对 images 短路，而 images 渠道通常配 `ServiceType=openai`，载荷会被当 Chat Completions 解析，`extractChatSafetySegments` 在 images 载荷上返回空 segments，既不报错也不拦截（比直接报错更难发现）；现与 chat 同样短路（二者都无协议转换环节）。同时补上 multipart 缺口：`isJSONContentType` 对 `multipart/*` 返回 false，此前 `/v1/images/edits`、`/v1/images/variations` 的表单 prompt **完全不过安全检查**；新增 `content_safety_multipart.go:runMultipartFormSafety`，检查全部**非文件文本部件**（字段名由客户端决定，只看 prompt 等于留后门；文件部件与非 UTF-8 部件跳过），掩码后重编码表单并把新 boundary 同步回 `Content-Type`（沿用旧 boundary 上游会读到空表单）。顺带消除 images 包内两份手写 multipart 脚手架与 `cloneMIMEHeader` 重复（改调 `utils/multipart.go`），并修掉 `extractImagesRequestMetadata`/`ParseRequest` 在 multipart 请求下 model 与观测 prompt 恒为空的问题（前者原先用 4096 字节截断读、后者只解析 JSON）。images **不接** post-response/stream 钩子：响应是 base64 图片或 URL，扫描无检出收益。反证验证：临时摘掉 multipart 分支后三个新增测试如期失败。

- **内容安全死代码子树清理（含错误注释修正）**：`hooks_pre_request.go` 的 `transformPromptPayload` 子树 8 个函数（`transformMessageList`/`transformGeminiContents`/`transformResponsesInput`/`transformPromptValue`/`transformPromptText`/`isUserRole`/`truncateSafetySnippet`，共 225 行）已被 segments 机制（`extractSafetySegments` + `applyContentSafetySegments`）完整取代，生产零引用、只剩测试在调，整棵删除。**测试没有跟着一起删**——它断言的两条行为契约仍然有效，已迁移到活着的实现上：`TestSafetySegmentsMaskUserTextAcrossProtocols`（messages/responses/gemini 三种载荷形状的用户正文都被掩码、助手历史原样保留、掩码后文本进观测 prompts）与 `TestSafetySegmentsBlockOnlyUserSensitiveWord`（助手历史命中敏感词不拦截——历史上下文拦下来等于让整段会话无法继续）。同时删除 `stream.go` 的 `PatchTokensInEvent`：它与 `PatchTokensInEventWithCache` 逻辑重复，后者的 `inferredCacheRead` 分支只打日志、从不写字段（`_ = inferredCacheRead // never write cache_read into client SSE`），故 `WithCache(..., 0, ...)` 与前者输出等价，而生产链路本就只调 WithCache 版；两处测试改指向 WithCache 版，重复的 `PatchTokensInEvent` 子测试合并。附带删除零引用的 `stream.go:abs` 与 `request.go:firstTextFromContent`，并修正 `PatchTokensInEventWithCache` 的**错误注释**——原文称"当 inferredCacheRead > 0 且事件中没有 cache_read_input_tokens 时将推断值写入"，与代码直接矛盾，照它推断行为会得出错误结论。上文第 83 条治理记录里出现的 `PatchTokensInEvent` 是当时的事实，现已不存在。反证验证：把 `extractMessagesSafetySegments` 的两道角色过滤同时改成永不命中后，两个迁移测试如期失败（assistant 号码进入检查范围 / assistant 历史被拦截）。**注意**这两道过滤互为兜底——`role == "assistant"` 分支（负责提 assistant 的 `tool_use` 参数）末尾的 `continue` 与后面的 `role != "user"` 过滤，单独破坏任一处行为都不变；别把其中一处当冗余删掉。

## 规则

1. 写任何通用能力前先查本表；有就复用，没有才允许新建。
2. 新建能力后必须登记本表，并同步 `docs/glossary.md`（新概念）、`docs/flows.md`（链路变化）。
3. 发现本表与代码不符（入口改名/废弃），当场修正本表，不攒着。
4. 治理历史重复时：先 grep 全部引用 → 改动 → 跑 `make check` 全绿才算完。
