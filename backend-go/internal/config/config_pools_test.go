package config

import (
	"path/filepath"
	"testing"
)

func TestSelectChannelPoolUsesLongestMatcher(t *testing.T) {
	pools := []ChannelPool{
		defaultChannelPool(),
		{ID: "claude", Name: "Claude", ModelMatcher: "claude", Priority: 2},
		{ID: "sonnet", Name: "Sonnet", ModelMatcher: "claude-sonnet", Priority: 3},
	}

	selected, err := SelectChannelPool(pools, "CLAUDE-SONNET-4")
	if err != nil {
		t.Fatalf("SelectChannelPool() error = %v", err)
	}
	if selected.ID != "sonnet" {
		t.Fatalf("SelectChannelPool() ID = %q, want sonnet", selected.ID)
	}
}

func TestSelectChannelPoolUsesWildcardOnlyAsFallback(t *testing.T) {
	pools := []ChannelPool{
		{ID: DefaultChannelPoolID, Name: "GPT", ModelMatcher: "gpt", Priority: 1},
		{ID: "fallback", Name: "Fallback", ModelMatcher: "*", Priority: 2},
	}

	selected, err := SelectChannelPool(pools, "gpt-5.4")
	if err != nil {
		t.Fatalf("SelectChannelPool() error = %v", err)
	}
	if selected.ID != DefaultChannelPoolID {
		t.Fatalf("SelectChannelPool() ID = %q, want %q", selected.ID, DefaultChannelPoolID)
	}

	selected, err = SelectChannelPool(pools, "claude-sonnet")
	if err != nil {
		t.Fatalf("SelectChannelPool() fallback error = %v", err)
	}
	if selected.ID != "fallback" {
		t.Fatalf("SelectChannelPool() fallback ID = %q, want fallback", selected.ID)
	}
}

func TestSelectChannelPoolWithoutWildcardReturnsError(t *testing.T) {
	pools := []ChannelPool{{ID: DefaultChannelPoolID, Name: "GPT", ModelMatcher: "gpt", Priority: 1}}
	if _, err := SelectChannelPool(pools, "claude-sonnet"); err == nil {
		t.Fatal("SelectChannelPool() error = nil")
	}
}

func TestNormalizeUpstreamPrioritiesScopesByPool(t *testing.T) {
	upstreams := []UpstreamConfig{
		{ID: "default-2", PoolID: DefaultChannelPoolID, Priority: 2},
		{ID: "default-1", PoolID: DefaultChannelPoolID, Priority: 1},
		{ID: "gpt-2", PoolID: "gpt", Priority: 2},
		{ID: "gpt-1", PoolID: "gpt", Priority: 1},
		{ID: "vision", PoolID: "gpt", Priority: 9, ExcludeFromConversation: true},
	}

	normalizeUpstreamPriorities(upstreams)
	if upstreams[0].Priority != 2 || upstreams[1].Priority != 1 {
		t.Fatalf("default pool priorities = [%d, %d], want [2, 1]", upstreams[0].Priority, upstreams[1].Priority)
	}
	if upstreams[2].Priority != 2 || upstreams[3].Priority != 1 {
		t.Fatalf("gpt pool priorities = [%d, %d], want [2, 1]", upstreams[2].Priority, upstreams[3].Priority)
	}
	if upstreams[4].Priority != 1 {
		t.Fatalf("public vision priority = %d, want 1", upstreams[4].Priority)
	}
}

