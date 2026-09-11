package proxycore

import (
	"net/http"

	"github.com/BenedictKing/api-proxy/internal/logger"
	"github.com/BenedictKing/api-proxy/internal/utils"
	"github.com/gin-gonic/gin"
)

func BindRequestLogID(c *gin.Context) string {
	if c == nil {
		return logger.NewRequestID()
	}
	if existing, ok := c.Get(utils.ContextKeyRequestLogID); ok {
		if requestID, ok := existing.(string); ok && requestID != "" {
			if c.Request != nil && logger.RequestIDFromContext(c.Request.Context()) == "" {
				c.Request = c.Request.WithContext(logger.ContextWithRequestID(c.Request.Context(), requestID))
			}
			return requestID
		}
	}
	requestID := logger.NewRequestID()
	c.Set(utils.ContextKeyRequestLogID, requestID)
	if c.Request != nil {
		c.Request = c.Request.WithContext(logger.ContextWithRequestID(c.Request.Context(), requestID))
	}
	return requestID
}

func AttachRequestLogID(c *gin.Context, req *http.Request) *http.Request {
	if req == nil {
		return req
	}
	requestID := RequestLogID(c)
	if requestID == "" {
		return req
	}
	return req.WithContext(logger.ContextWithRequestID(req.Context(), requestID))
}

func RequestLogID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if existing, ok := c.Get(utils.ContextKeyRequestLogID); ok {
		if requestID, ok := existing.(string); ok && requestID != "" {
			return requestID
		}
	}
	if c.Request != nil {
		return logger.RequestIDFromContext(c.Request.Context())
	}
	return ""
}
