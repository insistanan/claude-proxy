// Package responses 提供 Responses API 的渠道管理
package responses

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/gin-gonic/gin"
)

// GetUpstreams 获取 Responses 上游列表
func GetUpstreams(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := cfgManager.GetConfig()

		upstreams := make([]gin.H, 0, len(cfg.ResponsesUpstream))
		for i, up := range cfg.ResponsesUpstream {
			if config.GetChannelStatus(&up) == config.ChannelStatusDeleted {
				continue
			}
			status := config.GetChannelStatus(&up)
			priority := config.GetChannelPriority(&up, i)

			upstreams = append(upstreams, gin.H{
				"id":                 up.ID,
				"index":              i,
				"name":               up.Name,
				"serviceType":        up.ServiceType,
				"baseUrl":            up.BaseURL,
				"baseUrls":           up.BaseURLs,
				"apiKeys":            up.APIKeys,
				"description":        up.Description,
				"website":            up.Website,
				"insecureSkipVerify": up.InsecureSkipVerify,
				"modelMapping":       up.ModelMapping,
				"latency":            nil,
				"status":             status,
				"priority":           priority,
				"promotionUntil":     up.PromotionUntil,
				"promotionCount":     up.PromotionCount,
				"lowQuality":         up.LowQuality,
				"visionCapable":      up.VisionCapable,
			})
		}

		c.JSON(200, gin.H{
			"channels":    upstreams,
			"loadBalance": cfg.ResponsesLoadBalance,
		})
	}
}

// AddUpstream 添加 Responses 上游
func AddUpstream(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var upstream config.UpstreamConfig
		if err := c.ShouldBindJSON(&upstream); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		created, err := cfgManager.AddResponsesUpstreamWithResult(upstream)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{
			"message": "Responses upstream added successfully",
			"channel": gin.H{"id": created.ID, "index": created.Index},
		})
	}
}

// UpdateUpstream 更新 Responses 上游
func UpdateUpstream(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid upstream ID"})
			return
		}

		var updates config.UpstreamUpdate
		if err := c.ShouldBindJSON(&updates); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		shouldResetMetrics, err := cfgManager.UpdateResponsesUpstream(id, updates)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		// 单 key 更换时重置熔断状态
		if shouldResetMetrics {
			sch.ResetChannelMetrics(id, scheduler.ChannelKindResponses)
		}

		c.JSON(200, gin.H{"message": "Responses upstream updated successfully"})
	}
}

// DeleteUpstream 删除 Responses 上游
func DeleteUpstream(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid upstream ID"})
			return
		}

		removed, err := cfgManager.RemoveResponsesUpstream(id)
		if err != nil {
			if strings.Contains(err.Error(), "无效的") {
				c.JSON(404, gin.H{"error": "Upstream not found"})
			} else {
				c.JSON(500, gin.H{"error": err.Error()})
			}
			return
		}

		// 删除成功后清理指标数据（使用 RemoveResponsesUpstream 返回的渠道信息）
		sch.DeleteChannelMetrics(removed, scheduler.ChannelKindResponses)
		sch.GetTraceAffinityManager().RemoveByChannelForKind(string(scheduler.ChannelKindResponses), id)

		c.JSON(200, gin.H{"message": "Responses upstream deleted successfully"})
	}
}

// AddApiKey 添加 Responses 渠道 API 密钥
func AddApiKey(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid upstream ID"})
			return
		}

		var req struct {
			APIKey string `json:"apiKey"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request body"})
			return
		}

		if err := cfgManager.AddResponsesAPIKey(id, req.APIKey); err != nil {
			if strings.Contains(err.Error(), "无效的上游索引") {
				c.JSON(404, gin.H{"error": "Upstream not found"})
			} else if strings.Contains(err.Error(), "API密钥已存在") {
				c.JSON(400, gin.H{"error": "API密钥已存在"})
			} else {
				c.JSON(500, gin.H{"error": "Failed to save config"})
			}
			return
		}

		c.JSON(200, gin.H{
			"message": "API密钥已添加",
			"success": true,
		})
	}
}

// DeleteApiKey 删除 Responses 渠道 API 密钥
func DeleteApiKey(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid upstream ID"})
			return
		}

		apiKey := c.Param("apiKey")
		if apiKey == "" {
			c.JSON(400, gin.H{"error": "API key is required"})
			return
		}

		if err := cfgManager.RemoveResponsesAPIKey(id, apiKey); err != nil {
			if strings.Contains(err.Error(), "无效的上游索引") {
				c.JSON(404, gin.H{"error": "Upstream not found"})
			} else if strings.Contains(err.Error(), "API密钥不存在") {
				c.JSON(404, gin.H{"error": "API key not found"})
			} else {
				c.JSON(500, gin.H{"error": "Failed to save config"})
			}
			return
		}

		c.JSON(200, gin.H{
			"message": "API密钥已删除",
		})
	}
}

// MoveApiKeyToTop 将 Responses 渠道 API 密钥移到最前面
func MoveApiKeyToTop(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		apiKey := c.Param("apiKey")

		if err := cfgManager.MoveResponsesAPIKeyToTop(id, apiKey); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "API密钥已置顶"})
	}
}

// MoveApiKeyToBottom 将 Responses 渠道 API 密钥移到最后面
func MoveApiKeyToBottom(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		apiKey := c.Param("apiKey")

		if err := cfgManager.MoveResponsesAPIKeyToBottom(id, apiKey); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "API密钥已置底"})
	}
}

// UpdateLoadBalance 更新 Responses 负载均衡策略
func UpdateLoadBalance(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Strategy string `json:"strategy"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request body"})
			return
		}

		if err := cfgManager.SetResponsesLoadBalance(req.Strategy); err != nil {
			if strings.Contains(err.Error(), "无效的负载均衡策略") {
				c.JSON(400, gin.H{"error": err.Error()})
			} else {
				c.JSON(500, gin.H{"error": "Failed to save config"})
			}
			return
		}

		c.JSON(200, gin.H{
			"message":  "Responses 负载均衡策略已更新",
			"strategy": req.Strategy,
		})
	}
}

