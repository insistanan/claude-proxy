# 发布指南

本文档为项目维护者提供标准的版本发布流程。**版本单一事实源 = 根目录 `VERSION` 文件**；`CHANGELOG.md` 记录历史；`frontend/package.json` 的 version 仅作对齐（不应独立递增）。

## 版本规范

遵循语义化版本 2.0.0（Semantic Versioning）：`主版本号.次版本号.修订号`。

- **主版本号 (MAJOR)**: 不兼容的 API 修改。
- **次版本号 (MINOR)**: 向下兼容的功能性新增。
- **修订号 (PATCH)**: 向下兼容的问题修正。

## 发布流程

### 步骤 1: 准备工作

1. 确保本地 `main` 分支最新且稳定：

   ```bash
   git checkout main
   git pull origin main
   ```

2. 确认所有计划内的功能和修复已合并到 `main`。

3. 验证质量门禁全绿（后端 gofmt + vet + test，前端 type-check + 图标扫描）：

   ```bash
   make check
   ```

### 步骤 2: 更新版本号（根 `VERSION` 文件）

1. 打开根目录 `VERSION` 文件，写入新版本号（如 `v3.0.0`）。
2. 同步对齐 `frontend/package.json` 的 `version` 字段（不加 `v` 前缀）。
3. 构建时版本经 `-ldflags` 注入（`main.Version` / `main.BuildTime` / `main.GitCommit`），UI 版本显示即来自根 `VERSION`（详见 `docs/DEVELOPMENT.md` 的 Windows exe 打包流程，禁止裸 `go build`）。

### 步骤 3: 更新版本日志（`CHANGELOG.md`）

1. 打开 `CHANGELOG.md`，在顶部新增版本标题：`## [vX.Y.Z] - YYYY-MM-DD`。
2. 按分类整理变更：
   - `### 新功能` / `### 修复` / `### 重构` / `### 文档` / `### 其他`
3. 用 `git log <上一tag>...HEAD --oneline` 整理变更清单。

### 步骤 4: 提交并推送

```bash
# 将 vX.Y.Z 替换为新版本号
git add VERSION CHANGELOG.md frontend/package.json
git commit -m "chore(release): prepare for vX.Y.Z"
git push origin main
```

### 步骤 5: 创建并推送 Git 标签

```bash
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin vX.Y.Z
```

推送 tag 后 GitHub Actions 自动触发（**三平台并行**）：

- `release-linux.yml` - Linux amd64/arm64
- `release-macos.yml` - macOS amd64/arm64
- `release-windows.yml` - Windows amd64/arm64
- `docker-build.yml` - Docker 镜像

> **注意**: 各平台使用独立 concurrency group（`${{ github.workflow }}-${{ github.ref }}`），并行构建互不阻塞。

### 步骤 6: 在 GitHub 创建 Release（可选但推荐）

1. 进入项目 GitHub 页面的 "Releases"，点击 "Draft a new release"。
2. 选择刚推送的 tag（如 `vX.Y.Z`）。
3. 将 `CHANGELOG.md` 对应版本内容复制到发布说明。
4. 点击 "Publish release"。
