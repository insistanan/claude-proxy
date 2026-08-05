package modelcatalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BenedictKing/claude-proxy/internal/config"
)

func TestPublicModelEntriesFamilyOnly(t *testing.T) {
	t.Parallel()

	got := publicModelEntries(nil)
	wantIDs := []string{"opus", "sonnet", "gpt", "gemini", "chat"}
	if len(got) != len(wantIDs) {
		t.Fatalf("len=%d want %d; got=%v", len(got), len(wantIDs), modelIDs(got))
	}
	for index, want := range wantIDs {
		if got[index].ID != want {
			t.Fatalf("index %d = %q, want %q", index, got[index].ID, want)
		}
		if got[index].OwnedBy != "api-proxy" {
			t.Fatalf("owned_by for %s = %q", got[index].ID, got[index].OwnedBy)
		}
	}
}

func TestOpenAIModelsIncludesPoolMatchers(t *testing.T) {
	t.Parallel()

	cfgManager := newTestConfigManager(t, config.Config{
		MessagePools: []config.ChannelPool{
			{ID: "deepseek", Name: "DeepSeek", ModelMatcher: "deepseek", Priority: 1},
			{ID: config.DefaultChannelPoolID, Name: "默认子池", ModelMatcher: "*", Priority: 2},
		},
		ResponsesPools: []config.ChannelPool{
			{ID: "grok", Name: "Grok", ModelMatcher: "grok", Priority: 1},
			{ID: config.DefaultChannelPoolID, Name: "默认子池", ModelMatcher: "*", Priority: 2},
		},
		ChatPools: []config.ChannelPool{
			// same matcher as messages should still appear once
			{ID: "deepseek-chat", Name: "DeepSeek Chat", ModelMatcher: "deepseek", Priority: 1},
			{ID: config.DefaultChannelPoolID, Name: "默认子池", ModelMatcher: "*", Priority: 2},
		},
	})

	response := OpenAIModels(context.Background(), cfgManager)
	ids := modelIDs(response.Data)

	for _, family := range []string{"opus", "sonnet", "gpt", "gemini", "chat"} {
		if !containsString(ids, family) {
			t.Fatalf("missing family model %q in %v", family, ids)
		}
	}
	if !containsString(ids, "deepseek") {
		t.Fatalf("missing pool matcher deepseek in %v", ids)
	}
	if !containsString(ids, "grok") {
		t.Fatalf("missing pool matcher grok in %v", ids)
	}
	if containsString(ids, "*") || containsString(ids, "default") {
		t.Fatalf("should not expose default/wildcard pool as model id: %v", ids)
	}

	// no channel-style aliases
	for _, id := range ids {
		if looksLikeChatRouteAlias(id) {
			t.Fatalf("public list should not include chat route alias %q", id)
		}
	}

	deepseek, ok := ModelDetail(context.Background(), cfgManager, "deepseek")
	if !ok {
		t.Fatal("ModelDetail(deepseek) missing")
	}
	if deepseek.OwnedBy != "pool:chat,messages" {
		t.Fatalf("deepseek owned_by = %q, want pool:chat,messages", deepseek.OwnedBy)
	}
}

func TestOpenAIModelsSkipsFamilyCollisionFromPool(t *testing.T) {
	t.Parallel()

	cfgManager := newTestConfigManager(t, config.Config{
		MessagePools: []config.ChannelPool{
			{ID: "sonnet-pool", Name: "Sonnet Pool", ModelMatcher: "sonnet", Priority: 1},
			{ID: config.DefaultChannelPoolID, Name: "默认子池", ModelMatcher: "*", Priority: 2},
		},
	})

	response := OpenAIModels(context.Background(), cfgManager)
	count := 0
	for _, model := range response.Data {
		if model.ID == "sonnet" {
			count++
			if model.OwnedBy != "api-proxy" {
				t.Fatalf("family sonnet should win over pool, owned_by=%q", model.OwnedBy)
			}
		}
	}
	if count != 1 {
		t.Fatalf("sonnet count = %d, want 1", count)
	}
}

func newTestConfigManager(t *testing.T, cfg config.Config) *config.ConfigManager {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	manager, err := config.NewConfigManager(path)
	if err != nil {
		t.Fatalf("NewConfigManager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})
	return manager
}

func modelIDs(models []ModelEntry) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
