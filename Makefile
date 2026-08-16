# API Proxy Makefile

GREEN=\033[0;32m
YELLOW=\033[0;33m
NC=\033[0m

.PHONY: help dev run build check clean frontend-dev frontend-build embed-frontend

help:
	@echo "$(GREEN)API Proxy - 可用命令:$(NC)"
	@echo ""
	@echo "$(YELLOW)开发:$(NC)"
	@echo "  make dev            - Go 后端热重载开发(不含前端)"
	@echo "  make run            - 构建前端并运行 Go 后端"
	@echo "  make frontend-dev   - 前端开发服务器"
	@echo ""
	@echo "$(YELLOW)构建:$(NC)"
	@echo "  make build          - 构建前端并编译 Go 后端"
	@echo "  make frontend-build - 仅构建前端"
	@echo "  make clean          - 清理构建文件"
	@echo ""
	@echo "$(YELLOW)门禁:$(NC)"
	@echo "  make check          - 前端 type-check+图标扫描 & 后端 fmt+vet+test"

dev:
	@echo "$(GREEN)🚀 启动前后端开发模式...$(NC)"
	@cd frontend && bun run dev &
	@cd backend-go && $(MAKE) dev

run: embed-frontend
	@cd backend-go && $(MAKE) run

build: embed-frontend
	@cd backend-go && $(MAKE) build

embed-frontend:
	@echo "$(GREEN)📦 构建前端...$(NC)"
	@cd frontend && bun run build
	@echo "$(GREEN)📋 嵌入前端到 Go 后端...$(NC)"
	@rm -rf backend-go/frontend/dist
	@mkdir -p backend-go/frontend/dist
	@cp -r frontend/dist/* backend-go/frontend/dist/

clean:
	@cd backend-go && $(MAKE) clean
	@rm -rf frontend/dist

frontend-dev:
	@cd frontend && bun run dev

frontend-build:
	@cd frontend && bun run build

# 全量门禁: 前端 type-check + 图标扫描; 后端 gofmt + vet + test
check:
	@cd frontend && bun run check
	@cd backend-go && $(MAKE) check
