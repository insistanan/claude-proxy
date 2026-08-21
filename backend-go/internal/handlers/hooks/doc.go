// Package hooks 提供请求前/流式中/响应后三阶段的内容安全钩子管线。
//
//   - 管线骨架：hook_pipeline.go（HookPipeline / HookStage / HookResult）
//   - 请求前 Hook 与内容安全错误类型：hooks_pre_request.go
//   - 流式期间的延迟匹配与协议化错误写出：hooks_stream.go
//   - 响应后 Hook：hooks_post_response.go
//   - 协议载荷的安全分段提取与脱敏/拦截执行：content_safety_segments.go、
//     content_safety_multipart.go（multipart 表单）、content_safety_protocol.go
//
// 本包是 handlers 内部的叶子依赖：不依赖 proxycore 与 streams；
// proxycore（钩子挂载、错误分类）与 streams（流式钩子回灌）依赖本包。
package hooks
