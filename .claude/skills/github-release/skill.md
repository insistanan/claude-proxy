---
name: github-release
description: 发布 GitHub Release，从 CHANGELOG 生成发布公告并更新 Draft Release (project)
version: 1.2.0
author: https://github.com/insistanan/api-proxy/
allowed-tools: Bash, Read
context: fork
---

# GitHub Release 发布技能

## 触发条件

当用户输入包含以下关键词时触发：

- "发布公告"、"发布说明"、"release notes"
- "发布 release"、"publish release"
- "更新 draft"、"编辑 release"

## 执行步骤

### 1. 获取最新 tag 和检查所有 Draft Release

```bash
# 获取最新 tag
git describe --tags --abbrev=0

# 获取所有 tag 列表
git tag --sort=-v:refname | head -10

# 获取所有 release 列表（包含 draft 状态）
gh release list --limit 10
```

**多 Draft 处理策略**：

- 如果存在多个 Draft Release，只发布最新版本
- 删除中间版本的 Draft Release（快速迭代场景下的合理做法）
- 合并所有中间版本的 changelog 到最新版本的发布公告

### 2. 清理中间版本的 Draft Release

如果检测到多个 Draft：

```bash
# 列出所有 draft release
gh release list --limit 20 | grep -i draft

# 删除中间版本的 draft（保留最新的）
gh release delete <old-tag> --yes
```

**注意**：删除 draft 不会删除对应的 git tag，只是移除 GitHub Release 页面的条目。

### 3. 获取版本间的变更日志

```bash
# 从 CHANGELOG.md 中提取相关版本的内容
cat CHANGELOG.md
```

解析 CHANGELOG.md，提取从上次**公开发布**版本到当前版本的所有变更内容。

### 4. 生成发布公告

根据 CHANGELOG 内容生成简洁的发布公告。

> ⚠️ **【必须】发布公告格式要求**：
>
> 1. 必须按类型分组（✨ 新功能 / 🐛 修复 / 🔧 改进）
> 2. **必须在末尾包含 Full Changelog 链接**（从上次公开发布版本到最新版本）
> 3. Full Changelog 链接前必须加 `---` 分隔线

**标准格式**：

```markdown
### ✨ 新功能

- 功能点 1
- 功能点 2

### 🐛 修复

- 修复点 1
- 修复点 2

### 🔧 改进

- 改进点 1

---

**Full Changelog**: https://github.com/insistanan/api-proxy/compare/v2.3.5...v2.3.7
```

**注意事项**：

- 合并多个小版本的内容到一个公告
- 保持简洁，每个点一行
- **【必须】Full Changelog 链接必须从上次公开发布版本到最新版本**（不是从上一个 Draft 版本）

**内容精简规则（重要）**：

发布公告面向最终用户，必须移除技术实现细节，只保留用户可感知的变化：

| 应移除的内容                              | 应保留的内容                  |
| ----------------------------------------- | ----------------------------- |
| 具体文件路径（`internal/types/types.go`） | 功能名称                      |
| 代码结构（`ClaudeRequest` 结构体）        | 问题现象（返回 403）          |
| 字段名称（`metadata` 字段）               | 用户操作（配置 modelMapping） |
| 实现方式（JSON 反序列化）                 | 修复结果                      |

**精简示例**：

CHANGELOG 原文：

```
- **修复 ModelMapping 导致请求字段丢失** - 解决使用模型重定向时 Claude API 返回 403 的问题：
  - 原因：`ClaudeRequest` 结构体缺少 `metadata` 字段，JSON 反序列化时该字段被丢弃
  - 表现：配置 `modelMapping` 后请求被上游拒绝（如 `opus` → `claude-opus-4-5-20251101`）
  - 修复：在 `ClaudeRequest` 中添加 `Metadata map[string]interface{}` 字段
  - 涉及文件：`backend-go/internal/types/types.go`
```

发布公告精简后：

```
- **修复模型映射功能** - 解决配置 `modelMapping` 后请求被上游拒绝（返回 403）的问题
```

### 5. 更新 Draft Release 并发布

```bash
# 编辑 release 内容并发布
gh release edit <tag> \
  --title "<tag>" \
  --notes "发布公告内容" \
  --draft=false
```

或者如果没有 draft，直接创建：

```bash
gh release create <tag> \
  --title "<tag>" \
  --notes "发布公告内容" \
  --latest
```

### 6. 确认发布成功

```bash
gh release view <tag> --json url,publishedAt
```

输出发布链接供用户确认。

## 输出格式

> ⚠️ **【必须】严格按照以下格式输出，不可省略任何部分**

```
📦 Release 发布完成！

版本: v2.3.7
状态: ✅ 已发布
链接: https://github.com/insistanan/api-proxy/releases/tag/v2.3.7

已清理的 Draft: v2.3.5, v2.3.6（已合并到 v2.3.7 发布公告）

发布内容:
---
### ✨ 新功能
- 功能点

### 🐛 修复
- 修复点

### 🔧 改进
- 改进点

---

**Full Changelog**: https://github.com/insistanan/api-proxy/compare/v2.3.5...v2.3.7
---
```

## 注意事项

- 确保 `gh` CLI 已登录并有仓库权限
- 发布前会显示完整公告内容供用户确认
- 支持多版本合并发布（如 v2.3.5 ~ v2.3.7）
- 多个 Draft 时只发布最新版本，删除中间版本的 Draft
- 删除 Draft 不影响 git tag，仅清理 GitHub Release 页面
