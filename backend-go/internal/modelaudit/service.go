package modelaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ServiceConfig struct {
	MaxConcurrent   int
	MaxRetained     int
	MaxInputBytes   int
	MaxTools        int
	MaxImages       int
	MaxTimeout      time.Duration
	MaxOutputTokens int
}

func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		MaxConcurrent:   4,
		MaxRetained:     200,
		MaxInputBytes:   256 * 1024,
		MaxTools:        64,
		MaxImages:       8,
		MaxTimeout:      10 * time.Minute,
		MaxOutputTokens: 65_536,
	}
}

func (c ServiceConfig) validate() error {
	if c.MaxConcurrent <= 0 || c.MaxRetained <= 0 || c.MaxInputBytes <= 0 || c.MaxTools <= 0 || c.MaxImages <= 0 {
		return fmt.Errorf("执行服务限制必须全部大于 0")
	}
	if c.MaxTimeout <= 0 || c.MaxOutputTokens <= 0 {
		return fmt.Errorf("执行超时和最大输出限制必须大于 0")
	}
	return nil
}

type executionRecord struct {
	mu     sync.RWMutex
	result ExecutionResult
	cancel context.CancelFunc
}

func (r *executionRecord) snapshot() ExecutionResult {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneExecutionResult(r.result)
}

func (r *executionRecord) replace(result ExecutionResult) {
	r.mu.Lock()
	r.result = cloneExecutionResult(result)
	r.mu.Unlock()
}

type Service struct {
	resolver       TargetResolver
	clients        HTTPClientFactory
	config         ServiceConfig
	semaphore      chan struct{}
	mu             sync.RWMutex
	records        map[string]*executionRecord
	order          []string
	closed         bool
	now            func() time.Time
	newExecutionID func() string
}

func NewService(resolver TargetResolver, clients HTTPClientFactory, serviceConfig ServiceConfig) (*Service, error) {
	if resolver == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "目标解析器不能为空")
	}
	if clients == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "HTTP 客户端工厂不能为空")
	}
	if err := serviceConfig.validate(); err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, err.Error(), err)
	}
	return &Service{
		resolver:       resolver,
		clients:        clients,
		config:         serviceConfig,
		semaphore:      make(chan struct{}, serviceConfig.MaxConcurrent),
		records:        make(map[string]*executionRecord),
		now:            time.Now,
		newExecutionID: uuid.NewString,
	}, nil
}

func (s *Service) ResolveTarget(ctx context.Context, spec ExecutionSpec) (ResolvedTarget, error) {
	if s == nil || s.resolver == nil {
		return ResolvedTarget{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行服务未初始化")
	}
	if err := s.validateSpecLimits(spec); err != nil {
		return ResolvedTarget{}, err
	}
	return s.resolver.Resolve(ctx, spec)
}

func (s *Service) ExecuteResolved(ctx context.Context, spec ExecutionSpec, target ResolvedTarget) (ExecutionResult, error) {
	if s == nil || s.clients == nil || s.now == nil || s.newExecutionID == nil {
		return ExecutionResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行服务未初始化")
	}
	if err := s.validateSpecLimits(spec); err != nil {
		return ExecutionResult{}, err
	}
	if target.Snapshot.ChannelID != spec.Target.ChannelID {
		return ExecutionResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, fmt.Sprintf("冻结目标与执行规格不一致：channelId 期望 %q，实际 %q", target.Snapshot.ChannelID, spec.Target.ChannelID))
	}
	if target.Snapshot.ChannelKind != spec.Target.ChannelKind {
		return ExecutionResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, fmt.Sprintf("冻结目标与执行规格不一致：channelKind 期望 %q，实际 %q", target.Snapshot.ChannelKind, spec.Target.ChannelKind))
	}
	if target.Snapshot.Protocol != spec.Protocol {
		return ExecutionResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, fmt.Sprintf("冻结目标与执行规格不一致：protocol 期望 %q，实际 %q", target.Snapshot.Protocol, spec.Protocol))
	}
	if target.Snapshot.RequestedModel != spec.Model {
		return ExecutionResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, fmt.Sprintf("冻结目标与执行规格不一致：model 期望 %q，实际 %q", target.Snapshot.RequestedModel, spec.Model))
	}
	if target.Snapshot.RequestProfile != spec.RequestProfile {
		return ExecutionResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, fmt.Sprintf("冻结目标与执行规格不一致：requestProfile 期望 %q，实际 %q", target.Snapshot.RequestProfile, spec.RequestProfile))
	}
	// thinking 是每条审计样本的执行变量（身份策略会主动切换档位），不能被目标快照固定。
	executionTarget := target
	executionTarget.Snapshot.Thinking = spec.Thinking
	thinkingMapping, err := ResolveThinking(executionTarget.Snapshot.WireProtocol, spec.Thinking)
	if err != nil {
		return ExecutionResult{}, err
	}
	executionTarget.Snapshot.ThinkingMap = thinkingMapping
	request, err := NewDirectExecutionRequest(spec, executionTarget)
	if err != nil {
		return ExecutionResult{}, err
	}
	request.ExecutionID = s.newExecutionID()
	result, executeErr := s.ExecuteDirect(ctx, request)
	if executeErr != nil {
		result = resultFromExecutionError(result, executeErr)
	}
	finishedAt := s.now().UTC()
	result.FinishedAt = &finishedAt
	if result.Timing.TotalMillis == 0 {
		result.Timing.TotalMillis = finishedAt.Sub(result.StartedAt).Milliseconds()
	}
	if err := result.Validate(); err != nil {
		return ExecutionResult{}, contractError(ErrorCodeProtocol, ErrorCategoryProtocol, "同步执行结果无效", err)
	}
	return result, nil
}

