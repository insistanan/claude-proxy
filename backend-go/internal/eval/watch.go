package eval

import (
	"log"
	"sync"
	"time"
)

// Watch 单一值班 ticker。到期触发一次普通 eval_runs（trigger=watch）。
// 手动评测占用 Runner 时本次 skip，不排队。
type Watch struct {
	store  *Store
	runner *Runner

	stopOnce sync.Once
	stopCh   chan struct{}
}

func NewWatch(store *Store, runner *Runner) *Watch {
	return &Watch{store: store, runner: runner, stopCh: make(chan struct{})}
}

func (w *Watch) Start() {
	go w.loop()
}

func (w *Watch) Stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
	})
}

func (w *Watch) loop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	w.ensureNextRunNotImmediate()
	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.tick()
		}
	}
}

func (w *Watch) ensureNextRunNotImmediate() {
	watch, err := w.store.GetWatch()
	if err != nil {
		log.Printf("[Eval-Watch] 读取值班配置失败: %v", err)
		return
	}
	if !watch.Enabled {
		return
	}
	intervalSeconds, err := parseInterval(watch.Interval)
	if err != nil {
		log.Printf("[Eval-Watch] %v", err)
		return
	}
	now := time.Now().Unix()
	if watch.NextRunAt > now {
		return
	}
	watch.NextRunAt = now + intervalSeconds
	watch.LastSkipReason = ""
	if err := w.store.PutWatch(watch); err != nil {
		log.Printf("[Eval-Watch] 推迟首次值班失败: %v", err)
		return
	}
	log.Printf("[Eval-Watch] 进程启动不立即触发，下次运行 %s", time.Unix(watch.NextRunAt, 0).Format(time.RFC3339))
}

func (w *Watch) tick() {
	watch, err := w.store.GetWatch()
	if err != nil {
		log.Printf("[Eval-Watch] 读取值班配置失败: %v", err)
		return
	}
	if !watch.Enabled {
		return
	}
	if watch.NextRunAt == 0 {
		w.ensureNextRunNotImmediate()
		return
	}
	if time.Now().Unix() < watch.NextRunAt {
		return
	}

	intervalSeconds, err := parseInterval(watch.Interval)
	if err != nil {
		log.Printf("[Eval-Watch] %v", err)
		return
	}

	if w.runner.Busy() {
		watch.LastSkipReason = "上次跳过：已有评测在跑"
		watch.NextRunAt = time.Now().Unix() + intervalSeconds
		if err := w.store.PutWatch(watch); err != nil {
			log.Printf("[Eval-Watch] 写入 skip 失败: %v", err)
		}
		log.Printf("[Eval-Watch] skip：已有评测在跑")
		return
	}

	_, err = w.runner.Start(StartRunRequest{
		SuiteID:       watch.SuiteID,
		ChannelIDs:    watch.ChannelIDs,
		Model:         watch.Model,
		ChannelModels: watch.ChannelModels,
		Thinking:      watch.Thinking,
		Trigger:       TriggerWatch,
	})
	now := time.Now().Unix()
	if err != nil {
		if err == ErrBusy {
			watch.LastSkipReason = "上次跳过：已有评测在跑"
		} else {
			watch.LastSkipReason = err.Error()
			log.Printf("[Eval-Watch] 启动失败: %v", err)
		}
		watch.NextRunAt = now + intervalSeconds
		if persistErr := w.store.PutWatch(watch); persistErr != nil {
			log.Printf("[Eval-Watch] 启动失败后保存状态失败: %v", persistErr)
		}
		return
	}
	watch.LastRunAt = now
	watch.NextRunAt = now + intervalSeconds
	watch.LastSkipReason = ""
	if err := w.store.PutWatch(watch); err != nil {
		log.Printf("[Eval-Watch] 更新 last_run_at 失败: %v", err)
	}
}
