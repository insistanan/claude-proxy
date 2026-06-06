package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/conversation"
	"github.com/BenedictKing/claude-proxy/internal/handlers"
	"github.com/BenedictKing/claude-proxy/internal/handlers/chat"
	"github.com/BenedictKing/claude-proxy/internal/handlers/gemini"
	"github.com/BenedictKing/claude-proxy/internal/handlers/messages"
	"github.com/BenedictKing/claude-proxy/internal/handlers/responses"
	"github.com/BenedictKing/claude-proxy/internal/logger"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/middleware"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/warmup"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

//go:embed all:frontend/dist
var frontendFS embed.FS

func main() {
	// 加载环境变量
	if err := godotenv.Load(); err != nil {
		log.Println("没有找到 .env 文件，使用环境变量或默认值")
	}

	// 设置版本信息到 handlers 包
	handlers.SetVersionInfo(Version, BuildTime, GitCommit)

	// 初始化配置管理器
	envCfg := config.NewEnvConfig()

	// 初始化日志系统（必须在其他初始化之前）
	logCfg := &logger.Config{
		LogDir:     envCfg.LogDir,
		LogFile:    envCfg.LogFile,
		MaxSize:    envCfg.LogMaxSize,
		MaxBackups: envCfg.LogMaxBackups,
		MaxAge:     envCfg.LogMaxAge,
		Compress:   envCfg.LogCompress,
		Console:    envCfg.LogToConsole,
	}
	if err := logger.Setup(logCfg); err != nil {
		log.Fatalf("初始化日志系统失败: %v", err)
	}

	cfgManager, err := config.NewConfigManager(".config/config.json")
	if err != nil {
		log.Fatalf("初始化配置管理器失败: %v", err)
	}
	defer cfgManager.Close()

	// 初始化会话管理器（Responses API 专用）
	sessionManager := session.NewSessionManager(
		24*time.Hour, // 24小时过期
		100,          // 最多100条消息
		100000,       // 最多100k tokens
	)
	log.Printf("[Session-Init] 会话管理器已初始化")

	// 初始化指标持久化存储（可选）
	var metricsStore *metrics.SQLiteStore
	if envCfg.MetricsPersistenceEnabled {
		var err error
		metricsStore, err = metrics.NewSQLiteStore(&metrics.SQLiteStoreConfig{
			DBPath:        ".config/metrics.db",
			RetentionDays: envCfg.MetricsRetentionDays,
		})
		if err != nil {
			log.Printf("[Metrics-Init] 警告: 初始化指标持久化存储失败: %v，将使用纯内存模式", err)
			metricsStore = nil
		}
	} else {
		log.Printf("[Metrics-Init] 指标持久化已禁用，使用纯内存模式")
	}

	// 初始化多渠道调度器（Messages、Responses、Gemini 和 Chat 使用独立的指标管理器）
	var messagesMetricsManager, responsesMetricsManager, geminiMetricsManager, chatMetricsManager *metrics.MetricsManager
	if metricsStore != nil {
		messagesMetricsManager = metrics.NewMetricsManagerWithPersistence(
			envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold, metricsStore, "messages")
		responsesMetricsManager = metrics.NewMetricsManagerWithPersistence(
			envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold, metricsStore, "responses")
		geminiMetricsManager = metrics.NewMetricsManagerWithPersistence(
			envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold, metricsStore, "gemini")
		chatMetricsManager = metrics.NewMetricsManagerWithPersistence(
			envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold, metricsStore, "chat")
	} else {
		messagesMetricsManager = metrics.NewMetricsManagerWithConfig(envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold)
		responsesMetricsManager = metrics.NewMetricsManagerWithConfig(envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold)
		geminiMetricsManager = metrics.NewMetricsManagerWithConfig(envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold)
		chatMetricsManager = metrics.NewMetricsManagerWithConfig(envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold)
	}
	traceAffinityManager := session.NewTraceAffinityManager()
	conversationRegistry := conversation.NewRegistry()
	defer conversationRegistry.Stop()

	// 初始化 URL 管理器（非阻塞，动态排序）
	urlManager := warmup.NewURLManager(30*time.Second, 3) // 30秒冷却期，连续3次失败后移到末尾
	log.Printf("[URLManager-Init] URL管理器已初始化 (冷却期: 30秒, 最大连续失败: 3)")

	channelScheduler := scheduler.NewChannelScheduler(cfgManager, messagesMetricsManager, responsesMetricsManager, geminiMetricsManager, chatMetricsManager, traceAffinityManager, urlManager)
	channelScheduler.SetConversationRegistry(conversationRegistry)
	log.Printf("[Scheduler-Init] 多渠道调度器已初始化 (失败率阈值: %.0f%%, 滑动窗口: %d)",
		messagesMetricsManager.GetFailureThreshold()*100, messagesMetricsManager.GetWindowSize())

	// 设置 Gin 模式
	if envCfg.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	// 创建路由器（使用自定义 Logger，根据 QUIET_POLLING_LOGS 配置过滤轮询日志）
	r := gin.New()
	r.Use(middleware.FilteredLogger(envCfg))
	r.Use(gin.Recovery())

	// 配置 CORS
	r.Use(middleware.CORSMiddleware(envCfg))

	// Web UI 访问控制中间件
	r.Use(middleware.WebAuthMiddleware(envCfg, cfgManager))

	// 健康检查端点（固定路径 /health，与 Dockerfile HEALTHCHECK 保持一致）
	r.GET("/health", handlers.HealthCheck(envCfg, cfgManager))

	// 配置保存端点
	r.POST("/admin/config/save", handlers.SaveConfigHandler(cfgManager))

	// 开发信息端点
	if envCfg.IsDevelopment() {
		r.GET("/admin/dev/info", handlers.DevInfo(envCfg, cfgManager))
	}

	// Web 管理界面 API 路由
	apiGroup := r.Group("/api")
	{
		// Messages 渠道管理
		apiGroup.GET("/messages/channels", messages.GetUpstreams(cfgManager))
		apiGroup.POST("/messages/channels", messages.AddUpstream(cfgManager))
		apiGroup.PUT("/messages/channels/:id", messages.UpdateUpstream(cfgManager, channelScheduler))
		apiGroup.DELETE("/messages/channels/:id", messages.DeleteUpstream(cfgManager, channelScheduler))
		apiGroup.POST("/messages/channels/:id/keys", messages.AddApiKey(cfgManager))
		apiGroup.DELETE("/messages/channels/:id/keys/:apiKey", messages.DeleteApiKey(cfgManager))
		apiGroup.POST("/messages/channels/:id/keys/:apiKey/top", messages.MoveApiKeyToTop(cfgManager))
		apiGroup.POST("/messages/channels/:id/keys/:apiKey/bottom", messages.MoveApiKeyToBottom(cfgManager))

		// Messages 多渠道调度 API
		apiGroup.POST("/messages/channels/reorder", messages.ReorderChannels(cfgManager))
		apiGroup.POST("/messages/channels/tidy", handlers.TidyProblemChannels(cfgManager, scheduler.ChannelKindMessages))
		apiGroup.PATCH("/messages/channels/:id/status", messages.SetChannelStatus(cfgManager))
		apiGroup.POST("/messages/channels/:id/duplicate", handlers.DuplicateChannel(cfgManager, scheduler.ChannelKindMessages))
		apiGroup.POST("/messages/channels/:id/resume", handlers.ResumeChannel(channelScheduler, false))
		apiGroup.POST("/messages/channels/:id/promotion", messages.SetChannelPromotion(cfgManager))
		apiGroup.GET("/messages/channels/metrics", handlers.GetChannelMetricsWithConfig(messagesMetricsManager, cfgManager, false))
		apiGroup.GET("/messages/channels/metrics/history", handlers.GetChannelMetricsHistory(messagesMetricsManager, cfgManager, false))
		apiGroup.GET("/messages/channels/:id/keys/metrics/history", handlers.GetChannelKeyMetricsHistory(messagesMetricsManager, cfgManager, false))
		apiGroup.GET("/messages/channels/:id/logs", handlers.GetChannelLogs(channelScheduler, cfgManager, scheduler.ChannelKindMessages))
		apiGroup.GET("/messages/channels/scheduler/stats", handlers.GetSchedulerStats(channelScheduler))
		apiGroup.GET("/messages/global/stats/history", handlers.GetGlobalStatsHistory(messagesMetricsManager))
		apiGroup.GET("/messages/channels/dashboard", handlers.GetChannelDashboard(cfgManager, channelScheduler))
		apiGroup.GET("/messages/ping/:id", messages.PingChannel(cfgManager))
		apiGroup.GET("/messages/ping", messages.PingAllChannels(cfgManager))

		// Responses 渠道管理
		apiGroup.GET("/responses/channels", responses.GetUpstreams(cfgManager))
		apiGroup.POST("/responses/channels", responses.AddUpstream(cfgManager))
		apiGroup.PUT("/responses/channels/:id", responses.UpdateUpstream(cfgManager, channelScheduler))
		apiGroup.DELETE("/responses/channels/:id", responses.DeleteUpstream(cfgManager, channelScheduler))
		apiGroup.POST("/responses/channels/:id/keys", responses.AddApiKey(cfgManager))
		apiGroup.DELETE("/responses/channels/:id/keys/:apiKey", responses.DeleteApiKey(cfgManager))
		apiGroup.POST("/responses/channels/:id/keys/:apiKey/top", responses.MoveApiKeyToTop(cfgManager))
		apiGroup.POST("/responses/channels/:id/keys/:apiKey/bottom", responses.MoveApiKeyToBottom(cfgManager))

		// Responses 多渠道调度 API
		apiGroup.POST("/responses/channels/reorder", responses.ReorderChannels(cfgManager))
		apiGroup.POST("/responses/channels/tidy", handlers.TidyProblemChannels(cfgManager, scheduler.ChannelKindResponses))
		apiGroup.PATCH("/responses/channels/:id/status", responses.SetChannelStatus(cfgManager))
		apiGroup.POST("/responses/channels/:id/duplicate", handlers.DuplicateChannel(cfgManager, scheduler.ChannelKindResponses))
		apiGroup.POST("/responses/channels/:id/resume", handlers.ResumeChannel(channelScheduler, true))
		apiGroup.POST("/responses/channels/:id/promotion", handlers.SetResponsesChannelPromotion(cfgManager))
		apiGroup.GET("/responses/channels/metrics", handlers.GetChannelMetricsWithConfig(responsesMetricsManager, cfgManager, true))
		apiGroup.GET("/responses/channels/metrics/history", handlers.GetChannelMetricsHistory(responsesMetricsManager, cfgManager, true))
		apiGroup.GET("/responses/channels/:id/keys/metrics/history", handlers.GetChannelKeyMetricsHistory(responsesMetricsManager, cfgManager, true))
		apiGroup.GET("/responses/channels/:id/logs", handlers.GetChannelLogs(channelScheduler, cfgManager, scheduler.ChannelKindResponses))
		apiGroup.GET("/responses/global/stats/history", handlers.GetGlobalStatsHistory(responsesMetricsManager))

		// Gemini 渠道管理
		apiGroup.GET("/gemini/channels", gemini.GetUpstreams(cfgManager))
		apiGroup.POST("/gemini/channels", gemini.AddUpstream(cfgManager))
		apiGroup.PUT("/gemini/channels/:id", gemini.UpdateUpstream(cfgManager, channelScheduler))
		apiGroup.DELETE("/gemini/channels/:id", gemini.DeleteUpstream(cfgManager, channelScheduler))
		apiGroup.POST("/gemini/channels/:id/keys", gemini.AddApiKey(cfgManager))
		apiGroup.DELETE("/gemini/channels/:id/keys/:apiKey", gemini.DeleteApiKey(cfgManager))
		apiGroup.POST("/gemini/channels/:id/keys/:apiKey/top", gemini.MoveApiKeyToTop(cfgManager))
		apiGroup.POST("/gemini/channels/:id/keys/:apiKey/bottom", gemini.MoveApiKeyToBottom(cfgManager))

		// Gemini 多渠道调度 API
		apiGroup.POST("/gemini/channels/reorder", gemini.ReorderChannels(cfgManager))
		apiGroup.POST("/gemini/channels/tidy", handlers.TidyProblemChannels(cfgManager, scheduler.ChannelKindGemini))
		apiGroup.PATCH("/gemini/channels/:id/status", gemini.SetChannelStatus(cfgManager))
		apiGroup.POST("/gemini/channels/:id/duplicate", handlers.DuplicateChannel(cfgManager, scheduler.ChannelKindGemini))
		apiGroup.POST("/gemini/channels/:id/promotion", gemini.SetChannelPromotion(cfgManager))
		apiGroup.PUT("/gemini/loadbalance", gemini.UpdateLoadBalance(cfgManager))
		apiGroup.GET("/gemini/channels/dashboard", gemini.GetDashboard(cfgManager, channelScheduler))
		apiGroup.GET("/gemini/channels/metrics", handlers.GetGeminiChannelMetrics(geminiMetricsManager, cfgManager))
		apiGroup.GET("/gemini/channels/metrics/history", handlers.GetGeminiChannelMetricsHistory(geminiMetricsManager, cfgManager))
		apiGroup.GET("/gemini/channels/:id/keys/metrics/history", handlers.GetGeminiChannelKeyMetricsHistory(geminiMetricsManager, cfgManager))
		apiGroup.GET("/gemini/channels/:id/logs", handlers.GetChannelLogs(channelScheduler, cfgManager, scheduler.ChannelKindGemini))
		apiGroup.GET("/gemini/global/stats/history", handlers.GetGlobalStatsHistory(geminiMetricsManager))
		apiGroup.GET("/gemini/ping/:id", gemini.PingChannel(cfgManager))
		apiGroup.GET("/gemini/ping", gemini.PingAllChannels(cfgManager))

		// Chat 渠道管理
		apiGroup.GET("/chat/channels", chat.GetUpstreams(cfgManager))
		apiGroup.POST("/chat/channels", chat.AddUpstream(cfgManager))
		apiGroup.PUT("/chat/channels/:id", chat.UpdateUpstream(cfgManager, channelScheduler))
		apiGroup.DELETE("/chat/channels/:id", chat.DeleteUpstream(cfgManager, channelScheduler))
		apiGroup.POST("/chat/channels/:id/keys", chat.AddApiKey(cfgManager))
		apiGroup.DELETE("/chat/channels/:id/keys/:apiKey", chat.DeleteApiKey(cfgManager))
		apiGroup.POST("/chat/channels/:id/keys/:apiKey/top", chat.MoveApiKeyToTop(cfgManager))
		apiGroup.POST("/chat/channels/:id/keys/:apiKey/bottom", chat.MoveApiKeyToBottom(cfgManager))

		// Chat 多渠道调度 API
		apiGroup.POST("/chat/channels/reorder", chat.ReorderChannels(cfgManager))
		apiGroup.POST("/chat/channels/tidy", handlers.TidyProblemChannels(cfgManager, scheduler.ChannelKindChat))
		apiGroup.PATCH("/chat/channels/:id/status", chat.SetChannelStatus(cfgManager))
		apiGroup.POST("/chat/channels/:id/duplicate", handlers.DuplicateChannel(cfgManager, scheduler.ChannelKindChat))
		apiGroup.POST("/chat/channels/:id/resume", handlers.ResumeChannelByKind(channelScheduler, scheduler.ChannelKindChat))
		apiGroup.POST("/chat/channels/:id/promotion", chat.SetChannelPromotion(cfgManager))
		apiGroup.PUT("/chat/loadbalance", chat.UpdateLoadBalance(cfgManager))
		apiGroup.GET("/chat/channels/dashboard", chat.GetDashboard(cfgManager, channelScheduler))
		apiGroup.GET("/chat/channels/metrics", handlers.GetChannelMetricsWithKind(chatMetricsManager, cfgManager, scheduler.ChannelKindChat))
		apiGroup.GET("/chat/channels/metrics/history", handlers.GetChannelMetricsHistoryByKind(chatMetricsManager, cfgManager, scheduler.ChannelKindChat))
		apiGroup.GET("/chat/channels/:id/keys/metrics/history", handlers.GetChannelKeyMetricsHistoryByKind(chatMetricsManager, cfgManager, scheduler.ChannelKindChat))
		apiGroup.GET("/chat/channels/:id/logs", handlers.GetChannelLogs(channelScheduler, cfgManager, scheduler.ChannelKindChat))
		apiGroup.GET("/chat/global/stats/history", handlers.GetGlobalStatsHistory(chatMetricsManager))
		apiGroup.GET("/chat/ping/:id", chat.PingChannel(cfgManager))
		apiGroup.GET("/chat/ping", chat.PingAllChannels(cfgManager))

		// 对话与路由覆盖
		apiGroup.GET("/conversations/route-options", handlers.GetConversationRouteOptions(cfgManager))
		apiGroup.GET("/conversations", handlers.ListConversations(channelScheduler))
		apiGroup.GET("/conversations/:id", handlers.GetConversation(channelScheduler))
		apiGroup.PUT("/conversations/:id/route", handlers.SetConversationRouteOverride(channelScheduler, cfgManager))
		apiGroup.DELETE("/conversations/:id/route", handlers.ClearConversationRouteOverride(channelScheduler))

		// Fuzzy 模式设置
		apiGroup.GET("/settings/fuzzy-mode", handlers.GetFuzzyMode(cfgManager))
		apiGroup.PUT("/settings/fuzzy-mode", handlers.SetFuzzyMode(cfgManager))
	}

	// 代理端点 - Messages API
	r.POST("/v1/messages", messages.Handler(envCfg, cfgManager, channelScheduler))
	r.POST("/v1/messages/count_tokens", messages.CountTokensHandler(envCfg, cfgManager, channelScheduler))

	// 代理端点 - Models API（转发到上游）
	r.GET("/v1/models", messages.ModelsHandler(envCfg, cfgManager, channelScheduler))
	r.GET("/v1/models/:model", messages.ModelsDetailHandler(envCfg, cfgManager, channelScheduler))

	// 代理端点 - Responses API
	r.POST("/v1/responses", responses.Handler(envCfg, cfgManager, sessionManager, channelScheduler))
	r.POST("/v1/responses/compact", responses.CompactHandler(envCfg, cfgManager, sessionManager, channelScheduler))

	// 代理端点 - Chat Completions API (OpenAI-compatible 原生协议)
	r.POST("/v1/chat/completions", chat.Handler(envCfg, cfgManager, channelScheduler))
	r.POST("/v1/images/generations", chat.ImagesHandler(envCfg, cfgManager, channelScheduler, "/images/generations"))
	r.POST("/v1/images/edits", chat.ImagesHandler(envCfg, cfgManager, channelScheduler, "/images/edits"))
	r.POST("/v1/images/variations", chat.ImagesHandler(envCfg, cfgManager, channelScheduler, "/images/variations"))

	// 代理端点 - Gemini API (原生协议)
	// 使用通配符捕获 model:action 格式，如 gemini-pro:generateContent
	// 路径格式：/v1beta/models/{model}:generateContent (Gemini 原生格式)
	r.POST("/v1beta/models/*modelAction", gemini.Handler(envCfg, cfgManager, channelScheduler))

	// 静态文件服务 (嵌入的前端)
	if envCfg.EnableWebUI {
		handlers.ServeFrontend(r, frontendFS)
	} else {
		// 纯 API 模式
		r.GET("/", func(c *gin.Context) {
			c.JSON(200, gin.H{
				"name":    "Claude API Proxy",
				"mode":    "API Only",
				"version": Version,
				"endpoints": gin.H{
					"chat":      "/v1/chat/completions",
					"config":    "/admin/config/save",
					"gemini":    "/v1beta/models/*modelAction",
					"health":    "/health",
					"images":    "/v1/images/generations",
					"messages":  "/v1/messages",
					"responses": "/v1/responses",
				},
				"message": "Web界面已禁用，此服务器运行在纯API模式下",
			})
		})
	}

	// 启动服务器
	addr := fmt.Sprintf(":%d", envCfg.Port)
	fmt.Printf("\n[Server-Startup] Claude API代理服务器已启动\n")
	fmt.Printf("[Server-Info] 版本: %s\n", Version)
	if BuildTime != "unknown" {
		fmt.Printf("[Server-Info] 构建时间: %s\n", BuildTime)
	}
	if GitCommit != "unknown" {
		fmt.Printf("[Server-Info] Git提交: %s\n", GitCommit)
	}
	fmt.Printf("[Server-Info] 管理界面: http://localhost:%d\n", envCfg.Port)
	fmt.Printf("[Server-Info] API 地址: http://localhost:%d/v1\n", envCfg.Port)
	fmt.Printf("[Server-Info] Claude Messages: POST /v1/messages\n")
	fmt.Printf("[Server-Info] Codex Responses: POST /v1/responses\n")
	fmt.Printf("[Server-Info] OpenAI Chat: POST /v1/chat/completions\n")
	fmt.Printf("[Server-Info] OpenAI Images: POST /v1/images/{generations|edits|variations}\n")
	fmt.Printf("[Server-Info] Gemini API: POST /v1beta/models/{model}:generateContent\n")
	fmt.Printf("[Server-Info] Gemini API: POST /v1beta/models/{model}:streamGenerateContent\n")
	fmt.Printf("[Server-Info] 健康检查: GET /health\n")
	fmt.Printf("[Server-Info] 环境: %s\n", envCfg.Env)
	// 检查是否使用默认密码，给予提示
	if envCfg.ProxyAccessKey == "your-proxy-access-key" {
		fmt.Printf("[Server-Warn] 访问密钥: your-proxy-access-key (默认值，建议通过 .env 文件修改)\n")
	}
	fmt.Printf("\n")

	// 创建 HTTP 服务器
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 用于传递关闭结果
	shutdownDone := make(chan struct{})

	// 优雅关闭：监听系统信号
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		signal.Stop(sigChan) // 停止信号监听，避免资源泄漏

		log.Println("[Server-Shutdown] 收到关闭信号，正在优雅关闭服务器...")

		// 创建超时上下文
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("[Server-Shutdown] 警告: 服务器关闭时发生错误: %v", err)
		} else {
			log.Println("[Server-Shutdown] 服务器已安全关闭")
		}

		// 关闭指标持久化存储
		if metricsStore != nil {
			if err := metricsStore.Close(); err != nil {
				log.Printf("[Metrics-Shutdown] 警告: 关闭指标存储时发生错误: %v", err)
			} else {
				log.Println("[Metrics-Shutdown] 指标存储已安全关闭")
			}
		}

		close(shutdownDone)
	}()

	// 启动服务器（阻塞直到关闭）
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("服务器启动失败: %v", err)
	}

	// 等待关闭完成（带超时保护，避免死锁）
	select {
	case <-shutdownDone:
		// 正常关闭完成
	case <-time.After(15 * time.Second):
		log.Println("[Server-Shutdown] 警告: 等待关闭超时")
	}
}
