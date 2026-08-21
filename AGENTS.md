# AGENTS.md

本文件是编码代理（Claude Code / OpenCode / Codex 等）在本仓库工作的行为准则。项目知识**按需查 docs/，不复制、不背**（场景路由表见 [CLAUDE.md](CLAUDE.md)）。

## 项目概述

Claude / Codex / Gemini 多上游协议转换代理：五协议统一入口（Messages / Responses / Chat / Gemini / Images）、多渠道调度与故障转移、协议自动转换、内置 Web 管理界面。Go 1.22（Gin）后端 + Vue 3 / Vuetify 前端，前端产物 embed 进后端，单二进制部署。

## 项目结构

- `backend-go/`：主 Go 服务（Gin），Go 代码在 `backend-go/internal/`
- `frontend/`：Vue 3 + Vite + Vuetify 管理界面；产物复制到 `backend-go/frontend/dist/` 由后端 embed
- `dist/`：发布产物（勿手动编辑）；`.config/`：运行时配置（热重载）；`refs/`：外部参考（只读）
- 技术文档一律放 `docs/`

## 常用命令

```bash
make dev / make run / make build / make check       # 根目录（check = 全量门禁）
cd backend-go && make dev / test / check / lint     # 后端
cd frontend && bun run dev / build / check / test   # 前端
```

## 代码风格

- Go：gofmt 格式化，遵循官方规范；日志一律 `[Component-Action]` 标签、禁 emoji；错误显式抛出，不做静默兜底
- 前端：遵循 Prettier + ESLint 风格；新 mdi 图标先在 `iconMap` 注册（`bun run check:icons` 机器校验）
- 遵循 SOLID / KISS / DRY / YAGNI；优先修复根因，避免无关重构

## 测试与验证

- 提交前必跑根目录 `make check`（后端 gofmt + vet + test；前端 type-check + 图标扫描）
- 后端测试优先表驱动 + `httptest`；前端复杂逻辑用 vitest 补单测

## 协作规则

- 始终使用简体中文回复与书写文档。
- 除非用户明确要求：不创建/修改文档、不运行测试、不编译打包；确需验证时先说明原因并等待确认。
- 未经用户明确要求：不执行 git commit / push / branch。
- 新功能先复述：做什么 / 不做什么 / 改哪些文件；确认后再动手。
- 禁止在当前任务范围之外重构、改格式、动无关文件。

## 安全

- 配置/密钥只提交 `*.example`，绝不提交真实密钥或 `.env`
- 日志中上游 API 密钥绝不完整输出，一律 `utils.MaskAPIKey` 脱敏
- 代理端点统一鉴权；生产环境必须设强 `PROXY_ACCESS_KEY`

## 文档规范

- 根目录只保留 `README.md` / `CHANGELOG.md` / `CLAUDE.md` / `AGENTS.md` / `LICENSE`；技术文档只放 `docs/`
- 写入判据：**AI 不知道这条信息会做错决策才写**；代码能推导的事实（接口签名、路由清单）不写文档
- `docs/` 地图：`flows.md`（关键链路顺序）、`capabilities.md`（能力复用登记）、`glossary.md`（术语）、`invariants.md`（铁律细则）、`DEVELOPMENT.md`（开发/打包/发布）、`ENVIRONMENT.md`(环境变量)

## 工具注意

- `git diff` 指定文件用 `--` 分隔：`git diff -- path/to/file`（防路径歧义）
