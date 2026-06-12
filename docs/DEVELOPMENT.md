# 开发指南

## 推荐方式

| 方式 | 命令 | 适用场景 |
|-----|------|---------|
| 根目录 Make | `make dev` | 日常开发 |
| backend-go Make | `cd backend-go && make dev` | Go 后端专项 |
| Docker | `docker-compose up -d` | 生产环境测试 |

## 根目录开发

``bash
make dev              # Go 后端热重载
make run              # 构建前端 + 运行后端
make frontend-dev     # 前端开发服务器（端口 5173）
make build            # 完整构建
make clean            # 清理
``

## Go 后端专项

``bash
cd backend-go
make dev              # 热重载（air）
make test             # 运行测试
make test-cover       # 测试 + 覆盖率
make build            # 构建当前平台
``

## 前端开发

``bash
cd frontend
bun install && bun run dev   # 开发服务器
bun run build                 # 生产构建
``

## Windows 打包

详见 AGENTS.md 中"Windows exe 打包流程"章节。核心：先用 `bun run build` 构建前端，复制到 `backend-go/frontend/dist/`，再用版本注入编译 Go 二进制。

## 热重载

- `backend-go/.config/config.json` 修改后自动生效，无需重启
- `.env` 修改后需重启服务

## 代码规范

- Go：`go fmt ./...`，遵循官方规范
- 前端：遵循 Prettier 风格
- 提交前确认 `cd backend-go && make test` 通过