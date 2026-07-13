package images

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/httpclient"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/gin-gonic/gin"
)

func GetUpstreams(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := cfgManager.GetConfig()

		upstreams := make([]gin.H, 0, len(cfg.ImagesUpstream))
		for i, up := range cfg.ImagesUpstream {
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
				"defaultModel":       up.DefaultModel,
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
			"loadBalance": cfg.ImagesLoadBalance,
		})
	}
}

func AddUpstream(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var upstream config.UpstreamConfig
		if err := c.ShouldBindJSON(&upstream); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		created, err := cfgManager.AddImagesUpstreamWithResult(upstream)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{
			"message": "Images upstream added successfully",
			"channel": gin.H{"id": created.ID, "index": created.Index},
		})
	}
}

func UpdateUpstream(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid upstream ID"})
			return
		}

		var updates config.UpstreamUpdate
		if err := c.ShouldBindJSON(&updates); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		shouldResetMetrics, err := cfgManager.UpdateImagesUpstream(id, updates)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		if shouldResetMetrics {
			sch.ResetChannelMetrics(id, scheduler.ChannelKindImages)
		}

		c.JSON(200, gin.H{"message": "Images upstream updated successfully"})
	}
}

func DeleteUpstream(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid upstream ID"})
			return
		}

		removed, err := cfgManager.RemoveImagesUpstream(id)
		if err != nil {
			if strings.Contains(err.Error(), "无效的") {
				c.JSON(404, gin.H{"error": "Upstream not found"})
			} else {
				c.JSON(500, gin.H{"error": err.Error()})
			}
			return
		}

		sch.DeleteChannelMetrics(removed, scheduler.ChannelKindImages)
		sch.GetTraceAffinityManager().RemoveByChannelForKind(string(scheduler.ChannelKindImages), id)
		c.JSON(200, gin.H{"message": "Images upstream deleted successfully"})
	}
}

func AddApiKey(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
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

		if err := cfgManager.AddImagesAPIKey(id, req.APIKey); err != nil {
			if strings.Contains(err.Error(), "无效的上游索引") {
				c.JSON(404, gin.H{"error": "Upstream not found"})
			} else if strings.Contains(err.Error(), "API密钥已存在") {
				c.JSON(400, gin.H{"error": "API密钥已存在"})
			} else {
				c.JSON(500, gin.H{"error": "Failed to save config"})
			}
			return
		}

		c.JSON(200, gin.H{"message": "API密钥已添加", "success": true})
	}
}

func DeleteApiKey(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid upstream ID"})
			return
		}

		apiKey := c.Param("apiKey")
		if apiKey == "" {
			c.JSON(400, gin.H{"error": "API key is required"})
			return
		}

		if err := cfgManager.RemoveImagesAPIKey(id, apiKey); err != nil {
			if strings.Contains(err.Error(), "无效的上游索引") {
				c.JSON(404, gin.H{"error": "Upstream not found"})
			} else if strings.Contains(err.Error(), "API密钥不存在") {
				c.JSON(404, gin.H{"error": "API key not found"})
			} else {
				c.JSON(500, gin.H{"error": "Failed to save config"})
			}
			return
		}

		c.JSON(200, gin.H{"message": "API密钥已删除"})
	}
}

func MoveApiKeyToTop(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		apiKey := c.Param("apiKey")

		if err := cfgManager.MoveImagesAPIKeyToTop(id, apiKey); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "API密钥已置顶"})
	}
}

func MoveApiKeyToBottom(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := strconv.Atoi(c.Param("id"))
		apiKey := c.Param("apiKey")

		if err := cfgManager.MoveImagesAPIKeyToBottom(id, apiKey); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"message": "API密钥已置底"})
	}
}

func ReorderChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Order []int `json:"order"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request body"})
			return
		}

		if err := cfgManager.ReorderImagesUpstreams(req.Order); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{"success": true, "message": "Images 渠道优先级已更新"})
	}
}

func SetChannelStatus(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
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

		if err := cfgManager.SetImagesChannelStatus(id, req.Status); err != nil {
			if strings.Contains(err.Error(), "无效的上游索引") {
				c.JSON(404, gin.H{"error": "Channel not found"})
			} else {
				c.JSON(400, gin.H{"error": err.Error()})
			}
			return
		}

		c.JSON(200, gin.H{"success": true, "message": "Images 渠道状态已更新", "status": req.Status})
	}
}

func SetChannelPromotion(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(400, gin.H{"error": "Invalid channel ID"})
			return
		}

		var req struct {
			Duration int `json:"duration"`
			Count    int `json:"count"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request body"})
			return
		}

		if req.Duration <= 0 && req.Count <= 0 {
			if err := cfgManager.SetImagesChannelPromotion(id, 0, 0); err != nil {
				c.JSON(400, gin.H{"error": err.Error()})
				return
			}
			c.JSON(200, gin.H{"success": true, "message": "Images 渠道促销期已清除"})
			return
		}

		duration := time.Duration(req.Duration) * time.Second
		if err := cfgManager.SetImagesChannelPromotion(id, duration, req.Count); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{"success": true, "message": "Images 渠道促销期已设置", "duration": req.Duration, "count": req.Count})
	}
}

