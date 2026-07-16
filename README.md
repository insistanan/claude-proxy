> ⚠️ **项目已重命名**: 本项目已重命名为 **[CCX](https://github.com/BenedictKing/ccx)**，请访问新仓库获取最新版本和更新。本仓库已归档，不再维护。

---

# Claude / Codex / Gemini API Proxy

[![GitHub release](https://img.shields.io/github/v/release/BenedictKing/claude-proxy)](https://github.com/BenedictKing/claude-proxy/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

一个高性能的 Claude API 代理服务器，支持多种上游 AI 服务提供商（Claude、Codex、Gemini），提供故障转移、多 API 密钥管理和统一入口访问。

## 🚀 功能特性

- **🖥️ 一体化架构**: 后端集成前端，单容器部署，完全替代 Nginx
- **🔐 统一认证**: 一个密钥保护所有入口（前端界面、管理 API、代理 API）
- **📱 Web 管理面板**: 现代化可视化界面，支持渠道管理、实时监控和配置
- **三 API 支持**: 同时支持 Claude Messages API (`/v1/messages`)、Codex Responses API (`/v1/responses`) 和 Gemini API
- **统一入口**: 通过统一端点访问不同的 AI 服务
- **多上游支持**: 支持 Claude、Codex 和 Gemini 等多种上游服务
- **🔌 协议转换**: Messages API 支持协议自动转换，统一接入不同上游服务
- **🎯 智能调度**: 多渠道智能调度器，支持优先级排序、健康检查和自动熔断
- **📊 渠道编排**: 可视化渠道管理，拖拽调整优先级，实时查看健康状态
- **🔄 Trace 亲和**: 同一用户会话自动绑定到同一渠道，提升一致性体验
- **故障转移**: 自动切换到可用渠道，确保服务高可用
- **多 API 密钥**: 每个上游可配置多个 API 密钥，自动轮换使用（推荐 failover 策略以最大化利用 Prompt Caching）
- **🧠 缓存统计**: 按 Token 口径展示各渠道缓存读/写与命中率（命中率 = `cache_read_tokens / (cache_read_tokens + input_tokens)`）
- **增强的稳定性**: 内置上游请求超时与重试机制，确保服务在网络波动时依然可靠
- **自动重试与密钥降级**: 检测到额度/余额不足等错误时自动切换下一个可用密钥；若后续请求成功，再将失败密钥移动到末尾（降级）；所有密钥均失败时按上游原始错误返回
- **⚡ 自动熔断**: 基于滑动窗口算法检测渠道健康度，失败率过高自动熔断，15 分钟后自动恢复
- **双重配置**: 支持命令行工具和 Web 界面管理上游配置
- **环境变量**: 通过 `.env` 文件灵活配置服务器参数
- **健康检查**: 内置健康检查端点和实时状态监控
- **日志系统**: 完整的请求/响应日志记录
- **📡 支持流式和非流式响应**
- **🛠️ 支持工具调用**
- **💬 会话管理**: Responses API 支持多轮对话的会话跟踪和上下文保持

## 🗂️ 运行时数据与清理

运行时配置和本地数据默认保存在 `.config/` 目录：

| 文件 | 保存内容 | 是否可删除 |
| --- | --- | --- |
| `conversations.db` | 本地对话记录、会话上下文、对话名称、路由关联及图片理解缓存 | 可以。请先停止服务；删除后会自动重建，但历史对话和本地会话关联会清空。 |
| `metrics.db` | 渠道和密钥的请求统计、延迟、RPM/TPM、健康状态及性能数据 | 可以。请先停止服务；删除后会自动重建，统计数据会从零开始重新积累。 |
| `config.json` | 渠道、Base URL、API Key、模型映射、子池及图片理解等全部运行配置 | 不建议。删除等同于重置配置，需要重新配置渠道和密钥。 |

删除或修改 `config.json` 前，请先保留 `.config/backups/` 中的配置备份。

## 📄 许可证

本项目基于 MIT 许可证开源 - 查看 [LICENSE](LICENSE) 文件了解详情。
