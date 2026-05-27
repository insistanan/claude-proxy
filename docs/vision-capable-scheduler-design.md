# Vision-Capable 渠道调度设计方案

> 目标：让包含图片的请求优先进入支持视觉能力的渠道，避免明显错误的首跳路由。
>
> 文档版本：v1.1
> 更新日期：2026-05-27

---

## 1. 结论先行

这次改造分成两个层次：

1. **Phase 1：Vision 感知调度**
   - 给渠道增加 `visionCapable` 标记；
   - 对含图请求启用候选池过滤；
   - 对 Responses 会话持久化“历史含图”标记；
   - 保证图片请求不会优先落到明显不支持图片的渠道。

2. **Phase 1.5：补齐最小图片透传能力**
   - 当前仓库里，部分 OpenAI / Responses 转换链路会把图片块直接丢掉或文本化；
   - 如果只做调度，不补这层，仍可能出现“路由正确，但代理把图片吞掉”的问题；
   - 因此本次实现至少要补齐：
     - `/v1/messages` -> OpenAI Chat Completions
     - `/v1/responses` -> Claude Messages
     - `/v1/responses` -> OpenAI Chat Completions

**不在本阶段解决的问题**：

- 不做“按模型粒度”的视觉能力判断，只做渠道级 `visionCapable`；
- 不为 `models`、`compact` 等非对话主链路引入图片判定；
- 不补 OpenAI Completions 这类天然不适合图片输入的路径。

---

## 2. 背景与现状

### 2.1 当前问题

多渠道调度器现在只看：

1. 促销期渠道
2. Trace 亲和
3. 优先级
4. fallback

它不知道“这个请求有没有图片”。
因此一旦含图请求命中普通渠道，就会出现：

- 上游直接报 400/415/能力不支持；
- 或更糟：代理转换层把图片块吃掉，请求变成纯文本，结果悄悄失真。

### 2.2 与代码现状的关键差异

原方案里有几个前提不成立，必须修正：

1. **图片检测不能依赖强类型结构体遍历**
   - 当前 `ClaudeMessage.Content`、`ResponsesRequest.Input`、`ResponsesItem.Content` 都是 `interface{}`；
   - `json.Unmarshal` 后实际拿到的是 `map[string]interface{}` / `[]interface{}`；
   - 所以检测器必须走“通用 JSON 树遍历”，不能假设能直接拿到 `[]ClaudeContent` 或 `[]ContentBlock`。

2. **`SelectChannel` 的调用点不只在两个主 handler**
   - 还包括公共 failover 外壳；
   - 还包括 `responses/compact.go`；
   - 还包括 `messages/models.go`；
   - 还包括调度器测试。

3. **当前转换层并不完整支持图片**
   - `/v1/messages` -> OpenAI 的转换逻辑只处理 `text/tool_use/tool_result`；
   - `/v1/responses` 的 Claude / OpenAI Chat 转换也只提取文本；
   - 所以如果目标是“真实支持 Vision 请求”，调度与转换必须一起改。

---

## 3. 设计目标

### 3.1 必达目标

- 含图请求优先选择 `visionCapable=true` 的渠道；
- Responses API 在 `previous_response_id` 场景下可继承“历史含图”状态；
- 纯文本请求行为与现在保持一致；
- 在当前仓库支持的主链路上，图片块不能被静默丢弃。

### 3.2 可接受退化

- 如果没有任何 Vision 渠道，仍允许 fallback 到全渠道；
- 但必须打明确日志，说明这是“能力不足 fallback”，不是普通熔断恢复；
- 该 fallback 只是保留兼容性，不代表请求一定成功。

### 3.3 非目标

- 不自动探测上游真实视觉能力；
- 不根据 `modelMapping` 精确推导“某模型支持图像、某模型不支持图像”；
- 不为所有 provider 补齐全量多模态协议差异。

---

## 4. 核心方案

### 4.1 渠道增加视觉能力标记

在 `UpstreamConfig` / `UpstreamUpdate` 中新增：

```go
VisionCapable bool  `json:"visionCapable,omitempty"`
VisionCapable *bool `json:"visionCapable"`
```

语义：

- `true`：这个渠道允许接收含图请求；
- `false`：这个渠道只参与纯文本请求，或作为无 Vision 渠道时的 fallback。

### 4.2 调度器增加 `hasImage` 参数

