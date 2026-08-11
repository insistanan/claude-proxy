package sensitive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	BlockTypeSensitiveWord = "sensitive_word"
	BlockTypeSensitiveInfo = "sensitive_info"
	BlockTypeCredential    = "credential"
	BlockTypeDangerousCmd  = "dangerous_cmd"

	defaultBlockedLogPageSize = 20
	maxBlockedLogPageSize     = 100
	maxPromptSnippetRunes     = 500
	blockedLogTimeLayout      = "2006-01-02T15:04:05.000000000Z07:00"
)

var ErrBlockedStoreClosed = errors.New("拦截记录存储已关闭")

// BlockedLog 表示一次内容安全拦截或掩码事件。
type BlockedLog struct {
	ID            int64     `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	APIType       string    `json:"apiType"`
	BlockType     string    `json:"blockType"`
	RuleName      string    `json:"ruleName,omitempty"`
	PromptSnippet string    `json:"promptSnippet,omitempty"`
	ChannelName   string    `json:"channelName,omitempty"`
	Model         string    `json:"model,omitempty"`
	RequestID     string    `json:"requestId,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// BlockedLogListOptions 描述拦截记录的筛选与分页参数。
type BlockedLogListOptions struct {
	APIType   string
	BlockType string
	From      time.Time
	To        time.Time
	Page      int
	PageSize  int
}

// BlockedLogPage 是分页查询结果。
type BlockedLogPage struct {
	Logs     []BlockedLog `json:"logs"`
	Total    int64        `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"pageSize"`
}

// BlockedStore 使用独立 blocked_logs 表持久化内容安全事件。
type BlockedStore struct {
	db        *sql.DB
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
}

// NewBlockedStore 创建并初始化拦截记录存储。
func NewBlockedStore(path string) (*BlockedStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("拦截记录数据库路径不能为空")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, fmt.Errorf("创建拦截记录数据库目录失败: %w", err)
		}
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		dsn = "file:blocked_logs?mode=memory&cache=shared&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开拦截记录数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &BlockedStore{db: db}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *BlockedStore) initSchema() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS blocked_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			api_type TEXT NOT NULL,
			block_type TEXT NOT NULL,
			rule_name TEXT NOT NULL DEFAULT '',
			prompt_snippet TEXT NOT NULL DEFAULT '',
			channel_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_blocked_logs_timestamp ON blocked_logs(timestamp DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_blocked_logs_type_timestamp ON blocked_logs(block_type, timestamp DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_blocked_logs_api_timestamp ON blocked_logs(api_type, timestamp DESC, id DESC)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			return fmt.Errorf("初始化拦截记录表失败: %w", err)
		}
	}
	return nil
}

// Record 写入一条拦截记录并返回持久化后的数据。
func (s *BlockedStore) Record(ctx context.Context, entry BlockedLog) (BlockedLog, error) {
	if err := s.ensureOpen(); err != nil {
		return BlockedLog{}, err
	}
	entry, err := normalizeBlockedLog(entry)
	if err != nil {
		return BlockedLog{}, err
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	} else {
		entry.Timestamp = entry.Timestamp.UTC()
	}
	entry.CreatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `INSERT INTO blocked_logs (
		timestamp, api_type, block_type, rule_name, prompt_snippet, channel_name, model, request_id, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp.Format(blockedLogTimeLayout), entry.APIType, entry.BlockType, entry.RuleName,
		entry.PromptSnippet, entry.ChannelName, entry.Model, entry.RequestID, entry.CreatedAt.Format(blockedLogTimeLayout),
	)
	if err != nil {
		return BlockedLog{}, fmt.Errorf("写入拦截记录失败: %w", err)
	}
	entry.ID, err = result.LastInsertId()
	if err != nil {
		return BlockedLog{}, fmt.Errorf("读取拦截记录 ID 失败: %w", err)
	}
	return entry, nil
}

// List 按条件分页查询拦截记录。
func (s *BlockedStore) List(ctx context.Context, options BlockedLogListOptions) (BlockedLogPage, error) {
	if err := s.ensureOpen(); err != nil {
		return BlockedLogPage{}, err
	}
	options, err := normalizeBlockedLogListOptions(options)
	if err != nil {
		return BlockedLogPage{}, err
	}
	where, args := blockedLogWhere(options)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blocked_logs`+where, args...).Scan(&total); err != nil {
		return BlockedLogPage{}, fmt.Errorf("统计拦截记录失败: %w", err)
	}

	queryArgs := append(append([]any(nil), args...), options.PageSize, (options.Page-1)*options.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT id, timestamp, api_type, block_type, rule_name,
		prompt_snippet, channel_name, model, request_id, created_at
		FROM blocked_logs`+where+` ORDER BY timestamp DESC, id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return BlockedLogPage{}, fmt.Errorf("查询拦截记录失败: %w", err)
	}
	defer rows.Close()

	logs := make([]BlockedLog, 0, options.PageSize)
	for rows.Next() {
		entry, err := scanBlockedLog(rows)
		if err != nil {
			return BlockedLogPage{}, err
		}
		logs = append(logs, entry)
	}
	if err := rows.Err(); err != nil {
		return BlockedLogPage{}, fmt.Errorf("读取拦截记录失败: %w", err)
	}
	return BlockedLogPage{Logs: logs, Total: total, Page: options.Page, PageSize: options.PageSize}, nil
}

