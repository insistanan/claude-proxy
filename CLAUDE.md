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

### Windows exe 打包流程

目标产物：`dist/claude-proxy-windows-amd64.exe`

1. 先构建前端：`cd frontend && bun run build`；如果本机 `bun` 不在 PATH，但已有 npm 环境，可用 `npm run build`。
2. 将 `frontend/dist/*` 复制到 `backend-go/frontend/dist/`，确保 Go embed 打包到最新 UI。
3. 回到 `backend-go/`，读取根目录 `VERSION`，生成 `BuildTime`，读取 `git rev-parse --short HEAD`，并设置 `CGO_ENABLED=0`、`GOOS=windows`、`GOARCH=amd64`。
4. 使用版本注入编译，禁止裸 `go build`：
   ```powershell
   $version=(Get-Content ..\VERSION -Raw).Trim()
   $buildTime=(Get-Date -Format "yyyy-MM-dd_HH:mm:ss_zzz")
   $gitCommit=(git rev-parse --short HEAD 2>$null)
   if (-not $gitCommit) { $gitCommit="unknown" }
   $env:CGO_ENABLED="0"
   $env:GOOS="windows"
   $env:GOARCH="amd64"
   go build -ldflags "-X main.Version=$version -X main.BuildTime=$buildTime -X main.GitCommit=$gitCommit -s -w" -o ..\dist\claude-proxy-windows-amd64.exe .
   ```
5. 构建后用 `Get-Item dist\claude-proxy-windows-amd64.exe` 确认产物存在；运行时 UI 版本不应显示 `v0.0.0-dev`。

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

## 模型支持

### 静态模型列表

代理默认支持以下模型别名（通过 `/v1/models` 端点返回）：

- `opus` - Claude Opus 系列（高性能推理模型）
- `sonnet` - Claude Sonnet 系列（日常编码模型）
- `haiku` - Claude Haiku 系列（快速高效模型）
- `fable` - Claude Fable 5（最新复杂任务模型）
- `gpt` - OpenAI GPT 系列
- `codex` - OpenAI Codex 系列
- `gemini` - Google Gemini 系列

### 上下文窗口后缀

支持 Claude Code 的 `[1m]` 后缀标识 1M token 上下文窗口：

- `opus[1m]` - 使用 1M 上下文的 Opus
- `sonnet[1m]` - 使用 1M 上下文的 Sonnet
- `claude-opus-4-8[1m]` - 任何模型名 + `[1m]` 后缀

代理会自动剥离 `[1m]` 后缀后再发送到上游，确保与 Claude Code 和 Cursor 等客户端的兼容性。

详细说明请参考 [模型后缀处理文档](docs/MODEL_SUFFIX_HANDLING.md)。

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

- **执行边界**: 除非用户明确要求，不要主动创建或修改文档、不要运行测试、不要执行编译/打包；如确需这些操作来验证问题，先向用户说明原因并等待确认。
- **Git 操作**: 未经用户明确要求，不要执行 git commit/push/branch 操作
- **前端开发约束**:
  - **图标使用规则**: 本项目使用 **按需导入 SVG 图标** 方案。任何时候在 Vue 组件中使用新图标（如 `<v-icon>mdi-new-icon</v-icon>`），**都必须且只能**先编辑 `frontend/src/plugins/vuetify.ts`：
    1. 从 `@mdi/js` 导入对应的驼峰命名图标：`import { mdiNewIcon } from '@mdi/js'`
    2. 在 `iconMap` 中添加 kebab-case 图标别名到 SVG path 的映射：`'new-icon': mdiNewIcon`
    3. 页面中只使用已注册的 `mdi-new-icon`
    - **严禁**直接在 Vue 文件中使用未注册在 `iconMap` 里的 mdi 图标，否则在开发/生产环境中会显示文字异常别名（如 `[new-icon]` 浮窗文字）。
    - `customSvgIconSet` 可以降级到 `mdiHelpCircle`，但这只是兜底保护，不能代替显式注册。
    - 交付前用 `rg -n "mdi-[a-z0-9-]+" frontend/src` 扫描新增图标，并对照 `frontend/src/plugins/vuetify.ts` 的 `iconMap` 确认全部已注册。
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
- [ARCHITECTURE.md](docs/ARCHITECTURE.md) - 详细架构设计文档
- [docs/ENVIRONMENT.md](docs/ENVIRONMENT.md) - 环境变量配置指南
- [docs/PERFORMANCE_ANALYSIS.md](docs/PERFORMANCE_ANALYSIS.md) - 性能分析报告
- [CHANGELOG.md](CHANGELOG.md) - 版本历史和更新日志
