# 版本历史

> **注意**: v2.0.0 开始为 Go 语言重写版本，v1.x 为 TypeScript 版本

---

## [v2.8.0] - 2026-06-07

### 新增

- **请求日志归档与可视化可观测性**
  - 新增 `RequestLogStore` 组件，用于记录每次请求尝试（Attempt）和会话明细，支持本地 JSONL 文件自动归档与过期清理（默认保留 7 天）
  - 后端提供 `/api/request-logs` 查询接口，支持按 API 类型进行过滤与分页限制（最大 50 条）
  - 前端新增 `RequestLogsView` 请求日志管理页面，以表格形式直观展示请求时间、类型、渠道、状态、模型转换、首 Token 耗时、Token 吞吐（I/O）、缓存读写（C/R）、吞吐量比（TPM）以及错误排查日志
  - 侧边栏及导航栏新增请求日志入口，图标采用 `mdi-text-box-search-outline`（已导入 `@mdi/js`）
  - 支持会话上下文级别联动：可通过点击日志或对话列表中的 UUID 快速打开该会话/请求的“可观测性详情 Dialog”

- **会话可观测性深度解析**
  - 扩展 `Observation` 与 `Record` 数据结构，新增 `Prompts` 数组字段，支持追踪和存储对话中最近 3 条请求 Prompt 的摘要信息
  - 支持无 `conversationID` 时的 Fallback 路由机制，以 `model+prompt` 的 SHA1 摘要匹配历史会话亲和
  - 前端 `ConversationsView` 重构升级：
    - 支持点击 ID 弹窗展示“对话可观测详情”，包含：会话完整拓扑元数据、请求/路由模型、生成时间与最新活跃时间、完整 Prompt 历史轨迹追踪、重试次数、API 密钥掩码及失败日志
    - 对话列表中支持折叠展示最近 3 次 Prompt 历程，提供一键复制 ID 及直接跳转检索等便捷功能

- **渠道编排可观测性仪表盘升级**
  - 前端渠道编排 `ChannelOrchestration` 卡片集成各渠道实时状态监控与指标简图
  - 支持卡片内展示各 Key 的活跃度、失败统计、以及可观测性日志面板

- **多接口适配层与 Failover 机制补全**
  - 在 `upstream_failover` 和 `request` 中集成 `RequestLogStore` 的自动打点打标签，对每次 retry / failover / error 自动分级归档并提供脱敏记录
  - 完善 OpenAI, Gemini, Responses, Messages 等底层提供商对 logs 的统一打点转换逻辑

---

## [v2.7.0] - 2026-06-07

### 新增

- **OpenAI 协议转换增强**
  - 流式请求自动注入 `stream_options.include_usage: true`，确保上游返回 usage 数据
  - 统一 usage 解析：提取 `normalizeOpenAIUsage()` 处理多层嵌套缓存 token（支持 `input_tokens_details` 和 `prompt_tokens_details`）
  - 流式响应：累积 text delta 后合并发送，避免碎片化 `content_block_delta` 事件
  - 正确发送 `message_delta` 事件携带 `stop_reason` 和 `usage` 信息
  - 非流式响应补全 `cache_creation`/`cache_ttl` 等字段映射
  - Chat 转换器同步支持 `openAIChatStreamOptions()` 逻辑

- **指标系统增强**
  - `RequestRecord`/`PersistentRecord` 增加 `Model` 字段，指标追踪按模型记录
  - SQLite 存储 schema 新增 `model` 列，含平滑迁移逻辑（`ALTER TABLE` 兼容旧数据库）
  - `KeyHistoryDataPoint` API 返回 `model`（去重逗号连接）和 `cacheHitRate`（缓存命中率百分比）字段
  - `RecordRequestConnected` 签名扩展接收 `model` 参数

- **渠道统计图表重构 (ChannelMetricsChart)**
  - 新增 Token 使用量和缓存统计图表行
  - Tooltip 显示模型名称和缓存命中率
  - 多 key 指标历史数据切换展示
  - KeyTrendChart tooltip 优化：流量视图直接显示模型名称

- **对话路由改进**
  - `FallbackKey` 从 `conversationID` 改为 `model+prompt` 的 SHA1 摘要
  - 允许无 `conversationID` 时仍基于模型和首条 prompt 路由亲和
  - `ExtractUserID` 支持 `prompt_cache_key` 及多种 metadata 字段（`user_id`/`conversation_id`/`session_id`/`thread_id`）

- **日志管理增强**
  - 日志目录路径解析：支持相对路径自动转换为二进制文件同级目录
  - 自动清理过期日志：启动时和每 24 小时定时清理超过 `MaxAge` 天数的日志文件
  - 日志保留天数优化：默认从 30 天调整为 7 天，减少磁盘占用
  - Gin 日志集成：将 Gin 的日志输出重定向到统一的日志系统
  - 智能文件识别：根据 `.log`/`.log.gz` 后缀识别日志文件
  - 保护活跃日志：清理时跳过当前正在使用的日志文件

- **Usage 字段提取增强**
  - 新增 `nestedCachedTokens()` 统一提取 `cached_tokens`
  - `checkUsageFieldsWithPatch` 和 `extractUsageFromMap` 补全 `input_tokens_details`/`prompt_tokens_details` 的缓存 token 回退

- **模型目录管理**
  - 新增 `modelcatalog` 模块用于模型目录管理

### 修复

- **Failover 判定逻辑修复**
  - `shouldRetryWithNextKeyFuzzy` 重排检查优先级：不可重试错误 → 配额状态码 → 配额关键词 → 500+ → 默认 failover
  - 修复 500 + `sensitive_words_detected` 误判为可 failover 的问题
  - 修复 500 + 配额关键词未标记 `isQuotaRelated=true` 的问题

### 重构

- **文档结构重组**
  - 创建 `docs/` 统一存放技术文档
  - 移动 `ENVIRONMENT.md` → `docs/ENVIRONMENT.md`
  - 移动 `PERFORMANCE_ANALYSIS.md` → `docs/PERFORMANCE_ANALYSIS.md`
  - 根目录只保留标准文件（`README.md`/`CHANGELOG.md`/`CLAUDE.md`/`AGENTS.md`/`LICENSE`）
  - 更新 `CLAUDE.md` 和 `AGENTS.md` 新增文档编写规范约束

### 测试

- `openai_request_test`: 验证 `stream_options.include_usage` 注入
- `openai_stream_test`: 验证缓存 usage 映射和 text delta 合并
