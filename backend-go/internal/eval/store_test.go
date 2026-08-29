package eval

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "eval.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreSyncBuiltinsAndWatch(t *testing.T) {
	store := newTestStore(t)
	if err := store.SyncBuiltins(); err != nil {
		t.Fatal(err)
	}
	probes, err := store.ListProbes()
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) < 8 {
		t.Fatalf("内置探针过少: %d", len(probes))
	}
	cheap, err := store.GetSuiteBySlug("authenticity-cheap")
	if err != nil {
		t.Fatal(err)
	}
	if !cheap.Cheap {
		t.Fatal("真伪快检必须是便宜套件")
	}
	watch, err := store.GetWatch()
	if err != nil {
		t.Fatal(err)
	}
	if watch.SuiteID != cheap.ID {
		t.Fatalf("值班默认套件应为真伪快检，得到 %s", watch.SuiteID)
	}
}

// 内置题归代码所有：改不了、删不了，重复同步也不会换 ID（换了套件引用就断了）。
func TestBuiltinProbeIsCodeOwned(t *testing.T) {
	store := newTestStore(t)
	if err := store.SyncBuiltins(); err != nil {
		t.Fatal(err)
	}
	probe, err := store.GetProbeBySlug("auth-juice")
	if err != nil {
		t.Fatal(err)
	}
	if !probe.Builtin {
		t.Fatal("内置探针应带 builtin 标记")
	}

	probe.Name = "手工改名"
	if _, err := store.UpdateProbe(probe); err == nil {
		t.Fatal("内置探针不应允许 UpdateProbe")
	}
	if err := store.DeleteProbe(probe.ID); err == nil {
		t.Fatal("内置探针不应允许删除")
	}

	if err := store.SyncBuiltins(); err != nil {
		t.Fatal(err)
	}
	again, err := store.GetProbeBySlug("auth-juice")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != probe.ID {
		t.Fatalf("重复同步不该换 ID: %s -> %s", probe.ID, again.ID)
	}
	if again.Name == "手工改名" {
		t.Fatal("内置探针应被同步回代码里的定义")
	}
}

// 老库缺 builtin 列时能就地补列，不是重建库。
func TestMigrateAddsBuiltinColumnIdempotently(t *testing.T) {
	store := newTestStore(t)
	if err := store.ensureColumn("eval_probes", "builtin", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		t.Fatalf("重复补列应当无害: %v", err)
	}
	if err := store.migrate(); err != nil {
		t.Fatalf("重复 migrate 应当无害: %v", err)
	}
}

// 自建题不受内置保护影响，照样能改能删。
func TestCustomProbeStaysEditable(t *testing.T) {
	store := newTestStore(t)
	created, err := store.InsertProbe(Probe{
		Slug:                   "custom-echo",
		Name:                   "自建题",
		Category:               CategoryIQ,
		Stimulus:               Stimulus{Prompt: "回答 42"},
		Extract:                ExtractSpec{Kind: ExtractText},
		Judge:                  JudgeSpec{Kind: JudgeExact, Expected: "42"},
		SampleCount:            1,
		ApplicableServiceTypes: []string{ServiceClaude},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Builtin {
		t.Fatal("自建题不该带 builtin 标记")
	}
	created.Name = "改过的自建题"
	updated, err := store.UpdateProbe(created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "改过的自建题" {
		t.Fatalf("自建题应可改名，得到 %s", updated.Name)
	}
	if err := store.DeleteProbe(created.ID); err != nil {
		t.Fatalf("自建题应可删除: %v", err)
	}
}

// 渠道行深链要看单渠道的完整历史：按渠道过滤只留声明过该渠道的批次，
// 且必须靠带引号的完整 ID 匹配——ID 前缀相同的另一个渠道不能被误命中。
func TestListRunsFilterByChannel(t *testing.T) {
	store := newTestStore(t)
	mustCreateRun := func(id string, channelIDs []string, createdAt int64) {
		t.Helper()
		if _, err := store.CreateRun(Run{
			ID:         id,
			SuiteID:    "suite_x",
			Trigger:    TriggerManual,
			Status:     RunDone,
			ChannelIDs: channelIDs,
			CreatedAt:  createdAt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// ch_a1 与 ch_a1_long 前缀相同，用来挡住"不带引号的子串匹配"这类回归
	mustCreateRun("run_1", []string{"ch_a1", "ch_b"}, 100)
	mustCreateRun("run_2", []string{"ch_b"}, 200)
	mustCreateRun("run_3", []string{"ch_a1_long"}, 300)

	all, err := store.ListRuns("", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("不带渠道过滤应返回全部 3 条，得到 %d", len(all))
	}
	if all[0].ID != "run_3" {
		t.Fatalf("应按 created_at 倒序，首条得到 %s", all[0].ID)
	}

	filtered, err := store.ListRuns("ch_a1", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != "run_1" {
		t.Fatalf("按 ch_a1 过滤应只命中 run_1，得到 %+v", runIDs(filtered))
	}

	longOnly, err := store.ListRuns("ch_a1_long", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(longOnly) != 1 || longOnly[0].ID != "run_3" {
		t.Fatalf("按 ch_a1_long 过滤应只命中 run_3，得到 %+v", runIDs(longOnly))
	}

	none, err := store.ListRuns("ch_missing", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("未参与过评测的渠道应返回空列表，得到 %+v", runIDs(none))
	}

	limited, err := store.ListRuns("ch_b", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].ID != "run_2" {
		t.Fatalf("limit 应在过滤之后生效并保持倒序，得到 %+v", runIDs(limited))
	}
}

func runIDs(runs []Run) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	return ids
}
