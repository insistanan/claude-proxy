package modelaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/utils"
)

type AuditRunControllerConfig struct {
	MaximumActiveRuns int
	LeaseDuration     time.Duration
	ModAnalysis       AuditModAnalysisRunnerConfig
}

const (
	auditSampleTimeoutMillis                    int64 = 120_000
	capabilitySystemFailureConsecutiveThreshold       = 2
)

func DefaultAuditRunControllerConfig() AuditRunControllerConfig {
	return AuditRunControllerConfig{
		MaximumActiveRuns: 2,
		LeaseDuration:     10 * time.Minute,
		ModAnalysis:       DefaultAuditModAnalysisRunnerConfig(),
	}
}

func (c AuditRunControllerConfig) validate() error {
	if err := (AuditRunStartLimits{MaximumActiveRuns: c.MaximumActiveRuns, LeaseDurationMs: c.LeaseDuration.Milliseconds()}).Validate(); err != nil {
		return err
	}
	return c.ModAnalysis.validate()
}

type activeAuditRun struct {
	cancel context.CancelFunc
	done   chan struct{}

	leaseMu     sync.RWMutex
	lease       AuditRunLease
	leaseLost   error
	renewalStop chan struct{}
	renewalDone chan struct{}
	stopRenewal sync.Once
}

func newActiveAuditRun(cancel context.CancelFunc, lease AuditRunLease) *activeAuditRun {
	return &activeAuditRun{
		cancel: cancel, done: make(chan struct{}), lease: lease,
		renewalStop: make(chan struct{}), renewalDone: make(chan struct{}),
	}
}

func (r *activeAuditRun) currentLease() AuditRunLease {
	r.leaseMu.RLock()
	defer r.leaseMu.RUnlock()
	return r.lease
}

func (r *activeAuditRun) replaceLease(current, renewed AuditRunLease) bool {
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	if r.lease != current || r.leaseLost != nil {
		return false
	}
	r.lease = renewed
	return true
}

func (r *activeAuditRun) markLeaseLost(err error) {
	r.leaseMu.Lock()
	if r.leaseLost == nil {
		r.leaseLost = err
	}
	r.leaseMu.Unlock()
	r.cancel()
}

func (r *activeAuditRun) leaseLoss() error {
	r.leaseMu.RLock()
	defer r.leaseMu.RUnlock()
	return r.leaseLost
}

func (r *activeAuditRun) stopLeaseRenewal() {
	r.stopRenewal.Do(func() { close(r.renewalStop) })
	<-r.renewalDone
}

type BuiltinAuditRunController struct {
	store       *AuditSQLiteStore
	service     *Service
	strategies  *StrategyRegistry
	capability  *BuiltinCapabilityAssets
	modAnalysis *AuditModAnalysisRunner
	config      AuditRunControllerConfig
	ownerID     string
	now         func() time.Time
	newID       func(string) (string, error)

	mu     sync.Mutex
	active map[string]*activeAuditRun
}

func NewBuiltinAuditRunController(
	store *AuditSQLiteStore,
	service *Service,
	strategies *StrategyRegistry,
	config AuditRunControllerConfig,
) (*BuiltinAuditRunController, error) {
	capability, err := NewBuiltinCapabilityAssets()
	if err != nil {
		return nil, err
	}
	return NewAuditRunControllerWithCapabilityAssets(store, service, strategies, capability, config)
}

func NewAuditRunControllerWithCapabilityAssets(
	store *AuditSQLiteStore,
	service *Service,
	strategies *StrategyRegistry,
	capability *BuiltinCapabilityAssets,
	config AuditRunControllerConfig,
) (*BuiltinAuditRunController, error) {
	if store == nil || service == nil || strategies == nil || capability == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计运行控制器依赖未初始化")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	ownerID, err := NewAuditEntityID("audit-instance")
	if err != nil {
		return nil, err
	}
	modAnalysis, err := NewAuditModAnalysisRunner(store, service, config.ModAnalysis)
	if err != nil {
		return nil, err
	}
	return &BuiltinAuditRunController{
		store: store, service: service, strategies: strategies, capability: capability, modAnalysis: modAnalysis, config: config, ownerID: ownerID,
		now: time.Now, newID: NewAuditEntityID, active: make(map[string]*activeAuditRun),
	}, nil
}

func (c *BuiltinAuditRunController) StartManual(ctx context.Context, job AuditJob) (AuditRun, error) {
	return c.start(ctx, job, AuditRunManual, nil)
}

func (c *BuiltinAuditRunController) StartScheduled(ctx context.Context, job AuditJob, slot AuditScheduleSlot) (AuditRun, error) {
	return c.start(ctx, job, AuditRunScheduled, &slot)
}

func (c *BuiltinAuditRunController) ResumePending(ctx context.Context, entry AuditRecoveryEntry, now time.Time) (AuditRun, error) {
	if err := c.validate(); err != nil {
		return AuditRun{}, err
	}
	if err := ctx.Err(); err != nil {
		return AuditRun{}, contractError(ErrorCodeCancelled, ErrorCategoryRequest, "审计运行恢复请求已取消", err)
	}
	decision, err := PlanAuditRecovery(entry, now)
	if err != nil {
		return AuditRun{}, err
	}
	if decision.Action != AuditRecoveryResumePending {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行当前不能按 pending 恢复")
	}
	if err := c.validateJob(entry.Job); err != nil {
		return AuditRun{}, err
	}
	c.mu.Lock()
	_, alreadyActive := c.active[entry.Run.ID]
	c.mu.Unlock()
	if alreadyActive {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行已由当前进程恢复")
	}
	leaseToken, err := c.newID("audit-lease")
	if err != nil {
		return AuditRun{}, err
	}
	lease := AuditRunLease{
		JobID: entry.Job.ID, RunID: entry.Run.ID, OwnerID: c.ownerID, Token: leaseToken,
		AcquiredAt: now.UTC(), ExpiresAt: now.Add(c.config.LeaseDuration).UTC(),
	}
	if err := c.store.ClaimRecoveryLease(ctx, lease, now); err != nil {
		return AuditRun{}, err
	}
	runContext, cancel := context.WithCancel(context.Background())
	active := newActiveAuditRun(cancel, lease)
	c.mu.Lock()
	c.active[entry.Run.ID] = active
	c.mu.Unlock()
	start := AuditRunStart{Job: entry.Job, Run: entry.Run, Lease: lease}
	go c.execute(runContext, start, active)
	return entry.Run, nil
}

