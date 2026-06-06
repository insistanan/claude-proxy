# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

Claude / Codex / Gemini API Proxy - 支持多上游 AI 服务的协议转换代理，提供 Web 管理界面和统一 API 入口。

**技术栈**: Go 1.22 (后端) + Vue 3 + Vuetify (前端) + Docker

## 文档规范

- **文档位置**: 所有技术文档统一存放在 `docs/` 目录下
- **文档类型**:
  - 架构设计文档 → `docs/`
  - 环境配置文档 → `docs/`
  - 性能分析文档 → `docs/`
  - API 文档 → `docs/`
  - 开发指南 → `docs/`
- **例外**: 根目录只保留以下标准文件:
  - `README.md` - 项目介绍和快速开始
  - `CHANGELOG.md` - 版本历史
  - `CLAUDE.md` - Claude Code 工作指南
  - `AGENTS.md` - 仓库协作规范
  - `LICENSE` - 许可证
- **命名规范**: 使用大写下划线命名（如 `PERFORMANCE_ANALYSIS.md`）或小写连字符（如 `api-design.md`）

## 常用命令

```bash
# 根目录（推荐）
make dev              # Go 后端热重载开发（不含前端）
make run              # 构建前端并运行 Go 后端
make frontend-dev     # 前端开发服务器
make build            # 构建前端并编译 Go 后端
make clean            # 清理构建文件
docker-compose up -d  # Docker 部署

# Go 后端开发 (backend-go/)
make dev              # 热重载开发模式
make test             # 运行所有测试
make test-cover       # 测试 + 覆盖率报告（生成 coverage.html）
make build            # 构建生产版本
make lint             # 代码检查（需要 golangci-lint）
make fmt              # 格式化代码
make deps             # 更新依赖

# 运行特定测试
go test -v ./internal/converters/...       # 运行单个包测试
go test -v -run TestName ./internal/...    # 运行单个测试

# 前端开发 (frontend/)
bun install && bun run dev    # 开发服务器
bun run build                 # 生产构建
```

## 架构概览

```
claude-proxy/
├── backend-go/                 # Go 后端（主程序）
│   ├── main.go                # 入口、路由配置
│   └── internal/
│       ├── handlers/          # HTTP 处理器
│       │   ├── messages/      # Messages API 处理器
│       │   ├── responses/     # Responses API 处理器
│       │   ├── chat/          # Chat API 处理器
│       │   ├── gemini/        # Gemini API 处理器
│       │   └── common/        # 通用处理逻辑（failover、stream、conversation）
│       ├── providers/         # 上游适配器 (openai.go, gemini.go, claude.go)
│       ├── converters/        # 协议转换器（工厂模式）
│       ├── scheduler/         # 多渠道调度器（优先级、熔断、对话路由）
│       ├── session/           # 会话管理 + Trace 亲和性
│       ├── metrics/           # 渠道指标（滑动窗口算法、SQLite 持久化）
│       ├── config/            # 配置管理（fsnotify 热重载）
│       ├── middleware/        # 认证、CORS、日志过滤
│       ├── conversation/      # 对话上下文管理
│       ├── modelcatalog/      # 模型目录管理
│       └── types/             # 类型定义
├── frontend/                   # Vue 3 + Vuetify 前端
│   └── src/
│       ├── components/        # Vue 组件
│       │   ├── ChannelOrchestration.vue
│       │   ├── ChannelCard.vue
│       │   ├── AddChannelModal.vue
│       │   ├── ChannelMetricsChart.vue
│       │   └── KeyTrendChart.vue
│       ├── services/          # API 服务封装
│       └── stores/            # 状态管理
├── docs/                       # 项目文档
│   ├── ENVIRONMENT.md         # 环境变量配置指南
│   ├── PERFORMANCE_ANALYSIS.md # 性能分析报告
│   └── screenshots/           # 截图资源
├── .config/                    # 运行时配置（热重载）
└── dist/                       # 发布构建产物
```

## 核心设计模式

