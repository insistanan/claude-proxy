package images

import (
	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
	"github.com/gin-gonic/gin"
)

func GetDashboard(cfgManager *config.ConfigManager, sch *scheduler.ChannelScheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := cfgManager.GetConfig()
		upstreams := cfg.ImagesUpstream
		loadBalance := cfg.ImagesLoadBalance
		metricsManager := sch.GetImagesMetricsManager()

		channels := make([]gin.H, 0, len(upstreams))
		for i, up := range upstreams {
			if config.GetChannelStatus(&up) == config.ChannelStatusDeleted {
				continue
			}
			status := config.GetChannelStatus(&up)
			priority := config.GetChannelPriority(&up, i)

			channels = append(channels, gin.H{
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

		metricsResult := make([]gin.H, 0, len(upstreams))
		for i, upstream := range upstreams {
			if config.GetChannelStatus(&upstream) == config.ChannelStatusDeleted {
				continue
			}
			resp := metricsManager.ToResponseMultiURL(i, upstream.GetAllBaseURLs(), upstream.APIKeys, 0, upstream.HistoricalAPIKeys)

			item := gin.H{
				"channelIndex":        i,
				"channelName":         upstream.Name,
				"requestCount":        resp.RequestCount,
				"successCount":        resp.SuccessCount,
				"failureCount":        resp.FailureCount,
				"successRate":         resp.SuccessRate,
				"errorRate":           resp.ErrorRate,
				"consecutiveFailures": resp.ConsecutiveFailures,
				"latency":             resp.Latency,
				"keyMetrics":          resp.KeyMetrics,
				"timeWindows":         resp.TimeWindows,
			}

			if resp.LastSuccessAt != nil {
				item["lastSuccessAt"] = *resp.LastSuccessAt
			}
			if resp.LastFailureAt != nil {
				item["lastFailureAt"] = *resp.LastFailureAt
			}
			if resp.CircuitBrokenAt != nil {
				item["circuitBrokenAt"] = *resp.CircuitBrokenAt
			}

			metricsResult = append(metricsResult, item)
		}

		stats := gin.H{
			"multiChannelMode":    sch.IsMultiChannelMode(scheduler.ChannelKindImages),
			"activeChannelCount":  sch.GetActiveChannelCount(scheduler.ChannelKindImages),
			"traceAffinityCount":  sch.GetTraceAffinityManager().SizeForKind(string(scheduler.ChannelKindImages)),
			"traceAffinityTTL":    sch.GetTraceAffinityManager().GetTTL().String(),
			"failureThreshold":    metricsManager.GetFailureThreshold() * 100,
			"windowSize":          metricsManager.GetWindowSize(),
			"circuitRecoveryTime": metricsManager.GetCircuitRecoveryTime().String(),
		}

		recentActivity := make([]*metrics.ChannelRecentActivity, 0, len(upstreams))
		for i, upstream := range upstreams {
			if config.GetChannelStatus(&upstream) == config.ChannelStatusDeleted {
				continue
			}
			recentActivity = append(recentActivity, metricsManager.GetRecentActivityMultiURL(i, upstream.GetAllBaseURLs(), upstream.APIKeys))
		}

		c.JSON(200, gin.H{
			"channels":       channels,
			"loadBalance":    loadBalance,
			"metrics":        metricsResult,
			"stats":          stats,
			"recentActivity": recentActivity,
		})
	}
}