func (c *BuiltinAuditRunController) start(
	ctx context.Context,
	job AuditJob,
	trigger AuditRunTrigger,
	slot *AuditScheduleSlot,
) (AuditRun, error) {
	if err := c.validate(); err != nil {
		return AuditRun{}, err
	}
	if err := ctx.Err(); err != nil {
		return AuditRun{}, contractError(ErrorCodeCancelled, ErrorCategoryRequest, "审计运行启动请求已取消", err)
	}
	if err := c.validateJob(job); err != nil {
		return AuditRun{}, err
	}
	now := c.now().UTC()
	runID, err := c.newID("audit-run")
	if err != nil {
		return AuditRun{}, err
	}
	leaseToken, err := c.newID("audit-lease")
	if err != nil {
		return AuditRun{}, err
	}
	existing, found, err := c.store.GetLease(ctx, job.ID)
	if err != nil {
		return AuditRun{}, err
	}
	var existingPointer *AuditRunLease
	if found {
		existingPointer = &existing
	}
	limits := AuditRunStartLimits{MaximumActiveRuns: c.config.MaximumActiveRuns, LeaseDurationMs: c.config.LeaseDuration.Milliseconds()}
	start, err := PrepareAuditRunStart(AuditRunStartInput{
		Job: job, ExpectedRevision: job.Revision, RunID: runID, Trigger: trigger, ScheduledSlot: slot,
		OwnerID: c.ownerID, LeaseToken: leaseToken, Now: now, Limits: limits, ExistingLease: existingPointer,
	})
	if err != nil {
		return AuditRun{}, err
	}
	if err := c.store.CommitRunStart(ctx, start, limits.MaximumActiveRuns); err != nil {
		return AuditRun{}, err
	}
	runContext, cancel := context.WithCancel(context.Background())
	active := newActiveAuditRun(cancel, start.Lease)
	c.mu.Lock()
	c.active[start.Run.ID] = active
	c.mu.Unlock()
	go c.execute(runContext, start, active)
	return start.Run, nil
}

func (c *BuiltinAuditRunController) Shutdown(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return err
	}
	c.mu.Lock()
	active := make([]*activeAuditRun, 0, len(c.active))
	for _, run := range c.active {
		active = append(active, run)
		run.cancel()
	}
	c.mu.Unlock()
	for _, run := range active {
		select {
		case <-run.done:
		case <-ctx.Done():
			return contractError(ErrorCodeCancelled, ErrorCategoryRequest, "等待审计运行关闭时请求已结束", ctx.Err())
		}
	}
	return nil
}

func (c *BuiltinAuditRunController) CancelRun(ctx context.Context, run AuditRun) (AuditRun, error) {
	if err := c.validate(); err != nil {
		return AuditRun{}, err
	}
	c.mu.Lock()
	active := c.active[run.ID]
	c.mu.Unlock()
	if active == nil {
		return AuditRun{}, contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计运行不属于当前进程，不能直接取消")
	}
	active.cancel()
	select {
	case <-active.done:
	case <-ctx.Done():
		// 请求上下文已结束，但 execute goroutine 可能仍在运行。
		// 不阻塞返回，改用后台 goroutine 等待其完成后再清理，避免状态不一致。
		go func() {
			<-active.done
		}()
	}
	// 无论上面哪个分支，都尝试从 DB 读取最新状态返回。
	// 如果 execute 尚未完成写入，返回的 run 可能仍是 running——调用方可在后续轮询中看到终态。
	updated, found, err := c.store.GetRun(context.Background(), run.ID)
	if err != nil {
		return AuditRun{}, err
	}
	if !found {
		return AuditRun{}, auditNotFound("运行", run.ID)
	}
	return updated, nil
}

func (c *BuiltinAuditRunController) ImportManualModAnalysis(
	ctx context.Context,
	analysisID string,
	result json.RawMessage,
) (AuditModAnalysisRecord, error) {
	if err := c.validate(); err != nil {
		return AuditModAnalysisRecord{}, err
	}
	return c.modAnalysis.ImportManualResult(ctx, strings.TrimSpace(analysisID), result)
}

func (c *BuiltinAuditRunController) RerunModAnalysis(
	ctx context.Context,
	job AuditJob,
	run AuditRun,
	targetID string,
	bundle AuditModBundle,
	input json.RawMessage,
) (AuditModAnalysisRecord, error) {
	if err := c.validate(); err != nil {
		return AuditModAnalysisRecord{}, err
	}
	return c.modAnalysis.Rerun(ctx, job, run, strings.TrimSpace(targetID), bundle, input)
}

func (c *BuiltinAuditRunController) validate() error {
	if c == nil || c.store == nil || c.service == nil || c.strategies == nil || c.capability == nil || c.modAnalysis == nil || c.now == nil || c.newID == nil ||
		c.ownerID == "" || c.active == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计运行控制器未初始化")
	}
	return c.config.validate()
}

func (c *BuiltinAuditRunController) validateWorkload(workload AuditWorkload) error {
	if err := workload.Validate(); err != nil {
		return err
	}
	switch workload.Kind {
	case AuditWorkloadIdentity:
		_, err := c.strategies.Freeze(workload.Strategies)
		return err
	case AuditWorkloadCapability:
		_, _, found, err := c.capability.ResolvePreset(*workload.CapabilityPreset)
		if err != nil {
			return err
		}
		if !found {
			return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "审计任务引用的能力预设不可用")
		}
		return nil
	default:
		return contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "审计工作负载不受支持")
	}
}

func (c *BuiltinAuditRunController) ValidateWorkload(workload AuditWorkload) error {
	if err := c.validate(); err != nil {
		return err
	}
	return c.validateWorkload(workload)
}

func (c *BuiltinAuditRunController) PrepareJobDefinition(definition AuditJobDefinition) (AuditJobDefinition, error) {
	if err := c.validate(); err != nil {
		return AuditJobDefinition{}, err
	}
	canonical, err := CanonicalizeAuditWorkload(definition.Workload)
	if err != nil {
		return AuditJobDefinition{}, err
	}
	definition.Workload = canonical
	if canonical.Kind != AuditWorkloadIdentity {
		if err := c.validateWorkload(canonical); err != nil {
			return AuditJobDefinition{}, err
		}
		return definition, nil
	}
	snapshots, err := c.strategies.Freeze(canonical.Strategies)
	if err != nil {
		return AuditJobDefinition{}, err
	}
	needsAnalyzer := false
	for index := range definition.Workload.Strategies {
		ref := snapshots[index].Ref
		definition.Workload.Strategies[index].Ref = &ref
		if !snapshots[index].Enabled {
			continue
		}
		strategy, found := c.strategies.GetVersion(ref)
		if !found {
			return AuditJobDefinition{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "冻结身份策略实现不存在")
		}
		modStrategy, ok := strategy.(*DeclarativeAuditModStrategy)
		if ok && modStrategy.bundle.Manifest.Analysis.Mode == AuditModAnalysisLLM {
			needsAnalyzer = true
		}
	}
	if needsAnalyzer && definition.Analyzer == nil {
		return AuditJobDefinition{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "所选 LLM 分析 Mod 需要为任务选择分析模型")
	}
	if err := definition.Validate(); err != nil {
		return AuditJobDefinition{}, err
	}
	return definition, nil
}

