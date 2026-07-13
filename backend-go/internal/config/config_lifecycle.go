package config

import (
	"fmt"
	"log"
	"time"
)

func (cm *ConfigManager) DuplicateChannel(kind string, index int) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	now := time.Now()
	var err error
	switch kind {
	case "responses":
		cm.config.ResponsesUpstream, err = insertClonedUpstream(cm.config.ResponsesUpstream, index, now)
	case "gemini":
		cm.config.GeminiUpstream, err = insertClonedUpstream(cm.config.GeminiUpstream, index, now)
	case "chat":
		cm.config.ChatUpstream, err = insertClonedUpstream(cm.config.ChatUpstream, index, now)
	case "images":
		cm.config.ImagesUpstream, err = insertClonedUpstream(cm.config.ImagesUpstream, index, now)
	default:
		cm.config.Upstream, err = insertClonedUpstream(cm.config.Upstream, index, now)
	}
	if err != nil {
		return err
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) TidyProblemChannels(kind string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	changed := false
	switch kind {
	case "responses":
		changed = reorderProblemChannelsStable(cm.config.ResponsesUpstream)
	case "gemini":
		changed = reorderProblemChannelsStable(cm.config.GeminiUpstream)
	case "chat":
		changed = reorderProblemChannelsStable(cm.config.ChatUpstream)
	case "images":
		changed = reorderProblemChannelsStable(cm.config.ImagesUpstream)
	default:
		changed = reorderProblemChannelsStable(cm.config.Upstream)
	}
	if !changed {
		return nil
	}
	return cm.saveConfigLocked(cm.config)
}

func (cm *ConfigManager) startChannelLifecycleWorker() {
	ticker := time.NewTicker(channelLifecycleTick)
	defer ticker.Stop()

	lastDeprecatedCleanup := time.Now()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			cleanupDeprecated := now.Sub(lastDeprecatedCleanup) >= deprecatedCleanupInterval
			if err := cm.runChannelLifecycle(now, cleanupDeprecated); err != nil {
				log.Printf("[Config-Lifecycle] 警告: 渠道生命周期维护失败: %v", err)
			}
			if cleanupDeprecated {
				lastDeprecatedCleanup = now
			}
		case <-cm.stopChan:
			return
		}
	}
}

func (cm *ConfigManager) runChannelLifecycle(now time.Time, cleanupDeprecated bool) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	changed := false
	var listChanged bool
	cm.config.Upstream, listChanged = applyLifecycleToUpstreams(cm.config.Upstream, now, cleanupDeprecated, "Messages")
	changed = changed || listChanged
	cm.config.ResponsesUpstream, listChanged = applyLifecycleToUpstreams(cm.config.ResponsesUpstream, now, cleanupDeprecated, "Responses")
	changed = changed || listChanged
	cm.config.GeminiUpstream, listChanged = applyLifecycleToUpstreams(cm.config.GeminiUpstream, now, cleanupDeprecated, "Gemini")
	changed = changed || listChanged
	cm.config.ChatUpstream, listChanged = applyLifecycleToUpstreams(cm.config.ChatUpstream, now, cleanupDeprecated, "Chat")
	changed = changed || listChanged
	cm.config.ImagesUpstream, listChanged = applyLifecycleToUpstreams(cm.config.ImagesUpstream, now, cleanupDeprecated, "Images")
	changed = changed || listChanged

	if !changed {
		return nil
	}
	if err := cm.saveConfigLocked(cm.config); err != nil {
		return fmt.Errorf("保存生命周期维护结果失败: %w", err)
	}
	return nil
}
