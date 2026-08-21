# 开发指南

## 推荐方式

| 方式 | 命令 | 适用场景 |
|-----|------|---------|
| 根目录 Make | `make dev` | 日常开发 |
| backend-go Make | `cd backend-go && make dev` | Go 后端专项 |
| Docker | `docker-compose up -d` | 生产环境测试 |

## 根目录开发

```bash
make dev              # Go 后端热重载
make run              # 构建前端 + 运行后端
make frontend-dev     # 前端开发服务器（端口 5173）
make build            # 完整构建
make check            # 全量门禁（后端 fmt+vet+test + 前端 type-check+图标扫描）
make clean            # 清理
```

## Go 后端专项

```bash
cd backend-go
make dev              # 热重载（air）
make test             # 运行测试
make test-cover       # 测试 + 覆盖率
make check            # gofmt 校验 + go vet + go test（提交前必跑）
make build            # 构建当前平台
```

## 前端开发

```bash
cd frontend
bun install && bun run dev   # 开发服务器
bun run build                # 生产构建
bun run check                # type-check + 图标注册扫描
bun run test                 # vitest 单测（现有 quickInputParser 等）
```

## Windows exe 打包流程

目标产物：`dist/claude-proxy-windows-amd64.exe`。

1. 先构建前端：`cd frontend && bun run build`；如果本机 `bun` 不在 PATH，可用 `npm run build`。
2. 将 `frontend/dist/*` 复制到 `backend-go/frontend/dist/`，确保 Go embed 打包到最新 UI。
3. 回到 `backend-go/`，读取根目录 `VERSION`，生成 `BuildTime`，读取 `git rev-parse --short HEAD`，并设置 `CGO_ENABLED=0`、`GOOS=windows`、`GOARCH=amd64`。
4. 使用版本注入编译，**禁止裸 `go build`**：

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

## 热重载

- `backend-go/.config/config.json` 修改后自动生效，无需重启
- `.env` 修改后需重启服务

## 代码规范

- Go：`go fmt ./...`，遵循官方规范；提交前 `make check` 通过
- 前端：遵循 Prettier + ESLint 风格；`bun run type-check` 常跑；新图标先注册 iconMap（`bun run check:icons` 把关）
- 后端测试：新增/修改逻辑补 `_test.go`，优先表驱动 + `httptest`；前端复杂逻辑用 vitest 补单测
- 提交信息遵循 Conventional Commits：`feat:` / `fix:` / `refactor:` / `chore:` / `docs:`

## 版本发布

**版本单一事实源 = 根目录 `VERSION` 文件**；`CHANGELOG.md` 记录历史；`frontend/package.json` 的 version 仅作对齐（不应独立递增）。版本号遵循语义化版本 2.0.0。

1. 确认 `main` 分支最新且稳定，`make check` 全绿。
2. 更新根目录 `VERSION`（如 `v3.0.0`），同步对齐 `frontend/package.json` 的 `version`（不加 `v` 前缀）。
3. 更新 `CHANGELOG.md`：顶部新增 `## [vX.Y.Z] - YYYY-MM-DD`，按 `### 新功能 / 修复 / 重构 / 文档 / 其他` 分类，用 `git log <上一tag>...HEAD --oneline` 整理。
4. 提交并推送：`git add VERSION CHANGELOG.md frontend/package.json && git commit -m "chore(release): prepare for vX.Y.Z" && git push origin main`。
5. 打标签并推送：`git tag -a vX.Y.Z -m "Release vX.Y.Z" && git push origin vX.Y.Z`。推送 tag 后 GitHub Actions 自动触发三平台构建（Linux/macOS/Windows，各 amd64/arm64）与 Docker 镜像。
6. （推荐）在 GitHub Releases 页面基于该 tag 起草 Release，发布说明复制 `CHANGELOG.md` 对应版本内容。