package logger

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/BenedictKing/api-proxy/internal/utils"
	"github.com/google/uuid"
)

type requestIDContextKey struct{}

func NewRequestID() string {
	return uuid.NewString()
}

func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value := ctx.Value(requestIDContextKey{})
	requestID, _ := value.(string)
	return requestID
}

func RecordTraffic(entry TrafficLog) {
	if defaultStore == nil {
		return
	}
	defaultStore.RecordTraffic(entry)
}

func RecordStreamSynth(ctx context.Context, apiType string, body string) {
	if body == "" {
		return
	}
	RecordTraffic(TrafficLog{
		RequestID: RequestIDFromContext(ctx),
		Phase:     PhaseStreamSynth,
		APIType:   apiType,
		Body:      body,
		Stream:    true,
	})
}

func MaskedHeaderJSON(header http.Header) string {
	if header == nil {
		return ""
	}
	plain := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) > 0 {
			plain[key] = values[0]
		}
	}
	masked := utils.MaskSensitiveHeaders(plain)
	encoded, err := json.Marshal(masked)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func CaptureResponseBody(inner io.ReadCloser, entry TrafficLog) io.ReadCloser {
	if inner == nil {
		return inner
	}
	return &responseCapture{
		inner:     inner,
		entry:     entry,
		remaining: MaxBodyBytes,
	}
}

type responseCapture struct {
	inner     io.ReadCloser
	entry     TrafficLog
	buf       []byte
	remaining int
	once      sync.Once
}

func (c *responseCapture) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	if n > 0 && c.remaining > 0 {
		take := n
		if take > c.remaining {
			take = c.remaining
		}
		c.buf = append(c.buf, p[:take]...)
		c.remaining -= take
		if c.remaining == 0 {
			c.entry.Truncated = true
		}
	}
	return n, err
}

func (c *responseCapture) Close() error {
	c.once.Do(func() {
		c.entry.Phase = PhaseUpstreamResponse
		if len(c.buf) > 0 {
			c.entry.Body = string(c.buf)
		}
		RecordTraffic(c.entry)
		c.buf = nil
	})
	if c.inner != nil {
		return c.inner.Close()
	}
	return nil
}