// ReorderChannels 重新排序 Responses 渠道优先级
func ReorderChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Order []int `json:"order"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request body"})
			return
		}

		if err := cfgManager.ReorderResponsesUpstreams(req.Order); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{
			"success": true,
			"message": "Responses 渠道优先级已更新",
		})
	}
}

// SetChannelStatus 设置 Responses 渠道状态
func SetChannelStatus(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid channel ID"})
			return
		}

		var req struct {
			Status string `json:"status"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request body"})
			return
		}

		if err := cfgManager.SetResponsesChannelStatus(id, req.Status); err != nil {
			if strings.Contains(err.Error(), "无效的上游索引") {
				c.JSON(404, gin.H{"error": "Channel not found"})
			} else {
				c.JSON(400, gin.H{"error": err.Error()})
			}
			return
		}

		c.JSON(200, gin.H{
			"success": true,
			"message": "Responses 渠道状态已更新",
			"status":  req.Status,
		})
	}
}

// PingChannel 测试 Responses 渠道连通性
func PingChannel(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		idStr := c.Param("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid channel ID"})
			return
		}

		cfg := cfgManager.GetConfig()
		if id < 0 || id >= len(cfg.ResponsesUpstream) {
			c.JSON(404, gin.H{"error": "Channel not found"})
			return
		}

		channel := cfg.ResponsesUpstream[id]
		result := pingChannelWithAPIKey(&channel)
		c.JSON(200, result)
	}
}

// pingChannelWithAPIKey 使用真实 API 请求测试渠道（验证 URL + API Key）
func pingChannelWithAPIKey(ch *config.UpstreamConfig) gin.H {
	urls := ch.GetAllBaseURLs()
	if len(urls) == 0 {
		return gin.H{"success": false, "latency": 0, "status": "error", "error": "no_base_url"}
	}

	// 如果没有 API Key，回退到简单的连通性测试
	if len(ch.APIKeys) == 0 {
		return pingChannelURLs(ch)
	}

	// 使用第一个 API Key 测试（多 URL 并发，选最快的）
	apiKey := ch.APIKeys[0]

	type pingResult struct {
		url     string
		latency int64
		success bool
		err     string
	}

	results := make(chan pingResult, len(urls))
	for _, baseURL := range urls {
		go func(testURL string) {
			startTime := time.Now()
			testURL = strings.TrimSuffix(testURL, "/")

			// 根据 ServiceType 构建测试端点
			var endpoint string
			switch ch.ServiceType {
			case "responses":
				// Responses API 通常是 Codex 的封装，也支持 /v1/models
				endpoint = testURL + "/v1/models"
			case "claude":
				endpoint = testURL + "/v1/models"
			case "openai":
				endpoint = testURL + "/v1/models"
			default:
				endpoint = testURL + "/v1/models"
			}

			client := &http.Client{
				Timeout: 10 * time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: ch.InsecureSkipVerify},
				},
			}

			req, err := http.NewRequest("GET", endpoint, nil)
			if err != nil {
				results <- pingResult{url: testURL, latency: 0, success: false, err: "req_creation_failed"}
				return
			}

			// 设置认证头
			if strings.HasPrefix(apiKey, "sk-ant-") {
				req.Header.Set("x-api-key", apiKey)
			} else {
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}

			// 根据 ServiceType 补充对应客户端的请求头
			switch ch.ServiceType {
			case "responses":
				// Codex CLI 请求头（如果是 Responses API）
				req.Header.Set("X-Codex-Installation-Id", "proxy-ping-installation")
				req.Header.Set("X-Codex-Window-Id", "proxy-ping-window")
				req.Header.Set("X-Request-Id", fmt.Sprintf("ping-%d", time.Now().UnixNano()))
				req.Header.Set("User-Agent", "codex-cli")
			case "claude":
				// Claude Code CLI 请求头
				req.Header.Set("X-Claude-Code-Session-Id", fmt.Sprintf("ping-session-%d", time.Now().UnixNano()))
				req.Header.Set("X-App", "cli")
				req.Header.Set("X-Stainless-Lang", "js")
				req.Header.Set("X-Stainless-Package-Version", "0.74.0")
				req.Header.Set("X-Stainless-Runtime", "node")
				req.Header.Set("X-Stainless-Runtime-Version", "v22.12.0")
				req.Header.Set("X-Stainless-Os", "Linux")
				req.Header.Set("X-Stainless-Arch", "x64")
				req.Header.Set("X-Stainless-Retry-Count", "0")
				req.Header.Set("X-Stainless-Timeout", "600")
				req.Header.Set("User-Agent", "claude-cli/2.1.92 (external, cli)")
				req.Header.Set("anthropic-version", "2023-06-01")
			default:
				// 其他类型使用通用的 User-Agent
				req.Header.Set("User-Agent", "claude-cli/2.0.34 (external, cli)")
			}

			resp, err := client.Do(req)
			latency := time.Since(startTime).Milliseconds()
			if err != nil {
				results <- pingResult{url: testURL, latency: latency, success: false, err: err.Error()}
				return
			}
			defer resp.Body.Close()

			// 检查状态码（2xx 或 3xx 视为成功）
			success := resp.StatusCode >= 200 && resp.StatusCode < 400
			if !success {
				results <- pingResult{url: testURL, latency: latency, success: false, err: fmt.Sprintf("status_%d", resp.StatusCode)}
				return
			}
			results <- pingResult{url: testURL, latency: latency, success: true}
		}(baseURL)
	}

	// 收集结果，找最快的成功响应
	var bestResult *pingResult
	for i := 0; i < len(urls); i++ {
		r := <-results
		if r.success {
			if bestResult == nil || !bestResult.success || r.latency < bestResult.latency {
				bestResult = &r
			}
		} else if bestResult == nil || !bestResult.success {
			bestResult = &r
		}
	}

	if bestResult == nil {
		return gin.H{"success": false, "latency": 0, "status": "error", "error": "all_urls_failed"}
	}

	if bestResult.success {
		return gin.H{"success": true, "latency": bestResult.latency, "status": "healthy"}
	}
	return gin.H{"success": false, "latency": bestResult.latency, "status": "error", "error": bestResult.err}
}

