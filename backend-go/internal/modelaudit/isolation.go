package modelaudit

import (
	"context"
	"fmt"
	"strings"
)

type RoutingMode string

const RoutingDirect RoutingMode = "direct"

type ProductionMutation string

const (
	MutationHealthMetrics  ProductionMutation = "health_metrics"
	MutationCircuitBreaker ProductionMutation = "circuit_breaker"
	MutationPromotion      ProductionMutation = "promotion"
	MutationAffinity       ProductionMutation = "affinity"
	MutationKeyPriority    ProductionMutation = "key_priority"
	MutationLoadBalancing  ProductionMutation = "load_balancing"
	MutationDisableKey     ProductionMutation = "disable_key"
)

var allProductionMutations = []ProductionMutation{
	MutationHealthMetrics,
	MutationCircuitBreaker,
	MutationPromotion,
	MutationAffinity,
	MutationKeyPriority,
	MutationLoadBalancing,
	MutationDisableKey,
}

type EffectPolicy struct {
	Purpose            ExecutionPurpose            `json:"purpose"`
	Routing            RoutingMode                 `json:"routing"`
	PersistAuditResult bool                        `json:"persistAuditResult"`
	Production         map[ProductionMutation]bool `json:"production"`
}

func PolicyForPurpose(purpose ExecutionPurpose) (EffectPolicy, error) {
	if !purpose.Valid() {
		return EffectPolicy{}, contractError(ErrorCodeInvalidPurpose, ErrorCategoryRequest, fmt.Sprintf("无效的运行目的 %q", purpose))
	}
	production := make(map[ProductionMutation]bool, len(allProductionMutations))
	for _, mutation := range allProductionMutations {
		production[mutation] = false
	}
	return EffectPolicy{
		Purpose:            purpose,
		Routing:            RoutingDirect,
		PersistAuditResult: purpose == PurposeIdentityProbe || purpose == PurposeCapabilityEval,
		Production:         production,
	}, nil
}

func (p EffectPolicy) ValidateIsolation() error {
	if !p.Purpose.Valid() {
		return contractError(ErrorCodeInvalidPurpose, ErrorCategoryInternal, "副作用策略包含无效运行目的")
	}
	if p.Routing != RoutingDirect {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "模型审计执行只能直达指定渠道")
	}
	for _, mutation := range allProductionMutations {
		if p.Production[mutation] {
			return contractError(
				ErrorCodeInvalidRequest,
				ErrorCategoryInternal,
				fmt.Sprintf("模型审计执行禁止生产副作用 %q", mutation),
			)
		}
	}
	return nil
}

type DirectExecutionRequest struct {
	ExecutionID string         `json:"executionId"`
	Spec        ExecutionSpec  `json:"spec"`
	Target      ResolvedTarget `json:"target"`
	Policy      EffectPolicy   `json:"policy"`
}

func NewDirectExecutionRequest(spec ExecutionSpec, target ResolvedTarget) (DirectExecutionRequest, error) {
	policy, err := PolicyForPurpose(spec.Purpose)
	if err != nil {
		return DirectExecutionRequest{}, err
	}
	if err := policy.ValidateIsolation(); err != nil {
		return DirectExecutionRequest{}, err
	}
	if target.Snapshot.ChannelID != strings.TrimSpace(spec.Target.ChannelID) || target.Snapshot.ChannelKind != spec.Target.ChannelKind {
		return DirectExecutionRequest{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryTarget, "目标快照与执行规格不一致")
	}
	return DirectExecutionRequest{Spec: spec, Target: target, Policy: policy}, nil
}

// DirectExecutor 是快速测试、演武台和审计 Runner 的唯一执行边界。
// 实现必须直达 Request.Target，不能依赖生产 ChannelScheduler 重新选路。
type DirectExecutor interface {
	ExecuteDirect(context.Context, DirectExecutionRequest) (ExecutionResult, error)
	Cancel(context.Context, string) error
}

// AuditResultSink 只持久化审计域结果；接口故意不暴露生产指标、熔断或调度写入能力。
type AuditResultSink interface {
	SaveAuditResult(context.Context, ExecutionResult) error
}

func PersistResultIfRequired(ctx context.Context, policy EffectPolicy, sink AuditResultSink, result ExecutionResult) error {
	if err := policy.ValidateIsolation(); err != nil {
		return err
	}
	if !policy.PersistAuditResult {
		return nil
	}
	if sink == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计结果存储不能为空")
	}
	if err := sink.SaveAuditResult(ctx, result); err != nil {
		return fmt.Errorf("保存审计结果失败: %w", err)
	}
	return nil
}