1. **Provider Pattern** - `internal/providers/`: 所有上游实现统一 `Provider` 接口
2. **Converter Pattern** - `internal/converters/`: 协议转换，工厂模式创建转换器
3. **Failover Pattern** - `internal/handlers/common/`: 多渠道和单上游 failover 逻辑模块化
4. **Session Manager** - `internal/session/`: 基于 `previous_response_id` 的多轮对话跟踪
5. **Scheduler Pattern** - `internal/scheduler/`: 优先级调度、Trace 亲和性、自动熔断、对话路由
6. **Metrics Pattern** - `internal/metrics/`: 滑动窗口算法 + SQLite 持久化，支持模型追踪和缓存统计

## API 端点

**代理端点**:
- `POST /v1/messages` - Claude Messages API（支持 OpenAI/Gemini 协议转换）
- `POST /v1/messages/count_tokens` - Token 计数
- `GET /v1/models` - 模型列表（跨渠道聚合）
- `POST /v1/responses` - Codex Responses API（支持会话管理）
- `POST /v1/responses/compact` - 精简版 Responses API
- `POST /v1/chat/completions` - OpenAI Chat API（支持协议转换）
- `POST /gemini/*` - Gemini API 透传
- `GET /health` - 健康检查（无需认证）

**管理 API** (`/api/`):
- `/api/messages/channels` - Messages 渠道 CRUD
- `/api/responses/channels` - Responses 渠道 CRUD
- `/api/gemini/channels` - Gemini 渠道 CRUD
- `/api/messages/channels/:id/keys/metrics` - Key 级别指标
- `/api/messages/channels/:id/keys/metrics/history` - Key 历史指标
- `/api/messages/channels/metrics` - 全局渠道指标
- `/api/messages/channels/scheduler/stats` - 调度器统计
- `/api/messages/ping/:id` - 渠道连通性测试
- `/api/dashboard/:apiType` - Dashboard 数据（apiType: messages/responses/gemini）

## 关键配置

| 环境变量 | 默认值 | 说明 |
|---------|--------|------|
| `PORT` | 3000 | 服务器端口 |
| `ENV` | production | 运行环境 |
| `PROXY_ACCESS_KEY` | - | **必须设置** 访问密钥 |
| `QUIET_POLLING_LOGS` | true | 静默轮询日志 |
| `MAX_REQUEST_BODY_SIZE_MB` | 50 | 请求体最大大小 |

完整配置参考 `backend-go/.env.example`

## 常见任务

1. **添加新的上游服务**: 在 `internal/providers/` 实现 `Provider` 接口，在 `GetProvider()` 注册
2. **修改协议转换**: 编辑 `internal/converters/` 中的转换器
3. **调整调度策略**: 修改 `internal/scheduler/channel_scheduler.go`
4. **前端界面调整**: 编辑 `frontend/src/components/` 中的 Vue 组件

## 重要提示

- **Git 操作**: 未经用户明确要求，不要执行 git commit/push/branch 操作
- **配置热重载**: `backend-go/.config/config.json` 修改后自动生效，无需重启
- **环境变量变更**: 修改 `.env` 后需要重启服务
- **认证**: 所有端点（除 `/health`）需要 `x-api-key` 头或 `PROXY_ACCESS_KEY`

## Git 命令注意事项

- 执行 `git add`/`git commit` 前确保在项目根目录
- `git diff` 查看特定文件时使用 `--` 分隔符避免歧义：`git diff -- path/to/file`
- 错误示例：`git diff frontend/src/file.vue`（可能报 `unknown revision` 错误）
- 正确示例：`git diff -- frontend/src/file.vue`

## 模块文档

- [backend-go/CLAUDE.md](backend-go/CLAUDE.md) - Go 后端详细文档
- [frontend/CLAUDE.md](frontend/CLAUDE.md) - Vue 前端详细文档
- [ARCHITECTURE.md](ARCHITECTURE.md) - 详细架构设计文档
- [docs/ENVIRONMENT.md](docs/ENVIRONMENT.md) - 环境变量配置指南
- [docs/PERFORMANCE_ANALYSIS.md](docs/PERFORMANCE_ANALYSIS.md) - 性能分析报告
- [CHANGELOG.md](CHANGELOG.md) - 版本历史和更新日志
