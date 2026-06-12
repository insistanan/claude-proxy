# 项目架构

一体化架构：Go 后端（Gin）嵌入前端构建产物，单二进制部署。

## 目录结构

`
claude-proxy/
├── backend-go/              # Go 后端（主程序）
│   ├── main.go              # 入口：初始化各组件、注册路由、优雅关闭
│   └── internal/
│       ├── handlers/        # HTTP 处理器（按 API 协议拆分）
│       │   ├── messages/    # Anthropic Messages API
│       │   ├── responses/   # OpenAI Responses API（含 compact）
│       │   ├── chat/        # OpenAI Chat Completions API
│       │   ├── gemini/      # Google Gemini API
│       │   └── common/      # 多渠道 failover、流式处理、对话管理
│       ├── providers/        # 上游适配器（Claude/OpenAI/Gemini/Responses）
│       ├── converters/       # 双向协议转换器（工厂模式）
│       ├── scheduler/        # 多渠道调度：亲和 > 促销 > 优先级 + 熔断降级
│       ├── session/          # Responses API 会话管理 + Trace 亲和性
│       ├── metrics/          # 渠道健康指标（滑动窗口、SQLite 持久化）
│       ├── urlhealth/        # 多端点 URL 健康检测与动态排序
│       ├── config/           # 配置管理（fsnotify 热重载、JSON 持久化）
│       ├── conversation/     # 对话上下文注册与路由覆盖
│       ├── modelcatalog/     # 模型目录（别名解析、后缀剥离）
│       ├── middleware/       # 认证、CORS、日志过滤、Web UI 门控
│       └── types/           # 共享类型定义
├── frontend/                 # Vue 3 + Vuetify 3 管理界面
│   └── src/
│       ├── components/      # Vue 组件
│       └── services/        # API 封装 + 多协议模型发现
├── docs/                     # 技术文档
├── .config/                  # 运行时配置（热重载）
└── dist/                     # 发布构建产物
`

## 四类渠道池

| 渠道类型 | API 端点 | 上游协议 |
|---------|---------|---------|
| Messages | POST /v1/messages | Anthropic Claude |
| Responses | POST /v1/responses | OpenAI Responses |
| Chat | POST /v1/chat/completions | OpenAI Chat |
| Gemini | POST /v1beta/models/* | Google Gemini |

每类渠道拥有独立的指标管理器和渠道池，由 ChannelScheduler 统一调度。

## 调度优先级

1. 会话亲和（Trace Affinity）— 同一用户绑定之前成功渠道
2. 促销渠道 — 仅在无亲和或亲和渠道失败后生效
3. 优先级遍历 — 按 priority 字段升序
4. 降级选择 — 失败率最低的可用渠道

亲和策略精细化：仅在新会话、续期、或原亲和渠道在本次请求中失败时才重建亲和。

## 协议转换

converters/ 实现 Responses API 与各上游协议之间的双向转换：

- **Responses -> OpenAI Chat**：custom tool 降级为 function（嵌入原始定义）；namespace tool 拍平为 parent__child；web_search 降级为 function；无 tools 时移除 tool_choice/parallel_tool_calls
- **OpenAI Chat -> Responses**：从原始请求恢复 custom_tool_call / tool_search_call 类型；按类型构建不同 output 结构（arguments vs input vs execution）
- **Responses -> Claude**：custom_tool_call 使用 {"input": ...} 作为 tool_use 输入
- **Channels -> Gemini**：无 tools 时不发送 toolConfig

## 指标与熔断

- 滑动窗口（默认最近 10 次请求）计算失败率
- 失败率 >= 50% 触发熔断，15 分钟后自动恢复
- 成功请求立即清除熔断状态
- 支持 SQLite 持久化（可配置保留天数）
- 缓存命中率零值也序列化，确保前端正确显示

## 会话管理

Responses API 通过 previous_response_id 实现多轮对话，由 SessionManager 维护会话历史（默认 24h 过期、最多 100 条消息、100k tokens）。ConversationRegistry 基于 conversation_id / allback_key 建立对话路由，支持对话级别路由覆盖。

## 模型后缀

代理支持 [1m] 后缀（如 opus[1m]），自动剥离后发送到上游。详见 config.ResolveUpstreamModel。
