package modelaudit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ExecutionPurpose string

const (
	PurposeQuickTest      ExecutionPurpose = "quick_test"
	PurposePlayground     ExecutionPurpose = "playground"
	PurposeIdentityProbe  ExecutionPurpose = "identity_probe"
	PurposeCapabilityEval ExecutionPurpose = "capability_eval"
)

func (p ExecutionPurpose) Valid() bool {
	switch p {
	case PurposeQuickTest, PurposePlayground, PurposeIdentityProbe, PurposeCapabilityEval:
		return true
	default:
		return false
	}
}

type ChannelKind string

const (
	ChannelKindMessages  ChannelKind = "messages"
	ChannelKindResponses ChannelKind = "responses"
	ChannelKindGemini    ChannelKind = "gemini"
	ChannelKindChat      ChannelKind = "chat"
	ChannelKindImages    ChannelKind = "images"
)

func (k ChannelKind) Valid() bool {
	switch k {
	case ChannelKindMessages, ChannelKindResponses, ChannelKindGemini, ChannelKindChat, ChannelKindImages:
		return true
	default:
		return false
	}
}

type Protocol string

const (
	ProtocolMessages  Protocol = "messages"
	ProtocolResponses Protocol = "responses"
	ProtocolGemini    Protocol = "gemini"
	ProtocolChat      Protocol = "chat"
	ProtocolImages    Protocol = "images"
)

func (p Protocol) Valid() bool {
	return ChannelKind(p).Valid()
}

func (p Protocol) ChannelKind() ChannelKind {
	return ChannelKind(p)
}

type ThinkingLevel string

const (
	ThinkingUnset    ThinkingLevel = ""
	ThinkingOff      ThinkingLevel = "off"
	ThinkingMinimal  ThinkingLevel = "minimal"
	ThinkingLow      ThinkingLevel = "low"
	ThinkingMedium   ThinkingLevel = "medium"
	ThinkingHigh     ThinkingLevel = "high"
	ThinkingXHigh    ThinkingLevel = "xhigh"
	ThinkingAdaptive ThinkingLevel = "adaptive"
)

func (l ThinkingLevel) Valid() bool {
	switch l {
	case ThinkingUnset, ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingAdaptive:
		return true
	default:
		return false
	}
}

type ExecutionStatus string

const (
	StatusPending         ExecutionStatus = "pending"
	StatusRunning         ExecutionStatus = "running"
	StatusCompleted       ExecutionStatus = "completed"
	StatusFailed          ExecutionStatus = "failed"
	StatusIncomplete      ExecutionStatus = "incomplete"
	StatusEmptyOutput     ExecutionStatus = "empty_output"
	StatusStreamTruncated ExecutionStatus = "stream_truncated"
	StatusTimeout         ExecutionStatus = "timeout"
	StatusCancelled       ExecutionStatus = "cancelled"
	StatusUnsupported     ExecutionStatus = "unsupported"
	StatusProtocolError   ExecutionStatus = "protocol_error"
)

func (s ExecutionStatus) Valid() bool {
	switch s {
	case StatusPending, StatusRunning, StatusCompleted, StatusFailed, StatusIncomplete,
		StatusEmptyOutput, StatusStreamTruncated, StatusTimeout, StatusCancelled,
		StatusUnsupported, StatusProtocolError:
		return true
	default:
		return false
	}
}

func (s ExecutionStatus) Terminal() bool {
	return s.Valid() && s != StatusPending && s != StatusRunning
}

type ProtocolTerminal string

const (
	ProtocolTerminalNone       ProtocolTerminal = ""
	ProtocolTerminalCompleted  ProtocolTerminal = "completed"
	ProtocolTerminalFailed     ProtocolTerminal = "failed"
	ProtocolTerminalIncomplete ProtocolTerminal = "incomplete"
)

type ErrorCategory string

const (
	ErrorCategoryRequest        ErrorCategory = "request"
	ErrorCategoryTarget         ErrorCategory = "target"
	ErrorCategoryUnsupported    ErrorCategory = "unsupported"
	ErrorCategoryAuthentication ErrorCategory = "authentication"
	ErrorCategoryRateLimit      ErrorCategory = "rate_limit"
	ErrorCategoryTransport      ErrorCategory = "transport"
	ErrorCategoryUpstream       ErrorCategory = "upstream"
	ErrorCategoryProtocol       ErrorCategory = "protocol"
	ErrorCategoryInternal       ErrorCategory = "internal"
)

