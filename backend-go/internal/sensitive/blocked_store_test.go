package sensitive

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBlockedStoreRecordListAndFilters(t *testing.T) {
	store := openTestBlockedStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	entries := []BlockedLog{
		{Timestamp: base, APIType: "messages", BlockType: BlockTypeSensitiveWord, RuleName: "gambling", PromptSnippet: "sample-1", RequestID: "req-1", ConversationID: "conv-1"},
		{Timestamp: base.Add(time.Minute), APIType: "responses", BlockType: BlockTypeDangerousCmd, RuleName: "destructive", PromptSnippet: "sample-2", RequestID: "req-2", ConversationID: "conv-2"},
		{Timestamp: base.Add(2 * time.Minute), APIType: "messages", BlockType: BlockTypeSensitiveInfo, RuleName: "phone", PromptSnippet: "sample-3", RequestID: "req-3", ConversationID: "conv-1"},
	}
	for index := range entries {
		stored, err := store.Record(ctx, entries[index])
		if err != nil {
			t.Fatalf("写入第 %d 条记录失败: %v", index, err)
		}
		if stored.ID <= 0 || stored.CreatedAt.IsZero() {
			t.Fatalf("持久化结果不完整: %+v", stored)
		}
		entries[index] = stored
	}

	page, err := store.List(ctx, BlockedLogListOptions{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if page.Total != 3 || len(page.Logs) != 2 || page.Logs[0].RequestID != "req-3" || page.Logs[1].RequestID != "req-2" {
		t.Fatalf("分页或排序错误: %+v", page)
	}

	page, err = store.List(ctx, BlockedLogListOptions{
		APIType: "messages", BlockType: BlockTypeSensitiveWord,
		From: base.Add(-time.Second), To: base.Add(time.Second), Page: 1, PageSize: 20,
	})
	if err != nil {
		t.Fatalf("筛选查询失败: %v", err)
	}
	if page.Total != 1 || len(page.Logs) != 1 || page.Logs[0].RequestID != "req-1" {
		t.Fatalf("筛选结果错误: %+v", page)
	}

	entry, found, err := store.Get(ctx, entries[1].ID)
	if err != nil || !found || entry.RequestID != "req-2" {
		t.Fatalf("单条查询错误: %+v %v %v", entry, found, err)
	}
}

func TestBlockedStoreAssignsAndListsConversationGroups(t *testing.T) {
	store := openTestBlockedStore(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	for _, entry := range []BlockedLog{
		{Timestamp: base, APIType: "responses", BlockType: BlockTypeSensitiveInfo, RequestID: "req-1"},
		{Timestamp: base.Add(time.Second), APIType: "responses", BlockType: BlockTypeCredential, RequestID: "req-1"},
		{Timestamp: base.Add(2 * time.Second), APIType: "messages", BlockType: BlockTypeSensitiveWord, RequestID: "req-2", ConversationID: "conv-2"},
		{Timestamp: base.Add(3 * time.Second), APIType: "chat", BlockType: BlockTypeDangerousCmd},
	} {
		if _, err := store.Record(ctx, entry); err != nil {
			t.Fatalf("写入分组测试记录失败: %v", err)
		}
	}
	if err := store.AssignConversation(ctx, "req-1", "conv-1"); err != nil {
		t.Fatalf("关联会话失败: %v", err)
	}
	page, err := store.ListGrouped(ctx, BlockedLogListOptions{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("按会话分页失败: %v", err)
	}
	if page.Total != 4 || page.TotalGroups != 3 || len(page.Groups) != 2 {
		t.Fatalf("会话分页统计错误: %+v", page)
	}
	if page.Groups[0].ConversationID != "" || len(page.Groups[0].Logs) != 1 {
		t.Fatalf("无身份旧记录不应与其他记录合并: %+v", page.Groups[0])
	}
	if page.Groups[1].ConversationID != "conv-2" || len(page.Groups[1].Logs) != 1 {
		t.Fatalf("第二组错误: %+v", page.Groups[1])
	}
	page, err = store.ListGrouped(ctx, BlockedLogListOptions{Page: 2, PageSize: 2})
	if err != nil || len(page.Groups) != 1 || page.Groups[0].ConversationID != "conv-1" || len(page.Groups[0].Logs) != 2 {
		t.Fatalf("同一会话未完整归组: %+v %v", page, err)
	}
}

func TestBlockedStoreMigratesConversationColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-blocked.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("创建旧版数据库失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE blocked_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp TEXT NOT NULL, api_type TEXT NOT NULL,
		block_type TEXT NOT NULL, rule_name TEXT NOT NULL DEFAULT '', prompt_snippet TEXT NOT NULL DEFAULT '',
		channel_name TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', request_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("创建旧版表失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭旧版数据库失败: %v", err)
	}
	store, err := NewBlockedStore(path)
	if err != nil {
		t.Fatalf("迁移旧版数据库失败: %v", err)
	}
	defer store.Close()
	stored, err := store.Record(context.Background(), BlockedLog{
		APIType: "messages", BlockType: BlockTypeSensitiveWord, ConversationID: "conv-migrated",
	})
	if err != nil || stored.ConversationID != "conv-migrated" {
		t.Fatalf("迁移后写入会话 ID 失败: %+v %v", stored, err)
	}
}

func TestBlockedStoreTruncationValidationAndLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blocked.db")
	store, err := NewBlockedStore(path)
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	ctx := context.Background()
	stored, err := store.Record(ctx, BlockedLog{
		APIType: " MESSAGES ", BlockType: " SENSITIVE_WORD ", PromptSnippet: strings.Repeat("敏", 600),
	})
	if err != nil {
		t.Fatalf("写入长摘要失败: %v", err)
	}
	if len([]rune(stored.PromptSnippet)) != maxPromptSnippetRunes || !strings.HasSuffix(stored.PromptSnippet, "...") {
		t.Fatalf("摘要截断错误: %d %q", len([]rune(stored.PromptSnippet)), stored.PromptSnippet)
	}
	if _, err := store.Record(ctx, BlockedLog{APIType: "unknown", BlockType: BlockTypeSensitiveWord}); err == nil {
		t.Fatal("未知 API 类型应返回错误")
	}
	if _, err := store.List(ctx, BlockedLogListOptions{Page: 1, PageSize: 101}); err == nil {
		t.Fatal("超大分页应返回错误")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭存储失败: %v", err)
	}
	if _, err := store.Record(ctx, BlockedLog{APIType: "messages", BlockType: BlockTypeSensitiveWord}); !errors.Is(err, ErrBlockedStoreClosed) {
		t.Fatalf("关闭后写入错误不正确: %v", err)
	}

	reopened, err := NewBlockedStore(path)
	if err != nil {
		t.Fatalf("重新打开存储失败: %v", err)
	}
	defer reopened.Close()
	page, err := reopened.List(ctx, BlockedLogListOptions{})
	if err != nil || page.Total != 1 {
		t.Fatalf("重开后数据未持久化: %+v %v", page, err)
	}
}

func TestBlockedStoreDeleteAndClear(t *testing.T) {
	store := openTestBlockedStore(t)
	ctx := context.Background()
	first, err := store.Record(ctx, BlockedLog{APIType: "chat", BlockType: BlockTypeSensitiveWord})
	if err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	if _, err := store.Record(ctx, BlockedLog{APIType: "gemini", BlockType: BlockTypeDangerousCmd}); err != nil {
		t.Fatalf("写入记录失败: %v", err)
	}
	deleted, err := store.Delete(ctx, first.ID)
	if err != nil || !deleted {
		t.Fatalf("删除记录失败: %v %v", deleted, err)
	}
	if deleted, err := store.Delete(ctx, first.ID); err != nil || deleted {
		t.Fatalf("重复删除结果错误: %v %v", deleted, err)
	}
	count, err := store.Clear(ctx)
	if err != nil || count != 1 {
		t.Fatalf("清空记录失败: %d %v", count, err)
	}
	page, err := store.List(ctx, BlockedLogListOptions{})
	if err != nil || page.Total != 0 || len(page.Logs) != 0 {
		t.Fatalf("清空后仍有记录: %+v %v", page, err)
	}
}

func openTestBlockedStore(t *testing.T) *BlockedStore {
	t.Helper()
	store, err := NewBlockedStore(filepath.Join(t.TempDir(), "blocked.db"))
	if err != nil {
		t.Fatalf("创建测试存储失败: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭测试存储失败: %v", err)
		}
	})
	return store
}
