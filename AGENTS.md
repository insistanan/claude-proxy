# 仓库协作指南

> 协作行为准则。AI 工作流程与项目知识路由见 CLAUDE.md；硬性规则细则见 docs/invariants.md。

## 重要约定

- 始终使用简体中文回复。
- 遵循 SOLID / KISS / DRY / YAGNI；优先修复根因，避免无关重构。
- 除非用户明确要求：不创建/修改文档、不运行测试、不编译打包；确需验证时先说明原因并等待确认。
- 未经用户明确要求：不执行 git commit / push / branch。
- 配置/密钥：只提交 `*.example`，绝不提交真实密钥。

## 项目结构

- `backend-go/`：主 Go 服务（Gin），内置前端静态资源；Go 代码在 `backend-go/internal/`。
- `frontend/`：Vue 3 + Vite + Vuetify 管理界面；产物复制到 `backend-go/frontend/dist/` 由后端 embed。
- `dist/`：发布产物（勿手动编辑）；`.config/`：运行时配置（热重载）；`refs/`：外部参考（只读）。
- 文档入口：`README.md`、`CLAUDE.md`、`AGENTS.md`、`CHANGELOG.md`；其余技术文档一律 `docs/`。

## 文档规范要点

- 技术文档只放 `docs/`；根目录只保留 `README.md` / `CHANGELOG.md` / `CLAUDE.md` / `AGENTS.md` / `LICENSE`。
- 命名：大写下划线（`PERFORMANCE_ANALYSIS.md`）或小写连字符（`api-design.md`），保持一致性。
- 写入判据：**AI 不知道这条信息会做错决策才写**；代码能推导的事实（接口签名、路由清单）不写文档。

## 硬性规则（细则一律见 docs/invariants.md，不在此复制）

- 后端日志 `[Component-Action]` 标签、禁 emoji（标签表唯一出处：`backend-go/CLAUDE.md`）。
- 前端新图标必须先注册 `iconMap`（机器校验：`bun run check:icons`）。
- 发布 exe 禁止裸 `go build`，必须注入版本（流程：`docs/DEVELOPMENT.md`）。
- 代理端点统一鉴权；生产环境必须设强 `PROXY_ACCESS_KEY`。

## 工具注意

- `git diff` 指定文件用 `--` 分隔：`git diff -- path/to/file`（防路径歧义）。