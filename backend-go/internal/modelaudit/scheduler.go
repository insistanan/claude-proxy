package modelaudit

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type AuditSchedulerConfig struct {
	PollInterval time.Duration
	PageSize     int
}

func DefaultAuditSchedulerConfig() AuditSchedulerConfig {
	return AuditSchedulerConfig{PollInterval: time.Minute, PageSize: 100}
}

func (c AuditSchedulerConfig) validate() error {
	if c.PollInterval <= 0 || c.PageSize <= 0 || c.PageSize > AuditMaximumPageSize {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "审计调度轮询间隔或分页大小无效")
	}
	return nil
}

type AuditScheduler struct {
	store      *AuditSQLiteStore
	controller *BuiltinAuditRunController
	config     AuditSchedulerConfig
	now        func() time.Time
	reportErr  func(error)

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewAuditScheduler(
	store *AuditSQLiteStore,
	controller *BuiltinAuditRunController,
	config AuditSchedulerConfig,
) (*AuditScheduler, error) {
	if store == nil || controller == nil {
		return nil, contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计调度器依赖未初始化")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &AuditScheduler{
		store: store, controller: controller, config: config, now: time.Now,
		reportErr: func(err error) { log.Printf("[ModelAudit-Scheduler] %v", err) },
	}, nil
}

func (s *AuditScheduler) Start() error {
	if err := s.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return contractError(ErrorCodeConflict, ErrorCategoryRequest, "审计调度器已经启动")
	}
	startedAt := s.now().UTC()
	if startedAt.IsZero() {
		s.mu.Unlock()
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计调度器时钟无效")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := s.reconcileRecovery(ctx, startedAt); err != nil {
		cancel()
		s.mu.Unlock()
		return err
	}
	s.started = true
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go s.loop(ctx, startedAt, done)
	return nil
}

func (s *AuditScheduler) Close(ctx context.Context) error {
	if ctx == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryRequest, "关闭审计调度器缺少上下文")
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.cancel()
	done := s.done
	s.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return contractError(ErrorCodeCancelled, ErrorCategoryRequest, "等待审计调度器关闭时请求已结束", ctx.Err())
	}
}

func (s *AuditScheduler) validate() error {
	if s == nil || s.store == nil || s.controller == nil || s.now == nil || s.reportErr == nil {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计调度器未初始化")
	}
	return s.config.validate()
}

func (s *AuditScheduler) loop(ctx context.Context, scanAfter time.Time, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.now().UTC()
			if now.IsZero() || now.Before(scanAfter) {
				s.reportErr(contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计调度器时钟发生回退"))
				if !now.IsZero() {
					scanAfter = now
				}
				continue
			}
			if err := s.runCycle(ctx, scanAfter, now); err != nil && ctx.Err() == nil {
				s.reportErr(err)
			}
			scanAfter = now
		}
	}
}

func (s *AuditScheduler) runCycle(ctx context.Context, scanAfter, now time.Time) error {
	if scanAfter.IsZero() || now.IsZero() || now.Before(scanAfter) {
		return contractError(ErrorCodeInvalidRequest, ErrorCategoryInternal, "审计调度扫描窗口无效")
	}
	if err := s.reconcileRecovery(ctx, now); err != nil {
		return err
	}
	afterID := ""
	for {
		jobs, err := s.store.ListEnabledJobsAfter(ctx, afterID, s.config.PageSize)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.processJob(ctx, job, scanAfter, now); err != nil {
				s.reportErr(fmt.Errorf("审计任务 %s 调度失败: %w", job.ID, err))
			}
		}
		if len(jobs) < s.config.PageSize {
			return nil
		}
		afterID = jobs[len(jobs)-1].ID
	}
}

func (s *AuditScheduler) processJob(ctx context.Context, job AuditJob, scanAfter, now time.Time) error {
	endAt, err := job.Schedule.EffectiveEndAt()
	if err != nil {
		return err
	}
	if endAt != nil && !endAt.After(now) {
		expired, err := TransitionAuditJob(job, job.Revision, AuditJobExpired, now)
		if err != nil {
			return err
		}
		return s.store.UpdateJob(ctx, expired, job.Revision)
	}
	windowStart := scanAfter
	if job.UpdatedAt.After(windowStart) {
		windowStart = job.UpdatedAt
	}
	slot, err := DueAuditScheduleSlot(job.ID, job.Schedule, windowStart, now)
	if err != nil || slot == nil {
		return err
	}
	_, err = s.controller.StartScheduled(ctx, job, *slot)
	return err
}

func (s *AuditScheduler) reconcileRecovery(ctx context.Context, now time.Time) error {
	entries, err := s.store.LoadRecoveryState(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		decision, err := PlanAuditRecovery(entry, now)
		if err != nil {
			s.reportErr(fmt.Errorf("审计运行 %s 恢复规划失败: %w", entry.Run.ID, err))
			continue
		}
		switch decision.Action {
		case AuditRecoveryWait:
			continue
		case AuditRecoveryResumePending:
			if _, err := s.controller.ResumePending(ctx, entry, now); err != nil {
				s.reportErr(fmt.Errorf("审计运行 %s 恢复启动失败: %w", entry.Run.ID, err))
			}
		case AuditRecoveryFinalizeInterrupted:
			if err := s.store.CommitInterruptedRecovery(ctx, decision, now); err != nil {
				s.reportErr(fmt.Errorf("审计运行 %s 中断收口失败: %w", entry.Run.ID, err))
			}
		}
	}
	return nil
}
