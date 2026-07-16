package conversation

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct{ db *sql.DB }

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		return nil, fmt.Errorf("conversation database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS conversations (
		id TEXT PRIMARY KEY, identity_key TEXT NOT NULL UNIQUE, api_kind TEXT NOT NULL, name TEXT NOT NULL DEFAULT '',
		last_model TEXT NOT NULL DEFAULT '', last_resolved_model TEXT NOT NULL DEFAULT '', first_prompt TEXT NOT NULL DEFAULT '',
		prompts_json TEXT NOT NULL DEFAULT '[]', stream INTEGER NOT NULL DEFAULT 0,
		first_seen_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, last_request_at INTEGER NOT NULL, last_completed_at INTEGER NOT NULL DEFAULT 0,
		request_count INTEGER NOT NULL DEFAULT 0, error_count INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
		route_override_json TEXT NOT NULL DEFAULT '', last_resolved_json TEXT NOT NULL DEFAULT '', image_fingerprints_json TEXT NOT NULL DEFAULT '[]')`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS conversation_aliases (
		alias_key TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL
	)`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// 旧版全局图片缓存不绑定对话，无法满足删除对话即失效的语义。
	if _, err = db.Exec(`DROP TABLE IF EXISTS image_understandings`); err != nil {
		_ = db.Close()
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS conversation_image_understandings (
		conversation_id TEXT NOT NULL,
		cache_key TEXT NOT NULL,
		result TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_used_at INTEGER NOT NULL,
		PRIMARY KEY (conversation_id, cache_key),
		FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
	)`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err = db.Exec(`CREATE INDEX IF NOT EXISTS idx_conversation_image_understandings_conversation_id
		ON conversation_image_understandings(conversation_id)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) LoadAll() ([]*Record, error) {
	rows, err := s.db.Query(`SELECT id, identity_key, api_kind, name, last_model, last_resolved_model, first_prompt, prompts_json, stream,
		first_seen_at, last_seen_at, last_request_at, last_completed_at, request_count, error_count, last_error,
		route_override_json, last_resolved_json, image_fingerprints_json FROM conversations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (s *SQLiteStore) Upsert(rec *Record) error {
	prompts, err := json.Marshal(rec.Prompts)
	if err != nil {
		return err
	}
	fingerprints, err := json.Marshal(rec.ImageFingerprints)
	if err != nil {
		return err
	}
	route, err := marshalOptional(rec.RouteOverride)
	if err != nil {
		return err
	}
	resolved, err := marshalOptional(rec.LastResolved)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO conversations (id, identity_key, api_kind, name, last_model, last_resolved_model, first_prompt, prompts_json, stream,
		first_seen_at, last_seen_at, last_request_at, last_completed_at, request_count, error_count, last_error, route_override_json, last_resolved_json, image_fingerprints_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, last_model=excluded.last_model, last_resolved_model=excluded.last_resolved_model,
		first_prompt=excluded.first_prompt, prompts_json=excluded.prompts_json, stream=excluded.stream, last_seen_at=excluded.last_seen_at,
		last_request_at=excluded.last_request_at, last_completed_at=excluded.last_completed_at, request_count=excluded.request_count,
		error_count=excluded.error_count, last_error=excluded.last_error, route_override_json=excluded.route_override_json,
		last_resolved_json=excluded.last_resolved_json, image_fingerprints_json=excluded.image_fingerprints_json`,
		rec.ID, rec.identityKey, rec.APIKind, rec.Name, rec.LastModel, rec.LastResolvedModel, rec.FirstPrompt, string(prompts), boolToInt(rec.Stream),
		rec.FirstSeenAt.Unix(), rec.LastSeenAt.Unix(), rec.LastRequestAt.Unix(), rec.LastCompletedAt.Unix(), rec.RequestCount, rec.ErrorCount,
		rec.LastError, route, resolved, string(fingerprints))
	return err
}

