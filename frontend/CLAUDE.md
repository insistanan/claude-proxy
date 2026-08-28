# frontend 模块文档

[← 根目录](../CLAUDE.md)

## 模块职责

Vue 3 + Vuetify 3 Web 管理界面：渠道配置、实时监控、拖拽排序、主题切换。

## 启动命令

```bash
npm run dev       # 开发服务器
npm run build     # 生产构建
npm run preview   # 预览构建
```

## 核心视图与组件

视图在 `src/views/`（渠道、对话、请求/拦截日志、Skills、评测、Claude Code / OpenCode / DSH / PiAgent 客户端配置、Settings），组件在 `src/components/`。完整清单以目录为准，本文件不复述。层级关系：views → stores（`stores/` Pinia）→ services（`services/api.ts`）。

## API 服务

`src/services/api.ts`：`channelApiByType` 工厂供五协议渠道统一调用（对应后端 `RegisterChannelRoutes`）。不要在组件里拼裸 fetch，复用 store/api 封装。

## 主题配置

编辑 `src/plugins/vuetify.ts` 中的 `lightTheme` 和 `darkTheme`。

## 图标系统

项目使用 **SVG 按需导入** 方案，从 `@mdi/js` 导入单个图标 path，而非完整字体文件，显著减小打包体积。

**配置文件**: `src/plugins/vuetify.ts`（`iconMap`）

**新增图标步骤**:
1. 从 `@mdi/js` 添加导入（驼峰命名）
2. 在 `iconMap` 中添加映射（kebab-case）

```typescript
// 1. 导入
import { mdiNewIcon } from '@mdi/js'

// 2. 映射
const iconMap = {
  'new-icon': mdiNewIcon,
}
```

**使用方式**: 模板中使用 `mdi-xxx` 格式
```vue
<v-icon>mdi-new-icon</v-icon>
```

**图标查找**: https://pictogrammers.com/library/mdi/

**机器校验**: `npm run check:icons`（或 `npm run check`）会扫描全部 `mdi-*` 用法并对照 `iconMap`，未注册即报错。

## 构建产物

生产构建输出到 `dist/`，会被嵌入到 Go 后端二进制文件中（`embed.FS`）。