func UpdateLoadBalance(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Strategy string `json:"strategy"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "Invalid request body"})
			return
		}

		if err := cfgManager.SetImagesLoadBalance(req.Strategy); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{"success": true, "message": "Images 负载均衡策略已更新", "strategy": req.Strategy})
	}
}

func PingChannel(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel ID"})
			return
		}

		cfg := cfgManager.GetConfig()
		if id < 0 || id >= len(cfg.ImagesUpstream) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		channel := cfg.ImagesUpstream[id]
		result := pingChannelWithAPIKey(&channel)
		c.JSON(http.StatusOK, result)
	}
}

func pingChannelWithAPIKey(ch *config.UpstreamConfig) gin.H {
	urls := ch.GetAllBaseURLs()
	if len(urls) == 0 {
		return gin.H{"success": false, "latency": 0, "status": "error", "error": "no_base_url"}
	}

	if len(ch.APIKeys) == 0 {
		return pingChannelURLs(ch)
	}

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

			var endpoint string
			switch ch.ServiceType {
			case "claude":
				endpoint = testURL + "/v1/models"
			case "openai":
				endpoint = testURL + "/v1/models"
			default:
				endpoint = testURL + "/v1/models"
			}

			client := httpclient.GetManager().GetStandardClient(10*time.Second, ch.InsecureSkipVerify)
			req, err := http.NewRequest("GET", endpoint, nil)
			if err != nil {
				results <- pingResult{url: testURL, latency: 0, success: false, err: "req_creation_failed"}
				return
			}

			if strings.HasPrefix(apiKey, "sk-ant-") {
				req.Header.Set("x-api-key", apiKey)
			} else {
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
			req.Header.Set("User-Agent", "claude-cli/2.0.34 (external, cli)")

			resp, err := client.Do(req)
			latency := time.Since(startTime).Milliseconds()
			if err != nil {
				results <- pingResult{url: testURL, latency: latency, success: false, err: err.Error()}
				return
			}
			defer resp.Body.Close()

			success := resp.StatusCode >= 200 && resp.StatusCode < 400
			if !success {
				results <- pingResult{url: testURL, latency: latency, success: false, err: fmt.Sprintf("status_%d", resp.StatusCode)}
				return
			}
			results <- pingResult{url: testURL, latency: latency, success: true}
		}(baseURL)
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

func pingChannelURLs(ch *config.UpstreamConfig) gin.H {
	urls := ch.GetAllBaseURLs()
	if len(urls) == 0 {
		return gin.H{"success": false, "latency": 0, "status": "error", "error": "no_base_url"}
	}

	type pingResult struct {
		latency int64
		success bool
		err     string
	}

	results := make(chan pingResult, len(urls))
	for _, url := range urls {
		go func(testURL string) {
			start := time.Now()
			testURL = strings.TrimSuffix(testURL, "/")
			client := httpclient.GetManager().GetStandardClient(5*time.Second, ch.InsecureSkipVerify)
			req, err := http.NewRequest("HEAD", testURL, nil)
			if err != nil {
				results <- pingResult{latency: 0, success: false, err: "req_creation_failed"}
				return
			}
			resp, err := client.Do(req)
			latency := time.Since(start).Milliseconds()
			if err != nil {
				results <- pingResult{latency: latency, success: false, err: err.Error()}
				return
			}
			resp.Body.Close()
			results <- pingResult{latency: latency, success: true}
		}(url)
	}

	var best *pingResult
	for i := 0; i < len(urls); i++ {
		r := <-results
		if r.success {
			if best == nil || !best.success || r.latency < best.latency {
				best = &r
			}
		} else if best == nil || !best.success {
			best = &r
		}
	}

	if best == nil {
		return gin.H{"success": false, "latency": 0, "status": "error", "error": "all_urls_failed"}
	}
	if best.success {
		return gin.H{"success": true, "latency": best.latency, "status": "healthy"}
	}
	return gin.H{"success": false, "latency": best.latency, "status": "error", "error": best.err}
}

func PingAllChannels(cfgManager *config.ConfigManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := cfgManager.GetConfig()
		results := make(chan gin.H)
		var wg sync.WaitGroup

		for i, channel := range cfg.ImagesUpstream {
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

		finalResults := []gin.H{}
		for res := range results {
			finalResults = append(finalResults, res)
		}

		c.JSON(http.StatusOK, finalResults)
	}
}