type ErrorCode string

const (
	ErrorCodeInvalidRequest       ErrorCode = "invalid_request"
	ErrorCodeInvalidPurpose       ErrorCode = "invalid_purpose"
	ErrorCodeInvalidChannelKind   ErrorCode = "invalid_channel_kind"
	ErrorCodeInvalidProtocol      ErrorCode = "invalid_protocol"
	ErrorCodeProtocolKindMismatch ErrorCode = "protocol_channel_kind_mismatch"
	ErrorCodeChannelIDRequired    ErrorCode = "channel_id_required"
	ErrorCodeChannelNotFound      ErrorCode = "channel_not_found"
	ErrorCodeChannelIDAmbiguous   ErrorCode = "channel_id_ambiguous"
	ErrorCodeChannelIDMissing     ErrorCode = "channel_stable_id_missing"
	ErrorCodeModelRequired        ErrorCode = "model_required"
	ErrorCodeUnsupported          ErrorCode = "unsupported"
	ErrorCodeTimeout              ErrorCode = "timeout"
	ErrorCodeCancelled            ErrorCode = "cancelled"
	ErrorCodeProtocol             ErrorCode = "protocol_error"
	ErrorCodeAuthentication       ErrorCode = "authentication_failed"
	ErrorCodeRateLimit            ErrorCode = "rate_limited"
	ErrorCodeUpstream             ErrorCode = "upstream_failed"
	ErrorCodeTransport            ErrorCode = "transport_failed"
	ErrorCodeIncomplete           ErrorCode = "incomplete"
	ErrorCodeEmptyOutput          ErrorCode = "empty_output"
	ErrorCodeStreamTruncated      ErrorCode = "stream_truncated"
	ErrorCodeConcurrencyLimited   ErrorCode = "concurrency_limited"
	ErrorCodeConflict             ErrorCode = "conflict"
	ErrorCodeAuditNotFound        ErrorCode = "audit_not_found"
	ErrorCodeExecutionNotFound    ErrorCode = "execution_not_found"
	ErrorCodeExecutionNotRunning  ErrorCode = "execution_not_running"
)

type ContractError struct {
	Code     ErrorCode
	Category ErrorCategory
	Message  string
	Cause    error
}