func (s *Service) Start(ctx context.Context, spec ExecutionSpec) (CreateExecutionResponse, error) {
	if s == nil {
		return CreateExecutionResponse{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行服务未初始化")
	}
	if err := s.validateSpecLimits(spec); err != nil {
		return CreateExecutionResponse{}, err
	}
	target, err := s.resolver.Resolve(ctx, spec)
	if err != nil {
		return CreateExecutionResponse{}, err
	}
	directRequest, err := NewDirectExecutionRequest(spec, target)
	if err != nil {
		return CreateExecutionResponse{}, err
	}
	select {
	case s.semaphore <- struct{}{}:
	default:
		return CreateExecutionResponse{}, contractError(
			ErrorCodeConcurrencyLimited,
			ErrorCategoryRequest,
			fmt.Sprintf("并发执行数已达到上限 %d", s.config.MaxConcurrent),
		)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.semaphore
		return CreateExecutionResponse{}, contractError(ErrorCodeCancelled, ErrorCategoryInternal, "执行服务已关闭")
	}
	executionID := s.newExecutionID()
	directRequest.ExecutionID = executionID
	startedAt := s.now().UTC()
	executionContext, cancel := context.WithTimeout(context.Background(), time.Duration(spec.TimeoutMillis)*time.Millisecond)
	record := &executionRecord{
		cancel: cancel,
		result: ExecutionResult{
			ExecutionID:   executionID,
			Purpose:       spec.Purpose,
			Target:        target.Snapshot,
			Status:        StatusPending,
			DeclaredModel: target.Snapshot.ResolvedModel,
			StartedAt:     startedAt,
		},
	}
	s.records[executionID] = record
	s.order = append(s.order, executionID)
	s.pruneLocked()
	s.mu.Unlock()

	go s.run(executionContext, record, directRequest)
	return CreateExecutionResponse{ExecutionID: executionID, Status: StatusPending, Target: target.Snapshot}, nil
}

func (s *Service) run(ctx context.Context, record *executionRecord, request DirectExecutionRequest) {
	defer func() { <-s.semaphore }()
	defer record.cancel()

	result := record.snapshot()
	result.Status = StatusRunning
	record.replace(result)

	executed, err := s.ExecuteDirect(ctx, request)
	if err != nil {
		executed = resultFromExecutionError(result, err)
	}
	finishedAt := s.now().UTC()
	executed.FinishedAt = &finishedAt
	if executed.Timing.TotalMillis == 0 {
		executed.Timing.TotalMillis = finishedAt.Sub(executed.StartedAt).Milliseconds()
	}
	if validationErr := executed.Validate(); validationErr != nil {
		executed.Status = StatusProtocolError
		executed.ProtocolTerminal = ProtocolTerminalNone
		executed.Failure = &ExecutionFailure{
			Code: ErrorCodeProtocol, Category: ErrorCategoryProtocol, Message: validationErr.Error(),
		}
	}
	record.replace(executed)
}

func (s *Service) ExecuteDirect(ctx context.Context, request DirectExecutionRequest) (ExecutionResult, error) {
	if err := request.Policy.ValidateIsolation(); err != nil {
		return ExecutionResult{}, err
	}
	if strings.TrimSpace(request.ExecutionID) == "" {
		return ExecutionResult{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行 ID 不能为空")
	}
	startedAt := s.now().UTC()
	result := ExecutionResult{
		ExecutionID:   request.ExecutionID,
		Purpose:       request.Spec.Purpose,
		Target:        request.Target.Snapshot,
		Status:        StatusRunning,
		DeclaredModel: request.Target.Snapshot.ResolvedModel,
		StartedAt:     startedAt,
		Retry:         RetrySummary{Attempts: 1},
	}
	prepared, err := prepareProtocolRequest(ctx, request, request.ExecutionID)
	if err != nil {
		return result, err
	}
	result.RequestSummary = prepared.Summary
	client, err := s.clients.ClientFor(ctx, request.Target, request.Spec)
	if err != nil {
		return result, err
	}
	response, err := client.Do(prepared.Request)
	if err != nil {
		return resultFromTransportError(result, ctx, err), nil
	}
	defer response.Body.Close()
	result.Timing.FirstByteMillis = s.now().UTC().Sub(startedAt).Milliseconds()
	if err := parseProtocolResponse(response, request.Target.Snapshot, request.Spec.Stream, &result); err != nil {
		result.Status = StatusProtocolError
		result.ProtocolTerminal = ProtocolTerminalNone
		result.Failure = &ExecutionFailure{
			Code: ErrorCodeProtocol, Category: ErrorCategoryProtocol, Message: err.Error(),
		}
	}
	result.Timing.TotalMillis = s.now().UTC().Sub(startedAt).Milliseconds()
	return result, nil
}

func (s *Service) Get(executionID string) (ExecutionDetailResponse, bool) {
	if s == nil {
		return ExecutionDetailResponse{}, false
	}
	s.mu.RLock()
	record, ok := s.records[strings.TrimSpace(executionID)]
	s.mu.RUnlock()
	if !ok {
		return ExecutionDetailResponse{}, false
	}
	return ExecutionDetailResponse{Result: record.snapshot()}, true
}

func (s *Service) Cancel(_ context.Context, executionID string) error {
	if s == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "执行服务未初始化")
	}
	executionID = strings.TrimSpace(executionID)
	s.mu.RLock()
	record, ok := s.records[executionID]
	s.mu.RUnlock()
	if !ok {
		return contractError(ErrorCodeExecutionNotFound, ErrorCategoryRequest, fmt.Sprintf("执行 %q 不存在", executionID))
	}
	result := record.snapshot()
	if result.Status.Terminal() {
		return contractError(ErrorCodeExecutionNotRunning, ErrorCategoryRequest, fmt.Sprintf("执行 %q 已结束", executionID))
	}
	record.cancel()
	return nil
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	records := make([]*executionRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	s.mu.Unlock()
	for _, record := range records {
		record.cancel()
	}
}

func (s *Service) validateSpecLimits(spec ExecutionSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if err := ValidateSpecCapabilities(spec); err != nil {
		return err
	}
	inputBytes := len(spec.Input.Prompt) + len(spec.Input.System) + len(spec.Input.ProtocolOptions)
	for _, message := range spec.Input.Messages {
		inputBytes += len(message.Role) + len(message.Content)
	}
	for _, tool := range spec.Input.Tools {
		inputBytes += len(tool.Name) + len(tool.Description) + len(tool.InputSchema)
	}
	if inputBytes > s.config.MaxInputBytes {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("输入超过 %d 字节限制", s.config.MaxInputBytes))
	}
	if len(spec.Input.Tools) > s.config.MaxTools {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("工具数量超过 %d 个限制", s.config.MaxTools))
	}
	if len(spec.Input.Images) > s.config.MaxImages {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("图片数量超过 %d 个限制", s.config.MaxImages))
	}
	if time.Duration(spec.TimeoutMillis)*time.Millisecond > s.config.MaxTimeout {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("超时超过 %s 限制", s.config.MaxTimeout))
	}
	if spec.MaxOutputTokens > s.config.MaxOutputTokens {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, fmt.Sprintf("最大输出 token 超过 %d 限制", s.config.MaxOutputTokens))
	}
	return nil
}

