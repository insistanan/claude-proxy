package modelaudit

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *Handler) listAuditStrategies(c *gin.Context) {
	c.JSON(http.StatusOK, h.management.GetStrategyCatalog())
}

func (h *Handler) listAuditCapabilityPresets(c *gin.Context) {
	c.JSON(http.StatusOK, h.management.GetCapabilityCatalog())
}

func (h *Handler) listAuditJobs(c *gin.Context) {
	page, err := auditPageFromQuery(c, "page", "pageSize")
	if err != nil {
		writeAPIError(c, err)
		return
	}
	includeDeleted := false
	if raw := strings.TrimSpace(c.Query("includeDeleted")); raw != "" {
		includeDeleted, err = strconv.ParseBool(raw)
		if err != nil {
			writeAPIError(c, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "includeDeleted 必须是布尔值", err))
			return
		}
	}
	result, err := h.management.ListJobs(c.Request.Context(), AuditJobListOptions{Page: page, IncludeDeleted: includeDeleted})
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditJobPageResponse{Page: result})
}

func (h *Handler) createAuditJob(c *gin.Context) {
	var request CreateAuditJobRequest
	if err := decodeAuditManagementBody(c, &request); err != nil {
		writeAPIError(c, err)
		return
	}
	job, err := h.management.CreateJob(c.Request.Context(), request.Definition, request.InitialStatus)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusCreated, AuditJobResponse{Job: job})
}

func (h *Handler) getAuditJob(c *gin.Context) {
	job, err := h.management.GetJob(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditJobResponse{Job: job})
}

func (h *Handler) updateAuditJob(c *gin.Context) {
	var request UpdateAuditJobRequest
	if err := decodeAuditManagementBody(c, &request); err != nil {
		writeAPIError(c, err)
		return
	}
	job, err := h.management.UpdateJob(c.Request.Context(), c.Param("id"), request.ExpectedRevision, request.Definition)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditJobResponse{Job: job})
}

func (h *Handler) transitionAuditJob(c *gin.Context) {
	var request TransitionAuditJobRequest
	if err := decodeAuditManagementBody(c, &request); err != nil {
		writeAPIError(c, err)
		return
	}
	job, err := h.management.TransitionJob(c.Request.Context(), c.Param("id"), request.ExpectedRevision, request.Status)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditJobResponse{Job: job})
}

func (h *Handler) deleteAuditJob(c *gin.Context) {
	revision, err := requiredAuditRevision(c.Query("expectedRevision"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	job, err := h.management.TransitionJob(c.Request.Context(), c.Param("id"), revision, AuditJobDeleted)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditJobResponse{Job: job})
}

func (h *Handler) startManualAuditRun(c *gin.Context) {
	var request StartManualAuditRunRequest
	if err := decodeAuditManagementBody(c, &request); err != nil {
		writeAPIError(c, err)
		return
	}
	run, err := h.management.StartManualRun(c.Request.Context(), c.Param("id"), request.ExpectedRevision)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	view, err := NewAuditRunPresentation(run)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, AuditRunResponse{Run: view})
}

func (h *Handler) listAuditRuns(c *gin.Context) {
	page, err := auditPageFromQuery(c, "page", "pageSize")
	if err != nil {
		writeAPIError(c, err)
		return
	}
	options := AuditRunListOptions{
		Page: page, JobID: strings.TrimSpace(c.Query("jobId")), Status: AuditRunStatus(strings.TrimSpace(c.Query("status"))),
	}
	result, err := h.management.ListRuns(c.Request.Context(), options)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	views := make([]AuditRunPresentation, len(result.Runs))
	for index, run := range result.Runs {
		views[index], err = NewAuditRunPresentation(run)
		if err != nil {
			writeAPIError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, AuditRunPageResponse{Page: AuditRunPresentationPage{
		Runs: views, Total: result.Total, Page: result.Page, PageSize: result.PageSize,
	}})
}

func (h *Handler) getAuditRun(c *gin.Context) {
	run, err := h.management.GetRun(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	view, err := NewAuditRunPresentation(run)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditRunResponse{Run: view})
}

func (h *Handler) cancelAuditRun(c *gin.Context) {
	run, err := h.management.CancelRun(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	view, err := NewAuditRunPresentation(run)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, AuditRunResponse{Run: view})
}

func (h *Handler) getAuditChannelSummary(c *gin.Context) {
	summary, err := h.management.GetChannelSummary(
		c.Request.Context(),
		strings.TrimSpace(c.Param("id")),
		ChannelKind(strings.TrimSpace(c.Param("kind"))),
	)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditChannelSummaryResponse{Summary: summary})
}

func (h *Handler) getAuditReportDetail(c *gin.Context) {
	samples, err := auditPageFromQuery(c, "samplePage", "samplePageSize")
	if err != nil {
		writeAPIError(c, err)
		return
	}
	strategyResults, err := auditPageFromQuery(c, "strategyPage", "strategyPageSize")
	if err != nil {
		writeAPIError(c, err)
		return
	}
	result, err := h.management.GetReportDetail(c.Request.Context(), c.Param("id"), samples, strategyResults)
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditReportDetailResponse{Result: result})
}

func (h *Handler) exportAuditReportHTML(c *gin.Context) {
	exported, err := h.management.ExportReportHTML(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeAPIError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+exported.FileName+`"`)
	c.Data(http.StatusOK, "text/html; charset=utf-8", exported.Content)
}

func decodeAuditManagementBody(c *gin.Context, destination any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateExecutionBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计管理请求 JSON 无效", err)
	}
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计管理请求只能包含一个 JSON 对象")
	}
	return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计管理请求 JSON 无效", err)
}

func auditPageFromQuery(c *gin.Context, pageKey, pageSizeKey string) (AuditPageRequest, error) {
	page, err := optionalAuditInt(c.Query(pageKey), pageKey)
	if err != nil {
		return AuditPageRequest{}, err
	}
	pageSize, err := optionalAuditInt(c.Query(pageSizeKey), pageSizeKey)
	if err != nil {
		return AuditPageRequest{}, err
	}
	request := AuditPageRequest{Page: page, PageSize: pageSize}
	_, err = request.Normalize()
	if err != nil {
		return AuditPageRequest{}, err
	}
	return request, nil
}

func optionalAuditInt(raw, label string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, label+" 必须是整数", err)
	}
	return value, nil
}

func requiredAuditRevision(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "expectedRevision 不能为空")
	}
	revision, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || revision == 0 {
		return 0, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "expectedRevision 必须是正整数", err)
	}
	return revision, nil
}