func TestSaveChannelPoolLayoutMovesAndReordersChannels(t *testing.T) {
	manager := &ConfigManager{
		configFile: filepath.Join(t.TempDir(), "config.json"),
		config: Config{
			MessagePools: []ChannelPool{
				{ID: "gpt", Name: "GPT", ModelMatcher: "gpt", Priority: 1},
				defaultChannelPool(),
			},
			Upstream: []UpstreamConfig{
				{ID: "a", Name: "A", PoolID: DefaultChannelPoolID, Status: ChannelStatusActive, Priority: 1},
				{ID: "b", Name: "B", PoolID: DefaultChannelPoolID, Status: ChannelStatusActive, Priority: 2},
				{ID: "c", Name: "C", PoolID: "gpt", Status: ChannelStatusSuspended, Priority: 1},
				{ID: "disabled", Name: "Disabled", PoolID: "gpt", Status: ChannelStatusDisabled, Priority: 2},
			},
		},
	}

	err := manager.SaveChannelPoolLayout("messages", []ChannelPoolLayout{
		{PoolID: "gpt", ChannelIDs: []string{"a", "c"}},
		{PoolID: DefaultChannelPoolID, ChannelIDs: []string{"b"}},
	})
	if err != nil {
		t.Fatalf("SaveChannelPoolLayout() error = %v", err)
	}

	got := manager.config.Upstream
	if got[0].PoolID != "gpt" || got[0].Priority != 1 {
		t.Fatalf("moved channel = %#v, want pool gpt priority 1", got[0])
	}
	if got[2].PoolID != "gpt" || got[2].Priority != 2 {
		t.Fatalf("gpt tail channel = %#v, want priority 2", got[2])
	}
	if got[3].Priority != 3 {
		t.Fatalf("disabled channel priority = %d, want 3", got[3].Priority)
	}
	if got[1].PoolID != DefaultChannelPoolID || got[1].Priority != 1 {
		t.Fatalf("default channel = %#v, want default priority 1", got[1])
	}
}

func TestEnsurePoolsAndAssignmentsReportsNormalization(t *testing.T) {
	pools := []ChannelPool{
		{ID: DefaultChannelPoolID, Name: " 默认子池 ", ModelMatcher: "*", Priority: 99},
		{ID: "sonnet", Name: " Sonnet ", ModelMatcher: " CLAUDE-SONNET ", Priority: 7},
	}
	upstreams := []UpstreamConfig{
		{Name: "default-channel"},
		{Name: "sonnet-channel", PoolID: "sonnet"},
	}

	normalized, changed, err := ensurePoolsAndAssignments(pools, upstreams)
	if err != nil {
		t.Fatalf("ensurePoolsAndAssignments() error = %v", err)
	}
	if !changed {
		t.Fatal("ensurePoolsAndAssignments() changed = false, want true")
	}
	if normalized[0].ID != "sonnet" || normalized[0].ModelMatcher != "claude-sonnet" || normalized[0].Priority != 1 {
		t.Fatalf("normalized custom pool = %#v", normalized[0])
	}
	if upstreams[0].PoolID != DefaultChannelPoolID {
		t.Fatalf("default upstream pool = %q", upstreams[0].PoolID)
	}
}

func TestValidateAllVisionLayerConfigsChecksDependents(t *testing.T) {
	upstreams := []UpstreamConfig{
		{
			ID:                      "text",
			Name:                    "text",
			PoolID:                  "pool-a",
			VisionLayerEnabled:      true,
			VisionLayerChannelID:    "vision",
			ExcludeFromConversation: false,
		},
		{
			ID:            "vision",
			Name:          "vision",
			PoolID:        "pool-b",
			VisionCapable: true,
			Status:        ChannelStatusActive,
		},
	}

	if err := validateAllVisionLayerConfigs(upstreams); err == nil {
		t.Fatal("validateAllVisionLayerConfigs() error = nil")
	}

	upstreams[1].ExcludeFromConversation = true
	if err := validateAllVisionLayerConfigs(upstreams); err != nil {
		t.Fatalf("public vision channel should be valid: %v", err)
	}
}

func TestRemoveFromSliceRejectsVisionDependency(t *testing.T) {
	upstreams := []UpstreamConfig{
		{ID: "text", Name: "text", VisionLayerEnabled: true, VisionLayerChannelID: "vision"},
		{ID: "vision", Name: "vision", VisionCapable: true},
	}

	if _, _, err := removeFromSlice(upstreams, 1, "Messages"); err == nil {
		t.Fatal("removeFromSlice() error = nil")
	}
}
