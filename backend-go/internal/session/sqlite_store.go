package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type sqliteStore struct {
	db *sql.DB
}

func newSQLiteStore(path string) (*sqliteStore, error) {
	if path == "" {
		return nil, fmt.Errorf("session database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &sqliteStore{db: db}
	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *sqliteStore) initSchema() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS response_sessions (
		id TEXT PRIMARY KEY,
		conversation_id TEXT NOT NULL DEFAULT '',
		messages_json TEXT NOT NULL DEFAULT '[]',
		last_response_id TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		last_access_at INTEGER NOT NULL,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		has_vision_content INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return err
	}
	if err := s.ensureColumn("response_sessions", "conversation_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS response_session_mappings (
		response_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_response_sessions_conversation_id ON response_sessions(conversation_id)"); err != nil {
		return err
	}
	if _, err := s.db.Exec("CREATE INDEX IF NOT EXISTS idx_response_session_mappings_session_id ON response_session_mappings(session_id)"); err != nil {
		return err
	}
	return s.backfillConversationIDs()
}

func (s *sqliteStore) load() (map[string]*Session, map[string]string, error) {
	sessions := make(map[string]*Session)
	rows, err := s.db.Query(`SELECT id, conversation_id, messages_json, last_response_id, created_at, last_access_at,
		total_tokens, has_vision_content FROM response_sessions`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var session Session
		var messagesJSON string
		var createdAt, lastAccessAt int64
		var hasVision int
		if err := rows.Scan(&session.ID, &session.ConversationID, &messagesJSON, &session.LastResponseID, &createdAt, &lastAccessAt, &session.TotalTokens, &hasVision); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(messagesJSON), &session.Messages); err != nil {
			_ = rows.Close()
			return nil, nil, fmt.Errorf("解析 Responses 会话 %s 失败: %w", session.ID, err)
		}
		session.CreatedAt = time.Unix(createdAt, 0)
		session.LastAccessAt = time.Unix(lastAccessAt, 0)
		session.HasVisionContent = hasVision != 0
		sessions[session.ID] = &session
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	mappings := make(map[string]string)
	mappingRows, err := s.db.Query("SELECT response_id, session_id FROM response_session_mappings")
	if err != nil {
		return nil, nil, err
	}
	defer mappingRows.Close()
	for mappingRows.Next() {
		var responseID, sessionID string
		if err := mappingRows.Scan(&responseID, &sessionID); err != nil {
			return nil, nil, err
		}
		if _, ok := sessions[sessionID]; ok {
			mappings[responseID] = sessionID
		}
	}
	return sessions, mappings, mappingRows.Err()
}

func (s *sqliteStore) upsertSession(session *Session) error {
	messages, err := json.Marshal(session.Messages)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO response_sessions (
		id, conversation_id, messages_json, last_response_id, created_at, last_access_at, total_tokens, has_vision_content
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		conversation_id = excluded.conversation_id,
		messages_json = excluded.messages_json,
		last_response_id = excluded.last_response_id,
		last_access_at = excluded.last_access_at,
		total_tokens = excluded.total_tokens,
		has_vision_content = excluded.has_vision_content`,
		session.ID, session.ConversationID, string(messages), session.LastResponseID, session.CreatedAt.Unix(), session.LastAccessAt.Unix(),
		session.TotalTokens, boolToInt(session.HasVisionContent))
	return err
}

func (s *sqliteStore) upsertMapping(responseID string, sessionID string) error {
	_, err := s.db.Exec(`INSERT INTO response_session_mappings (response_id, session_id) VALUES (?, ?)
		ON CONFLICT(response_id) DO UPDATE SET session_id = excluded.session_id`, responseID, sessionID)
	return err
}

func (s *sqliteStore) deleteSession(sessionID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec("DELETE FROM response_session_mappings WHERE session_id = ?", sessionID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM response_sessions WHERE id = ?", sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) deleteConversationSessions(conversationID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM response_session_mappings WHERE session_id IN (
		SELECT id FROM response_sessions WHERE conversation_id = ?
	)`, conversationID); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM response_sessions WHERE conversation_id = ?", conversationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqliteStore) ensureColumn(tableName string, columnName string, declaration string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + tableName + ")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue interface{}
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == columnName {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.Exec("ALTER TABLE " + tableName + " ADD COLUMN " + columnName + " " + declaration)
	return err
}

func (s *sqliteStore) backfillConversationIDs() error {
	var aliasesTableExists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'conversation_aliases'`).Scan(&aliasesTableExists); err != nil {
		return err
	}
	if aliasesTableExists == 0 {
		return nil
	}
	_, err := s.db.Exec(`UPDATE response_sessions
	SET conversation_id = (
		SELECT ca.conversation_id
		FROM response_session_mappings AS rsm
		JOIN conversation_aliases AS ca ON ca.alias_key = lower('responses|' || rsm.response_id)
		WHERE rsm.session_id = response_sessions.id
		LIMIT 1
	)
	WHERE conversation_id = '' AND EXISTS (
		SELECT 1
		FROM response_session_mappings AS rsm
		JOIN conversation_aliases AS ca ON ca.alias_key = lower('responses|' || rsm.response_id)
		WHERE rsm.session_id = response_sessions.id
	)`)
	return err
}

func (s *sqliteStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
