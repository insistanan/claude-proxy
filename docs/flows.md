# 关键数据流

顺序感文档：改代理链路前先读这里。每个流程标注了关键代码位置，细节以代码为准——本文件只描述"顺序与分工"，代码描述"具体实现"。

## F1 统一代理主链路（messages / chat / images / gemini 四协议）

四个协议收敛到同一个骨架 `proxycore.RunProxyRequest`（协议差异由 `ProtocolSpec` 描述：ParseRequest / BuildUpstreamRequest / HandleSuccess / PreRoute / HookPipeline）。**responses 例外**，走独立链路（见 F5）。

```
1. 认证        ProxyAuthMiddleware（RunProxyRequest 函数内第一步；Web UI 路由另有 WebAuthMiddleware）
2. 读体        ReadRequestBody
3. 解析        spec.ParseRequest（得 model/stream/prompts）
4. 钩子挂载     AttachHookPipeline（内容安全管线）
5. 会话观测     ObserveConversationRequest（对话注册、attempt 记录、绑定 userID）
6. 原生日志     LogOriginalRequest（脱敏）
7. 指定渠道     body 带 channel_index → 直接单渠道分支
8. PreRoute    spec.PreRoute（协议特有前置路由；目前仅 chat 使用，为 modelcatalog 路由）
9. 分派         IsMultiChannelModeForModel → 单渠道 或 多渠道 HandleMultiChannelFailover
```

单渠道尝试（`UpstreamAttempt.TryWithModelMappingFailover`，多渠道模式下每个候选渠道内重复此循环）：

```
选 key（GetNextAPIKey）
→ 构建上游请求（spec.BuildUpstreamRequest，含模型映射）
→ prepareRequestForUpstream：内容安全 pre-request 钩子 → visionlayer.PrepareRequest → 内容安全钩子二次
→ SendRequest
→ 失败：按状态码/错误分类（ShouldRetryWithNextKey；Fuzzy 模式下所有非 2xx 都转移）
→ 成功：spec.HandleSuccess（流式经 HandleStreamResponseCtx + IdleTimeoutReader）
       → post-response 钩子 → scheduler.RecordSuccessWithUsage → 会话/对话成功标记
```

渠道选择顺序（`SelectChannel`，见 `internal/scheduler/channel_scheduler.go`）：

```
1. 对话路由覆盖（Route Override）— 最高优先级，冲突 409
2. 促销渠道（Promotion）— 促销期内 + 次数配额
3. Trace 亲和 — 同 userID+kind 绑定 channelIndex
4. 自适应调度（性能画像打分）
5. 按优先级降级（同优先级选 in-flight 最低）
以上各步均过滤熔断/挂起渠道；渠道内多 BaseURL 按 urlhealth 延迟排序。
```

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

## F5 responses 链路（独立于主骨架）

`/v1/responses` 有自己的 handler 链路：入站后经 `converters` 转换（主分发在 `responses_protocol.go`，仅 Claude 上游走 factory.go 工厂）、内嵌 `session.SessionManager` 会话管理（previous_response_id 链）、流式事件经 `converters.ConvertUpstreamStreamLineToResponses` 转换。它与 F1 共享的是渠道/调度/failover 基础设施，但**不经过 RunProxyRequest**——改 F1 骨架时不会自动波及 responses，反过来也一样。

## 模块间通信约定

- 同 JVM：包间直接调用；跨层依赖在 main.go 组装注入，不在业务包内 new 全局单例。
- 持久化：metrics / session / blocked 各自带 sqlite_store，路径由 main.go 注入。
- 路由总表：main.go 是路由唯一真相；文档不复制路由清单。
