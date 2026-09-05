package pricing

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pricing.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	return store, path
}

func TestLookupMatching(t *testing.T) {
	store, _ := newTempStore(t)

	t.Run("exact match ignores case and spaces", func(t *testing.T) {
		entry := store.Lookup("  GPT-4O  ")
		if entry == nil || entry.ModelID != "gpt-4o" {
			t.Fatalf("expected gpt-4o, got %+v", entry)
		}
	})

	t.Run("dated suffix falls back to containment", func(t *testing.T) {
		entry := store.Lookup("claude-3-5-sonnet-20241022")
		if entry == nil || entry.ModelID != "claude-3-5-sonnet" {
			t.Fatalf("expected claude-3-5-sonnet, got %+v", entry)
		}
	})

	t.Run("longest containment key wins", func(t *testing.T) {
		entry := store.Lookup("gpt-4o-mini-2024-07-18")
		if entry == nil || entry.ModelID != "gpt-4o-mini" {
			t.Fatalf("expected gpt-4o-mini, got %+v", entry)
		}
	})

	// 包含匹配必须单向：表里有 gpt-4o / gpt-4o-mini，查 "gpt-4" 不能反向命中它们，
	// 否则一个从没配过价的模型会按另一个模型的价格计费。
	t.Run("containment is one directional", func(t *testing.T) {
		if entry := store.Lookup("gpt-4"); entry != nil {
			t.Fatalf("gpt-4 must not match longer table keys, got %+v", entry)
		}
	})

	t.Run("unknown model and empty name return nil", func(t *testing.T) {
		if entry := store.Lookup("totally-unknown-model"); entry != nil {
			t.Fatalf("expected nil, got %+v", entry)
		}
		if entry := store.Lookup("   "); entry != nil {
			t.Fatalf("expected nil for blank name, got %+v", entry)
		}
	})

	t.Run("nil store never panics and reports unpriced", func(t *testing.T) {
		var nilStore *Store
		if entry := nilStore.Lookup("gpt-4o"); entry != nil {
			t.Fatalf("nil store must return nil")
		}
		if _, ok := nilStore.CostFor("gpt-4o", TokenUsage{InputTokens: 1}, "messages", 1); ok {
			t.Fatalf("nil store must report unpriced")
		}
		if all := nilStore.All(); all != nil {
			t.Fatalf("nil store must return nil table")
		}
	})
}

func TestMissingFileLoadsBuiltinsWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if len(store.All()) != len(DefaultBuiltinPricing) {
		t.Fatalf("expected %d builtin entries, got %d", len(DefaultBuiltinPricing), len(store.All()))
	}
	// 启动阶段不写文件：少一条失败路径，也避免只读目录下起不来。
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("startup must not create the pricing file, stat err=%v", err)
	}
}

func TestUpsertReplacesBuiltinAndPersistsOverridesOnly(t *testing.T) {
	store, path := newTempStore(t)

	if err := store.Upsert(ModelPricing{
		ModelID:        "  GPT-4o  ",
		DisplayName:    "我改过的 GPT-4o",
		InputCostPerM:  1,
		OutputCostPerM: 2,
	}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}

	entry := store.Lookup("gpt-4o")
	if entry == nil || entry.InputCostPerM != 1 || entry.OutputCostPerM != 2 {
		t.Fatalf("override must take effect, got %+v", entry)
	}
	// 整条替换而不是按字段合并：内置的缓存价不能残留成"半改写"状态。
	if entry.CacheReadCostPerM != 0 || entry.CacheCreationCostPerM != 0 {
		t.Fatalf("override must not merge builtin fields, got %+v", entry)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pricing file failed: %v", err)
	}
	var file pricingFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("pricing file is not valid JSON: %v", err)
	}
	if file.Version != FileVersion {
		t.Fatalf("expected version %d, got %d", FileVersion, file.Version)
	}
	// 内置价绝不落盘，否则后续版本修正内置价再也覆盖不回来。
	if len(file.Overrides) != 1 {
		t.Fatalf("expected exactly 1 override on disk, got %d", len(file.Overrides))
	}
	// 模型名只 trim 不改写：查表靠归一化，落盘保留用户写的原样。
	if file.Overrides[0].ModelID != "GPT-4o" {
		t.Fatalf("expected model id %q, got %q", "GPT-4o", file.Overrides[0].ModelID)
	}
}

