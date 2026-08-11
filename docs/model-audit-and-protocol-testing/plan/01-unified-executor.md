# 统一协议执行器、快速测试与演武台

## 1. 目标

用一个后端执行合同替代前端分散的请求拼装，并让快速测试、演武台和模型审计共享真实协议能力。执行器只负责“如何可靠请求指定渠道并解释协议结果”，不负责身份或能力判分。

## 2. 运行目的和隔离

每次执行必须声明 `purpose`：

- `quick_test`：单次低成本正常请求。
- `playground`：用户自定义输入的交互诊断。
- `identity_probe`：身份策略生成的受控样本。
- `capability_eval`：能力题目的受控样本。

所有目的都必须直达 `channel_id + channel_kind` 指定渠道，不经过故障转移选路。审计类目的不得更新生产健康分、熔断、促销计数、亲和性、密钥优先级或负载均衡结果。鉴权失败可以记录在审计运行中，但不能自动禁用密钥。

## 3. 核心合同

### 3.1 ExecutionSpec

至少包含：运行目的、渠道 ID、渠道类型、请求协议、可选请求模型、解析后的实际模型、思考档位、请求轮廓、流式模式、超时、最大输出、工具/图片能力、会话链参数和脱敏日志级别。

模型留空时由后端读取渠道默认模型；默认模型同样为空时必须返回可操作错误，不能随意选择平台模型。运行创建后保存目标快照，渠道配置后续变化不影响本次解释。

### 3.2 ExecutionResult

统一结果至少包含：状态、HTTP 状态、协议终态、响应 ID、声明模型、返回模型、正文、结构化输出、工具调用、usage、reasoning usage、首字节时间、总耗时、SSE 事件摘要、重试信息、脱敏请求摘要、错误分类和原始证据引用。

状态至少区分：`completed`、`failed`、`incomplete`、`empty_output`、`stream_truncated`、`timeout`、`cancelled`、`unsupported`、`protocol_error`。不得把 HTTP 200、收到任意 delta 或连接关闭直接等同于 completed。

## 4. 协议适配矩阵

| 协议 | 正常请求 | 思考参数 | 流式完成判定 | 特别处理 |
| --- | --- | --- | --- | --- |
| Messages | `messages`、system、max tokens | Claude thinking 或渠道兼容字段 | message/content block 终态 | 版本头、工具块、usage |
| Responses | `input`、`instructions`、`text` | `reasoning.effort` 等官方或轮廓字段 | typed SSE 的 completed/failed/incomplete | `store`、`include`、`previous_response_id`、Codex 元数据 |
| Chat | `messages`、tools、response format | `reasoning_effort` 或渠道声明字段 | choices finish reason / error | OpenAI-compatible 差异和工具调用 |
| Gemini | contents、generation config、tools | thinking config / budget | candidate finish reason / error | thought signature 和原生流事件 |
| Images | prompt、尺寸、质量等 | 不适用 | 图片结果或明确错误 | generation/edit/variation 分入口，不套用文本判定 |

协议能力矩阵必须区分“官方支持”“渠道轮廓支持”“不支持”。思考档位只通过适配器映射；若用户选择的档位无法可靠表达，执行前返回 `unsupported`，禁止静默删除参数。

## 5. Responses 请求轮廓

首期至少支持：

1. 标准 Responses：官方字段和 typed SSE。
2. Codex-compatible：复用渠道现有 ServiceType、ProxyMode 和请求头注入规则。
3. Native Codex：使用明确版本化的客户端元数据和请求合同。
4. 多轮 Responses：支持 `previous_response_id`，同时区分服务端存储和 `store: false` 的显式上下文重放。

执行器必须记录发出的轮廓版本，处理 `response.created`、`response.in_progress`、正文 delta、output item/content part 完成、`response.completed`、`response.failed`、`response.incomplete` 和无终态断流。最终正文优先来自协议终态对象，并校验与累计 delta 的一致性。

不把某个私有渠道恰好需要的非标准 Header 宣称为 OpenAI 官方要求。轮廓字段的来源必须标记为官方、项目兼容或用户配置。

## 6. 快速测试

- 默认使用短小、自然、能够判断是否有有效输出的请求模板，不重复发送明显的 `ping`、`hi` 或探针口令。
- 模板按协议和模态版本化，允许少量轮换；快速测试不得夹带身份判定逻辑。
- 用户可选择模型和思考档位；留空模型按渠道默认值解析。
- `/models` 仅作为可选网络/鉴权诊断步骤，不能代表快速测试成功。
- 结果直接展示正文、模型、usage、耗时、终态和明确错误。

## 7. 演武台

- 复用相同的后端 ExecutionSpec API，提供协议、模型、思考档位、stream、多轮和工具等受支持选项。
- 展示用户输入、规范化请求、脱敏后的实际请求摘要、SSE 时间线、最终输出和错误详情。
- UI 仅展示服务端声明为该协议支持的控件，避免提交后才静默忽略。
- 多轮会话显式管理 response ID 或本地历史；重置会话必须清除相应链状态。

## 8. API 和安全边界

管理 API 应提供能力描述、执行、取消和读取执行详情的接口。密钥只在后端读取，响应和日志必须脱敏。限制输入大小、工具数量、图片大小、超时和并发；错误返回稳定机器码和可读信息。

快速测试与演武台的原始请求证据默认只保存脱敏摘要。若未来允许保存完整内容，必须另加显式配置和保留期，本期不默认开启。

## 9. 验收条件

- 五种 ChannelKind 均经过统一后端执行入口。
- Responses 模拟上游覆盖成功、failed、incomplete、空输出、乱序/重复事件、无终态断流和多轮 ID。
- 思考档位映射有协议级表驱动验证，不支持组合返回显式错误。
- 快速测试不再以 `/models` 成功作为最终结论。
- 演武台能够展示脱敏请求和协议时间线，失败不静默兜底。
- 模拟审计请求不会改变生产健康、调度或密钥状态。
