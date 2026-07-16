package config

import "testing"

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
			ID:                     "text",
			Name:                   "text",
			PoolID:                 "pool-a",
			VisionLayerEnabled:     true,
			VisionLayerChannelID:   "vision",
			ExcludeFromConversation: false,
		},
		{
			ID:             "vision",
			Name:           "vision",
			PoolID:         "pool-b",
			VisionCapable:  true,
			Status:         ChannelStatusActive,
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
