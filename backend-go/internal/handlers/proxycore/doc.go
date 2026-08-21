// Package proxycore 提供五协议共享的代理编排核心。
//
// 五协议（Messages/Responses/Gemini/Chat/Images）共享的请求骨架、上游尝试
// 与故障转移决策，按垂直职责分区；文件命名与分区对齐：
//
//   - 入口/分派：protocol.go（RunProxyRequest + ProtocolSpec）
//   - 请求处理：request_body / request_send / request_channel /
//     request_prompts / request_sanitize
//   - 单次上游尝试：upstream_attempt.go 及 _state/_keys/_error/_log、
//     upstream_attempt_builder.go（构造收敛）
//   - 故障转移：failover_classify（重试分类）、failover_error（全失败兜底）、
//     failover_compat（上游兼容性探测）、content_policy_retry（审核兼容重试）、
//     upstream_failover（候选模型映射）、multi_channel_failover（多渠道循环）
//   - 会话观测：conversation.go（注册表读写）、conversation_identity.go
//     （身份/指纹解析）
//
// 依赖方向：本包只依赖 hooks（内容安全错误类型与钩子管线）；
// 流式链路在 internal/handlers/streams，由各协议 handler 直接调用。
package proxycore
