package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/BenedictKing/claude-proxy/internal/types"
	"github.com/BenedictKing/claude-proxy/internal/utils"
)

// Session 会话数据结构
type Session struct {
	ID               string                // sess_xxxxx
	ConversationID   string                // 对话注册表中的 conv_xxxxx
	Messages         []types.ResponsesItem // 完整对话历史
	LastResponseID   string                // 最后一个 response ID
	CreatedAt        time.Time
	LastAccessAt     time.Time
	TotalTokens      int
	HasVisionContent bool // 会话历史是否包含图片内容
}

// SessionManager 会话管理器
type SessionManager struct {
	sessions        map[string]*Session // sessionID → Session
	responseMapping map[string]string   // responseID → sessionID
	mu              sync.RWMutex

	// 清理配置
	maxAge      time.Duration // 最大保留时间
	maxMessages int           // 单会话消息上限，<= 0 表示不限制
	maxTokens   int           // 单会话 Token 上限，<= 0 表示不限制

	// 资源限制
	maxSessions int // 全局最大 session 数，0 表示不限制
	store       *sqliteStore
	stopCh      chan struct{}
	doneCh      chan struct{}
	stopOnce    sync.Once
}

// NewSessionManager 创建会话管理器
// maxSessions 参数已弃用，请使用 SetMaxSessions() 设置上限
func NewSessionManager(maxAge time.Duration, maxMessages int, maxTokens int) *SessionManager {
	sm := newSessionManager(maxAge, maxMessages, maxTokens, nil)
	go sm.cleanupLoop()
	return sm
}

func NewPersistentSessionManager(path string, maxAge time.Duration, maxMessages int, maxTokens int) (*SessionManager, error) {
	store, err := newSQLiteStore(path)
	if err != nil {
		return nil, err
	}
	sm := newSessionManager(maxAge, maxMessages, maxTokens, store)
	sessions, mappings, err := store.load()
	if err != nil {
		_ = store.close()
		return nil, err
	}
	sm.sessions = sessions
	sm.responseMapping = mappings
	sm.cleanup()
	go sm.cleanupLoop()
	return sm, nil
}

func newSessionManager(maxAge time.Duration, maxMessages int, maxTokens int, store *sqliteStore) *SessionManager {
	return &SessionManager{
		sessions:        make(map[string]*Session),
		responseMapping: make(map[string]string),
		maxAge:          maxAge,
		maxMessages:     maxMessages,
		maxTokens:       maxTokens,
		maxSessions:     0, // 默认不限制，由调用方按需设置
		store:           store,
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}
}

func (sm *SessionManager) Stop() {
	if sm == nil {
		return
	}
	sm.stopOnce.Do(func() {
		close(sm.stopCh)
		<-sm.doneCh
		if sm.store != nil {
			if err := sm.store.close(); err != nil {
				log.Printf("[Session] 关闭持久化存储失败: %v", err)
			}
		}
	})
}

// SetMaxSessions 设置全局最大 session 数量上限
// 设置为 0 表示不限制（默认）
// 达到上限时，新会话会触发淘汰最久未访问的 session
func (sm *SessionManager) SetMaxSessions(limit int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.maxSessions = limit
}

// GetOrCreateSession 获取或创建会话
func (sm *SessionManager) GetOrCreateSession(previousResponseID string) (*Session, error) {
	return sm.GetOrCreateSessionForConversation(previousResponseID, "")
}

// GetOrCreateSessionForConversation 获取或创建会话，并将 Responses 会话链关联到持久化对话记录。
func (sm *SessionManager) GetOrCreateSessionForConversation(previousResponseID string, conversationID string) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	conversationID = strings.TrimSpace(conversationID)

	// 如果提供了 previousResponseID，尝试查找对应的会话
	if previousResponseID != "" {
		if sessionID, ok := sm.responseMapping[previousResponseID]; ok {
			if session, exists := sm.sessions[sessionID]; exists {
				previousConversationID := session.ConversationID
				if err := bindConversation(session, conversationID); err != nil {
					return nil, err
				}
				session.LastAccessAt = time.Now()
				if err := sm.persistSessionLocked(session); err != nil {
					session.ConversationID = previousConversationID
					return nil, err
				}
				return session, nil
			}
		}
		// 兼容升级前只保存在内存、重启后已经丢失的 Responses 链。
		// 无法恢复旧消息内容，但允许从当前输入继续，避免所有渠道构建请求失败并返回 503。
		log.Printf("[Session-Recovery] previous_response_id %s 未找到持久化会话，将从当前输入恢复为空会话", previousResponseID)
		return sm.createSessionLocked(previousResponseID, conversationID)
	}

	return sm.createSessionLocked("", conversationID)
}