func TestDeleteBuiltinWritesTombstoneAndSurvivesReload(t *testing.T) {
	store, path := newTempStore(t)

	if err := store.Delete("GPT-4O"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if entry := store.Lookup("gpt-4o"); entry != nil {
		t.Fatalf("deleted model must not resolve, got %+v", entry)
	}

	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	// 没有墓碑的话，重启时内置表会把删掉的条目原样带回来。
	if entry := reloaded.Lookup("gpt-4o"); entry != nil {
		t.Fatalf("tombstone must survive reload, got %+v", entry)
	}

	// Upsert 是把误删的内置模型放回表里的唯一路径。
	if err := reloaded.Upsert(ModelPricing{ModelID: "gpt-4o", InputCostPerM: 5}); err != nil {
		t.Fatalf("Upsert after delete failed: %v", err)
	}
	if entry := reloaded.Lookup("gpt-4o"); entry == nil || entry.InputCostPerM != 5 {
		t.Fatalf("expected restored price 5, got %+v", entry)
	}
	if ids := reloaded.DeletedModelIDs(); len(ids) != 0 {
		t.Fatalf("upsert must clear the tombstone, got %v", ids)
	}
}

func TestDeleteOverrideOnlyModelLeavesNoTombstone(t *testing.T) {
	store, _ := newTempStore(t)

	if err := store.Upsert(ModelPricing{ModelID: "my-private-model", InputCostPerM: 1}); err != nil {
		t.Fatalf("Upsert failed: %v", err)
	}
	if err := store.Delete("my-private-model"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if ids := store.DeletedModelIDs(); len(ids) != 0 {
		t.Fatalf("non-builtin delete must not write a tombstone, got %v", ids)
	}
	if overrides := store.Overrides(); len(overrides) != 0 {
		t.Fatalf("expected no overrides left, got %v", overrides)
	}
}

func TestDeleteIsExactMatchOnly(t *testing.T) {
	store, _ := newTempStore(t)

	// Lookup 会把带日期后缀的名字落到 gpt-4o 上，但删表是行操作，
	// 模糊命中会删错行，所以 Delete 只认精确名。
	err := store.Delete("gpt-4o-2024-11-20")
	if !errors.Is(err, ErrModelNotListed) {
		t.Fatalf("expected ErrModelNotListed, got %v", err)
	}
	if entry := store.Lookup("gpt-4o"); entry == nil {
		t.Fatalf("gpt-4o must still be listed")
	}
}

func TestUpsertRejectsInvalidEntriesWithoutTouchingDisk(t *testing.T) {
	store, path := newTempStore(t)

	cases := []struct {
		name    string
		entries []ModelPricing
	}{
		{"empty batch", nil},
		{"blank model id", []ModelPricing{{ModelID: "   "}}},
		{"negative price", []ModelPricing{{ModelID: "m", InputCostPerM: -1}}},
		{"NaN price", []ModelPricing{{ModelID: "m", OutputCostPerM: math.NaN()}}},
		{"Inf price", []ModelPricing{{ModelID: "m", CacheReadCostPerM: math.Inf(1)}}},
		{"duplicate in batch", []ModelPricing{{ModelID: "m"}, {ModelID: "M"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.Upsert(tc.entries...); err == nil {
				t.Fatalf("expected Upsert to fail")
			}
		})
	}

	// 校验发生在落盘之前：被拒绝的写入不能留下任何文件。
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected upsert must not create the file, stat err=%v", err)
	}
}

func TestLoadRejectsBrokenFilesInsteadOfResetting(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"future version", `{"version":99}`, "拒绝加载"},
		{"invalid json", `{"version":1,`, "不是合法 JSON"},
		{
			"duplicate overrides",
			`{"version":1,"overrides":[{"modelId":"a","inputCostPerMillion":1},{"modelId":"A","inputCostPerMillion":2}]}`,
			"多条改写",
		},
		{
			"override and tombstone collide",
			`{"version":1,"overrides":[{"modelId":"a"}],"deletedModelIds":["A"]}`,
			"同时出现",
		},
		{
			"negative price",
			`{"version":1,"overrides":[{"modelId":"a","outputCostPerMillion":-2}]}`,
			"不能为负数",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pricing.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write fixture failed: %v", err)
			}
			// 静默重置只会表现为"费用凭空变少"，所以一律显式报错。
			_, err := NewStore(path)
			if err == nil {
				t.Fatalf("expected NewStore to fail")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadDedupesTombstonesAndRejectsEmptyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	// 墓碑写了两遍只是同一个"已删除"事实重复，去重即可，不该拦住启动。
	content := `{"version":1,"deletedModelIds":["GPT-4o","gpt-4o"," "]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture failed: %v", err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	if ids := store.DeletedModelIDs(); len(ids) != 1 || ids[0] != "gpt-4o" {
		t.Fatalf("expected a single normalized tombstone, got %v", ids)
	}

	if _, err := NewStore("   "); err == nil {
		t.Fatalf("expected empty path to be rejected")
	}
}
