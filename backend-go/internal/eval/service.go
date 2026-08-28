package eval

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/config"
	"github.com/BenedictKing/claude-proxy/internal/scheduler"
)

// Service 评测工作台装配根。
type Service struct {
	Store  *Store
	Runner *Runner
	Watch  *Watch
}

func NewService(path string, cfg *config.ConfigManager, envCfg *config.EnvConfig, sch *scheduler.ChannelScheduler) (*Service, error) {
	store, err := NewStore(path)
	if err != nil {
		return nil, err
	}
	// 进程重启后，库里可能残留上次崩溃/中断留下的 queued / running 幽灵批次。
	// Runner 的 busy 只存在内存里，不清掉它们在 UI 上会一直显示进行中并锁住「开始」。
	if _, err := store.RecoverStaleRuns(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("清理评测残留批次失败: %w", err)
	}
	if err := store.SyncBuiltins(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("同步评测内置题库失败: %w", err)
	}
	runner := NewRunner(store, NewSender(cfg, envCfg), cfg, sch)
	watch := NewWatch(store, runner)
	return &Service{Store: store, Runner: runner, Watch: watch}, nil
}

func (s *Service) Start() {
	if s == nil || s.Watch == nil {
		return
	}
	s.Watch.Start()
	log.Printf("[Eval-Init] 评测工作台已启动")
}

func (s *Service) Stop() {
	if s == nil {
		return
	}
	if s.Watch != nil {
		s.Watch.Stop()
	}
	if s.Store != nil {
		if err := s.Store.Close(); err != nil {
			log.Printf("[Eval-Shutdown] 关闭评测存储失败: %v", err)
			return
		}
		log.Printf("[Eval-Shutdown] 评测存储已关闭")
	}
}

func (s *Service) LatestMap() (map[string]ChannelLatest, WatchConfig, error) {
	items, err := s.Store.ListChannelLatest()
	if err != nil {
		return nil, WatchConfig{}, err
	}
	watch, err := s.Store.GetWatch()
	if err != nil {
		return nil, WatchConfig{}, err
	}
	watching := map[string]struct{}{}
	if watch.Enabled {
		for _, channelID := range watch.ChannelIDs {
			watching[channelID] = struct{}{}
		}
	}
	result := make(map[string]ChannelLatest, len(items))
	for _, item := range items {
		aggregate, label := AggregateLabel(item.Aggregate, item.FinishedAt)
		item.Aggregate = aggregate
		item.Label = label
		_, item.Watching = watching[item.ChannelID]
		result[item.ChannelID] = item
	}
	return result, watch, nil
}

func (s *Service) PutWatch(incoming WatchConfig) (WatchConfig, error) {
	current, err := s.Store.GetWatch()
	if err != nil {
		return WatchConfig{}, err
	}
	if strings.TrimSpace(incoming.Interval) == "" {
		incoming.Interval = Interval2h
	}
	incoming.Model = strings.TrimSpace(incoming.Model)
	incoming.Thinking = firstNonEmpty(incoming.Thinking, ThinkingInherit)
	incoming.ChannelModels = normalizeChannelModels(incoming.ChannelModels, incoming.ChannelIDs)
	if strings.TrimSpace(incoming.SuiteID) == "" {
		// 关掉值班时前端可能不带套件；保留原套件，只落盘开关。
		if incoming.Enabled {
			return WatchConfig{}, fmt.Errorf("开启值班必须选择便宜套件")
		}
		incoming.SuiteID = current.SuiteID
	}
	// 套件仍可能为空（题库被清空），这种情况只允许写「关」，不做套件校验。
	if strings.TrimSpace(incoming.SuiteID) != "" {
		suite, probes, suiteErr := s.Store.SuiteWithProbes(incoming.SuiteID)
		if suiteErr != nil {
			return WatchConfig{}, suiteErr
		}
		if err := ValidateWatch(incoming, suite, probes); err != nil {
			return WatchConfig{}, err
		}
	}
	intervalSeconds, err := parseInterval(incoming.Interval)
	if err != nil {
		return WatchConfig{}, err
	}
	incoming.LastRunAt = current.LastRunAt
	incoming.LastSkipReason = current.LastSkipReason
	if incoming.Enabled {
		incoming.NextRunAt = time.Now().Unix() + intervalSeconds
	} else {
		incoming.NextRunAt = 0
	}
	if err := s.Store.PutWatch(incoming); err != nil {
		return WatchConfig{}, err
	}
	return s.Store.GetWatch()
}
