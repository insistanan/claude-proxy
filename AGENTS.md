# 仓库协作指南

## 重要约定
- **始终使用简体中文回复**。
- 遵循 SOLID / KISS / DRY / YAGNI；优先修复根因，避免无关重构。

## 项目结构与模块
- `backend-go/`：主 Go 服务（Gin），构建后内嵌前端静态资源；Go 代码位于 `backend-go/internal/`。
- `frontend/`：Vue 3 + Vite + Vuetify 管理界面；构建产物复制到 `backend-go/frontend/dist/` 并由后端 embed。
- `dist/`：发布构建产物（Go 二进制/打包后的 UI），不要手动编辑。
- `.config/`：运行时配置目录（`config.json` 及 `backups/`），随容器/本地持久化。
- `docs/`：项目文档目录（技术文档、配置指南、性能分析等）。
- `refs/`：外部参考项目存档，仅供对照，默认只读。
- 文档入口：`README.md`、`CLAUDE.md`、`AGENTS.md`、`ARCHITECTURE.md`、`CHANGELOG.md`。

## 文档编写规范
- **所有技术文档必须存放在 `docs/` 目录下**，包括但不限于：
  - 架构设计文档、API 文档
  - 环境配置指南、部署文档
  - 性能分析报告、优化建议
  - 开发指南、测试文档
- **根目录只保留以下标准文件**：
  - `README.md` - 项目介绍和快速开始
  - `CHANGELOG.md` - 版本历史
  - `CLAUDE.md` - Claude Code 工作指南
  - `AGENTS.md` - 仓库协作规范
  - `LICENSE` - 许可证
- **命名规范**：
  - 使用大写下划线（如 `PERFORMANCE_ANALYSIS.md`）
  - 或小写连字符（如 `api-design.md`）
  - 保持项目内一致性

## 构建/测试/开发命令
- 全栈开发（推荐）：根目录 `make dev`（前端 `bun run dev` + 后端 `air` 热重载）。
- 仅后端：`cd backend-go && make dev`。
- 构建运行：
  - 根目录 `make run` / `make build`（先构建前端再编译后端）。
  - 后端本地构建：`cd backend-go && make build-local`。
  - 发布/交付到 `dist/` 的 exe 必须注入版本信息，禁止使用裸 `go build`。Windows amd64 示例：先读取根目录 `VERSION`，再用 `-ldflags "-X main.Version=$version -X main.BuildTime=$buildTime -X main.GitCommit=$gitCommit -s -w"` 编译到 `dist/claude-proxy-windows-amd64.exe`，否则 UI 会显示默认 `v0.0.0-dev`。
- 测试：`cd backend-go && make test`（或 `make test-cover` 生成覆盖率）。
- 前端：`cd frontend && bun install` 后 `bun run dev|build|type-check`。
- Docker：`docker-compose up -d` 默认拉取镜像；本地构建请按 `docker-compose.yml` 注释说明启用 `build`（可选 `Dockerfile_China`）。

## 代码风格
- Go：保持包职责单一、接口清晰；修改后运行 `go fmt ./...`。
- 前端：遵循现有 Vuetify/Tailwind/Prettier 风格；TypeScript 保持 strict。
- 配置/密钥：`.env`/`.json` 只提交示例文件（`*.example`），禁止提交真实密钥。

## 测试规范
- 新增/修改后端逻辑尽量补 `_test.go`，优先表驱动 + `httptest`。
- 前端目前无测试框架；如增加复杂逻辑再引入轻量单测。

## 安全与配置提示
- 部署前必须设置强 `PROXY_ACCESS_KEY`；生产环境建议关闭详细请求/响应日志。
- 代理端点统一鉴权（Header `x-api-key` / `Authorization: Bearer`）；生产环境不建议使用 query `key`。
