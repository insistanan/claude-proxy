package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BenedictKing/api-proxy/internal/sensitive"
	"github.com/gin-gonic/gin"
)

// GetBlockedLogs 分页查询拦截记录。
func GetBlockedLogs(store *sensitive.BlockedStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if store == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "拦截记录存储未初始化"})
			return
		}
		page, ok := parseBlockedLogPositiveInt(c, "page", 1, 0)
		if !ok {
			return
		}
		pageSize, ok := parseBlockedLogPositiveInt(c, "pageSize", 20, 100)
		if !ok {
			return
		}
		apiType := strings.ToLower(strings.TrimSpace(c.Query("apiType")))
		if apiType != "" && !validBlockedLogAPIType(apiType) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 API 类型"})
			return
		}
		blockType := strings.ToLower(strings.TrimSpace(c.Query("blockType")))
		if blockType != "" && !validBlockedLogType(blockType) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的拦截类型"})
			return
		}
		from, ok := parseBlockedLogTime(c, "from")
		if !ok {
			return
		}
		to, ok := parseBlockedLogTime(c, "to")
		if !ok {
			return
		}
		if !from.IsZero() && !to.IsZero() && from.After(to) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "开始时间不能晚于结束时间"})
			return
		}

		options := sensitive.BlockedLogListOptions{
			APIType: apiType, BlockType: blockType, From: from, To: to, Page: page, PageSize: pageSize,
		}
		groupKey := strings.TrimSpace(c.Query("groupKey"))
		groupBy := strings.TrimSpace(c.Query("groupBy"))
		if groupKey != "" && groupBy != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "groupKey 与 groupBy 不能同时使用"})
			return
		}
		if groupKey != "" {
			result, err := store.ListGroupEntries(c.Request.Context(), options, groupKey)
			if err != nil {
				if errors.Is(err, sensitive.ErrInvalidBlockedLogGroupKey) {
					c.JSON(http.StatusBadRequest, gin.H{"error": "无效的会话分组标识"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": "读取会话明细拦截记录失败"})
				return
			}
			c.JSON(http.StatusOK, result)
			return
		}
		if strings.EqualFold(groupBy, "conversation") {
			result, err := store.ListGrouped(c.Request.Context(), options)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "读取会话分组拦截记录失败"})
				return
			}
			c.JSON(http.StatusOK, result)
			return
		}
		if groupBy != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无效的分组方式"})
			return
		}
		result, err := store.List(c.Request.Context(), options)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取拦截记录失败"})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

// GetBlockedLog 获取单条拦截记录。
func GetBlockedLog(store *sensitive.BlockedStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseBlockedLogID(c)
		if !ok {
			return
		}
		if store == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "拦截记录存储未初始化"})
			return
		}
		entry, found, err := store.Get(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取拦截记录失败"})
			return
		}
		if !found {
			c.JSON(http.StatusNotFound, gin.H{"error": "拦截记录不存在"})
			return
		}
		c.JSON(http.StatusOK, entry)
	}
}

// DeleteBlockedLog 删除单条拦截记录。
func DeleteBlockedLog(store *sensitive.BlockedStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseBlockedLogID(c)
		if !ok {
			return
		}
		if store == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "拦截记录存储未初始化"})
			return
		}
		deleted, err := store.Delete(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "删除拦截记录失败"})
			return
		}
		if !deleted {
			c.JSON(http.StatusNotFound, gin.H{"error": "拦截记录不存在"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// ClearBlockedLogs 清空全部拦截记录。
func ClearBlockedLogs(store *sensitive.BlockedStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if store == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "拦截记录存储未初始化"})
			return
		}
		deleted, err := store.Clear(c.Request.Context())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "清空拦截记录失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "deleted": deleted})
	}
}

func parseBlockedLogPositiveInt(c *gin.Context, name string, defaultValue, maximum int) (int, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return defaultValue, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || maximum > 0 && value > maximum {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的分页参数 " + name})
		return 0, false
	}
	return value, true
}

func parseBlockedLogTime(c *gin.Context, name string) (time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return time.Time{}, true
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的时间参数 " + name})
		return time.Time{}, false
	}
	return value, true
}

func parseBlockedLogID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的拦截记录 ID"})
		return 0, false
	}
	return id, true
}

func validBlockedLogAPIType(value string) bool {
	switch value {
	case "messages", "responses", "chat", "gemini":
		return true
	default:
		return false
	}
}

func validBlockedLogType(value string) bool {
	switch value {
	case sensitive.BlockTypeSensitiveWord, sensitive.BlockTypeSensitiveInfo, sensitive.BlockTypeCredential,
		sensitive.BlockTypeDangerousCmd, sensitive.BlockTypeWhitelist:
		return true
	default:
		return false
	}
}