func (c *BuiltinAuditRunController) validateJob(job AuditJob) error {
	if err := job.Validate(); err != nil {
		return err
	}
	definition := AuditJobDefinition{
		Name: job.Name, Workload: job.Workload, Targets: job.Targets, Schedule: job.Schedule,
		Budget: job.Budget, Analyzer: job.Analyzer,
	}
	_, err := c.PrepareJobDefinition(definition)
	return err
}

func (c *BuiltinAuditRunController) StrategyCatalog() []StrategyDescriptor {
	if c == nil || c.strategies == nil {
		return nil
	}
	return c.strategies.Descriptors()
}

func (c *BuiltinAuditRunController) CapabilityCatalog() AuditCapabilityAssetCatalog {
	if c == nil || c.capability == nil {
		return AuditCapabilityAssetCatalog{Available: false, Reason: "能力资产未初始化", Packages: []CapabilityTaskPackageSnapshot{}, Presets: []CapabilityRunPreset{}}
	}
	catalog := c.capability.Catalog()
	catalog.QuestionBanks = c.capability.QuestionBankCatalog()
	return catalog
}

func (c *BuiltinAuditRunController) ReloadQuestionBanks() (QuestionBankCatalogSnapshot, error) {
	if c == nil || c.capability == nil {
		return QuestionBankCatalogSnapshot{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "能力资产未初始化")
	}
	return c.capability.ReloadQuestionBanks()
}

type auditTargetWorkloadResult struct {
	identity   *IdentityReport
	capability *CapabilityReport
	evidence   []EvidenceReference
	samples    int
}

func (c *BuiltinAuditRunController) execute(ctx context.Context, start AuditRunStart, active *activeAuditRun) {
	go c.renewLease(ctx, start.Run.ID, active)
	defer func() {
		if r := recover(); r != nil {
			c.reportBackgroundError(start.Run.ID, fmt.Errorf("审计运行 execute panic: %v", r))
			// 尽力将 run 标记为失败终态，避免永远停留在 running
			c.ensureTerminalRun(start, active, AuditRunStopFailure, "审计运行因内部异常而中止")
		}
		active.cancel()
		active.stopLeaseRenewal()
		c.mu.Lock()
		delete(c.active, start.Run.ID)
		c.mu.Unlock()
		close(active.done)
	}()

	run := start.Run
	startedAt := c.now().UTC()
	targets, resolved := c.resolveTargets(ctx, start.Job)
	if ctx.Err() != nil {
		c.finishRun(start, active, run, nil, AuditRunStopCancelled, "")
		return
	}
	activated, err := ActivateAuditRun(run, start.Job, targets, startedAt)
	if err != nil {
		c.finishRun(start, active, run, nil, AuditRunStopCancelled, "")
		c.reportBackgroundError(run.ID, err)
		return
	}
	if err := c.store.SaveRun(context.Background(), activated, AuditRunPending, active.currentLease(), startedAt); err != nil {
		c.reportBackgroundError(run.ID, err)
		// SaveRun 失败时仍尝试写入终态，避免 run 永远停留在 pending
		c.ensureTerminalRun(start, active, AuditRunStopFailure, "审计运行激活后持久化失败")
		return
	}
	run = activated
	results := make(map[string]auditTargetWorkloadResult, len(targets))
	stopReason := AuditRunStopNone
	failure := ""
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			stopReason = AuditRunStopCancelled
			break
		}
		resolvedTarget, ok := resolved[target.TargetID]
		if !ok {
			stopReason = AuditRunStopFailure
			failure = "至少一个审计目标无法解析"
			break
		}
		var result auditTargetWorkloadResult
		var updatedRun AuditRun
		var reason AuditRunStopReason
		var err error
		switch start.Job.Workload.Kind {
		case AuditWorkloadIdentity:
			result, updatedRun, reason, err = c.executeIdentityTarget(ctx, run, active, target, resolvedTarget, start.Job)
		case AuditWorkloadCapability:
			result, updatedRun, reason, err = c.executeCapabilityTarget(ctx, run, active, target, resolvedTarget, start.Job.Workload)
		default:
			err = contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "审计工作负载不受支持")
			updatedRun = run
		}
		run = updatedRun
		results[target.TargetID] = result
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				stopReason = AuditRunStopCancelled
			} else {
				stopReason = AuditRunStopFailure
				workloadName := "身份审计"
				if start.Job.Workload.Kind == AuditWorkloadCapability {
					workloadName = "能力评测"
				}
				failure = workloadName + "执行失败：" + NewAPIError(err).Error.Message
				c.reportBackgroundError(run.ID, err)
			}
			break
		}
		if reason != AuditRunStopNone {
			stopReason = reason
			break
		}
	}
	c.finishRun(start, active, run, results, stopReason, failure)
}

func (c *BuiltinAuditRunController) renewLease(ctx context.Context, runID string, active *activeAuditRun) {
	defer close(active.renewalDone)
	defer func() {
		if r := recover(); r != nil {
			c.reportBackgroundError(runID, fmt.Errorf("审计运行租约续期 panic: %v", r))
			active.markLeaseLost(fmt.Errorf("租约续期 panic: %v", r))
		}
	}()
	interval := c.config.LeaseDuration / 3
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-active.renewalStop:
			return
		case <-timer.C:
			current := active.currentLease()
			now := c.now().UTC()
			renewed, err := RenewAuditRunLease(current, current.OwnerID, current.Token, now, c.config.LeaseDuration.Milliseconds())
			if err == nil {
				err = c.store.RenewLease(context.Background(), current, renewed)
			}
			if err != nil {
				active.markLeaseLost(err)
				c.reportBackgroundError(runID, fmt.Errorf("审计运行租约续期失败: %w", err))
				return
			}
			if !active.replaceLease(current, renewed) {
				err = contractError(ErrorCodeConflict, ErrorCategoryInternal, "审计运行租约在续期后发生并发变化")
				active.markLeaseLost(err)
				c.reportBackgroundError(runID, err)
				return
			}
			timer.Reset(interval)
		}
	}
}

func (c *BuiltinAuditRunController) resolveTargets(ctx context.Context, job AuditJob) ([]AuditRunTargetSnapshot, map[string]ResolvedTarget) {
	targets := make([]AuditRunTargetSnapshot, len(job.Targets))
	resolved := make(map[string]ResolvedTarget, len(job.Targets))
	for index, requested := range job.Targets {
		spec := auditTargetResolutionSpec(requested, job.Workload.Kind)
		target, err := c.service.ResolveTarget(ctx, spec)
		if err != nil {
			targets[index] = AuditRunTargetSnapshot{TargetID: requested.ID, Requested: requested, ResolutionError: NewAPIError(err).Error.Message}
			continue
		}
		snapshot := target.Snapshot
		targets[index] = AuditRunTargetSnapshot{TargetID: requested.ID, Requested: requested, Resolved: &snapshot}
		resolved[requested.ID] = target
	}
	return targets, resolved
}

