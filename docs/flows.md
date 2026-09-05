# 关键数据流

顺序感文档：改代理链路前先读这里。每个流程标注了关键代码位置，细节以代码为准——本文件只描述"顺序与分工"，代码描述"具体实现"。

## F1 统一代理主链路（messages / chat / images / gemini 四协议）

五个协议收敛到同一个骨架 `proxycore.RunProxyRequest`（协议差异由 `ProtocolSpec` 描述：ParseRequest / BuildUpstreamRequest / HandleSuccess / PreRoute / HookPipeline / AllowContentPolicyChannelFailover）。

```
1. 认证        ProxyAuthMiddleware（RunProxyRequest 函数内第一步；Web UI 路由另有 WebAuthMiddleware）
2. 读体        ReadRequestBody
3. 解析        spec.ParseRequest（得 model/stream/prompts）
4. 钩子挂载     AttachHookPipeline（内容安全管线）
5. 会话观测     ObserveConversationRequest（对话注册、attempt 记录、绑定 userID）
6. 原生日志     LogOriginalRequest（脱敏）
7. 指定渠道     body 带 channel_index → 直接单渠道分支（快捷测试带 purpose: quick_test 或 X-Proxy-Purpose 头时跳过 ModelMapping 与 DefaultModel 重定向，直接使用请求模型）
8. PreRoute    spec.PreRoute（协议特有前置路由；目前仅 chat 使用，为 modelcatalog 路由）
9. 分派         IsMultiChannelModeForModel → 单渠道 或 多渠道 HandleMultiChannelFailover
```

单渠道尝试（`UpstreamAttempt.TryWithModelMappingFailover`，多渠道模式下每个候选渠道内重复此循环）：

```
选 key（GetNextAPIKey）
→ 构建上游请求（spec.BuildUpstreamRequest，含模型映射）
→ prepareRequestForUpstream：内容安全 pre-request 钩子 → visionlayer.PrepareRequest → 内容安全钩子二次
→ SendRequest
→ 失败：兼容性与整流重试（prompt_cache_key 移除、reasoning_content 补齐、Unicode 转义、Thinking Budget 约束整流、Thinking Signature 块自愈、模态拒绝图片纯文本降级）→ 按状态码/错误分类（ShouldRetryWithNextKey；Fuzzy 模式下所有非 2xx 都转移）
→ 成功：spec.HandleSuccess（非流式 2xx 错误信封嗅探拦截；Messages 流式经 prime-then-commit 预读首包健康校验后再提交响应头）
       → post-response 钩子 → scheduler.RecordSuccessWithUsage → 会话/对话成功标记
```

渠道选择顺序（`SelectChannel`，见 `internal/scheduler/selection.go`）：

```
1. 对话路由覆盖（Route Override）— 最高优先级，冲突 409
2. 当前命中渠道池内按配置优先级选择第一个可用渠道
3. 当前渠道失败后，按同一池的配置顺序依次选择下一个渠道
4. 命中渠道池全部不可用后进入兜底分组，并按兜底分组配置顺序尝试
仍过滤熔断/挂起渠道，渠道内多 BaseURL 按 urlhealth 延迟排序。
```

渠道级三态熔断（`internal/circuit`）叠加在第 2/3/4 步的健康过滤之上：Open 渠道直接跳过；Open 冷却到期后惰性转 HalfOpen 并限量放行 1 个探测请求（探测失败立即回 Open，连续成功 2 次恢复 Closed）。渠道级记账在跨渠道 failover（`HandleMultiChannelFailover`）与单渠道出口（`handleSingleChannelProxy`）按渠道整体结果记录——成功记 Success、可重试失败记 Failure、客户端取消与内容审核转移记 Neutral（只释放探测名额不计健康度）。`CIRCUIT_ENABLED=false` 一键关闭；运行时状态经 `GET /api/circuit-breakers` 查询、`POST /api/circuit-breakers/reset` 手动重置。

模型映射目标列表同样遵循显式顺序：管理端 `ModelMappingEditor` 支持拖拽
`modelMapping[source]`，请求先尝试第一个目标模型，失败后才尝试后续目标。

## F2 视觉旁路（最不透明的链路段）

位置：**单渠道尝试循环内部**、上游请求构建之后（`prepareRequestForUpstream`，见 `handlers/proxycore/upstream_attempt_keys.go`）。