```go
func (s *ChannelScheduler) SelectChannel(
    ctx context.Context,
    userID string,
    failedChannels map[int]bool,
    kind ChannelKind,
    hasImage bool,
) (*SelectionResult, error)
```

调度流程改为：

```text
Step -1: Vision 过滤（仅 hasImage=true 时）
  - activeChannels 中筛出 visionCapable=true 的子集
  - 若子集非空：后续所有步骤仅在子集内进行
  - 若子集为空：记录告警，fallback 到全 activeChannels

Step 0: promotion
Step 1: trace affinity
Step 2: priority
Step 3: fallback
```

原则：

- Vision 过滤只缩小候选池，不改子集内部规则；
- 图片请求下，如果亲和渠道不在 Vision 子集，等价于忽略该亲和绑定。

### 4.3 图片检测

新增 `internal/utils/vision.go`（名称可调整），提供：

```go
func DetectImageContent(body []byte) bool
func ResponsesItemHasVisionContent(item types.ResponsesItem) bool
func ValueHasVisionContent(v interface{}) bool
```

检测策略：

- 先把请求体反序列化成 `map[string]interface{}`；
- 只遍历对话相关字段：
  - `messages[].content`
  - `input`
  - `input[].content`
- 识别以下 block type：
  - `image`
  - `image_url`
  - `input_image`

取舍：

- 宁可少量误报，也不要漏报；
- JSON 解析失败直接返回 `false`，不阻断请求。

### 4.4 Responses 会话含图标记

`Session` 增加：

```go
HasVisionContent bool
```

需要补的能力：

- `AppendMessage`：追加消息时自动更新 `HasVisionContent`
- `GetSessionByResponseID`：通过 `previous_response_id` 查询已有会话
- `MarkSessionHasVisionContent`：在 handler 已经知道 `hasImage=true` 时显式打标

为什么需要显式打标：

- 现有 `parseInputToItems` 逻辑比较宽松；
- 即使输入解析不完整，只要本次请求已被检测为含图，也应保证后续轮次继承该状态。

---

## 5. 转换层范围收敛

### 5.1 本次必须补的链路

#### A. `/v1/messages` -> OpenAI Chat Completions

需要把 Claude 图片块转换成 OpenAI 可接受的 content block：

- Claude:
  - `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}`
- OpenAI Chat:
  - `{"type":"image_url","image_url":{"url":"data:image/png;base64,..."}}`

同时保留同一消息中的文本块：

- `text` -> `{"type":"text","text":"..."}`

#### B. `/v1/responses` -> Claude Messages

需要把 Responses message content 中的图片块还原成 Claude image block。

支持的最小集合：

- `input_text` / `output_text` -> Claude `text`
- `image` / `image_url` / `input_image` -> Claude `image`

#### C. `/v1/responses` -> OpenAI Chat Completions

与 A 类似：

- 文本块映射到 `text`
- 图片块映射到 `image_url`

### 5.2 本次不补的链路

- `Responses -> OpenAI Completions`
- `models`
- `compact`

这些路径本次只保证接口签名兼容，统一传 `hasImage=false`。

---

## 6. 真实改动面