func auditTargetResolutionSpec(target AuditJobTarget, workload AuditWorkloadKind) ExecutionSpec {
	purpose := PurposeIdentityProbe
	if workload == AuditWorkloadCapability {
		purpose = PurposeCapabilityEval
	}
	return ExecutionSpec{
		Purpose: purpose, Target: ChannelTarget{ChannelID: target.ChannelID, ChannelKind: target.ChannelKind},
		Protocol: target.Protocol, Model: target.Model, Thinking: target.Thinking, RequestProfile: target.RequestProfile,
		Stream: target.Protocol != ProtocolImages, TimeoutMillis: auditSampleTimeoutMillis, MaxOutputTokens: 128,
		Input: ExecutionInput{Prompt: "审计目标解析"}, Redaction: RedactionDigest,
	}
}

func (c *BuiltinAuditRunController) executeIdentityTarget(
	ctx context.Context,
	run AuditRun,
	active *activeAuditRun,
	target AuditRunTargetSnapshot,
	resolved ResolvedTarget,
	job AuditJob,
) (auditTargetWorkloadResult, AuditRun, AuditRunStopReason, error) {
	workload := job.Workload
	snapshots, err := c.strategies.Freeze(workload.Strategies)
	if err != nil {
		return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
	}
	allSamples := make([]StrategySample, 0)
	evaluations := make([]StrategyEvaluation, 0)
	signals := make([]IdentitySignal, 0)
	evidence := make([]EvidenceReference, 0)
	returnedModels := make([]string, 0)
	for _, snapshot := range snapshots {
		if !snapshot.Enabled {
			continue
		}
		strategy, found := c.strategies.GetVersion(snapshot.Ref)
		if !found {
			return auditTargetWorkloadResult{}, run, AuditRunStopNone, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "冻结身份策略实现不存在")
		}
		plans, err := strategy.Plan(ctx, StrategyPlanInput{
			RunID: run.ID, Target: *target.Resolved, Config: snapshot,
			BaseSeed: auditStrategySeed(run.ID, target.TargetID, snapshot.Ref.ID), MaximumSamples: strategy.Descriptor().MaximumSamples,
		})
		if err != nil {
			if ErrorCodeOf(err) == ErrorCodeUnsupported {
				evaluation := StrategyEvaluation{
					Strategy: snapshot.Ref, Status: EvaluationUnsupported, Reason: err.Error(),
				}
				evaluations = append(evaluations, evaluation)
				continue
			}
			return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
		}
		strategySamples := make([]StrategySample, 0, len(plans))
		strategyEvidence := make([]EvidenceReference, 0, len(plans))
		strategyExecutionFailures := make([]string, 0)
		for _, plan := range plans {
			if err := ctx.Err(); err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopCancelled, err
			}
			if err := waitForAuditSampleDelay(ctx, plan.DelayBeforeMs); err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopCancelled, err
			}
			decision, err := CheckAuditRunBudget(run, AuditRequestBudget{
				MaximumInputTokens:  int64(maxInt(1, utils.EstimateTokens(plan.Execution.Input.Prompt))),
				MaximumOutputTokens: int64(plan.Execution.MaxOutputTokens),
			})
			if err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			if !decision.Allowed {
				return auditTargetWorkloadResult{evidence: evidence, samples: len(allSamples)}, run, AuditRunStopBudget, nil
			}
			execution, err := c.service.ExecuteResolved(ctx, plan.Execution, resolved)
			if err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			sample := StrategySample{
				SampleID: plan.SampleID, RunID: run.ID, Strategy: plan.Strategy, ConfigSHA256: snapshot.ConfigSHA256,
				Ordinal: plan.Ordinal, Seed: plan.Seed, Applicability: ApplicabilityApplicable, Execution: &execution, CapturedAt: c.now().UTC(),
			}
			if err := sample.Validate(); err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			usage := AuditRunUsage{
				Requests: 1, InputTokens: int64(execution.Usage.InputTokens), OutputTokens: int64(execution.Usage.OutputTokens),
				TotalTokens: int64(execution.Usage.InputTokens + execution.Usage.OutputTokens),
			}
			updated, err := RecordAuditRunUsage(run, usage)
			if err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			if err := c.store.SaveRun(context.Background(), updated, AuditRunRunning, active.currentLease(), c.now().UTC()); err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			run = updated
			reference, err := c.saveRunPayload(run.ID, target.TargetID, AuditStoredSample,
				VersionedRef{ID: "audit.strategy-sample", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
				plan.SampleID, sample, sample.CapturedAt)
			if err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			strategySamples = append(strategySamples, sample)
			allSamples = append(allSamples, sample)
			strategyEvidence = append(strategyEvidence, reference)
			evidence = append(evidence, reference)
			if execution.ReturnedModel != "" {
				returnedModels = append(returnedModels, execution.ReturnedModel)
			}
			if execution.Status != StatusCompleted {
				strategyExecutionFailures = append(strategyExecutionFailures, executionFailureSummary(execution))
			}
		}
		evaluation, err := strategy.Evaluate(ctx, StrategyEvaluationInput{Config: snapshot, Samples: strategySamples})
		if err != nil {
			return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
		}
		evaluation.Evidence = append(evaluation.Evidence, strategyEvidence...)
		if len(strategyExecutionFailures) > 0 {
			failureReason := fmt.Sprintf("%d 个身份样本执行失败：%s", len(strategyExecutionFailures), strings.Join(uniqueSortedStrings(strategyExecutionFailures), "；"))
			if evaluation.Reason == "" {
				evaluation.Reason = failureReason
			} else {
				evaluation.Reason += "；" + failureReason
			}
			evaluation.AlternativeExplanations = append(evaluation.AlternativeExplanations, failureReason)
		}
		signal, err := identitySignalFromEvaluation(evaluation)
		if err != nil {
			return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
		}
		if len(strategyExecutionFailures) > 0 {
			failureReason := fmt.Sprintf("%d 个身份样本执行失败：%s", len(strategyExecutionFailures), strings.Join(uniqueSortedStrings(strategyExecutionFailures), "；"))
			if signal.Reason == "" {
				signal.Reason = failureReason
			} else {
				signal.Reason += "；" + failureReason
			}
		}
		signal.Evidence = append(signal.Evidence, strategyEvidence...)
		metrics, err := encodeIdentityEvaluationMetrics(signal)
		if err != nil {
			return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
		}
		evaluation.Metrics = metrics
		evaluationReference, err := c.saveRunPayload(run.ID, target.TargetID, AuditStoredStrategyResult,
			VersionedRef{ID: "audit.strategy-evaluation", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
			run.ID+"."+snapshot.Ref.ID+".evaluation", evaluation, c.now().UTC())
		if err != nil {
			return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
		}
		evaluation.Evidence = append(evaluation.Evidence, evaluationReference)
		evaluations = append(evaluations, evaluation)
		signals = append(signals, signal)
		evidence = append(evidence, evaluationReference)
		if modStrategy, ok := strategy.(*DeclarativeAuditModStrategy); ok {
			var features AuditModEvaluationFeatures
			if err := json.Unmarshal(signal.Features, &features); err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone,
					contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码 Mod 分析输入特征失败", err)
			}
			_, analysisUsage, analysisErr := c.modAnalysis.Run(
				ctx, job, run, target.TargetID, *target.Resolved, modStrategy.Bundle(), strategySamples, features,
			)
			if errors.Is(analysisErr, ErrAuditModAnalysisBudgetExceeded) {
				return auditTargetWorkloadResult{evidence: evidence, samples: len(allSamples)}, run, AuditRunStopBudget, nil
			}
			if analysisErr != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, analysisErr
			}
			if analysisUsage.Requests > 0 {
				updated, err := RecordAuditRunUsage(run, analysisUsage)
				if err != nil {
					return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
				}
				if err := c.store.SaveRun(context.Background(), updated, AuditRunRunning, active.currentLease(), c.now().UTC()); err != nil {
					return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
				}
				run = updated
			}
		}
	}
	identity, err := aggregateBuiltinIdentity(workload, *target.Resolved, allSamples, signals, returnedModels, c.strategies.Descriptors())
	if err != nil {
		return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
	}
	return auditTargetWorkloadResult{identity: &identity, evidence: evidence, samples: len(allSamples)}, run, AuditRunStopNone, nil
}

