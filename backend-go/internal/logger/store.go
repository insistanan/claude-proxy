package logger

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type writeKind int

const (
	writeApp writeKind = iota
	writeTraffic
	writeRequest
	writeFlush
)

type queuedWrite struct {
	kind      writeKind
	app       AppLog
	traffic   TrafficLog
	request   RequestLogRecord
	flushDone chan struct{}
}

// Store 是 logs.db 的唯一写入口。服务进程持有一份；CLI 只读打开。
type Store struct {
	db        *sql.DB
	dbPath    string
	queue     chan queuedWrite
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	readOnly  bool
}

func openSQLite(path string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("日志数据库路径不能为空")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, fmt.Errorf("创建日志数据库目录失败: %w", err)
		}
	}
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		dsn = "file:app_logs?mode=memory&cache=shared&_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开日志数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return db, nil
}

func initLogSchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS app_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			unix_milli INTEGER NOT NULL,
			timestamp TEXT NOT NULL,
			tag TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_app_logs_unix ON app_logs(unix_milli, id)`,
		`CREATE TABLE IF NOT EXISTS traffic_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			unix_milli INTEGER NOT NULL,
			timestamp TEXT NOT NULL,
			request_id TEXT NOT NULL,
			attempt_id TEXT NOT NULL DEFAULT '',
			phase TEXT NOT NULL,
			api_type TEXT NOT NULL DEFAULT '',
			method TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			status_code INTEGER NOT NULL DEFAULT 0,
			stream INTEGER NOT NULL DEFAULT 0,
			headers_json TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			truncated INTEGER NOT NULL DEFAULT 0,
			original_bytes INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_logs_unix ON traffic_logs(unix_milli, id)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_logs_request ON traffic_logs(request_id, unix_milli, id)`,
		`CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			unix_milli INTEGER NOT NULL,
			timestamp TEXT NOT NULL,
			request_id TEXT NOT NULL DEFAULT '',
			api_type TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_unix ON request_logs(unix_milli, id)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_api ON request_logs(api_type, unix_milli, id)`,
		`CREATE INDEX IF NOT EXISTS idx_request_logs_request ON request_logs(request_id, unix_milli, id)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("初始化日志表失败: %w", err)
		}
	}
	return nil
}

// Open 打开可写日志库并启动后台写入 / 清理。
func Open(path string) (*Store, error) {
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	if err := initLogSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{
		db:     db,
		dbPath: path,
		queue:  make(chan queuedWrite, writeQueueSize),
		done:   make(chan struct{}),
	}
	store.cleanupExpired()
	store.wg.Add(2)
	go store.writeLoop()
	go store.cleanupLoop()
	return store, nil
}

// OpenReadOnly 供 CLI 查询。不启动写入循环。
func OpenReadOnly(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultDBPath
	}
	if path != ":memory:" {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("日志数据库不存在: %s（服务启动后才会创建）", path)
			}
			return nil, fmt.Errorf("访问日志数据库失败: %w", err)
		}
	}
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	store := &Store{
		db:       db,
		dbPath:   path,
		readOnly: true,
	}
	return store, nil
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.dbPath
}

func (s *Store) enqueue(item queuedWrite) {
	if s == nil || s.readOnly {
		return
	}
	select {
	case s.queue <- item:
	default:
		fmt.Fprintf(os.Stderr, "[Logger] 警告: 日志写入队列已满，丢弃一条 %d\n", item.kind)
	}
}

func (s *Store) RecordAppLog(entry AppLog) {
	if s == nil {
		return
	}
	entry = normalizeAppLog(entry)
	s.enqueue(queuedWrite{kind: writeApp, app: entry})
}

func (s *Store) RecordTraffic(entry TrafficLog) {
	if s == nil {
		return
	}
	entry = normalizeTrafficLog(entry)
	s.enqueue(queuedWrite{kind: writeTraffic, traffic: entry})
}

func (s *Store) RecordRequest(entry RequestLogRecord) {
	if s == nil {
		return
	}
	entry = normalizeRequestRecord(entry)
	s.enqueue(queuedWrite{kind: writeRequest, request: entry})
}