func (s *SQLiteStore) Delete(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec("DELETE FROM conversation_image_understandings WHERE conversation_id = ?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM conversation_aliases WHERE conversation_id = ?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM conversations WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) DeleteAll() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, table := range []string{
		"conversation_image_understandings",
		"conversation_aliases",
		"conversations",
	} {
		if _, err = tx.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *SQLiteStore) LoadAliases() (map[string]string, error) {
	rows, err := s.db.Query("SELECT alias_key, conversation_id FROM conversation_aliases")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	aliases := make(map[string]string)
	for rows.Next() {
		var alias, conversationID string
		if err := rows.Scan(&alias, &conversationID); err != nil {
			return nil, err
		}
		aliases[alias] = conversationID
	}
	return aliases, rows.Err()
}
func (s *SQLiteStore) UpsertAlias(alias string, conversationID string) error {
	_, err := s.db.Exec(`INSERT INTO conversation_aliases (alias_key, conversation_id) VALUES (?, ?)
		ON CONFLICT(alias_key) DO UPDATE SET conversation_id = excluded.conversation_id`, alias, conversationID)
	return err
}

func (s *SQLiteStore) LoadConversationImageUnderstanding(conversationID string, cacheKey string) (string, bool, error) {
	var result string
	err := s.db.QueryRow(`SELECT result FROM conversation_image_understandings
		WHERE conversation_id = ? AND cache_key = ?`, conversationID, cacheKey).Scan(&result)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if _, err := s.db.Exec(`UPDATE conversation_image_understandings SET last_used_at = ?
		WHERE conversation_id = ? AND cache_key = ?`, time.Now().Unix(), conversationID, cacheKey); err != nil {
		return "", false, err
	}
	return result, true, nil
}

func (s *SQLiteStore) UpsertConversationImageUnderstanding(conversationID string, cacheKey string, result string) error {
	now := time.Now().Unix()
	resultSet, err := s.db.Exec(`INSERT INTO conversation_image_understandings (
		conversation_id, cache_key, result, created_at, last_used_at
	) SELECT ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM conversations WHERE id = ?)
	ON CONFLICT(conversation_id, cache_key) DO UPDATE SET
		result = excluded.result, last_used_at = excluded.last_used_at`, conversationID, cacheKey, result, now, now, conversationID)
	if err != nil {
		return err
	}
	rows, err := resultSet.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("conversation not found")
	}
	return nil
}
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

type scanner interface{ Scan(...interface{}) error }

func scanRecord(row scanner) (*Record, error) {
	var rec Record
	var prompts, route, resolved, fingerprints string
	var stream int
	var first, last, request, completed int64
	err := row.Scan(&rec.ID, &rec.identityKey, &rec.APIKind, &rec.Name, &rec.LastModel, &rec.LastResolvedModel, &rec.FirstPrompt, &prompts, &stream,
		&first, &last, &request, &completed, &rec.RequestCount, &rec.ErrorCount, &rec.LastError, &route, &resolved, &fingerprints)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(prompts), &rec.Prompts); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(fingerprints), &rec.ImageFingerprints); err != nil {
		return nil, err
	}
	if optionalJSONPresent(route) {
		var override RouteOverride
		if err = json.Unmarshal([]byte(route), &override); err != nil {
			return nil, err
		}
		// 兼容旧版本把 nil 写成 JSON null、重启后又污染成空对象的记录。
		// 空 kind 不是合法的固定渠道，必须继续视为“未固定”。
		if strings.TrimSpace(override.Kind) != "" {
			rec.RouteOverride = &override
		}
	}
	if optionalJSONPresent(resolved) {
		var lastResolved ChannelRef
		if err = json.Unmarshal([]byte(resolved), &lastResolved); err != nil {
			return nil, err
		}
		if strings.TrimSpace(lastResolved.Kind) != "" {
			rec.LastResolved = &lastResolved
		}
	}
	rec.Stream = stream != 0
	rec.FirstSeenAt = unixTime(first)
	rec.LastSeenAt = unixTime(last)
	rec.LastRequestAt = unixTime(request)
	rec.LastCompletedAt = unixTime(completed)
	return &rec, nil
}
func marshalOptional[T any](v *T) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}
func optionalJSONPresent(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.EqualFold(value, "null")
}
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func unixTime(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}