type capabilityRunSample struct {
	Instance  CapabilityTaskInstance `json:"instance"`
	Execution ExecutionResult        `json:"execution"`
}

func (c *BuiltinAuditRunController) executeCapabilityTarget(
	ctx context.Context,
	run AuditRun,
	active *activeAuditRun,
	target AuditRunTargetSnapshot,
	resolved ResolvedTarget,
	workload AuditWorkload,
) (auditTargetWorkloadResult, AuditRun, AuditRunStopReason, error) {
	_, plan, found, err := c.capability.ResolvePreset(*workload.CapabilityPreset)
	if err != nil {
		return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
	}
	if !found {
		return auditTargetWorkloadResult{}, run, AuditRunStopNone, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力预设不可用")
	}
	results := make([]CapabilityTaskResult, 0, plan.Estimate.Requests)
	evidence := make([]EvidenceReference, 0, plan.Estimate.Requests*2)
	executedSamples := 0
	stoppedByBudget := false
	consecutiveFailureKey := ""
	consecutiveFailureCount := 0
	var systematicFailure *ExecutionFailure
	for _, coverage := range plan.Tasks {
		task, found := c.capability.Task(coverage.Task)
		if !found || task.Ref != coverage.Task {
			return auditTargetWorkloadResult{}, run, AuditRunStopNone, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力运行计划引用的任务不可用")
		}
		for repetition := 0; repetition < coverage.PlannedInstances; repetition++ {
			if err := ctx.Err(); err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopCancelled, err
			}
			seed := auditStrategySeed(run.ID, target.TargetID, task.Ref.ID, fmt.Sprintf("%d", repetition))
			instance, err := c.capability.GenerateInstance(run.ID, *target.Resolved, task, repetition, seed, c.now().UTC())
			if err != nil {
				if ErrorCodeOf(err) == ErrorCodeUnsupported {
					results = append(results, CapabilityTaskResult{
						InstanceID: run.ID + "." + target.TargetID + "." + task.Ref.ID + "." + fmt.Sprintf("%d", repetition),
						Task:       task.Ref, Dimension: task.Dimension, Status: CapabilityTaskUnsupported,
						Reason: err.Error(), CapturedAt: c.now().UTC(),
					})
					continue
				}
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			// 内置能力包的超时在 task 定义处统一为 30s，
			// 但实际运行时上游响应可能较慢，这里对内置包做最小超时保障，
			// 与原逻辑保持一致，避免破坏任务包哈希。
			if instance.Package.ID == BuiltinCapabilityPackageID && instance.Execution.TimeoutMillis < auditSampleTimeoutMillis {
				instance.Execution.TimeoutMillis = auditSampleTimeoutMillis
			}
			decision, err := CheckAuditRunBudget(run, AuditRequestBudget{
				MaximumInputTokens: int64(task.MaximumInputTokens), MaximumOutputTokens: int64(task.MaximumOutputTokens),
			})
			if err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			if !decision.Allowed {
				stoppedByBudget = true
				break
			}
			execution, err := c.service.ExecuteResolved(ctx, instance.Execution, resolved)
			if err != nil {
				if ErrorCodeOf(err) == ErrorCodeUnsupported {
					results = append(results, CapabilityTaskResult{
						InstanceID: instance.InstanceID, Task: task.Ref, Dimension: task.Dimension,
						Status: CapabilityTaskUnsupported, Reason: err.Error(), CapturedAt: c.now().UTC(),
					})
					continue
				}
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			usage := AuditRunUsage{
				Requests: 1, InputTokens: int64(execution.Usage.InputTokens), OutputTokens: int64(execution.Usage.OutputTokens),
				TotalTokens: int64(execution.Usage.InputTokens + execution.Usage.OutputTokens),
			}
			updated, err := RecordAuditRunUsage(run, usage)
			if err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			if err := c.store.SaveRun(context.Background(), updated, AuditRunRunning, active.currentLease(), c.now().UTC()); err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			run = updated
			executedSamples++
			sampleReference, err := c.saveRunPayload(run.ID, target.TargetID, AuditStoredSample,
				VersionedRef{ID: "audit.capability-sample", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
				instance.InstanceID, capabilityRunSample{Instance: instance, Execution: execution}, c.now().UTC())
			if err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			evidence = append(evidence, sampleReference)
			result := CapabilityTaskResult{
				InstanceID: instance.InstanceID, Task: task.Ref, Dimension: task.Dimension,
				ExecutionID: execution.ExecutionID, Usage: execution.Usage, Timing: execution.Timing,
				Evidence: []EvidenceReference{sampleReference}, CapturedAt: c.now().UTC(),
			}
			if execution.Status == StatusCompleted {
				consecutiveFailureKey = ""
				consecutiveFailureCount = 0
				score, err := c.capability.Score(ctx, task, instance, execution, result.Evidence)
				if err != nil {
					result.Status = CapabilityTaskScoringFailed
					result.Reason = "客观判分器执行失败"
					result.ErrorClass = "scorer_error"
				} else {
					result.Status = CapabilityTaskScored
					result.Score = &score.Score
					scorer := task.Scorer
					result.Scorer = &scorer
					result.Assertions = score.Assertions
					result.ErrorClass = score.ErrorClass
				}
			} else {
				result.Status = CapabilityTaskExecutionFailed
				result.Reason = executionFailureSummary(execution)
				if execution.Failure != nil {
					result.ErrorClass = string(execution.Failure.Code)
					if key, infrastructure := capabilityInfrastructureFailureKey(execution); infrastructure {
						if key == consecutiveFailureKey {
							consecutiveFailureCount++
						} else {
							consecutiveFailureKey = key
							consecutiveFailureCount = 1
						}
						if consecutiveFailureCount >= capabilitySystemFailureConsecutiveThreshold {
							failure := *execution.Failure
							systematicFailure = &failure
						}
					} else {
						consecutiveFailureKey = ""
						consecutiveFailureCount = 0
					}
				}
			}
			if err := result.Validate(); err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			resultReference, err := c.saveRunPayload(run.ID, target.TargetID, AuditStoredStrategyResult,
				VersionedRef{ID: "audit.capability-result", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
				instance.InstanceID+".result", result, result.CapturedAt)
			if err != nil {
				return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
			}
			results = append(results, result)
			evidence = append(evidence, resultReference)
			if systematicFailure != nil {
				break
			}
		}
		if stoppedByBudget || systematicFailure != nil {
			break
		}
	}
	stopReason := CapabilityRunStopNone
	runStopReason := AuditRunStopNone
	if stoppedByBudget {
		stopReason = CapabilityRunStoppedByBudget
		runStopReason = AuditRunStopBudget
	}
	packageSnapshot, found := c.capability.PackageForTask(plan.Tasks[0].Task)
	if !found || packageSnapshot.Package.Ref != plan.Package {
		return auditTargetWorkloadResult{}, run, AuditRunStopNone, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "能力题库冻结快照不可用")
	}
	report, err := AggregateCapability(CapabilityAggregationConfig{
		Aggregator:                         VersionedRef{ID: "capability.aggregate.weighted", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		MinimumScoredInstancesPerDimension: 1, MinimumDimensionsForIndex: 1, MinimumFormalDimensions: 7,
		FormalCoverageThreshold: 1, BootstrapSamples: 400, ConfidenceLevel: 0.95,
		BootstrapSeed:                     int64(auditStrategySeed(run.ID, target.TargetID, plan.Preset.ID) & uint64(^uint64(0)>>1)),
		MinimumScoredInstancesForInterval: 2,
	}, CapabilityAggregationInput{
		Package: packageSnapshot, Plan: plan.Tasks, Results: results, StopReason: stopReason,
	})
	if err != nil {
		return auditTargetWorkloadResult{}, run, AuditRunStopNone, err
	}
	if systematicFailure != nil {
		message := fmt.Sprintf("连续 %d 个能力样本发生同类基础设施错误，已停止后续评测：%s", consecutiveFailureCount, executionFailureMessage(*systematicFailure, 0))
		return auditTargetWorkloadResult{capability: &report, evidence: evidence, samples: executedSamples}, run, AuditRunStopFailure,
			contractError(systematicFailure.Code, systematicFailure.Category, message)
	}
	return auditTargetWorkloadResult{capability: &report, evidence: evidence, samples: executedSamples}, run, runStopReason, nil
}

func capabilityInfrastructureFailureKey(execution ExecutionResult) (string, bool) {
	if execution.Failure == nil {
		return "", false
	}
	failure := execution.Failure
	if failure.Category != ErrorCategoryUpstream && failure.Category != ErrorCategoryTransport {
		return "", false
	}
	return fmt.Sprintf("%s|%s|%d", failure.Category, failure.Code, firstNonZero(execution.HTTPStatus, failure.StatusCode)), true
}

func executionFailureSummary(execution ExecutionResult) string {
	if execution.Failure == nil {
		return fmt.Sprintf("执行以 %s 状态结束", execution.Status)
	}
	return executionFailureMessage(*execution.Failure, execution.HTTPStatus)
}

func executionFailureMessage(failure ExecutionFailure, httpStatus int) string {
	status := firstNonZero(httpStatus, failure.StatusCode)
	if status > 0 {
		statusText := http.StatusText(status)
		if statusText == "" {
			return fmt.Sprintf("上游请求失败：HTTP %d", status)
		}
		return fmt.Sprintf("上游请求失败：HTTP %d %s", status, statusText)
	}
	switch failure.Code {
	case ErrorCodeTimeout:
		return "上游请求超时"
	case ErrorCodeTransport:
		return "上游连接失败"
	case ErrorCodeUpstream:
		return "上游服务执行失败"
	}
	message := strings.TrimSpace(failure.Message)
	if message != "" {
		return message
	}
	return string(failure.Code)
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func (c *BuiltinAuditRunController) saveRunPayload(
	runID string,
	targetID string,
	kind AuditStoredPayloadKind,
	schema VersionedRef,
	id string,
	value any,
	createdAt time.Time,
) (EvidenceReference, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return EvidenceReference{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码审计运行载荷失败", err)
	}
	payload, err := NewAuditStoredPayload(id, runID, targetID, kind, schema, encoded, createdAt)
	if err != nil {
		return EvidenceReference{}, err
	}
	if err := c.store.SavePayload(context.Background(), payload); err != nil {
		return EvidenceReference{}, err
	}
	return EvidenceReference{ID: payload.ID, Kind: string(payload.Kind), SHA256: payload.SHA256, Redacted: true}, nil
}

func (c *BuiltinAuditRunController) finishRun(
	start AuditRunStart,
	active *activeAuditRun,
	run AuditRun,
	results map[string]auditTargetWorkloadResult,
	reason AuditRunStopReason,
	failure string,
) {
	active.stopLeaseRenewal()
	lease := active.currentLease()
	if active.leaseLoss() != nil && run.Status == AuditRunRunning {
		reason = AuditRunStopInterrupted
		failure = "审计运行因租约失效而中断"
	}
	finishedAt := c.now().UTC()
	var terminal AuditRun
	var err error
	switch reason {
	case AuditRunStopNone:
		terminal, err = CompleteAuditRun(run, finishedAt)
	case AuditRunStopBudget, AuditRunStopCancelled:
		terminal, err = StopAuditRun(run, reason, "", finishedAt)
	case AuditRunStopFailure:
		terminal, err = StopAuditRun(run, reason, failure, finishedAt)
	case AuditRunStopInterrupted:
		if run.Usage != (AuditRunUsage{}) {
			failure = ""
		}
		terminal, err = StopAuditRun(run, reason, failure, finishedAt)
	default:
		err = contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计运行终止原因无效")
	}
	if err != nil {
		c.reportBackgroundError(run.ID, err)
		return
	}
	coordination, err := PrepareAuditRunFinish(start.Job, start.Job.Revision, terminal, lease, finishedAt)
	if err != nil {
		c.reportBackgroundError(run.ID, err)
		return
	}
	if err := c.store.CommitRunFinish(context.Background(), coordination, terminal, lease, start.Job.Revision); err != nil {
		c.reportBackgroundError(run.ID, err)
		// 终态提交失败时尝试强制兜底，避免 run 永远停留在 running
		c.ensureTerminalRun(start, active, reason, failure)
		return
	}
	for _, target := range terminal.Targets {
		result := results[target.TargetID]
		report, err := c.targetReport(terminal, target, result, finishedAt)
		if err != nil {
			c.reportBackgroundError(run.ID, err)
			continue
		}
		if _, err := c.store.SaveTargetReport(context.Background(), report); err != nil {
			c.reportBackgroundError(run.ID, err)
		}
	}
}

// ensureTerminalRun 是终态兜底：当 finishRun 的正常终态提交失败时，
// 尝试用宽松条件将 run 强制标记为终态，避免 run 永远停留在 running/pending。
// 此方法尽力而为，失败仅记录日志，不返回错误。
func (c *BuiltinAuditRunController) ensureTerminalRun(
	start AuditRunStart,
	active *activeAuditRun,
	reason AuditRunStopReason,
	failure string,
) {
	run := start.Run
	finishedAt := c.now().UTC()

	// 根据 run 当前状态选择合法的停止原因：
	// - pending 状态只能用 cancelled
	// - running 状态有用量用 interrupted（可恢复），否则用 failure
	stopReason := reason
	if stopReason == AuditRunStopNone {
		stopReason = AuditRunStopFailure
	}
	if run.Status == AuditRunPending {
		stopReason = AuditRunStopCancelled
		failure = ""
	} else if run.Status == AuditRunRunning {
		if stopReason == AuditRunStopFailure && run.Usage != (AuditRunUsage{}) {
			stopReason = AuditRunStopInterrupted
			failure = ""
		}
	}

	terminal, err := StopAuditRun(run, stopReason, failure, finishedAt)
	if err != nil {
		c.reportBackgroundError(run.ID, fmt.Errorf("终态兜底 StopAuditRun 失败: %w", err))
		return
	}
	coordination, err := PrepareAuditRunFinish(start.Job, start.Job.Revision, terminal, active.currentLease(), finishedAt)
	if err != nil {
		c.reportBackgroundError(run.ID, fmt.Errorf("终态兜底 PrepareAuditRunFinish 失败: %w", err))
		return
	}
	if err := c.store.CommitRunFinish(context.Background(), coordination, terminal, active.currentLease(), start.Job.Revision); err != nil {
		c.reportBackgroundError(run.ID, fmt.Errorf("终态兜底 CommitRunFinish 失败: %w", err))
	}
}

func (c *BuiltinAuditRunController) targetReport(
	run AuditRun,
	target AuditRunTargetSnapshot,
	result auditTargetWorkloadResult,
	createdAt time.Time,
) (AuditTargetReport, error) {
	createdAt = auditMillisecondCeiling(createdAt)
	reportID, err := c.newID("audit-report")
	if err != nil {
		return AuditTargetReport{}, err
	}
	resultStatus := AuditReportFailed
	switch run.Status {
	case AuditRunCompleted:
		resultStatus = AuditReportInsufficientEvidence
		if (result.identity != nil && result.identity.Formal) || (result.capability != nil && result.capability.Formal) {
			resultStatus = AuditReportComplete
		}
	case AuditRunPartial, AuditRunCancelled:
		resultStatus = AuditReportPartial
	case AuditRunFailed:
		resultStatus = AuditReportFailed
	}
	report := AuditTargetReport{
		Schema: VersionedRef{ID: AuditTargetReportSchemaID, SemanticVersion: AuditTargetReportSchemaVersion, ImplementationVersion: "builtin-1"},
		ID:     reportID, RunID: run.ID, JobID: run.JobID, JobRevision: run.JobRevision, JobSnapshotSHA256: run.JobSnapshotSHA256,
		Target: target, ResultStatus: resultStatus, RunStatus: run.Status, StopReason: run.StopReason, Usage: run.Usage,
		SampleCount: result.samples, Identity: result.identity, Capability: result.capability, Evidence: append([]EvidenceReference(nil), result.evidence...),
		FreshnessPolicy: AuditFreshnessPolicy{
			Ref:      VersionedRef{ID: "freshness.identity.builtin", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
			MaxAgeMs: (7 * 24 * time.Hour).Milliseconds(),
		},
		CreatedAt: createdAt,
	}
	if err := report.Validate(); err != nil {
		return AuditTargetReport{}, err
	}
	return report, nil
}

func auditMillisecondCeiling(value time.Time) time.Time {
	normalized := time.UnixMilli(value.UTC().UnixMilli()).UTC()
	if normalized.Before(value) {
		normalized = normalized.Add(time.Millisecond)
	}
	return normalized
}

func identitySignalFromEvaluation(evaluation StrategyEvaluation) (IdentitySignal, error) {
	var metrics struct {
		Signal IdentitySignal `json:"signal"`
	}
	if err := json.Unmarshal(evaluation.Metrics, &metrics); err != nil {
		return IdentitySignal{}, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "解码身份策略信号失败", err)
	}
	if err := metrics.Signal.Validate(); err != nil {
		return IdentitySignal{}, err
	}
	return metrics.Signal, nil
}

func encodeIdentityEvaluationMetrics(signal IdentitySignal) (json.RawMessage, error) {
	encoded, err := json.Marshal(struct {
		Signal IdentitySignal `json:"signal"`
	}{Signal: signal})
	if err != nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "编码身份策略信号失败", err)
	}
	canonical, _, err := canonicalJSONObject(encoded)
	return canonical, err
}

func aggregateBuiltinIdentity(
	workload AuditWorkload,
	target TargetSnapshot,
	samples []StrategySample,
	signals []IdentitySignal,
	returnedModels []string,
	descriptors []StrategyDescriptor,
) (IdentityReport, error) {
	descriptorByID := make(map[string]StrategyDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		descriptorByID[descriptor.Ref.ID] = descriptor
	}
	rules := make([]IdentitySignalRule, 0, len(signals))
	groups := make(map[string]float64)
	for _, signal := range signals {
		descriptor, found := descriptorByID[signal.ID]
		if !found {
			return IdentityReport{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, fmt.Sprintf("身份信号 %q 缺少生产策略描述", signal.ID))
		}
		rules = append(rules, IdentitySignalRule{
			SignalID: signal.ID, MinimumSamples: maxInt(1, signal.SampleCount), FormalEligible: descriptor.FormalEligible,
			Strategy: descriptor.Ref, Baseline: descriptor.Baseline,
		})
		groups[signal.CorrelationGroup] = 1
	}
	if len(rules) == 0 {
		return IdentityReport{}, contractError(ErrorCodeUnsupported, ErrorCategoryUnsupported, "没有身份策略产生可聚合信号")
	}
	candidateFit, mixture, temporal, err := returnedModelAssessments(target, samples)
	if err != nil {
		return IdentityReport{}, err
	}
	report, err := AggregateIdentity(IdentityAggregationConfig{
		Aggregator:        VersionedRef{ID: "identity.aggregate.builtin", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		DeclaredCandidate: target.ResolvedModel, MinimumSamples: 1, MinimumApplicableSignals: 1,
		MinimumFormalSignals: 1, MinimumCorrelationGroups: 1, MatchedThreshold: 0.8, SubstitutionThreshold: 0.35,
		MixtureComponentLowerBound: 0.1, GroupWeights: groups, SignalRules: rules,
	}, IdentityAggregationInput{
		DeclaredModel: target.ResolvedModel, RequestedModel: target.RequestedModel, ReturnedModels: returnedModels,
		Protocol: target.Protocol, Thinking: target.Thinking, SampleCount: len(samples), Signals: signals,
		CandidateFit: candidateFit, Mixture: mixture, Temporal: temporal,
	})
	if err != nil {
		return IdentityReport{}, err
	}
	if !report.Qualification.Eligible && !returnedModelEvidenceSupportsSuspicion(target, candidateFit, mixture, temporal) {
		report.Conclusion = IdentityInsufficientEvidence
		report.Formal = false
		report.Confidence = 0
		report.AlternativeExplanations = uniqueSortedStrings(append(report.AlternativeExplanations, "当前信号均不具备正式身份结论资格"))
		if err := report.Validate(); err != nil {
			return IdentityReport{}, err
		}
	}
	return report, nil
}

func returnedModelEvidenceSupportsSuspicion(
	target TargetSnapshot,
	classification *CandidateClassification,
	mixture *MixtureEstimate,
	temporal *TemporalAssessment,
) bool {
	declared := normalizeModelName(firstNonEmpty(target.ResolvedModel, target.RequestedModel))
	if classification != nil && (classification.OOD ||
		classification.BestCandidate != "" && normalizeModelName(classification.BestCandidate) != declared) {
		return true
	}
	if mixtureHasMultipleComponents(mixture, 0.1) {
		return true
	}
	if temporal != nil {
		return temporal.Pattern == TemporalStageSwitch || temporal.Pattern == TemporalIntermittent || temporal.Pattern == TemporalStableShifted
	}
	return false
}

func returnedModelAssessments(
	target TargetSnapshot,
	samples []StrategySample,
) (*CandidateClassification, *MixtureEstimate, *TemporalAssessment, error) {
	declared := normalizeModelName(firstNonEmpty(target.ResolvedModel, target.RequestedModel))
	if declared == "" {
		return nil, nil, nil, nil
	}
	ordered := append([]StrategySample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].CapturedAt.Equal(ordered[j].CapturedAt) {
			return ordered[i].SampleID < ordered[j].SampleID
		}
		return ordered[i].CapturedAt.Before(ordered[j].CapturedAt)
	})
	type observation struct {
		sample StrategySample
		model  string
	}
	observations := make([]observation, 0, len(ordered))
	categories := map[string]struct{}{declared: {}}
	for _, sample := range ordered {
		if sample.Execution == nil {
			continue
		}
		model := normalizeModelName(sample.Execution.ReturnedModel)
		if model == "" {
			continue
		}
		categories[model] = struct{}{}
		observations = append(observations, observation{sample: sample, model: model})
	}
	if len(observations) == 0 {
		return nil, nil, nil, nil
	}
	names := make([]string, 0, len(categories))
	for name := range categories {
		names = append(names, name)
	}
	sort.Strings(names)
	indexByName := make(map[string]int, len(names))
	counts := make([]int, len(names))
	floatCounts := make([]float64, len(names))
	candidates := make(map[string][]float64, len(names))
	for index, name := range names {
		indexByName[name] = index
		probabilities := make([]float64, len(names))
		probabilities[index] = 1
		candidates[name] = probabilities
	}
	for _, item := range observations {
		index := indexByName[item.model]
		counts[index]++
		floatCounts[index]++
	}
	classification, err := ClassifyCandidateDistribution(floatCounts, candidates, 0.5, 0.1)
	if err != nil {
		return nil, nil, nil, err
	}
	var mixture *MixtureEstimate
	if len(names) >= 2 {
		estimate, estimateErr := EstimateMixture(counts, candidates, MixtureConfig{
			MaxIterations: 100, Tolerance: 1e-9, BootstrapSamples: 200, ConfidenceLevel: 0.95,
			Seed: int64(len(observations))*7919 + int64(len(names)),
		})
		if estimateErr != nil {
			return nil, nil, nil, estimateErr
		}
		mixture = &estimate
	}
	temporalObservations := make([]TemporalObservation, len(observations))
	for index, item := range observations {
		score := 0.0
		if item.model == declared {
			score = 1
		}
		temporalObservations[index] = TemporalObservation{
			SampleID: item.sample.SampleID, Ordinal: index, CapturedAt: item.sample.CapturedAt, Score: score,
		}
	}
	temporalResult, err := AnalyzeTemporalIdentity(TemporalConfig{
		Detector:   VersionedRef{ID: "identity.temporal.returned-model", SemanticVersion: "1.0.0", ImplementationVersion: "builtin-1"},
		WindowSize: 2, WindowStep: 2, MinimumWindows: 4, MinimumSegmentWindows: 2,
		MatchedThreshold: 0.8, ShiftedThreshold: 0.2, ChangeThreshold: 0.6,
		SegmentSpreadThreshold: 0.2, AnomalyThreshold: 0.6, MaximumShortAnomalyWindows: 1,
	}, temporalObservations)
	if err != nil {
		return nil, nil, nil, err
	}
	return &classification, mixture, &temporalResult, nil
}

func auditStrategySeed(values ...string) uint64 {
	var result uint64 = 1469598103934665603
	for _, value := range values {
		for _, char := range []byte(value) {
			result ^= uint64(char)
			result *= 1099511628211
		}
	}
	return result
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func waitForAuditSampleDelay(ctx context.Context, delayMillis int64) error {
	if delayMillis <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(time.Duration(delayMillis) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *BuiltinAuditRunController) reportBackgroundError(runID string, err error) {
	if err != nil {
		log.Printf("[ModelAudit-Run] 运行 %s 处理失败: %v", strings.TrimSpace(runID), err)
	}
}

var _ AuditRunController = (*BuiltinAuditRunController)(nil)
var _ AuditWorkloadValidator = (*BuiltinAuditRunController)(nil)
var _ AuditRuntimeCatalogProvider = (*BuiltinAuditRunController)(nil)
