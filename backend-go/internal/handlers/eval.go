package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/eval"
	"github.com/gin-gonic/gin"
)

type EvalAPI struct {
	service *eval.Service
}

func NewEvalAPI(service *eval.Service) *EvalAPI {
	return &EvalAPI{service: service}
}

func RegisterEvalRoutes(apiGroup *gin.RouterGroup, service *eval.Service) {
	api := NewEvalAPI(service)
	group := apiGroup.Group("/eval")
	group.GET("/probes", api.ListProbes)
	group.POST("/probes", api.CreateProbe)
	group.PUT("/probes/:id", api.UpdateProbe)
	group.DELETE("/probes/:id", api.DeleteProbe)
	group.POST("/probes/validate", api.ValidateProbe)

	group.GET("/suites", api.ListSuites)
	group.POST("/suites", api.CreateSuite)
	group.PUT("/suites/:id", api.UpdateSuite)
	group.DELETE("/suites/:id", api.DeleteSuite)

	group.POST("/runs", api.StartRun)
	group.GET("/runs", api.ListRuns)
	group.GET("/runs/:id", api.GetRun)
	group.POST("/runs/:id/cancel", api.CancelRun)
	group.GET("/runs/:id/events", api.RunEvents)

	group.GET("/channels/latest-map", api.LatestMap)
	group.GET("/watch", api.GetWatch)
	group.PUT("/watch", api.PutWatch)
	group.POST("/estimate", api.Estimate)
}

func (a *EvalAPI) ListProbes(c *gin.Context) {
	probes, err := a.service.Store.ListProbes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"probes": probes})
}

func (a *EvalAPI) CreateProbe(c *gin.Context) {
	var probe eval.Probe
	if err := c.ShouldBindJSON(&probe); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	report := eval.ValidateProbe(probe)
	if !report.OK {
		status := http.StatusBadRequest
		if report.Mode == eval.ValidateNeedsNewGrader || report.Mode == eval.ValidateUnsupportedGrader {
			status = http.StatusUnprocessableEntity
		}
		c.JSON(status, gin.H{"error": strings.Join(report.Errors, "; "), "validate": report})
		return
	}
	created, err := a.service.Store.InsertProbe(probe)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "validate": report})
		return
	}
	c.JSON(http.StatusOK, gin.H{"probe": created, "validate": report})
}

func (a *EvalAPI) UpdateProbe(c *gin.Context) {
	var probe eval.Probe
	if err := c.ShouldBindJSON(&probe); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	probe.ID = c.Param("id")
	updated, err := a.service.Store.UpdateProbe(probe)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"probe": updated})
}

func (a *EvalAPI) DeleteProbe(c *gin.Context) {
	if err := a.service.Store.DeleteProbe(c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *EvalAPI) ValidateProbe(c *gin.Context) {
	var probe eval.Probe
	if err := c.ShouldBindJSON(&probe); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, eval.ValidateProbe(probe))
}

func (a *EvalAPI) ListSuites(c *gin.Context) {
	suites, err := a.service.Store.ListSuites()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"suites": suites})
}

func (a *EvalAPI) CreateSuite(c *gin.Context) {
	var suite eval.Suite
	if err := c.ShouldBindJSON(&suite); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	probes, err := a.service.Store.ProbesForSuite(suite)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := eval.ValidateSuite(suite, probes); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	created, err := a.service.Store.InsertSuite(suite)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"suite": created})
}

func (a *EvalAPI) UpdateSuite(c *gin.Context) {
	var suite eval.Suite
	if err := c.ShouldBindJSON(&suite); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	suite.ID = c.Param("id")
	probes, err := a.service.Store.ProbesForSuite(suite)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := eval.ValidateSuite(suite, probes); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := a.service.Store.UpdateSuite(suite)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"suite": updated})
}

func (a *EvalAPI) DeleteSuite(c *gin.Context) {
	if err := a.service.Store.DeleteSuite(c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

func (a *EvalAPI) StartRun(c *gin.Context) {
	var req eval.StartRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	run, err := a.service.Runner.Start(req)
	if errors.Is(err, eval.ErrBusy) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "runId": a.service.Runner.CurrentRunID()})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": run})
}

// 评测历史列表的取数上限。默认给页面够用的条数；渠道深链要看某个渠道的完整历史，
// 会显式带上更大的 limit，但仍然封顶，避免一次把整库批次全查出来。
const (
	evalRunsDefaultLimit = 30
	evalRunsMaxLimit     = 200
)

func (a *EvalAPI) ListRuns(c *gin.Context) {
	limit := evalRunsDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit 必须是正整数"})
			return
		}
		if parsed > evalRunsMaxLimit {
			parsed = evalRunsMaxLimit
		}
		limit = parsed
	}

	// channel 为渠道稳定 UUID（评测侧一律用它，不是 channelIndex）。
	channelID := strings.TrimSpace(c.Query("channel"))
	runs, err := a.service.Store.ListRuns(channelID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs, "busy": a.service.Runner.Busy(), "currentRunId": a.service.Runner.CurrentRunID()})
}

func (a *EvalAPI) GetRun(c *gin.Context) {
	run, err := a.service.Store.GetRun(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": run})
}

func (a *EvalAPI) CancelRun(c *gin.Context) {
	if err := a.service.Runner.Cancel(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RunEvents 用 SSE 推批次进度。只在 status / 已完成格子数 变化时发帧，避免空转刷屏。
func (a *EvalAPI) RunEvents(c *gin.Context) {
	runID := c.Param("id")
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	lastSignature := ""
	// push 返回 false 表示连接断了或批次已终态，调用方结束循环。
	push := func() bool {
		run, err := a.service.Store.GetRun(runID)
		if err != nil {
			writeEvalEvent(c, gin.H{"error": err.Error()})
			return false
		}
		signature := fmt.Sprintf("%s|%d|%d", run.Status, len(run.Results), run.FinishedAt)
		if signature != lastSignature {
			lastSignature = signature
			if !writeEvalEvent(c, gin.H{"run": run}) {
				return false
			}
		}
		switch run.Status {
		case eval.RunDone, eval.RunPartial, eval.RunFailed, eval.RunCancelled, eval.RunSkipped:
			return false
		}
		return true
	}

	if !push() {
		return
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
			if !push() {
				return
			}
		}
	}
}

func writeEvalEvent(c *gin.Context, payload gin.H) bool {
	encoded, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[Eval-Run] 序列化 SSE 载荷失败: %v", err)
		return false
	}
	if _, err := io.WriteString(c.Writer, "data: "+string(encoded)+"\n\n"); err != nil {
		return false
	}
	c.Writer.Flush()
	return true
}

func (a *EvalAPI) LatestMap(c *gin.Context) {
	latest, watch, err := a.service.LatestMap()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"channels": latest, "watch": watch, "busy": a.service.Runner.Busy()})
}

func (a *EvalAPI) GetWatch(c *gin.Context) {
	watch, err := a.service.Store.GetWatch()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"watch": watch})
}

func (a *EvalAPI) PutWatch(c *gin.Context) {
	var watch eval.WatchConfig
	if err := c.ShouldBindJSON(&watch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	saved, err := a.service.PutWatch(watch)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"watch": saved})
}

func (a *EvalAPI) Estimate(c *gin.Context) {
	var req eval.StartRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	suite, probes, err := a.service.Store.SuiteWithProbes(req.SuiteID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"estimatedCalls": eval.EstimateCalls(probes, len(req.ChannelIDs)), "suiteCheap": suite.Cheap})
}