func (sm *SessionManager) createSessionLocked(previousResponseID string, conversationID string) (*Session, error) {
	sm.evictIfNeededLocked()
	now := time.Now()
	session := &Session{
		ID:             generateID("sess"),
		ConversationID: conversationID,
		Messages:       []types.ResponsesItem{},
		LastResponseID: previousResponseID,
		CreatedAt:      now,
		LastAccessAt:   now,
	}
	sm.sessions[session.ID] = session
	if previousResponseID != "" {
		sm.responseMapping[previousResponseID] = session.ID
	}
	if err := sm.persistSessionLocked(session); err != nil {
		delete(sm.sessions, session.ID)
		delete(sm.responseMapping, previousResponseID)
		return nil, err
	}
	if previousResponseID != "" && sm.store != nil {
		if err := sm.store.upsertMapping(previousResponseID, session.ID); err != nil {
			delete(sm.sessions, session.ID)
			delete(sm.responseMapping, previousResponseID)
			if cleanupErr := sm.store.deleteSession(session.ID); cleanupErr != nil {
				return nil, fmt.Errorf("保存 response ID 映射失败: %v；清理未完成会话失败: %w", err, cleanupErr)
			}
			return nil, err
		}
	}
	log.Printf("[Session-Create] 创建新会话: %s (总数: %d)", session.ID, len(sm.sessions))
	return session, nil
}

func bindConversation(session *Session, conversationID string) error {
	if session == nil || conversationID == "" {
		return nil
	}
	if session.ConversationID == "" {
		session.ConversationID = conversationID
		return nil
	}
	if session.ConversationID != conversationID {
		return fmt.Errorf("Responses 会话 %s 已关联到其他对话 %s", session.ID, session.ConversationID)
	}
	return nil
}

// evictIfNeededLocked 当 session 数达到上限时，淘汰最久未访问的 session
// 调用方必须持有写锁
func (sm *SessionManager) evictIfNeededLocked() {
	if sm.maxSessions <= 0 || len(sm.sessions) < sm.maxSessions {
		return
	}

	// 找到最久未访问的 session（不包含 responseID 映射仍在使用的）
	var oldestID string
	var oldestTime time.Time
	for id, s := range sm.sessions {
		if s.LastAccessAt.Before(oldestTime) || oldestID == "" {
			oldestID = id
			oldestTime = s.LastAccessAt
		}
	}

	if oldestID != "" {
		session := sm.sessions[oldestID]
		log.Printf("[Session-Evict] 会话数已达上限 (%d)，淘汰最久未访问会话: %s (最后访问: %v 前)",
			sm.maxSessions, oldestID, time.Since(oldestTime))
		// 清理关联的 responseMapping
		for respID, sessID := range sm.responseMapping {
			if sessID == oldestID {
				delete(sm.responseMapping, respID)
			}
		}
		delete(sm.sessions, oldestID)
		if sm.store != nil {
			if err := sm.store.deleteSession(oldestID); err != nil {
				log.Printf("[Session-Evict] 删除持久化会话失败: %v", err)
			}
		}
		// 标记 session 对象可被 GC 回收
		_ = session
	}
}

// RecordResponseMapping 记录 responseID 到 sessionID 的映射
func (sm *SessionManager) RecordResponseMapping(responseID, sessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.responseMapping[responseID] = sessionID
	if sm.store != nil {
		if err := sm.store.upsertMapping(responseID, sessionID); err != nil {
			delete(sm.responseMapping, responseID)
			return err
		}
	}
	log.Printf("[Session-Mapping] 记录映射: %s -> %s", responseID, sessionID)
	return nil
}

// GetSessionByResponseID 根据 responseID 获取会话
func (sm *SessionManager) GetSessionByResponseID(responseID string) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if responseID == "" {
		return nil, fmt.Errorf("response_id 不能为空")
	}

	sessionID, ok := sm.responseMapping[responseID]
	if !ok {
		return nil, fmt.Errorf("无效的 previous_response_id: %s", responseID)
	}

	session, exists := sm.sessions[sessionID]
	if !exists {
		return nil, fmt.Errorf("无效的 previous_response_id: %s", responseID)
	}

	session.LastAccessAt = time.Now()
	if err := sm.persistSessionLocked(session); err != nil {
		return nil, err
	}
	return session, nil
}

// AppendMessage 追加消息到会话
func (sm *SessionManager) AppendMessage(sessionID string, item types.ResponsesItem, tokensUsed int) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}

	session.Messages = append(session.Messages, item)
	session.TotalTokens += tokensUsed
	session.LastAccessAt = time.Now()
	if utils.ResponsesItemHasVisionContent(item) {
		session.HasVisionContent = true
	}
	return sm.persistSessionLocked(session)
}