// Get 返回指定 ID 的拦截记录。
func (s *BlockedStore) Get(ctx context.Context, id int64) (BlockedLog, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BlockedLog{}, false, err
	}
	if id <= 0 {
		return BlockedLog{}, false, fmt.Errorf("拦截记录 ID 必须大于 0")
	}
	entry, err := scanBlockedLog(s.db.QueryRowContext(ctx, `SELECT id, timestamp, api_type, block_type, rule_name,
		prompt_snippet, channel_name, model, request_id, created_at FROM blocked_logs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return BlockedLog{}, false, nil
	}
	if err != nil {
		return BlockedLog{}, false, err
	}
	return entry, true, nil
}

// Delete 删除指定 ID 的拦截记录。
func (s *BlockedStore) Delete(ctx context.Context, id int64) (bool, error) {
	if err := s.ensureOpen(); err != nil {
		return false, err
	}
	if id <= 0 {
		return false, fmt.Errorf("拦截记录 ID 必须大于 0")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM blocked_logs WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("删除拦截记录失败: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("读取删除结果失败: %w", err)
	}
	return count > 0, nil
}

// Clear 清空全部拦截记录并返回删除数量。
func (s *BlockedStore) Clear(ctx context.Context) (int64, error) {
	if err := s.ensureOpen(); err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM blocked_logs`)
	if err != nil {
		return 0, fmt.Errorf("清空拦截记录失败: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("读取清空结果失败: %w", err)
	}
	return count, nil
}

// Close 幂等关闭数据库连接。
func (s *BlockedStore) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		closeErr = s.db.Close()
		s.mu.Unlock()
	})
	return closeErr
}

func (s *BlockedStore) ensureOpen() error {
	if s == nil || s.db == nil {
		return ErrBlockedStoreClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrBlockedStoreClosed
	}
	return nil
}

func normalizeBlockedLog(entry BlockedLog) (BlockedLog, error) {
	entry.APIType = strings.ToLower(strings.TrimSpace(entry.APIType))
	entry.BlockType = strings.ToLower(strings.TrimSpace(entry.BlockType))
	entry.RuleName = strings.TrimSpace(entry.RuleName)
	entry.PromptSnippet = truncatePromptSnippet(strings.TrimSpace(entry.PromptSnippet))
	entry.ChannelName = strings.TrimSpace(entry.ChannelName)
	entry.Model = strings.TrimSpace(entry.Model)
	entry.RequestID = strings.TrimSpace(entry.RequestID)
	if !validBlockedLogAPIType(entry.APIType) {
		return BlockedLog{}, fmt.Errorf("不支持的 API 类型 %q", entry.APIType)
	}
	if !validBlockType(entry.BlockType) {
		return BlockedLog{}, fmt.Errorf("不支持的拦截类型 %q", entry.BlockType)
	}
	for label, value := range map[string]string{
		"规则名": entry.RuleName, "渠道名": entry.ChannelName, "模型": entry.Model, "请求 ID": entry.RequestID,
	} {
		if len([]rune(value)) > 256 {
			return BlockedLog{}, fmt.Errorf("%s不能超过 256 个字符", label)
		}
	}
	return entry, nil
}