// pingChannelURLs 简单的 URL 连通性测试（不验证 API Key）
func pingChannelURLs(ch *config.UpstreamConfig) gin.H {
	urls := ch.GetAllBaseURLs()
	if len(urls) == 0 {
		return gin.H{"success": false, "latency": 0, "status": "error", "error": "no_base_url"}
	}

	type pingResult struct {
		url     string
		latency int64
		success bool
		err     string
	}

	results := make(chan pingResult, len(urls))
	for _, url := range urls {
		go func(testURL string) {
			startTime := time.Now()
			testURL = strings.TrimSuffix(testURL, "/")

			client := &http.Client{
				Timeout: 5 * time.Second,
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: ch.InsecureSkipVerify},
				},
			}

			req, err := http.NewRequest("HEAD", testURL, nil)
			if err != nil {
				results <- pingResult{url: testURL, latency: 0, success: false, err: "req_creation_failed"}
				return
			}

			resp, err := client.Do(req)
			latency := time.Since(startTime).Milliseconds()
			if err != nil {
				results <- pingResult{url: testURL, latency: latency, success: false, err: err.Error()}
				return
			}
			resp.Body.Close()
			results <- pingResult{url: testURL, latency: latency, success: true}
		}(url)
	}

	var bestResult *pingResult
	for i := 0; i < len(urls); i++ {
		r := <-results
		if r.success {
			if bestResult == nil || !bestResult.success || r.latency < bestResult.latency {
				bestResult = &r
			}
		} else if bestResult == nil || !bestResult.success {
			bestResult = &r
		}
	}

	if bestResult == nil {
		return gin.H{"success": false, "latency": 0, "status": "error", "error": "all_urls_failed"}
	}

	if bestResult.success {
		return gin.H{"success": true, "latency": bestResult.latency, "status": "healthy"}
	}
	return gin.H{"success": false, "latency": bestResult.latency, "status": "error", "error": bestResult.err}
}

// PingAllChannels 测试所有 Responses 渠道连通性
func PingAllChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := cfgManager.GetConfig()
		results := make(chan gin.H)
		var wg sync.WaitGroup

		for i, channel := range cfg.ResponsesUpstream {
			wg.Add(1)
			go func(id int, ch config.UpstreamConfig) {
				defer wg.Done()
				result := pingChannelWithAPIKey(&ch)
				result["id"] = id
				result["name"] = ch.Name
				results <- result
			}(i, channel)
		}

		go func() {
			wg.Wait()
			close(results)
		}()

		var finalResults []gin.H
		for res := range results {
			finalResults = append(finalResults, res)
		}

		c.JSON(200, finalResults)
	}
}
