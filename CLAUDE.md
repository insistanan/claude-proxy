# CLAUDE.md

> Claude Code 工作指南。协作规范、常用命令、代码风格见 [AGENTS.md](AGENTS.md)；项目知识**按需查 docs/，不复制、不背**（路由表见下）。

## 项目

Claude / Codex / Gemini 多上游协议转换代理。Go 1.22（Gin）后端 + Vue 3 / Vuetify 前端；前端产物 embed 进后端，单二进制部署。

## 动手前（按场景读，唯一正版出处）

| 场景 | 先读 |
|---|---|
| 改渠道 / 调度 / 协议 / 流式链路 | `docs/flows.md`（主链路 + vision 旁路 + responses 例外） |
| 写任何"通用能力" | `docs/capabilities.md`——有现成实现一律复用，新建必须登记 |
| 概念 / 术语 | `docs/glossary.md`——用既有术语，禁止发明平行概念 |
| 硬性规则（违反即返工） | `docs/invariants.md` |
| 后端细节（日志标签表 / 扩展指南） | `backend-go/CLAUDE.md` |
| 前端细节（组件 / store / 图标） | `frontend/CLAUDE.md` |
| 开发 / 打包 / 发布 | `docs/DEVELOPMENT.md`（含 Windows exe 打包与版本发布） |

## 工作流

- 新功能先复述：做什么 / 不做什么 / 改哪些文件；确认后再动手。
- 禁止在当前任务范围之外重构、改格式、动无关文件。

## 完成定义（全部满足才算完）

1. `npm run check` 全绿（后端 gofmt+vet+test；前端 type-check + 图标注册扫描）。
2. 本次变更影响的 docs（上表所指文件）已同步更新。
3. 未新增重复代码；新能力已在 `docs/capabilities.md` 登记。
