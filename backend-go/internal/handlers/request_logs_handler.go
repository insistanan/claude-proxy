package handlers

import (
	"net/http"
	"strconv"

	"github.com/BenedictKing/claude-proxy/internal/metrics"
	"github.com/gin-gonic/gin"
)

func GetRequestLogs(store *metrics.RequestLogStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := 50
		if rawLimit := c.Query("limit"); rawLimit != "" {
			parsed, err := strconv.Atoi(rawLimit)
			if err != nil || parsed < 1 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
				return
			}
			limit = parsed
		}
		if limit > 50 {
			limit = 50
		}

		logs, err := store.List(metrics.RequestLogListOptions{
			APIType: c.Query("type"),
			Limit:   limit,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load request logs"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"logs":  logs,
			"limit": limit,
		})
	}
}