```text
+--------------------------------------------------+----------+------------------------------+
| 文件/模块                                        | 类型     | 说明                         |
+--------------------------------------------------+----------+------------------------------+
| internal/config/config.go                        | 修改     | 新增 VisionCapable 字段      |
+--------------------------------------------------+----------+------------------------------+
| internal/config/config_messages.go               | 修改     | Messages CRUD 支持新字段     |
+--------------------------------------------------+----------+------------------------------+
| internal/config/config_responses.go              | 修改     | Responses CRUD 支持新字段    |
+--------------------------------------------------+----------+------------------------------+
| internal/session/manager.go                      | 修改     | Session 含图标记与查询方法   |
+--------------------------------------------------+----------+------------------------------+
| internal/utils/vision.go                         | 新增     | 图片检测与内容转换辅助       |
+--------------------------------------------------+----------+------------------------------+
| internal/scheduler/channel_scheduler.go          | 修改     | hasImage + Vision 过滤       |
+--------------------------------------------------+----------+------------------------------+
| internal/handlers/common/multi_channel_failover.go | 修改   | 透传 hasImage                |
+--------------------------------------------------+----------+------------------------------+
| internal/handlers/messages/handler.go            | 修改     | 入口检测并传递 hasImage      |
+--------------------------------------------------+----------+------------------------------+
| internal/handlers/responses/handler.go           | 修改     | 检测 + previous_id 会话继承  |
+--------------------------------------------------+----------+------------------------------+
| internal/handlers/responses/compact.go           | 修改     | SelectChannel 新签名适配     |
+--------------------------------------------------+----------+------------------------------+
| internal/handlers/messages/models.go             | 修改     | SelectChannel 新签名适配     |
+--------------------------------------------------+----------+------------------------------+
| internal/providers/openai.go                     | 修改     | Claude -> OpenAI 图片透传    |
+--------------------------------------------------+----------+------------------------------+
| internal/converters/responses_converter.go       | 修改     | Responses -> Claude/OpenAI 图 |
+--------------------------------------------------+----------+------------------------------+
| internal/handlers/messages/channels.go           | 修改     | 返回 visionCapable           |
+--------------------------------------------------+----------+------------------------------+
| internal/handlers/responses/channels.go          | 修改     | 返回 visionCapable           |
+--------------------------------------------------+----------+------------------------------+
| frontend/src/services/api.ts                     | 修改     | Channel 类型增加字段         |
+--------------------------------------------------+----------+------------------------------+
| frontend/src/components/AddChannelModal.vue      | 修改     | 编辑/创建开关                |
+--------------------------------------------------+----------+------------------------------+
| frontend/src/components/ChannelCard.vue          | 修改     | Vision 标识展示              |
+--------------------------------------------------+----------+------------------------------+
```

---

## 7. 分阶段实施

### Phase 1：配置、检测、调度

- 配置模型加 `visionCapable`
- Session 加 `HasVisionContent`
- handler 入口检测 `hasImage`
- 调度器加 Vision 子集过滤
- common failover 透传 `hasImage`

交付标准：

- 纯文本请求行为不变
- 含图请求在存在 Vision 渠道时只从 Vision 子集选择
- Responses 后续轮次可继承“历史含图”

### Phase 1.5：最小图片透传

- Messages -> OpenAI Chat 保留 image blocks
- Responses -> Claude / OpenAI Chat 保留 image blocks

交付标准：

- 图片块不会在代理转换层被静默丢失

### Phase 2：后续可选优化

- `visionCapable` 从渠道级升级到模型级
- 根据请求模型名和 `modelMapping` 做能力校验
- 为 compact 等边角接口补策略

---

## 8. 风险与取舍

```text
+----------------------+--------+--------------------------------------+----------------------------------+
| 风险                 | 级别   | 说明                                 | 处理方式                         |
+----------------------+--------+--------------------------------------+----------------------------------+
| 没有 Vision 渠道     | 中     | 调度仍可能 fallback 到普通渠道       | 保留 fallback，但打明确告警      |
+----------------------+--------+--------------------------------------+----------------------------------+
| 渠道级标记过粗       | 中     | 同一渠道不同模型能力可能不同         | 本阶段接受，后续再做模型级能力   |
+----------------------+--------+--------------------------------------+----------------------------------+
| 图片检测漏报         | 高     | 含图请求仍可能误选普通渠道           | 检测器按 JSON 树宽松遍历         |
+----------------------+--------+--------------------------------------+----------------------------------+
| 转换层静默丢图       | 高     | 看似成功，实际请求失真               | 本次同步补最小图片透传链路       |
+----------------------+--------+--------------------------------------+----------------------------------+
| 旧配置兼容           | 低     | 旧配置中没有 visionCapable 字段      | 缺省 false，前端默认 false       |
+----------------------+--------+--------------------------------------+----------------------------------+
```

---

## 9. 日志建议

建议新增以下日志分组：

- `[Messages-Vision]`
- `[Responses-Vision]`
- `[Scheduler-Vision]`

至少记录：

- 当前请求是否判定为含图
- Vision 子集数量
- 亲和渠道因不在 Vision 子集而被跳过
- 无 Vision 渠道时 fallback 到全渠道

---

## 10. 最终实施原则

1. 先保证“路由不明显错误”。
2. 再保证“代理不把图片偷偷丢掉”。
3. 不为这次需求顺手做大范围抽象重构。
4. 对无法支持的路径，宁可显式保留限制，也不要制造“看起来支持、实际失真”的假象。