- `visionlayer.PrepareRequest` 就地改写上游请求：图片 → 分析描述文本（按目标协议替换为文本块）。
- 缓存两级：进程内存 + 持久（经 scheduler 按会话/kind/模型落存储）；同图并发去重（claim 机制）。
- 需要分析时用**独立的视觉渠道选择**（`SelectVisionChannel`），不受主请求渠道绑定约束。
- 改消息/图片相关处理时：**绝不绕过 `prepareRequestForUpstream` 直接改 `spec.BuildUpstreamRequest` 的产物**，否则图片请求会漏掉分流与缓存。

## F3 /v1/models 聚合

静态模型别名（opus/sonnet/haiku/gpt/codex/gemini 等）+ 渠道上游 `/models` 发现（modelcatalog 的 parse*）+ 池匹配（family/后缀）→ 合并去重返回。入口在 messages 包 handler（modelcatalog 无独立路由）。

模型名按客户端发来的**原样**参与上游映射匹配，代理不对后缀（历史上的 `[1m]`）做任何剥离或改写：`config.redirectModelList` 依次尝试 `*` 通配 → 精确匹配 → 双向 `Contains` 模糊匹配，全不命中则原样发往上游。需要区分同一别名的不同变体时，把完整名字配成 `ModelMapping` 的键。

## F4 内容安全（HookPipeline）

阶段：pre-request / request-time / post-response / stream。管线在 main.go 组建（`NewContentSafetyPipelineWithRecorder`），注入 **messages / responses / chat / gemini / images 五协议** handler。检测实现在 `sensitive` 包（敏感词/凭据/危险命令），拦截写 blocked_store（前端 Blocked Logs 视图）。pre-request 在 vision 前后各跑一次（vision 改写后再查一遍）。

images 的两点差异：① 请求体有 JSON 与 multipart/form-data 两种形态，后者走 `contentSafetyPreRequestHook.runMultipartFormSafety`（检查全部非文件文本部件；掩码改写后重编码表单，**新 boundary 同步回 Content-Type**），文件部件不扫描——图片内容由 vision 层负责；② 不接 post-response / stream 钩子：响应是 base64 图片或 URL，扫描多 MB base64 无检出收益。

白名单（`ContentSafetyConfig.Whitelist`）：启用后 `tool_result` / `tool_argument` 来源的片段若携带的工具名命中 `ToolNames`，则跳过全部检测维度（敏感词/凭据/敏感信息），但仍写一条 `BlockTypeWhitelist` 审计事件到 blocked_store，让拦截记录页可见"已放行"条目。工具名在各协议提取器里提取：messages 用 `tool_use_id` 关联前一条 assistant 的 `tool_use.name`；chat 的 `tool`/`function` role 消息有 `name`，assistant 的 `tool_calls.function.name`；responses 用 `call_id` 关联 `function_call.name`；gemini 的 `functionCall.name` / `functionResponse.name`。流式阶段（post-response stream）逐 chunk 拼接，无完整工具名上下文，白名单不生效。

流式拦截终止：`WriteAttachedStreamError` / `WriteAttachedStreamHookError` 在写完 error SSE 事件后补发该协议的流终止序列（messages 发 `message_stop`，responses 发 `response.completed` + ``，chat 发 `data: [DONE]`，gemini/images 无统一终止标记靠连接关闭）。不补终止序列客户端 agent 会一直等 `message_stop`，表现为"卡死、无法中断对话"。

## F5 responses 链路

`/v1/responses` 已并入 `RunProxyRequest` 主骨架（F1），协议差异全部经 `ProtocolSpec` 闭包表达：BuildUpstreamRequest 内走 `converters` 转换（主分发在 `responses_protocol.go`，仅 Claude 上游走 factory.go 工厂）、`session.SessionManager` 会话管理（previous_response_id 链）与流式事件转换（`converters.ConvertUpstreamStreamLineToResponses`）都在 HandleSuccess/Provider 回调内完成。会话记录 ID 经 `utils.ContextKeyConversationUserID` 从骨架传入回调。多渠道模式下内容审核错误跨渠道转移（`AllowContentPolicyChannelFailover`），单渠道不生效。`/v1/responses/compact` 是唯一独立于主骨架的端点。

### 历史边界（压缩 / 换根）

Responses 只承认两种续接：完整历史作为 input，或只发新增输入并用 `previous_response_id` 链接。两者同时到达上游时同一段历史被算两次，上下文与计费都翻倍，客户端读到的 usage 压缩后不降反升，于是压缩后立刻又要压缩。