func (s *Store) Flush() {
	if s == nil || s.readOnly {
		return
	}
	done := make(chan struct{})
	select {
	case s.queue <- queuedWrite{kind: writeFlush, flushDone: done}:
		<-done
	case <-s.done:
	}
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		if !s.readOnly {
			close(s.done)
			s.wg.Wait()
		}
		closeErr = s.db.Close()
	})
	return closeErr
}

func (s *Store) writeLoop() {
	defer s.wg.Done()
	for {
		select {
		case item := <-s.queue:
			s.applyWrite(item)
		case <-s.done:
			for {
				select {
				case item := <-s.queue:
					s.applyWrite(item)
				default:
					return
				}
			}
		}
	}
}

func (s *Store) applyWrite(item queuedWrite) {
	switch item.kind {
	case writeFlush:
		if item.flushDone != nil {
			close(item.flushDone)
		}
	case writeApp:
		_, err := s.db.Exec(`INSERT INTO app_logs (unix_milli, timestamp, tag, message) VALUES (?, ?, ?, ?)`,
			item.app.UnixMilli, item.app.Timestamp, item.app.Tag, item.app.Message)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[Logger] 警告: 写入 app_logs 失败: %v\n", err)
		}
	case writeTraffic:
		streamValue := 0
		if item.traffic.Stream {
			streamValue = 1
		}
		truncatedValue := 0
		if item.traffic.Truncated {
			truncatedValue = 1
		}
		_, err := s.db.Exec(`INSERT INTO traffic_logs (
			unix_milli, timestamp, request_id, attempt_id, phase, api_type, method, url,
			status_code, stream, headers_json, body, truncated, original_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.traffic.UnixMilli, item.traffic.Timestamp, item.traffic.RequestID, item.traffic.AttemptID,
			item.traffic.Phase, item.traffic.APIType, item.traffic.Method, item.traffic.URL,
			item.traffic.StatusCode, streamValue, item.traffic.HeadersJSON, item.traffic.Body,
			truncatedValue, item.traffic.OriginalBytes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[Logger] 警告: 写入 traffic_logs 失败: %v\n", err)
		}
	case writeRequest:
		_, err := s.db.Exec(`INSERT INTO request_logs (unix_milli, timestamp, request_id, api_type, payload_json) VALUES (?, ?, ?, ?, ?)`,
			item.request.UnixMilli, item.request.Timestamp, item.request.RequestID, item.request.APIType, string(item.request.Payload))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[Logger] 警告: 写入 request_logs 失败: %v\n", err)
		}
	}
}

func (s *Store) cleanupLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanupExpired()
		case <-s.done:
			return
		}
	}
}

func (s *Store) cleanupExpired() {
	if s == nil || s.db == nil {
		return
	}
	cutoff := RetentionCutoff(time.Now()).UnixMilli()
	statements := []string{
		`DELETE FROM app_logs WHERE unix_milli < ?`,
		`DELETE FROM traffic_logs WHERE unix_milli < ?`,
		`DELETE FROM request_logs WHERE unix_milli < ?`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement, cutoff); err != nil {
			fmt.Fprintf(os.Stderr, "[Logger] 警告: 清理过期日志失败: %v\n", err)
		}
	}
}

// RetentionCutoff 返回“昨天 00:00:00”本地时间。早于该时刻的日志会被删除，
// 因此库里只保留今天与昨天。
func RetentionCutoff(now time.Time) time.Time {
	localNow := now.In(time.Local)
	year, month, day := localNow.Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, time.Local)
	return today.AddDate(0, 0, -1)
}

func (s *Store) QueryAppLogs(ctx context.Context, opts QueryOptions) ([]AppLog, error) {
	if s == nil {
		return nil, nil
	}
	query := `SELECT id, unix_milli, timestamp, tag, message FROM app_logs WHERE unix_milli >= ? AND unix_milli <= ?`
	args := []any{opts.From, opts.To}
	query += ` ORDER BY unix_milli ASC, id ASC`
	if opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 app_logs 失败: %w", err)
	}
	defer rows.Close()
	entries := make([]AppLog, 0)
	for rows.Next() {
		var entry AppLog
		if err := rows.Scan(&entry.ID, &entry.UnixMilli, &entry.Timestamp, &entry.Tag, &entry.Message); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) QueryTrafficLogs(ctx context.Context, opts QueryOptions) ([]TrafficLog, error) {
	if s == nil {
		return nil, nil
	}
	query := `SELECT id, unix_milli, timestamp, request_id, attempt_id, phase, api_type, method, url,
		status_code, stream, headers_json, body, truncated, original_bytes
		FROM traffic_logs WHERE unix_milli >= ? AND unix_milli <= ?`
	args := []any{opts.From, opts.To}
	if opts.RequestID != "" {
		query += ` AND request_id = ?`
		args = append(args, opts.RequestID)
	}
	query += ` ORDER BY unix_milli ASC, id ASC`
	if opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 traffic_logs 失败: %w", err)
	}
	defer rows.Close()
	entries := make([]TrafficLog, 0)
	for rows.Next() {
		var entry TrafficLog
		var streamValue, truncatedValue int
		if err := rows.Scan(
			&entry.ID, &entry.UnixMilli, &entry.Timestamp, &entry.RequestID, &entry.AttemptID,
			&entry.Phase, &entry.APIType, &entry.Method, &entry.URL, &entry.StatusCode,
			&streamValue, &entry.HeadersJSON, &entry.Body, &truncatedValue, &entry.OriginalBytes,
		); err != nil {
			return nil, err
		}
		entry.Stream = streamValue != 0
		entry.Truncated = truncatedValue != 0
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) QueryRequestLogs(ctx context.Context, opts QueryOptions) ([]json.RawMessage, error) {
	if s == nil {
		return nil, nil
	}
	query := `SELECT payload_json FROM request_logs WHERE 1=1`
	args := make([]any, 0, 6)
	if opts.From > 0 {
		query += ` AND unix_milli >= ?`
		args = append(args, opts.From)
	}
	if opts.To > 0 {
		query += ` AND unix_milli <= ?`
		args = append(args, opts.To)
	}
	if opts.RequestID != "" {
		query += ` AND request_id = ?`
		args = append(args, opts.RequestID)
	}
	if opts.APIType != "" {
		query += ` AND api_type = ?`
		args = append(args, opts.APIType)
	}
	order := ` ORDER BY unix_milli DESC, id DESC`
	if opts.From > 0 || opts.To > 0 {
		order = ` ORDER BY unix_milli ASC, id ASC`
	}
	query += order
	if opts.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, opts.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 request_logs 失败: %w", err)
	}
	defer rows.Close()
	entries := make([]json.RawMessage, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		entries = append(entries, json.RawMessage(payload))
	}
	return entries, rows.Err()
}

func normalizeAppLog(entry AppLog) AppLog {
	now := time.Now()
	if entry.UnixMilli == 0 {
		if parsed, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			entry.UnixMilli = parsed.UnixMilli()
			now = parsed
		} else {
			entry.UnixMilli = now.UnixMilli()
		}
	} else {
		now = time.UnixMilli(entry.UnixMilli)
	}
	if entry.Timestamp == "" {
		entry.Timestamp = now.UTC().Format(time.RFC3339Nano)
	}
	return entry
}

func normalizeTrafficLog(entry TrafficLog) TrafficLog {
	now := time.Now()
	if entry.UnixMilli == 0 {
		entry.UnixMilli = now.UnixMilli()
	} else {
		now = time.UnixMilli(entry.UnixMilli)
	}
	if entry.Timestamp == "" {
		entry.Timestamp = now.UTC().Format(time.RFC3339Nano)
	}
	if entry.Body != "" {
		prepared, originalBytes, truncated := PrepareBody([]byte(entry.Body))
		entry.Body = prepared
		if entry.OriginalBytes == 0 {
			entry.OriginalBytes = originalBytes
		}
		entry.Truncated = entry.Truncated || truncated
	}
	return entry
}

func normalizeRequestRecord(entry RequestLogRecord) RequestLogRecord {
	now := time.Now()
	if entry.UnixMilli == 0 {
		if parsed, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			entry.UnixMilli = parsed.UnixMilli()
			now = parsed
		} else {
			entry.UnixMilli = now.UnixMilli()
		}
	}
	if entry.Timestamp == "" {
		entry.Timestamp = now.UTC().Format(time.RFC3339Nano)
	}
	if entry.APIType == "" {
		entry.APIType = "unknown"
	}
	if len(entry.Payload) == 0 {
		entry.Payload = []byte("{}")
	}
	return entry
}
