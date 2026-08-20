package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/conversation"
	"github.com/BenedictKing/claude-proxy/internal/handlers"
	"github.com/BenedictKing/claude-proxy/internal/handlers/chat"
	"github.com/BenedictKing/claude-proxy/internal/handlers/common"
	"github.com/BenedictKing/claude-proxy/internal/handlers/gemini"
	"github.com/BenedictKing/claude-proxy/internal/handlers/images"
	"github.com/BenedictKing/claude-proxy/internal/handlers/messages"
	"github.com/BenedictKing/claude-proxy/internal/handlers/responses"
	"github.com/BenedictKing/claude-proxy/internal/logger"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/middleware"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/BenedictKing/claude-proxy/internal/sensitive"
	"github.com/BenedictKing/claude-proxy/internal/session"
	"github.com/BenedictKing/claude-proxy/internal/urlhealth"
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
	requestLogStore := metrics.NewRequestLogStore(envCfg.LogDir, 7)
	defer requestLogStore.Close()

	cfgManager, err := config.NewConfigManager(".config/config.json")
	if err != nil {
		log.Fatalf("初始化配置管理器失败: %v", err)
	}
	defer cfgManager.Close()

	blockedStore, err := sensitive.NewBlockedStore(".config/blocked-logs.db")
	if err != nil {
		log.Fatalf("初始化内容安全拦截记录存储失败: %v", err)
	}
	defer func() {
		if err := blockedStore.Close(); err != nil {
			log.Printf("[ContentSafety-Shutdown] 关闭拦截记录存储失败: %v", err)
		}
	}()
	contentSafetyPipeline := common.NewContentSafetyPipelineWithRecorder(cfgManager, blockedStore)
	log.Printf("[ContentSafety-Init] 拦截记录存储已初始化")

	// 初始化会话管理器（Responses API 专用），与对话记录共用持久化数据库。
	sessionManager, err := session.NewPersistentSessionManager(
		".config/conversations.db",
		7*24*time.Hour, // 与对话记录一致，保留 7 天
		0,              // 不按消息数删除整段会话
		0,              // 不按 Token 数删除整段会话
	)
	if err != nil {
		log.Fatalf("初始化 Responses 会话持久化存储失败: %v", err)
	}
	defer sessionManager.Stop()
	sessionManager.SetMaxSessions(5000) // 全局最多 5000 个活跃 session
	log.Printf("[Session-Init] 持久化会话管理器已初始化 (保留: 7天, 上限: 5000)")

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

	// 初始化多渠道调度器（Messages、Responses、Gemini、Chat 和 Images 使用独立的指标管理器）
	// 收敛为按 ChannelKind 分组的 map，消除五组几乎相同的初始化代码
	metricsKinds := []scheduler.ChannelKind{
		scheduler.ChannelKindMessages,
		scheduler.ChannelKindResponses,
		scheduler.ChannelKindGemini,
		scheduler.ChannelKindChat,
		scheduler.ChannelKindImages,
	}
	kindMetricsManagers := make(map[scheduler.ChannelKind]*metrics.MetricsManager, len(metricsKinds))
	for _, kind := range metricsKinds {
		if metricsStore != nil {
			kindMetricsManagers[kind] = metrics.NewMetricsManagerWithPersistence(
				envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold, metricsStore, string(kind))
		} else {
			kindMetricsManagers[kind] = metrics.NewMetricsManagerWithConfig(envCfg.MetricsWindowSize, envCfg.MetricsFailureThreshold)
		}
	}

	traceAffinityManager := session.NewTraceAffinityManager()
	conversationRegistry, err := conversation.NewPersistentRegistry(".config/conversations.db")
	if err != nil {
		log.Fatalf("初始化对话持久化存储失败: %v", err)
	}
	defer conversationRegistry.Stop()

	// 初始化 URL 管理器（非阻塞，动态排序）
	urlManager := urlhealth.NewURLManager(30*time.Second, 3) // 30秒冷却期，连续3次失败后移到末尾
	log.Printf("[URLManager-Init] URL管理器已初始化 (冷却期: 30秒, 最大连续失败: 3)")

	channelScheduler := scheduler.NewChannelScheduler(
		cfgManager,
		kindMetricsManagers[scheduler.ChannelKindMessages],
		kindMetricsManagers[scheduler.ChannelKindResponses],
		kindMetricsManagers[scheduler.ChannelKindGemini],
		kindMetricsManagers[scheduler.ChannelKindChat],
		kindMetricsManagers[scheduler.ChannelKindImages],
		traceAffinityManager,
		urlManager,
	)
	channelScheduler.SetRequestLogStore(requestLogStore)
	channelScheduler.SetConversationRegistry(conversationRegistry)
	log.Printf("[Scheduler-Init] 多渠道调度器已初始化 (失败率阈值: %.0f%%, 滑动窗口: %d)",
		kindMetricsManagers[scheduler.ChannelKindMessages].GetFailureThreshold()*100,
		kindMetricsManagers[scheduler.ChannelKindMessages].GetWindowSize())

	// 初始化自适应负载均衡
	profileManager := metrics.NewProfileManager()
	adaptiveScheduler := scheduler.NewAdaptiveScheduler(profileManager)
	channelScheduler.SetProfileManager(profileManager)
	channelScheduler.SetAdaptiveScheduler(adaptiveScheduler)
	log.Println("[Main] 自适应负载均衡已启用")

	// 从现有指标同步数据到性能画像
	syncConfig := cfgManager.GetConfig()
	kindToUpstreams := []struct {
		kind      scheduler.ChannelKind
		upstreams []config.UpstreamConfig
	}{
		{scheduler.ChannelKindMessages, syncConfig.Upstream},
		{scheduler.ChannelKindResponses, syncConfig.ResponsesUpstream},
		{scheduler.ChannelKindGemini, syncConfig.GeminiUpstream},
		{scheduler.ChannelKindChat, syncConfig.ChatUpstream},
		{scheduler.ChannelKindImages, syncConfig.ImagesUpstream},
	}
	for _, entry := range kindToUpstreams {
		for i := range entry.upstreams {
			syncUpstreamProfiles(kindMetricsManagers[entry.kind], profileManager, &entry.upstreams[i], i)
		}
	}
	log.Println("[Main] 已从现有指标同步性能画像数据")

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
		apiGroup.GET("/request-logs", handlers.GetRequestLogs(requestLogStore))
		apiGroup.GET("/blocked-logs", handlers.GetBlockedLogs(blockedStore))
		apiGroup.GET("/blocked-logs/:id", handlers.GetBlockedLog(blockedStore))
		apiGroup.DELETE("/blocked-logs/:id", handlers.DeleteBlockedLog(blockedStore))
		apiGroup.DELETE("/blocked-logs", handlers.ClearBlockedLogs(blockedStore))

		// 渠道管理 API 路由（messages/responses/gemini/chat/images 五组收敛为一次调用）
		// 各渠道类型的 CRUD / key 管理 / pools / reorder / status / promotion / metrics / ping
		// 全部由 handlers.RegisterChannelRoutes 统一注册，差异点通过 ChannelRouteOptions 表达。
		channelDeps := handlers.ChannelRouteDeps{
			Cfg:       cfgManager,
			Scheduler: channelScheduler,
			MetricsByKind: func(kind scheduler.ChannelKind) *metrics.MetricsManager {
				return kindMetricsManagers[kind]
			},
		}

		// Messages 渠道管理
		handlers.RegisterChannelRoutes(apiGroup, scheduler.ChannelKindMessages, channelDeps, handlers.ChannelRouteOptions{
			Crud:           messages.Crud(cfgManager, channelScheduler),
			LoadBalance:    messages.UpdateLoadBalance(cfgManager),
			Dashboard:      handlers.GetChannelDashboard(cfgManager, channelScheduler, scheduler.ChannelKindMessages),
			SchedulerStats: handlers.GetSchedulerStats(channelScheduler),
		})

		// Responses 渠道管理
		handlers.RegisterChannelRoutes(apiGroup, scheduler.ChannelKindResponses, channelDeps, handlers.ChannelRouteOptions{
			Crud:        responses.Crud(cfgManager, channelScheduler),
			LoadBalance: responses.UpdateLoadBalance(cfgManager),
			Dashboard:   handlers.GetChannelDashboard(cfgManager, channelScheduler, scheduler.ChannelKindResponses),
		})

		// Gemini 渠道管理
		handlers.RegisterChannelRoutes(apiGroup, scheduler.ChannelKindGemini, channelDeps, handlers.ChannelRouteOptions{
			Crud:        gemini.Crud(cfgManager, channelScheduler),
			LoadBalance: gemini.UpdateLoadBalance(cfgManager),
			Dashboard:   handlers.GetChannelDashboard(cfgManager, channelScheduler, scheduler.ChannelKindGemini),
		})

		// Chat 渠道管理
		handlers.RegisterChannelRoutes(apiGroup, scheduler.ChannelKindChat, channelDeps, handlers.ChannelRouteOptions{
			Crud:        chat.Crud(cfgManager, channelScheduler),
			LoadBalance: chat.UpdateLoadBalance(cfgManager),
			Dashboard:   handlers.GetChannelDashboard(cfgManager, channelScheduler, scheduler.ChannelKindChat),
		})

		// Images 渠道管理
		handlers.RegisterChannelRoutes(apiGroup, scheduler.ChannelKindImages, channelDeps, handlers.ChannelRouteOptions{
			Crud:        images.Crud(cfgManager, channelScheduler),
			LoadBalance: images.UpdateLoadBalance(cfgManager),
			Dashboard:   handlers.GetChannelDashboard(cfgManager, channelScheduler, scheduler.ChannelKindImages),
		})

		// 渠道性能报告（自适应负载均衡）
		apiGroup.GET("/performance/report", func(c *gin.Context) {
			reports := adaptiveScheduler.GetChannelPerformanceReport()
			c.JSON(200, gin.H{
				"success": true,
				"data":    reports,
			})
		})

		// 对话与路由覆盖
		apiGroup.GET("/conversations/route-options", handlers.GetConversationRouteOptions(cfgManager))
		apiGroup.GET("/conversations", handlers.ListConversations(channelScheduler))
		apiGroup.DELETE("/conversations", handlers.DeleteAllConversations(channelScheduler, sessionManager))
		apiGroup.GET("/conversations/:id", handlers.GetConversation(channelScheduler))
		apiGroup.PUT("/conversations/:id/name", handlers.SetConversationName(channelScheduler))
		apiGroup.DELETE("/conversations/:id", handlers.DeleteConversation(channelScheduler, sessionManager))
		apiGroup.PUT("/conversations/:id/route", handlers.SetConversationRouteOverride(channelScheduler, cfgManager))
		apiGroup.DELETE("/conversations/:id/route", handlers.ClearConversationRouteOverride(channelScheduler))

		// 管理界面设置
		apiGroup.GET("/settings/fuzzy-mode", handlers.GetFuzzyMode(cfgManager))
		apiGroup.PUT("/settings/fuzzy-mode", handlers.SetFuzzyMode(cfgManager))
		apiGroup.GET("/settings", handlers.GetSettings(cfgManager))
		apiGroup.PUT("/settings", handlers.UpdateSettings(cfgManager))
		apiGroup.POST("/upstream/models", handlers.DiscoverUpstreamModels(cfgManager))
		apiGroup.GET("/settings/client-disguise", handlers.GetClientDisguise(cfgManager))
		apiGroup.PUT("/settings/client-disguise", handlers.SetClientDisguise(cfgManager))
		apiGroup.GET("/settings/opencode", handlers.GetOpenCodeConfig())
		apiGroup.PUT("/settings/opencode", handlers.SaveOpenCodeConfig())
		apiGroup.GET("/settings/claude-code", handlers.GetClaudeCodeSettings())
		apiGroup.PUT("/settings/claude-code", handlers.SaveClaudeCodeSettings())
		apiGroup.GET("/settings/dsh", handlers.GetDSHSettings())
		apiGroup.PUT("/settings/dsh", handlers.SaveDSHSettings())

		// pi-agent 配置管理（沿用 apiGroup 的统一鉴权）
		apiGroup.GET("/settings/pi-agent", handlers.GetPiAgentStatus())
		apiGroup.GET("/settings/pi-agent/providers", handlers.ListPiAgentProviders())
		apiGroup.POST("/settings/pi-agent/providers", handlers.CreatePiAgentProvider())
		apiGroup.GET("/settings/pi-agent/providers/:id", handlers.GetPiAgentProvider())
		apiGroup.PUT("/settings/pi-agent/providers/:id", handlers.UpdatePiAgentProvider())
		apiGroup.DELETE("/settings/pi-agent/providers/:id", handlers.DeletePiAgentProvider())
		apiGroup.POST("/settings/pi-agent/validate", handlers.ValidatePiAgentProvider())
		apiGroup.POST("/settings/pi-agent/providers/:id/discover-models", handlers.DiscoverPiAgentModels())
		apiGroup.POST("/settings/pi-agent/providers/:id/test", handlers.TestPiAgentProvider())
		apiGroup.GET("/settings/pi-agent/credentials", handlers.ListPiAgentCredentials())
		apiGroup.PUT("/settings/pi-agent/credentials/:id", handlers.UpdatePiAgentCredential())
		apiGroup.DELETE("/settings/pi-agent/credentials/:id", handlers.DeletePiAgentCredential())
		apiGroup.GET("/settings/pi-agent/model-settings", handlers.GetPiAgentModelSettings())
		apiGroup.PATCH("/settings/pi-agent/model-settings", handlers.UpdatePiAgentModelSettings())
		apiGroup.GET("/settings/pi-agent/backups", handlers.ListPiAgentBackups())
		apiGroup.POST("/settings/pi-agent/backups", handlers.CreatePiAgentBackup())
		apiGroup.POST("/settings/pi-agent/backups/:id/restore", handlers.RestorePiAgentBackup())
		apiGroup.GET("/skills", handlers.ListSkills())
		apiGroup.POST("/skills/content", handlers.GetSkillContent())
		apiGroup.POST("/skills/backup/latest", handlers.GetLatestSkillBackup())
		apiGroup.POST("/skills/note", handlers.UpdateSkillNote())
		apiGroup.POST("/skills/consolidate", handlers.ConsolidateSkills())
		apiGroup.POST("/skills/import", handlers.ImportSkill())
		apiGroup.GET("/skills/search", handlers.SearchSkills())
		apiGroup.POST("/skills/remote/inspect", handlers.InspectRemoteSkill())
		apiGroup.POST("/skills/remote/install", handlers.InstallRemoteSkill())
		apiGroup.POST("/skills/copy", handlers.CopySkill())
		apiGroup.DELETE("/skills", handlers.DeleteSkill())
		apiGroup.POST("/skills/backup", handlers.BackupSkill())
	}

	// 代理端点 - Messages API
	r.POST("/v1/messages", messages.Handler(envCfg, cfgManager, channelScheduler, contentSafetyPipeline))
	r.POST("/v1/messages/count_tokens", messages.CountTokensHandler(envCfg, cfgManager, channelScheduler))

	// 代理端点 - Models API（转发到上游）
	r.GET("/v1/models", messages.ModelsHandler(envCfg, cfgManager, channelScheduler))
	r.GET("/v1/models/:model", messages.ModelsDetailHandler(envCfg, cfgManager, channelScheduler))

	// 代理端点 - Responses API
	r.POST("/v1/responses", responses.Handler(envCfg, cfgManager, sessionManager, channelScheduler, contentSafetyPipeline))
	r.POST("/v1/responses/compact", responses.CompactHandler(envCfg, cfgManager, sessionManager, channelScheduler, contentSafetyPipeline))

	// 代理端点 - Chat Completions API (OpenAI-compatible 原生协议)
	r.POST("/v1/chat/completions", chat.Handler(envCfg, cfgManager, channelScheduler, contentSafetyPipeline))
	// 代理端点 - Images API (OpenAI-compatible 独立协议)
	r.POST("/v1/images/generations", images.Handler(envCfg, cfgManager, channelScheduler, "/images/generations"))
	r.POST("/v1/images/edits", images.Handler(envCfg, cfgManager, channelScheduler, "/images/edits"))
	r.POST("/v1/images/variations", images.Handler(envCfg, cfgManager, channelScheduler, "/images/variations"))

	// 代理端点 - Gemini API (原生协议)
	// 使用通配符捕获 model:action 格式，如 gemini-pro:generateContent
	// 路径格式：/v1beta/models/{model}:generateContent (Gemini 原生格式)
	r.POST("/v1beta/models/*modelAction", gemini.Handler(envCfg, cfgManager, channelScheduler, contentSafetyPipeline))

	// 静态文件服务 (嵌入的前端)
	if envCfg.EnableWebUI {
		handlers.ServeFrontend(r, frontendFS)
	} else {
		// 纯 API 模式
		r.GET("/", func(c *gin.Context) {
			c.JSON(200, gin.H{
				"name":    "API Proxy",
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
	fmt.Printf("\n[Server-Startup] API代理服务器已启动\n")
	fmt.Printf("[Server-Info] 版本: %s\n", Version)
	if BuildTime != "unknown" {
		fmt.Printf("[Server-Info] 构建时间: %s\n", BuildTime)
	}
	if GitCommit != "unknown" {
		fmt.Printf("[Server-Info] Git提交: %s\n", GitCommit)
	}
	fmt.Printf("[Server-Info] 管理界面: http://localhost:%d\n", envCfg.Port)
	fmt.Printf("[Server-Info] API 地址: http://localhost:%d/v1\n", envCfg.Port)
	fmt.Printf("[Server-Info] Messages: POST /v1/messages\n")
	fmt.Printf("[Server-Info] Responses: POST /v1/responses\n")
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

		// 停止调度器聚合的后台组件（指标清理/亲和清理/性能画像更新循环）。
		// 必须先于 metricsStore.Close：先停数据生产者，再关持久化存储，
		// 避免关闭期间后台循环与存储 Close 产生写并发。
		channelScheduler.Stop()

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

func syncUpstreamProfiles(mm *metrics.MetricsManager, pm *metrics.ProfileManager, upstream *config.UpstreamConfig, channelIdx int) {
	if mm == nil || pm == nil || upstream == nil || len(upstream.APIKeys) == 0 {
		return
	}

	models := make(map[string]struct{})
	if model := strings.TrimSpace(upstream.DefaultModel); model != "" {
		models[model] = struct{}{}
	}
	for sourceModel, targetModels := range upstream.ModelMapping {
		if source := strings.TrimSpace(sourceModel); source != "" && source != "*" {
			models[source] = struct{}{}
		}
		for _, targetModel := range targetModels {
			if target := strings.TrimSpace(targetModel); target != "" {
				models[target] = struct{}{}
			}
		}
	}
	if len(models) == 0 {
		return
	}

	for _, baseURL := range upstream.GetAllBaseURLs() {
		for model := range models {
			mm.SyncToProfile(pm, baseURL, upstream.APIKeys, model, channelIdx)
		}
	}
}
