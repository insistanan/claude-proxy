# UI Skills 推荐清单

本文用于为 Claude Proxy 的 Skills 管理功能筛选可用的 UI/UX Skill 来源。安装第三方 Skill 前，应先检查其 `SKILL.md`、许可证、引用的脚本以及网络访问要求。

## 本机发现范围

Skills 页面按服务进程的当前 Windows/Linux/macOS 用户扫描以下位置：

- Claude Code：`~/.claude/skills`，以及 `~/.claude/plugins/cache/**/skills`、`~/.claude/plugins/marketplaces/**/skills`。
- Codex：`~/.codex/skills` 和内置的 `~/.codex/skills/.system`。
- OpenCode：`$OPENCODE_CONFIG_DIR/skills` 或 `~/.config/opencode/skills`。
- 通用目录：`~/.agents/skills`。
- Cursor：`~/.cursor/skills`，用于将用户目录 Skill 快速复制给 Cursor。

插件缓存和 Codex `.system` 会列出但标记为只读。若页面显示“目录不存在”，通常是 exe 以服务账号或另一个系统用户运行；应检查页面显示的实际路径和 `USERPROFILE`/`HOME`，而不是只看当前桌面用户的目录。

来源标签和顶部目录标签可直接点击筛选。用户目录中的 Skill 可通过复制操作写入其他用户 Agent 目录；内置 Skill、插件缓存和 Marketplace 来源不会提供复制或删除操作。

## 从零设计与构建

### UI UX Pro Max

- 仓库：[nextlevelbuilder/ui-ux-pro-max-skill](https://github.com/nextlevelbuilder/ui-ux-pro-max-skill)
- 许可证：MIT
- 适用：从产品类型、视觉风格、配色、排版到组件细节的设计决策；覆盖 Vue、React、shadcn、Tailwind 等技术栈。
- 建议：适合作为设计方向和设计系统生成的主 Skill。其资源包较大，含 CSV 数据和 Python 脚本，安装前应检查脚本依赖。

### Anthropic frontend-design

- 仓库：[anthropics/skills](https://github.com/anthropics/skills/tree/main/skills/frontend-design)
- 适用：通用前端页面的视觉与交互设计。
- 建议：适合作为保守、通用的基础设计流程，与项目现有组件库搭配使用。

### Anthropic theme-factory

- 仓库：[anthropics/skills](https://github.com/anthropics/skills/tree/main/skills/theme-factory)
- 适用：颜色、排版、间距和语义 token 的主题体系设计。
- 建议：适合新建或重整设计系统，不适合直接替代既有组件库的设计规范。

## 对既有项目审查与整改

### Vercel web-design-guidelines

- 仓库：[vercel-labs/agent-skills](https://github.com/vercel-labs/agent-skills/tree/main/skills/web-design-guidelines)
- 适用：审查任意 Web 项目的可访问性、性能、表单、响应式布局与交互体验。
- 输出：以 `file:line` 形式给出整改项。
- 建议：优先安装。它不依赖 shadcn，因此同样适用于 Vue、Vuetify、Element Plus、原生 CSS 和混合组件库项目。

### Anthropic brand-guidelines

- 仓库：[anthropics/skills](https://github.com/anthropics/skills/tree/main/skills/brand-guidelines)
- 适用：让既有页面遵守品牌语言、颜色、Logo 与文案约束。
- 建议：适合已经存在品牌规范或设计资产的项目。

### Anthropic webapp-testing

- 仓库：[anthropics/skills](https://github.com/anthropics/skills/tree/main/skills/webapp-testing)
- 适用：UI 改造后的浏览器验证和可视化回归检查。
- 建议：应与 UI 审查 Skill 配合使用，避免只产生视觉建议却没有验证闭环。

## 发现与分发平台

### skills.sh

- 网站：[skills.sh](https://skills.sh/)
- 搜索接口：[skills.sh/api/search](https://skills.sh/api/search?q=ui)，使用 `q` 参数进行模糊搜索。
- 内容：按安装量聚合公开 Agent Skill，结果包含 `owner/repository/skill` ID，可回到 GitHub 检查原始文件。
- 本项目集成：Skills 页面中的“发现 Skill”由后端代理搜索；安装前读取 GitHub 默认分支、许可证、目标目录文件清单和 `SKILL.md` frontmatter，确认后再复制到 Claude Code、Codex、OpenCode 或 `~/.agents/skills` 用户目录。
- 安全边界：插件缓存与 Codex `.system` 只读；远程内容不会执行脚本，含 `scripts/` 的 Skill 会在安装前明确提示；同名安装会覆盖目标用户目录，应先审阅文件和许可证。

### skills CLI

- 项目：[vercel-labs/skills](https://github.com/vercel-labs/skills)
- 常见用法：`npx skills find <关键词>`、`npx skills add owner/repository --skill skill-name`；也可以传入 GitHub 的 Skill 目录 URL。
- 适用：需要在终端批量发现、安装或更新 Skill 时使用；它的安装范围和 Agent 适配由 CLI 管理。本项目的网页管理器只复用公开目录和 GitHub 文件，不依赖本机 Node.js 或直接执行 `npx`。

### Anthropic Skills

- 仓库：[anthropics/skills](https://github.com/anthropics/skills)
- 内容：官方示例，覆盖设计、文档、测试、MCP 等方向。
- 注意：仓库中不同目录的许可证和使用限制并不完全一致，不能在安装时一概视为 MIT。

### Agentic Awesome Skills

- 仓库：[sickn33/agentic-awesome-skills](https://github.com/sickn33/agentic-awesome-skills)
- 内容：大规模社区目录，覆盖多种 Agent 和工作流。
- 注意：适合检索和比较，不建议一键批量安装。每个 Skill 都应独立审查质量、依赖、许可证和脚本安全性。

## 本项目的推荐组合

1. `web-design-guidelines`：作为所有 UI 改造任务的审查基线。
2. `ui-ux-pro-max`：用于决定新页面和重构页面的整体设计方向。
3. 项目专用 `vuetify-admin-ui` Skill：沉淀 Vuetify 组件选择、图标必须在 `frontend/src/plugins/vuetify.ts` 注册、响应式规则和现有管理台视觉语言。

第三项建议由团队基于本项目的 `AGENTS.md` 建立。通用 Skill 无法准确表达本项目对 Vuetify 图标映射、管理台信息密度和代理配置表单的长期约束。
