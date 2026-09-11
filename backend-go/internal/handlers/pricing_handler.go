package handlers

// 模型单价表的管理端 API。写侧全部落在 pricing.Store 上：校验、归一化、落盘与
// 失败回滚都在 Store 里完成，本文件只做 HTTP 形态转换与错误码映射，不重复校验。

import (
	"errors"
	"net/http"
	"strings"

	"github.com/BenedictKing/api-proxy/internal/pricing"
	"github.com/gin-gonic/gin"
)

// pricingEntryView 单条单价的展示形态。Source 让界面能区分"内置价"与"我改过的价"：
// 只给合并后的数字，用户无法判断某个价格是自己填的还是版本自带的，
// 也就不知道删除后会回落到什么值。
type pricingEntryView struct {
	pricing.ModelPricing
	Source string `json:"source"` // builtin | override
}

// pricingUpsertRequest 批量写入请求。一律用数组：界面上单行编辑发一个元素的数组，
// 与批量导入共用同一条代码路径，省掉"单条/批量"两种形态各自的校验分支。
type pricingUpsertRequest struct {
	Models []pricing.ModelPricing `json:"models"`
}

// GetModelPricing 返回合并后的完整单价表 + 删除墓碑。
// GET /api/pricing/models
func GetModelPricing(prices *pricing.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if prices == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "模型单价表未初始化"})
			return
		}

		overridden := make(map[string]struct{})
		for _, entry := range prices.Overrides() {
			overridden[pricing.NormalizeModelID(entry.ModelID)] = struct{}{}
		}

		resolved := prices.All()
		views := make([]pricingEntryView, 0, len(resolved))
		for _, entry := range resolved {
			source := "builtin"
			if _, ok := overridden[pricing.NormalizeModelID(entry.ModelID)]; ok {
				source = "override"
			}
			views = append(views, pricingEntryView{ModelPricing: entry, Source: source})
		}

		c.JSON(http.StatusOK, gin.H{
			"models":          views,
			"deletedModelIds": prices.DeletedModelIDs(),
		})
	}
}

// UpsertModelPricing 写入或更新用户单价改写。
// POST /api/pricing/models  body: {"models":[{...}]}
func UpsertModelPricing(prices *pricing.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if prices == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "模型单价表未初始化"})
			return
		}

		var req pricingUpsertRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体不是合法 JSON: " + err.Error()})
			return
		}
		if len(req.Models) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "models 不能为空"})
			return
		}

		// 落盘失败是 500（此时 Store 已把内存状态回滚），其余都是入参非法 → 400。
		// 靠哨兵错误分类，不去匹配错误文案。
		if err := prices.Upsert(req.Models...); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, pricing.ErrPricingPersist) {
				status = http.StatusInternalServerError
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "count": len(req.Models)})
	}
}

// DeleteModelPricing 把模型从单价表里移除（内置条目改为写删除墓碑）。
// DELETE /api/pricing/models?modelId=xxx
// 模型名走 query 而不是路径参数：OpenRouter 风格的 `vendor/model` 里带斜杠，
// 放进 :modelId 会被 gin 当成两段路径，永远匹配不到这条路由。
func DeleteModelPricing(prices *pricing.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		if prices == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "模型单价表未初始化"})
			return
		}

		modelID := strings.TrimSpace(c.Query("modelId"))
		if modelID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 modelId 参数"})
			return
		}
		if err := prices.Delete(modelID); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, pricing.ErrModelNotListed) {
				status = http.StatusNotFound
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	}
}