上下文压缩有两种来源，检测方式不同：
- **服务端压缩**：`/v1/responses/compact` 产出显式 `type:"compaction"` item，`utils.ResponsesRawItemsContainCompaction` / `ResponsesItemsContainCompactionFromInput` 一眼可辨。
- **客户端压缩**（Cursor）：客户端静默重写自己的 transcript——把中间若干轮换成一条摘要、再原样重发最近几轮，没有任何结构标记。此时 input 比本地历史更短且可能不以 user/developer 开头，按条数或按形态都判不出来，只有与本地 session 的**重叠**能认出来（合法增量只发服务端链上还不存在的 item）。判据与保守规则见 `docs/capabilities.md` 的「Responses 续接边界判定」。

`providers/responses.go` 在转换前判定边界：命中则清空 `PreviousResponseID`、从请求体删掉 `previous_response_id`、把 `sess` 置 nil（旧 session 只用于响应完成后替换本地状态，不能参与本次转换），并置 `ContextKeyResponsesHistoryBoundary` / `ContextKeyResponsesPreviousIDDropped`。这两个 context key 下游驱动三件事：`prepareResponsesUsageRequestBody` 的 usage 校正门控、session 侧走 `ReplaceSessionAfterBoundary` 而非 `CommitTurn`、以及 `clearResponsesPreviousIDs`。同一请求可能在多渠道/多 Key 间 failover，两个 key 在每个候选开始时都会重置，避免前一个候选的状态污染后续候选。

usage 侧另有一条独立的循环成因：OpenAI 兼容上游（grok-4.6 实测）的 `input_tokens_details.cached_tokens` 是跨请求单调递增的累积器，原样下发会让客户端一直看到满缓存。剥离判据见 `docs/capabilities.md` 的「累积式缓存统计剥离」。

## F6 评测原生发送（观察，不进生产调度）

管理端 `/api/eval/*`（`WebAuthMiddleware`）。与 F1 并列，**不**走 `RunProxyRequest`。

```
FindChannelByID（稳定 UUID，跨五类切片）
→ 空 http.Header + SetAuthenticationHeader / SetGeminiAuthenticationHeader
  + 可选 ApplyClaudeCodeDisguise / ApplyCodexDisguise
→ 按 serviceType 组原生 body（claude messages / openai chat / gemini generateContent / responses）
→ 第一 BaseURL + ResolveUpstreamProxyURL + proxycore.SendRequest
→ Extract（封闭 kind）→ Judge（封闭 kind；rubric 再发一封给同一渠道）
→ 写 eval.db，批次结束后做跨渠道随机数指纹比对（只回填 detail，不改 verdict）
→ 更新 latest-map
```

默认非流式。模型：请求覆盖优先（走 `ResolveUpstreamModel` 映射，但清掉 DefaultModel，免得覆盖值被吞）；否则渠道 `defaultModel`。值班 ticker 到期触发普通 `eval_runs`（`trigger=watch`）；Runner 忙则 skip。
前端进度走 `GET /api/eval/runs/:id/events`（SSE），后端只在 status / 已完成格子数变化时发帧，终态自动断开。

## 模块间通信约定

- 同 JVM：包间直接调用；跨层依赖在 main.go 组装注入，不在业务包内 new 全局单例。
- 持久化：metrics / session / blocked 各自带 sqlite_store，路径由 main.go 注入。
- 日志库：`.config/logs.db`，由 `logger.Setup` 在 main.go 注入；运行日志 / 流量正文 / 请求元数据三表，保留今天与昨天。Web 日志页「系统日志」Tab 按 requestId 看三段正文（`GET /api/system-logs`）。
- 评测库：`.config/eval.db`，由 `eval.NewService` 在 main.go 注入；关闭顺序在调度器 Stop 之后。
- 单价表：`.config/pricing.json`，由 `pricing.NewStore` 在 main.go 注入；**只存用户改写与删除墓碑**，内置价留在代码里。加载失败中止启动（空价表会让全部花费静默变 0，界面上分不清"没配价"与"配置坏了"）。花费不落盘，按当前单价表在读取时折算——详见 `docs/capabilities.md` 的「模型单价表 / 花费折算」。
- 路由总表：main.go 是路由唯一真相；文档不复制路由清单。
