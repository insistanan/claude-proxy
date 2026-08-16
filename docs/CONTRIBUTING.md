# 贡献指南

## 如何贡献

1. Fork 本项目
2. 创建特性分支：`git checkout -b feature/xxx`
3. 提交改动：`git commit -m 'feat: xxx'`
4. 推送并开启 Pull Request

## 编码规范

- Go 代码通过 `go fmt ./...` 格式化
- 遵循 Go 官方代码规范，错误处理完整
- 提交信息遵循 Conventional Commits：`feat:` / `fix:` / `refactor:` / `chore:` / `docs:`
- 写通用能力前先查 `docs/capabilities.md`，有现成实现一律复用，不重复造轮子
- 新概念登记 `docs/glossary.md`，新链路变化同步 `docs/flows.md`

## 验证

- 全量门禁：根目录 `make check`（后端 gofmt + vet + test；前端 type-check + 图标扫描）
- 后端专项：`cd backend-go && make test`（或 `make test-cover` 生成覆盖率）
- 前端专项：`cd frontend && bun run check`（type-check + 图标注册扫描）、`bun run test`（vitest）
- 冒烟测试：`curl http://localhost:3000/health`

## 安全

- 切勿提交密钥或 `.env` 文件
- 日志中 API 密钥自动脱敏（`utils.MaskAPIKey`）
- `PROXY_ACCESS_KEY` 必须设置强密钥