func (s *Service) pruneLocked() {
	if len(s.order) <= s.config.MaxRetained {
		return
	}
	retained := make([]string, 0, len(s.order))
	for _, executionID := range s.order {
		record := s.records[executionID]
		if len(s.records) > s.config.MaxRetained && record != nil && record.snapshot().Status.Terminal() {
			delete(s.records, executionID)
			continue
		}
		retained = append(retained, executionID)
	}
	s.order = retained
}

func resultFromExecutionError(base ExecutionResult, err error) ExecutionResult {
	if errors.Is(err, context.DeadlineExceeded) {
		base.Status = StatusTimeout
		base.Failure = &ExecutionFailure{Code: ErrorCodeTimeout, Category: ErrorCategoryTransport, Message: "执行超时", Retryable: true}
		return base
	}
	if errors.Is(err, context.Canceled) {
		base.Status = StatusCancelled
		base.Failure = &ExecutionFailure{Code: ErrorCodeCancelled, Category: ErrorCategoryRequest, Message: "执行已取消"}
		return base
	}
	var contractErr *ContractError
	if errors.As(err, &contractErr) {
		base.Status = StatusFailed
		if contractErr.Category == ErrorCategoryUnsupported {
			base.Status = StatusUnsupported
		}
		base.Failure = &ExecutionFailure{Code: contractErr.Code, Category: contractErr.Category, Message: contractErr.Message}
		return base
	}
	base.Status = StatusFailed
	base.Failure = &ExecutionFailure{Code: ErrorCodeTransport, Category: ErrorCategoryInternal, Message: err.Error()}
	return base
}

