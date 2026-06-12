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
- 运行 `cd backend-go && make test` 确保测试通过

## 测试

- 后端：`cd backend-go && make test`（或 `make test-cover` 生成覆盖率）
- 前端：`cd frontend && bun run build` 验证构建
- 冒烟测试：`curl http://localhost:3000/health`

## 安全

- 切勿提交密钥或 `.env` 文件
- 日志中 API 密钥自动脱敏
- `PROXY_ACCESS_KEY` 必须设置强密钥
