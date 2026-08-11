package modelaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
)

type CapabilitiesResponse struct {
	Protocols []ProtocolDescriptor `json:"protocols"`
}

func NewCapabilitiesResponse() CapabilitiesResponse {
	return CapabilitiesResponse{Protocols: ProtocolDescriptors()}
}

type CreateExecutionRequest struct {
	Purpose         ExecutionPurpose `json:"purpose"`
	ChannelID       string           `json:"channelId"`
	ChannelKind     ChannelKind      `json:"channelKind"`
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

func (r CreateExecutionRequest) ExecutionSpec() (ExecutionSpec, error) {
	spec := ExecutionSpec{
		Purpose:         r.Purpose,
		Target:          ChannelTarget{ChannelID: strings.TrimSpace(r.ChannelID), ChannelKind: r.ChannelKind},
		Protocol:        r.Protocol,
		Model:           strings.TrimSpace(r.Model),
		Thinking:        r.Thinking,
		RequestProfile:  strings.TrimSpace(r.RequestProfile),
		Stream:          r.Stream,
		TimeoutMillis:   r.TimeoutMillis,
		MaxOutputTokens: r.MaxOutputTokens,
		Features:        r.Features,
		Input:           r.Input,
		Conversation:    r.Conversation,
		Redaction:       r.Redaction,
	}
	if err := spec.Validate(); err != nil {
		return ExecutionSpec{}, err
	}
	if err := ValidateSpecCapabilities(spec); err != nil {
		return ExecutionSpec{}, err
	}
	return spec, nil
}

type CreateExecutionResponse struct {
	ExecutionID string          `json:"executionId"`
	Status      ExecutionStatus `json:"status"`
	Target      TargetSnapshot  `json:"target"`
}

type ExecutionDetailResponse struct {
	Result ExecutionResult `json:"result"`
}

type CancelExecutionResponse struct {
	ExecutionID string          `json:"executionId"`
	Status      ExecutionStatus `json:"status"`
}

type APIErrorResponse struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code      ErrorCode     `json:"code"`
	Category  ErrorCategory `json:"category"`
	Message   string        `json:"message"`
	Retryable bool          `json:"retryable"`
}

func NewAPIError(err error) APIErrorResponse {
	var contractErr *ContractError
	if errors.As(err, &contractErr) {
		return APIErrorResponse{Error: APIError{
			Code: contractErr.Code, Category: contractErr.Category, Message: contractErr.Message,
		}}
	}
	return APIErrorResponse{Error: APIError{
		Code: ErrorCodeInvalidRequest, Category: ErrorCategoryInternal, Message: "内部错误",
	}}
}

func NewRedactedRequestSummary(
	level RedactionLevel,
	profile string,
	prompt []byte,
	headers http.Header,
	toolCount int,
	imageCount int,
	protocolFields []string,
) (RedactedRequestSummary, error) {
	if !level.Valid() {
		return RedactedRequestSummary{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "无效的脱敏级别")
	}
	if toolCount < 0 || imageCount < 0 {
		return RedactedRequestSummary{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "工具和图片数量不能小于 0")
	}

	headerNames := make([]string, 0, len(headers))
	for name := range headers {
		headerNames = append(headerNames, http.CanonicalHeaderKey(name))
	}
	sort.Strings(headerNames)
	fields := append([]string(nil), protocolFields...)
	sort.Strings(fields)

	summary := RedactedRequestSummary{
		Profile:        strings.TrimSpace(profile),
		InputBytes:     len(prompt),
		ToolCount:      toolCount,
		ImageCount:     imageCount,
		HeaderNames:    headerNames,
		ProtocolFields: fields,
	}
	if level == RedactionDigest && len(prompt) > 0 {
		digest := sha256.Sum256(prompt)
		summary.PromptSHA256 = hex.EncodeToString(digest[:])
	}
	return summary, nil
}