func (e *ContractError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *ContractError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func contractError(code ErrorCode, category ErrorCategory, message string, cause ...error) *ContractError {
	var err error
	if len(cause) > 0 {
		err = cause[0]
	}
	return &ContractError{Code: code, Category: category, Message: message, Cause: err}
}

func ErrorCodeOf(err error) ErrorCode {
	var contractErr *ContractError
	if errors.As(err, &contractErr) {
		return contractErr.Code
	}
	return ""
}

type ChannelTarget struct {
	ChannelID   string      `json:"channelId"`
	ChannelKind ChannelKind `json:"channelKind"`
}

type RequestFeatures struct {
	Tools  bool `json:"tools"`
	Images bool `json:"images"`
}

type InputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ImageInput 只保存对受控上传内容的引用，不允许在合同或历史中内嵌原始图片数据。
type ImageInput struct {
	MediaType string `json:"mediaType"`
	SourceRef string `json:"sourceRef"`
}

type ExecutionInput struct {
	Prompt          string           `json:"prompt,omitempty"`
	System          string           `json:"system,omitempty"`
	Messages        []InputMessage   `json:"messages,omitempty"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	Images          []ImageInput     `json:"images,omitempty"`
	ProtocolOptions json.RawMessage  `json:"protocolOptions,omitempty"`
}

type ConversationSpec struct {
	PreviousResponseID    string `json:"previousResponseId,omitempty"`
	PreviousInteractionID string `json:"previousInteractionId,omitempty"`
	Store                 *bool  `json:"store,omitempty"`
}

type RedactionLevel string

const (
	RedactionMetadataOnly RedactionLevel = "metadata_only"
	RedactionDigest       RedactionLevel = "digest"
)

func (l RedactionLevel) Valid() bool {
	return l == RedactionMetadataOnly || l == RedactionDigest
}

type ExecutionSpec struct {
	Purpose         ExecutionPurpose `json:"purpose"`
	Target          ChannelTarget    `json:"target"`
	Protocol        Protocol         `json:"protocol"`
	Model           string           `json:"model,omitempty"`
	Thinking        ThinkingLevel    `json:"thinking,omitempty"`
	RequestProfile  string           `json:"requestProfile"`
	Stream          bool             `json:"stream"`
	TimeoutMillis   int64            `json:"timeoutMs"`
	MaxOutputTokens int              `json:"maxOutputTokens"`
	Features        RequestFeatures  `json:"features"`
	Input           ExecutionInput   `json:"input"`
	Conversation    ConversationSpec `json:"conversation,omitempty"`
	Redaction       RedactionLevel   `json:"redaction"`
}

func (s ExecutionSpec) Validate() error {
	if !s.Purpose.Valid() {
		return contractError(ErrorCodeInvalidPurpose, ErrorCategoryRequest, fmt.Sprintf("无效的运行目的 %q", s.Purpose))
	}
	if strings.TrimSpace(s.Target.ChannelID) == "" {
		return contractError(ErrorCodeChannelIDRequired, ErrorCategoryTarget, "渠道 ID 不能为空")
	}
	if !s.Target.ChannelKind.Valid() {
		return contractError(ErrorCodeInvalidChannelKind, ErrorCategoryTarget, fmt.Sprintf("无效的渠道类型 %q", s.Target.ChannelKind))
	}
	if !s.Protocol.Valid() {
		return contractError(ErrorCodeInvalidProtocol, ErrorCategoryRequest, fmt.Sprintf("无效的请求协议 %q", s.Protocol))
	}
	if s.Protocol.ChannelKind() != s.Target.ChannelKind {
		return contractError(
			ErrorCodeProtocolKindMismatch,
			ErrorCategoryRequest,
			fmt.Sprintf("请求协议 %q 不能用于 %q 渠道", s.Protocol, s.Target.ChannelKind),
		)
	}
	if !s.Thinking.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("无效的思考档位 %q", s.Thinking))
	}
	if strings.TrimSpace(s.RequestProfile) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "请求轮廓不能为空")
	}
	if s.TimeoutMillis <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "超时必须大于 0 毫秒")
	}
	if s.MaxOutputTokens <= 0 {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "最大输出 token 必须大于 0")
	}
	if !s.Redaction.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("无效的脱敏级别 %q", s.Redaction))
	}
	if len(s.Input.Tools) > 0 && !s.Features.Tools {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "输入包含工具定义，但未声明 tools 能力")
	}
	if len(s.Input.Images) > 0 && !s.Features.Images {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "输入包含图片引用，但未声明 images 能力")
	}
	for i, tool := range s.Input.Tools {
		if strings.TrimSpace(tool.Name) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个工具缺少名称", i+1))
		}
	}
	for i, image := range s.Input.Images {
		if strings.TrimSpace(image.SourceRef) == "" || strings.TrimSpace(image.MediaType) == "" {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("第 %d 个图片引用不完整", i+1))
		}
	}
	if len(s.Input.ProtocolOptions) > 0 {
		var options map[string]interface{}
		if err := json.Unmarshal(s.Input.ProtocolOptions, &options); err != nil || options == nil {
			return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "protocolOptions 必须是 JSON 对象", err)
		}
	}
	return nil
}

type TargetSnapshot struct {
	ChannelID      string           `json:"channelId"`
	ChannelKind    ChannelKind      `json:"channelKind"`
	ChannelName    string           `json:"channelName"`
	ChannelStatus  string           `json:"channelStatus"`
	ServiceType    string           `json:"serviceType"`
	Protocol       Protocol         `json:"protocol"`
	WireProtocol   Protocol         `json:"wireProtocol"`
	RequestedModel string           `json:"requestedModel,omitempty"`
	ResolvedModel  string           `json:"resolvedModel"`
	DefaultModel   string           `json:"defaultModel,omitempty"`
	Thinking       ThinkingLevel    `json:"thinking,omitempty"`
	ThinkingMap    *ThinkingMapping `json:"thinkingMapping,omitempty"`
	RequestProfile string           `json:"requestProfile"`
	CapturedAt     time.Time        `json:"capturedAt"`
}

type Usage struct {
	InputTokens     int `json:"inputTokens,omitempty"`
	OutputTokens    int `json:"outputTokens,omitempty"`
	ReasoningTokens int `json:"reasoningTokens,omitempty"`
	CachedTokens    int `json:"cachedTokens,omitempty"`
	TotalTokens     int `json:"totalTokens,omitempty"`
}

type ExecutionTiming struct {
	FirstByteMillis int64 `json:"firstByteMs,omitempty"`
	TotalMillis     int64 `json:"totalMs"`
}

type SSEEventSummary struct {
	Sequence int    `json:"sequence"`
	Type     string `json:"type"`
	Bytes    int    `json:"bytes,omitempty"`
}

type RetrySummary struct {
	Attempts int      `json:"attempts"`
	Reasons  []string `json:"reasons,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type RedactedRequestSummary struct {
	Profile        string   `json:"profile"`
	InputBytes     int      `json:"inputBytes"`
	PromptSHA256   string   `json:"promptSha256,omitempty"`
	ToolCount      int      `json:"toolCount"`
	ImageCount     int      `json:"imageCount"`
	HeaderNames    []string `json:"headerNames,omitempty"`
	ProtocolFields []string `json:"protocolFields,omitempty"`
}

type EvidenceReference struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	SHA256   string `json:"sha256"`
	Redacted bool   `json:"redacted"`
}

