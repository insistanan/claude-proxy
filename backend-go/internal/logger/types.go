package logger

import "encoding/json"

const (
	// DefaultDBPath 与 eval.db / conversations.db 一样落在 .config。
	DefaultDBPath = ".config/logs.db"

	// MaxBodyBytes 单条流量正文硬顶（sanitize 之后）。
	MaxBodyBytes = 1 << 20

	PhaseClientRequest    = "client_request"
	PhaseUpstreamRequest  = "upstream_request"
	PhaseUpstreamResponse = "upstream_response"
	PhaseStreamSynth      = "stream_synth"

	writeQueueSize = 8192
)

// AppLog 是一条运行日志（原 app.log 的 sqlite 形态）。
type AppLog struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	UnixMilli int64  `json:"unixMilli"`
	Tag       string `json:"tag,omitempty"`
	Message   string `json:"message"`
}

// TrafficLog 是一次请求或响应的结构化正文。
type TrafficLog struct {
	ID            int64  `json:"id,omitempty"`
	Timestamp     string `json:"timestamp"`
	UnixMilli     int64  `json:"unixMilli,omitempty"`
	RequestID     string `json:"requestId"`
	AttemptID     string `json:"attemptId,omitempty"`
	Phase         string `json:"phase"`
	APIType       string `json:"apiType,omitempty"`
	Method        string `json:"method,omitempty"`
	URL           string `json:"url,omitempty"`
	StatusCode    int    `json:"statusCode,omitempty"`
	Stream        bool   `json:"stream,omitempty"`
	HeadersJSON   string `json:"headers,omitempty"`
	Body          string `json:"body,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`
	OriginalBytes int    `json:"originalBytes,omitempty"`
}

// RequestLogRecord 是 Web 请求日志页的元数据行（payload 为完整 JSON）。
type RequestLogRecord struct {
	UnixMilli int64
	Timestamp string
	RequestID string
	APIType   string
	Payload   []byte
}

// QueryOptions 描述时段查询。Limit=0 表示不截断。
type QueryOptions struct {
	From      int64
	To        int64
	RequestID string
	APIType   string
	Limit     int
}

// QueryResult 是 CLI `logs query` 的 stdout JSON。
type QueryResult struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	AppLogs     []AppLog          `json:"appLogs"`
	TrafficLogs []TrafficLog      `json:"trafficLogs"`
	RequestLogs []json.RawMessage `json:"requestLogs"`
}

// ShowResult 是 CLI `logs show` 的 stdout JSON。
type ShowResult struct {
	RequestID   string            `json:"requestId"`
	RequestLogs []json.RawMessage `json:"requestLogs"`
	TrafficLogs []TrafficLog      `json:"trafficLogs"`
}
