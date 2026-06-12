# 仓库协作指南

## 重要约定
- **始终使用简体中文回复**。
- 遵循 SOLID / KISS / DRY / YAGNI；优先修复根因，避免无关重构。
- 除非用户明确要求，不要主动创建或修改文档、不要运行测试、不要执行编译/打包；如确需这些操作来验证问题，先向用户说明原因并等待确认。

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
- Windows exe 打包流程（目标：`dist/claude-proxy-windows-amd64.exe`）：
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
- 测试：`cd backend-go && make test`（或 `make test-cover` 生成覆盖率）。
- 前端：`cd frontend && bun install` 后 `bun run dev|build|type-check`。
- Docker：`docker-compose up -d` 默认拉取镜像；本地构建请按 `docker-compose.yml` 注释说明启用 `build`（可选 `Dockerfile_China`）。

## 代码风格
- Go：保持包职责单一、接口清晰；修改后运行 `go fmt ./...`。
- 前端：遵循现有 Vuetify/Tailwind/Prettier 风格；TypeScript 保持 strict。
  - **重要图标约束**: 任何时候在前端引入新的 mdi 图标（如 `mdi-icon-name`），**都必须且只能**去 `frontend/src/plugins/vuetify.ts`：
    1. 导入该图标：`import { mdiIconName } from '@mdi/js'`
    2. 注册该图标映射：`'icon-name': mdiIconName`
    - **禁止**直接使用未经注册的图标名，否则会导致 UI 降级显示出如 `[icon-name]` 格式的浮窗占位文本，严重破坏页面美观！
  - 图标问题避免方案：
    1. 组件里只使用已注册的 `mdi-xxx` 名称；新增图标时，先在 `frontend/src/plugins/vuetify.ts` 从 `@mdi/js` 导入 PascalCase 变量，例如 `mdiChatOutline`。
    2. 在同一文件的 `iconMap` 中注册 kebab-case 映射，例如 `'chat-outline': mdiChatOutline`，页面中再使用 `mdi-chat-outline`。
    3. 禁止在组件内直接写未注册的 `mdi-xxx`；Vuetify 自定义 iconset 无法解析时会出现占位/fallback，造成菜单、按钮或浮层缺图。
    4. `customSvgIconSet` 可以降级到 `mdiHelpCircle`，但这只是兜底保护，不能代替显式注册。
    5. 交付前用 `rg -n "mdi-[a-z0-9-]+" frontend/src` 扫描新增图标，并对照 `frontend/src/plugins/vuetify.ts` 的 `iconMap` 确认全部已注册。
- 配置/密钥：`.env`/`.json` 只提交示例文件（`*.example`），禁止提交真实密钥。

## 测试规范
- 新增/修改后端逻辑尽量补 `_test.go`，优先表驱动 + `httptest`。
- 前端目前无测试框架；如增加复杂逻辑再引入轻量单测。

## 安全与配置提示
- 部署前必须设置强 `PROXY_ACCESS_KEY`；生产环境建议关闭详细请求/响应日志。
- 代理端点统一鉴权（Header `x-api-key` / `Authorization: Bearer`）；生产环境不建议使用 query `key`。