// MarkSessionHasVisionContent 显式标记会话历史含图
func (sm *SessionManager) MarkSessionHasVisionContent(sessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}

	session.HasVisionContent = true
	session.LastAccessAt = time.Now()
	return sm.persistSessionLocked(session)
}

// UpdateLastResponseID 更新会话的最后一个 responseID
func (sm *SessionManager) UpdateLastResponseID(sessionID, responseID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return fmt.Errorf("会话不存在: %s", sessionID)
	}

	session.LastResponseID = responseID
	session.LastAccessAt = time.Now()
	return sm.persistSessionLocked(session)
}

// GetSession 获取会话（只读）
func (sm *SessionManager) GetSession(sessionID string) (*Session, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, exists := sm.sessions[sessionID]
	if !exists {
		return nil, fmt.Errorf("会话不存在: %s", sessionID)
	}

	return session, nil
}

// DeleteConversation 删除一个持久化对话下的全部 Responses 会话、消息和 response ID 映射。
func (sm *SessionManager) DeleteConversation(conversationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("conversation_id 不能为空")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sessionIDs := make(map[string]struct{})
	for sessionID, current := range sm.sessions {
		if current != nil && current.ConversationID == conversationID {
			sessionIDs[sessionID] = struct{}{}
		}
	}
	if sm.store != nil {
		if err := sm.store.deleteConversationSessions(conversationID); err != nil {
			return fmt.Errorf("删除 Responses 持久化会话失败: %w", err)
		}
	}
	for sessionID := range sessionIDs {
		delete(sm.sessions, sessionID)
	}
	for responseID, sessionID := range sm.responseMapping {
		if _, ok := sessionIDs[sessionID]; ok {
			delete(sm.responseMapping, responseID)
		}
	}
	if len(sessionIDs) > 0 {
		log.Printf("[Session-Delete] 已删除对话 %s 的 %d 条 Responses 会话链", conversationID, len(sessionIDs))
	}
	return nil
}

// cleanupLoop 定期清理过期会话
func (sm *SessionManager) cleanupLoop() {
	defer close(sm.doneCh)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm.cleanup()
		case <-sm.stopCh:
			return
		}
	}
}

// cleanup 执行清理逻辑
func (sm *SessionManager) cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	removedSessions := 0
	removedMappings := 0

	// 清理过期会话
	for sessionID, session := range sm.sessions {
		shouldRemove := false

		// 时间过期
		if now.Sub(session.LastAccessAt) > sm.maxAge {
			shouldRemove = true
			log.Printf("[Session-Cleanup] 清理过期会话 (时间): %s (最后访问: %v 前)", sessionID, now.Sub(session.LastAccessAt))
		}

		// 可选的资源上限；主程序传 0，表示只按七天保留期清理。
		if sm.maxMessages > 0 && len(session.Messages) > sm.maxMessages {
			shouldRemove = true
			log.Printf("[Session-Cleanup] 清理过期会话 (消息数): %s (%d 条)", sessionID, len(session.Messages))
		}

		// Token 超限
		if sm.maxTokens > 0 && session.TotalTokens > sm.maxTokens {
			shouldRemove = true
			log.Printf("[Session-Cleanup] 清理过期会话 (Token): %s (%d tokens)", sessionID, session.TotalTokens)
		}

		if shouldRemove {
			delete(sm.sessions, sessionID)
			if sm.store != nil {
				if err := sm.store.deleteSession(sessionID); err != nil {
					log.Printf("[Session-Cleanup] 删除持久化会话失败: %v", err)
				}
			}
			removedSessions++
		}
	}

	// 清理孤立的 responseID 映射
	for responseID, sessionID := range sm.responseMapping {
		if _, exists := sm.sessions[sessionID]; !exists {
			delete(sm.responseMapping, responseID)
			removedMappings++
		}
	}

	if removedSessions > 0 || removedMappings > 0 {
		log.Printf("[Session-Cleanup] 清理完成: 删除 %d 个会话, %d 个映射", removedSessions, removedMappings)
		log.Printf("[Session-Stats] 当前活跃会话: %d 个, 映射: %d 个", len(sm.sessions), len(sm.responseMapping))
	}
}

func (sm *SessionManager) persistSessionLocked(session *Session) error {
	if sm.store == nil || session == nil {
		return nil
	}
	return sm.store.upsertSession(session)
}

// GetStats 获取统计信息
func (sm *SessionManager) GetStats() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return map[string]interface{}{
		"total_sessions": len(sm.sessions),
		"total_mappings": len(sm.responseMapping),
	}
}

// generateID 生成唯一ID
func generateID(prefix string) string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		// 降级方案：使用时间戳
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(bytes))
}