type ExecutionFailure struct {
	Code       ErrorCode     `json:"code"`
	Category   ErrorCategory `json:"category"`
	Message    string        `json:"message"`
	Retryable  bool          `json:"retryable"`
	StatusCode int           `json:"statusCode,omitempty"`
}

type ExecutionResult struct {
	ExecutionID      string                 `json:"executionId"`
	Purpose          ExecutionPurpose       `json:"purpose"`
	Target           TargetSnapshot         `json:"target"`
	Status           ExecutionStatus        `json:"status"`
	HTTPStatus       int                    `json:"httpStatus,omitempty"`
	ProtocolTerminal ProtocolTerminal       `json:"protocolTerminal,omitempty"`
	ResponseID       string                 `json:"responseId,omitempty"`
	DeclaredModel    string                 `json:"declaredModel,omitempty"`
	ReturnedModel    string                 `json:"returnedModel,omitempty"`
	Text             string                 `json:"text,omitempty"`
	StructuredOutput json.RawMessage        `json:"structuredOutput,omitempty"`
	ToolCalls        []ToolCall             `json:"toolCalls,omitempty"`
	Usage            Usage                  `json:"usage"`
	Timing           ExecutionTiming        `json:"timing"`
	SSEEvents        []SSEEventSummary      `json:"sseEvents,omitempty"`
	Retry            RetrySummary           `json:"retry"`
	RequestSummary   RedactedRequestSummary `json:"requestSummary"`
	Failure          *ExecutionFailure      `json:"error,omitempty"`
	Evidence         []EvidenceReference    `json:"evidence,omitempty"`
	StartedAt        time.Time              `json:"startedAt"`
	FinishedAt       *time.Time             `json:"finishedAt,omitempty"`
}

func (r ExecutionResult) Validate() error {
	if strings.TrimSpace(r.ExecutionID) == "" {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行 ID 不能为空")
	}
	if !r.Purpose.Valid() {
		return contractError(ErrorCodeInvalidPurpose, ErrorCategoryInternal, "执行结果包含无效运行目的")
	}
	if !r.Status.Valid() {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行结果包含无效状态")
	}
	if r.Status == StatusCompleted && r.ProtocolTerminal != ProtocolTerminalCompleted {
		return contractError(ErrorCodeProtocol, ErrorCategoryProtocol, "completed 状态缺少协议完成终态")
	}
	if r.Status.Terminal() && r.FinishedAt == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "终态执行结果缺少完成时间")
	}
	if r.Status.Terminal() && r.Status != StatusCompleted && r.Failure == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "非成功终态缺少错误信息")
	}
	return nil
}