func resultFromTransportError(base ExecutionResult, ctx context.Context, err error) ExecutionResult {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || isTimeoutError(err) {
		base.Status = StatusTimeout
		base.Failure = &ExecutionFailure{Code: ErrorCodeTimeout, Category: ErrorCategoryTransport, Message: "上游请求超时", Retryable: true}
		return base
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		base.Status = StatusCancelled
		base.Failure = &ExecutionFailure{Code: ErrorCodeCancelled, Category: ErrorCategoryRequest, Message: "执行已取消"}
		return base
	}
	base.Status = StatusFailed
	base.Failure = &ExecutionFailure{Code: ErrorCodeTransport, Category: ErrorCategoryTransport, Message: err.Error(), Retryable: true}
	return base
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func cloneExecutionResult(result ExecutionResult) ExecutionResult {
	cloned := result
	cloned.StructuredOutput = append(json.RawMessage(nil), result.StructuredOutput...)
	cloned.ToolCalls = append([]ToolCall(nil), result.ToolCalls...)
	for i := range cloned.ToolCalls {
		cloned.ToolCalls[i].Arguments = append(json.RawMessage(nil), result.ToolCalls[i].Arguments...)
	}
	cloned.SSEEvents = append([]SSEEventSummary(nil), result.SSEEvents...)
	cloned.Retry.Reasons = append([]string(nil), result.Retry.Reasons...)
	cloned.RequestSummary.HeaderNames = append([]string(nil), result.RequestSummary.HeaderNames...)
	cloned.RequestSummary.ProtocolFields = append([]string(nil), result.RequestSummary.ProtocolFields...)
	cloned.Evidence = append([]EvidenceReference(nil), result.Evidence...)
	if result.Failure != nil {
		failure := *result.Failure
		cloned.Failure = &failure
	}
	if result.FinishedAt != nil {
		finishedAt := *result.FinishedAt
		cloned.FinishedAt = &finishedAt
	}
	return cloned
}