func normalizeBlockedLogListOptions(options BlockedLogListOptions) (BlockedLogListOptions, error) {
	options.APIType = strings.ToLower(strings.TrimSpace(options.APIType))
	options.BlockType = strings.ToLower(strings.TrimSpace(options.BlockType))
	if options.APIType != "" && !validBlockedLogAPIType(options.APIType) {
		return BlockedLogListOptions{}, fmt.Errorf("不支持的 API 类型 %q", options.APIType)
	}
	if options.BlockType != "" && !validBlockType(options.BlockType) {
		return BlockedLogListOptions{}, fmt.Errorf("不支持的拦截类型 %q", options.BlockType)
	}
	if options.Page == 0 {
		options.Page = 1
	}
	if options.Page < 1 {
		return BlockedLogListOptions{}, fmt.Errorf("页码必须大于 0")
	}
	if options.PageSize == 0 {
		options.PageSize = defaultBlockedLogPageSize
	}
	if options.PageSize < 1 || options.PageSize > maxBlockedLogPageSize {
		return BlockedLogListOptions{}, fmt.Errorf("每页数量必须在 1 到 %d 之间", maxBlockedLogPageSize)
	}
	if !options.From.IsZero() {
		options.From = options.From.UTC()
	}
	if !options.To.IsZero() {
		options.To = options.To.UTC()
	}
	if !options.From.IsZero() && !options.To.IsZero() && options.From.After(options.To) {
		return BlockedLogListOptions{}, fmt.Errorf("开始时间不能晚于结束时间")
	}
	return options, nil
}

func blockedLogWhere(options BlockedLogListOptions) (string, []any) {
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if options.APIType != "" {
		conditions = append(conditions, "api_type = ?")
		args = append(args, options.APIType)
	}
	if options.BlockType != "" {
		conditions = append(conditions, "block_type = ?")
		args = append(args, options.BlockType)
	}
	if !options.From.IsZero() {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, options.From.Format(blockedLogTimeLayout))
	}
	if !options.To.IsZero() {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, options.To.Format(blockedLogTimeLayout))
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

type blockedLogScanner interface {
	Scan(...any) error
}

func scanBlockedLog(scanner blockedLogScanner) (BlockedLog, error) {
	var entry BlockedLog
	var timestamp string
	var createdAt string
	if err := scanner.Scan(&entry.ID, &timestamp, &entry.APIType, &entry.BlockType, &entry.RuleName,
		&entry.PromptSnippet, &entry.ChannelName, &entry.Model, &entry.RequestID, &createdAt); err != nil {
		return BlockedLog{}, err
	}
	var err error
	entry.Timestamp, err = time.Parse(blockedLogTimeLayout, timestamp)
	if err != nil {
		return BlockedLog{}, fmt.Errorf("解析拦截时间失败: %w", err)
	}
	entry.CreatedAt, err = time.Parse(blockedLogTimeLayout, createdAt)
	if err != nil {
		return BlockedLog{}, fmt.Errorf("解析创建时间失败: %w", err)
	}
	return entry, nil
}

func truncatePromptSnippet(value string) string {
	runes := []rune(value)
	if len(runes) <= maxPromptSnippetRunes {
		return value
	}
	return string(runes[:maxPromptSnippetRunes-3]) + "..."
}

func validBlockedLogAPIType(value string) bool {
	switch value {
	case "messages", "responses", "chat", "gemini":
		return true
	default:
		return false
	}
}

func validBlockType(value string) bool {
	switch value {
	case BlockTypeSensitiveWord, BlockTypeSensitiveInfo, BlockTypeCredential, BlockTypeDangerousCmd:
		return true
	default:
		return false
	}
}
