package modelaudit

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const maxCreateExecutionBodyBytes = 512 * 1024

type Handler struct {
	service    *Service
	management *AuditManagementService
}

func NewHandler(service *Service, management ...*AuditManagementService) (*Handler, error) {
	if service == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行服务不能为空")
	}
	if len(management) > 1 {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计管理服务只能提供一个")
	}
	var auditManagement *AuditManagementService
	if len(management) == 1 {
		auditManagement = management[0]
		if auditManagement == nil {
			return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计管理服务不能为空")
		}
	}
	return &Handler{service: service, management: auditManagement}, nil
}

func RegisterRoutes(group *gin.RouterGroup, service *Service, management ...*AuditManagementService) error {
	if group == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "管理 API 路由组不能为空")
	}
	handler, err := NewHandler(service, management...)
	if err != nil {
		return err
	}
	routes := group.Group("/model-audit")
	routes.GET("/capabilities", handler.capabilities)
	routes.POST("/executions", handler.createExecution)
	routes.GET("/executions/:id", handler.getExecution)
	routes.POST("/executions/:id/cancel", handler.cancelExecution)
	if handler.management == nil {
		return nil
	}
	routes.GET("/jobs", handler.listAuditJobs)
	routes.GET("/strategies", handler.listAuditStrategies)
	routes.GET("/capability-presets", handler.listAuditCapabilityPresets)
	routes.POST("/jobs", handler.createAuditJob)
	routes.GET("/jobs/:id", handler.getAuditJob)
	routes.PUT("/jobs/:id", handler.updateAuditJob)
	routes.POST("/jobs/:id/transition", handler.transitionAuditJob)
	routes.DELETE("/jobs/:id", handler.deleteAuditJob)
	routes.POST("/jobs/:id/runs", handler.startManualAuditRun)
	routes.GET("/runs", handler.listAuditRuns)
	routes.GET("/runs/:id", handler.getAuditRun)
	routes.POST("/runs/:id/cancel", handler.cancelAuditRun)
	routes.GET("/channels/:kind/:id/summary", handler.getAuditChannelSummary)
	routes.GET("/reports/:id", handler.getAuditReportDetail)
	routes.GET("/reports/:id/export.html", handler.exportAuditReportHTML)
	return nil
}

func (h *Handler) capabilities(c *gin.Context) {
	c.JSON(http.StatusOK, NewCapabilitiesResponse())
}

func (h *Handler) createExecution(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateExecutionBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request CreateExecutionRequest
	if err := decoder.Decode(&request); err != nil {
		writeAPIError(c, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "执行请求 JSON 无效", err))
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeAPIError(c, err)
		return
	}
	spec, err := request.ExecutionSpec()
	if err != nil {
		writeAPIError(c, err)
		return
	}
	response, err := h.service.Start(c.Request.Context(), spec)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, response)
}

func (h *Handler) getExecution(c *gin.Context) {
	executionID := strings.TrimSpace(c.Param("id"))
	if executionID == "" {
		writeAPIError(c, contractError(ErrorCodeExecutionNotFound, ErrorCategoryRequest, "执行 ID 不能为空"))
		return
	}
	response, ok := h.service.Get(executionID)
	if !ok {
		writeAPIError(c, contractError(ErrorCodeExecutionNotFound, ErrorCategoryRequest, "执行不存在"))
		return
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) cancelExecution(c *gin.Context) {
	executionID := strings.TrimSpace(c.Param("id"))
	if err := h.service.Cancel(c.Request.Context(), executionID); err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, CancelExecutionResponse{ExecutionID: executionID, Status: StatusCancelled})
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "执行请求只能包含一个 JSON 对象")
	}
	return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "执行请求 JSON 无效", err)
}

func writeAPIError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch ErrorCodeOf(err) {
	case ErrorCodeInvalidRequest, ErrorCodeInvalidPurpose, ErrorCodeInvalidChannelKind,
		ErrorCodeInvalidProtocol, ErrorCodeProtocolKindMismatch, ErrorCodeChannelIDRequired,
		ErrorCodeModelRequired:
		status = http.StatusBadRequest
	case ErrorCodeChannelNotFound, ErrorCodeExecutionNotFound, ErrorCodeAuditNotFound:
		status = http.StatusNotFound
	case ErrorCodeChannelIDAmbiguous, ErrorCodeExecutionNotRunning, ErrorCodeConflict:
		status = http.StatusConflict
	case ErrorCodeUnsupported:
		status = http.StatusUnprocessableEntity
	case ErrorCodeConcurrencyLimited, ErrorCodeRateLimit:
		status = http.StatusTooManyRequests
	}
	c.JSON(status, NewAPIError(err))
}
