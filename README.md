# Claude / Codex / Gemini API Proxy

[![GitHub release](https://img.shields.io/github/v/release/insistanan/api-proxy)](https://github.com/insistanan/api-proxy/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

一个高性能的多上游 AI 代理服务器：五协议统一入口（Messages / Responses / Gemini / Chat / Images），多渠道智能调度与故障转移，Web 管理面板单二进制部署。

## 🚀 功能特性

- **🖥️ 一体化架构**: 后端（Go/Gin）集成前端（Vue 3 + Vuetify），单二进制/单容器部署
- **五协议入口**: Claude Messages API (`/v1/messages`)、Responses API (`/v1/responses`)、Chat Completions (`/v1/chat/completions`)、Images (`/v1/images/*`)、模型列表 (`/v1beta/models/*`)
- **🔌 协议转换**: 统一接入 Claude / OpenAI / Gemini 等多上游，Messages 支持协议自动转换
- **🔐 统一认证**: 一个 `PROXY_ACCESS_KEY` 保护前端界面、管理 API、代理 API
- **📊 智能调度**: 优先级排序、健康检查、滑动窗口自动熔断（失败率超阈值挂起 15 分钟自动恢复）、Trace 亲和（同用户绑同渠道）、促销渠道、对话路由覆盖
- **🔄 故障转移**: 渠道 / key / 多 BaseURL 三级 failover，key 失败降级轮换
- **🖼️ 图片理解**: 图片请求自动转分析描述文本（视觉分流 + 两级缓存 + 并发去重）
- **🧠 内容安全**: 敏感词 / 凭据 / 危险命令检测管线，拦截记录可查
- **💬 会话管理**: Responses 多轮会话（previous_response_id 链），SQLite 持久化
- **🛠️ 工具调用**: 支持工具调用与流式/非流式响应
- **🧾 缓存统计**: 按 Token 口径展示缓存读/写与命中率
- **📱 Web 管理面板**: 渠道管理、实时监控、对话/日志/拦截记录、Skills、客户端配置（DSH / OpenCode / Claude Code / PiAgent）、Settings

## 🚀 快速开始

```bash
# 1. 复制环境变量示例并设置强密钥
cp backend-go/.env.example backend-go/.env
# 编辑 .env：设置 PROXY_ACCESS_KEY=<strong-random-key>、ENV=production

# 2. 构建（自动构建前端 + 版本注入，禁止裸 go build）
make build

# 3. 运行
make run
# 或直接运行产物 dist/api-proxy-<platform>
```

开发模式见 `docs/DEVELOPMENT.md`（热重载 / 前端 dev server）。

## 🗂️ 运行时数据与清理

运行时配置和本地数据默认保存在 `.config/` 目录：

| 文件 / 目录 | 保存内容 | 是否可删除 |
| --- | --- | --- |
| `config.json` | 渠道、Base URL、API Key、模型映射、分组等全部运行配置 | 不建议。删除等同于重置配置 |
| `conversations.db` | 本地对话记录、会话上下文、路由关联及图片理解缓存 | 可以。停止服务后删除会自动重建，历史会话清空 |
| `metrics.db` | 渠道和 key 的请求统计、延迟、RPM/TPM、健康状态及性能数据 | 可以。停止服务后删除会自动重建，统计重新积累 |
| `blocked-logs.db` | 内容安全拦截记录 | 可以。停止服务后删除会自动重建 |
| `logs.db` | 运行日志、请求/响应正文、请求元数据（保留今天与昨天） | 可以。停止服务后删除会自动重建 |

> 删除或修改 `config.json` 前，请先保留 `.config/backups/` 中的配置备份。

## 📚 文档

- 关键数据流（主链路 / 视觉旁路 / 内容安全）：`docs/flows.md`
- 术语表：`docs/glossary.md`（概念定义，禁止发明平行概念）
- 能力注册表：`docs/capabilities.md`（写通用能力前先查，有现成实现一律复用）
- 硬性规则：`docs/invariants.md`
- 环境变量：`docs/ENVIRONMENT.md`
- 开发 / 打包 / 发布：`docs/DEVELOPMENT.md`

## 🤝 贡献

1. Fork 本项目，创建特性分支开发
2. 提交信息遵循 Conventional Commits：`feat:` / `fix:` / `refactor:` / `chore:` / `docs:`
3. 提交前跑全量门禁 `make check`（后端 gofmt + vet + test；前端 type-check + 图标扫描）
4. 推送并开启 Pull Request

## 🔗 友情链接

- [LINUX DO - 新的理想型社区](https://linux.do/)

## 📄 许可证

本项目基于 MIT 许可证开源 - 查看 [LICENSE](LICENSE) 文件了解详情。
