package conversation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxIdle         = 7 * 24 * time.Hour
	defaultCleanupInterval = 10 * time.Minute
)

type Observation struct {
	APIKind           string
	Model             string
	Stream            bool
	ConversationID    string
	FirstPrompt       string
	Prompts           []string
	ImageFingerprints []string
}

type RouteOverride struct {
	Kind         string    `json:"kind"`
	ChannelIndex int       `json:"channelIndex"`
	ChannelName  string    `json:"channelName,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type ChannelRef struct {
	Kind         string    `json:"kind"`
	ChannelIndex int       `json:"channelIndex"`
	ChannelName  string    `json:"channelName,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Record struct {
	ID                string         `json:"id"`
	Name              string         `json:"name,omitempty"`
	APIKind           string         `json:"apiKind"`
	LastModel         string         `json:"lastModel,omitempty"`
	LastResolvedModel string         `json:"lastResolvedModel,omitempty"`
	FirstPrompt       string         `json:"firstPrompt,omitempty"`
	Prompts           []string       `json:"prompts,omitempty"`
	Stream            bool           `json:"stream"`
	IsSending         bool           `json:"isSending"`
	ActiveRequests    int64          `json:"activeRequests,omitempty"`
	FirstSeenAt       time.Time      `json:"firstSeenAt"`
	LastSeenAt        time.Time      `json:"lastSeenAt"`
	LastRequestAt     time.Time      `json:"lastRequestAt"`
	LastCompletedAt   time.Time      `json:"lastCompletedAt,omitempty"`
	RequestCount      int64          `json:"requestCount"`
	ErrorCount        int64          `json:"errorCount"`
	LastError         string         `json:"lastError,omitempty"`
	RouteOverride     *RouteOverride `json:"routeOverride,omitempty"`
	LastResolved      *ChannelRef    `json:"lastResolved,omitempty"`
	ImageFingerprints []string       `json:"imageFingerprints,omitempty"`

	identityKey string
}

func (r *Record) RouteOverrideString() string {
	if r == nil || r.RouteOverride == nil {
		return ""
	}
	return strings.ToLower(strings.Join([]string{
		r.RouteOverride.Kind,
		fmt.Sprintf("%d", r.RouteOverride.ChannelIndex),
		r.RouteOverride.ChannelName,
	}, " "))
}

type Registry struct {
	mu            sync.RWMutex
	records       map[string]*Record
	identityIndex map[string]string
	maxIdle       time.Duration
	stopCh        chan struct{}
	doneCh        chan struct{}
	stopOnce      sync.Once
	store         *SQLiteStore
}

func NewRegistry() *Registry {
	return newRegistry(nil)
}

func NewPersistentRegistry(dbPath string) (*Registry, error) {
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		return nil, err
	}
	r := newRegistry(store)
	records, err := store.LoadAll()
	if err != nil {
		r.Stop()
		return nil, err
	}
	for _, rec := range records {
		if rec == nil || rec.ID == "" || rec.identityKey == "" {
			continue
		}
		rec.IsSending = false
		rec.ActiveRequests = 0
		r.records[rec.ID] = rec
		r.identityIndex[rec.identityKey] = rec.ID
	}
	aliases, err := store.LoadAliases()
	if err != nil {
		r.Stop()
		return nil, err
	}
	for alias, recordID := range aliases {
		if _, ok := r.records[recordID]; ok {
			r.identityIndex[alias] = recordID
		}
	}
	r.cleanup()
	return r, nil
}

func newRegistry(store *SQLiteStore) *Registry {
	r := &Registry{
		records:       make(map[string]*Record),
		identityIndex: make(map[string]string),
		maxIdle:       defaultMaxIdle,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		store:         store,
	}
	go r.cleanupLoop()
	return r
}

func (r *Registry) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		<-r.doneCh
		if r.store != nil {
			if err := r.store.Close(); err != nil {
				log.Printf("[Conversation] 关闭持久化存储失败: %v", err)
			}
		}
	})
}

func (r *Registry) ObserveRequest(obs Observation) *Record {
	now := time.Now()
	identityKey := buildIdentityKey(obs)

	r.mu.Lock()
	defer r.mu.Unlock()

	recordID, ok := r.identityIndex[identityKey]
	var rec *Record
	if ok {
		rec = r.records[recordID]
	}
	if rec == nil {
		rec = &Record{
			ID:            generateID("conv"),
			APIKind:       firstNonEmpty(obs.APIKind, "unknown"),
			LastModel:     obs.Model,
			FirstPrompt:   firstPromptFromObservation(obs),
			Stream:        obs.Stream,
			IsSending:     true,
			FirstSeenAt:   now,
			LastSeenAt:    now,
			LastRequestAt: now,
			identityKey:   identityKey,
		}
		appendPrompts(rec, promptsFromObservation(obs)...)
		r.records[rec.ID] = rec
		r.identityIndex[identityKey] = rec.ID
	}

	rec.APIKind = firstNonEmpty(obs.APIKind, rec.APIKind)
	rec.LastModel = firstNonEmpty(obs.Model, rec.LastModel)
	if rec.FirstPrompt == "" {
		rec.FirstPrompt = firstPromptFromObservation(obs)
	}
	appendPrompts(rec, promptsFromObservation(obs)...)
	appendImageFingerprints(rec, obs.ImageFingerprints...)
	rec.Stream = obs.Stream
	if rec.ActiveRequests < 0 {
		rec.ActiveRequests = 0
	}
	rec.ActiveRequests++
	rec.IsSending = rec.ActiveRequests > 0
	rec.LastSeenAt = now
	rec.LastRequestAt = now
	rec.RequestCount++

	r.persistLocked(rec)
	return cloneRecord(rec)
}

func (r *Registry) MarkAttempt(recordID string, kind string, channelIndex int, channelName string, requestedModel string, resolvedModel string, stream bool) {
	if strings.TrimSpace(recordID) == "" {
		return
	}
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	rec := r.records[recordID]
	if rec == nil {
		return
	}
	rec.APIKind = firstNonEmpty(kind, rec.APIKind)
	rec.LastModel = firstNonEmpty(requestedModel, rec.LastModel)
	rec.LastResolvedModel = firstNonEmpty(resolvedModel, rec.LastResolvedModel)
	rec.Stream = stream
	rec.IsSending = rec.ActiveRequests > 0
	rec.LastSeenAt = now
	rec.LastResolved = &ChannelRef{
		Kind:         kind,
		ChannelIndex: channelIndex,
		ChannelName:  channelName,
		UpdatedAt:    now,
	}
	r.persistLocked(rec)
}

func (r *Registry) MarkSuccess(recordID string, kind string, channelIndex int, channelName string) {
	if strings.TrimSpace(recordID) == "" {
		return
	}
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	rec := r.records[recordID]
	if rec == nil {
		return
	}
	rec.APIKind = firstNonEmpty(kind, rec.APIKind)
	rec.LastSeenAt = now
	rec.LastCompletedAt = now
	rec.LastError = ""
	rec.LastResolved = &ChannelRef{
		Kind:         kind,
		ChannelIndex: channelIndex,
		ChannelName:  channelName,
		UpdatedAt:    now,
	}
	r.persistLocked(rec)
}

func (r *Registry) MarkFailure(recordID string, kind string, errorMessage string) {
	if strings.TrimSpace(recordID) == "" {
		return
	}
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	rec := r.records[recordID]
	if rec == nil {
		return
	}
	rec.APIKind = firstNonEmpty(kind, rec.APIKind)
	rec.LastSeenAt = now
	rec.LastCompletedAt = now
	rec.ErrorCount++
	rec.LastError = truncate(errorMessage, 500)
	r.persistLocked(rec)
}

func (r *Registry) MarkComplete(recordID string, kind string) {
	if strings.TrimSpace(recordID) == "" {
		return
	}
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	rec := r.records[recordID]
	if rec == nil {
		return
	}
	rec.APIKind = firstNonEmpty(kind, rec.APIKind)
	if rec.ActiveRequests > 0 {
		rec.ActiveRequests--
	}
	rec.IsSending = rec.ActiveRequests > 0
	rec.LastSeenAt = now
	rec.LastCompletedAt = now
	r.persistLocked(rec)
}

func (r *Registry) List() []*Record {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Record, 0, len(r.records))
	for _, rec := range r.records {
		if rec == nil {
			continue
		}
		result = append(result, cloneRecord(rec))
	}

	sortRecords(result)
	return result
}

func (r *Registry) Get(recordID string) (*Record, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec := r.records[recordID]
	if rec == nil {
		return nil, false
	}
	return cloneRecord(rec), true
}

func (r *Registry) SetRouteOverride(recordID string, kind string, channelIndex int, channelName string) (*Record, error) {
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	rec := r.records[recordID]
	if rec == nil {
		return nil, fmt.Errorf("conversation not found")
	}

	rec.RouteOverride = &RouteOverride{
		Kind:         strings.TrimSpace(kind),
		ChannelIndex: channelIndex,
		ChannelName:  strings.TrimSpace(channelName),
		UpdatedAt:    now,
	}
	rec.LastSeenAt = now

	r.persistLocked(rec)
	return cloneRecord(rec), nil
}

func (r *Registry) ClearRouteOverride(recordID string) (*Record, error) {
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	rec := r.records[recordID]
	if rec == nil {
		return nil, fmt.Errorf("conversation not found")
	}
	rec.RouteOverride = nil
	rec.LastSeenAt = now
	r.persistLocked(rec)
	return cloneRecord(rec), nil
}

func (r *Registry) SetName(recordID string, name string) (*Record, error) {
	name = truncate(name, 120)
	if name == "" {
		return nil, fmt.Errorf("conversation name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.records[recordID]
	if rec == nil {
		return nil, fmt.Errorf("conversation not found")
	}
	rec.Name = name
	rec.LastSeenAt = time.Now()
	r.persistLocked(rec)
	return cloneRecord(rec), nil
}

func (r *Registry) Delete(recordID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.records[recordID]
	if rec == nil {
		return fmt.Errorf("conversation not found")
	}
	if r.store != nil {
		if err := r.store.Delete(recordID); err != nil {
			return err
		}
	}
	delete(r.records, recordID)
	r.removeIdentityKeysLocked(recordID)
	return nil
}

// AssociateExternalID 将客户端后续会携带的外部响应 ID 关联到已有对话。
// 目前主要用于 Responses API 的 previous_response_id 链。
func (r *Registry) AssociateExternalID(recordID string, apiKind string, externalID string) error {
	if strings.TrimSpace(recordID) == "" || strings.TrimSpace(externalID) == "" {
		return nil
	}
	alias := explicitIdentityKey(apiKind, externalID)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.records[recordID] == nil {
		return fmt.Errorf("conversation not found")
	}
	if existingID, ok := r.identityIndex[alias]; ok && existingID != recordID {
		return fmt.Errorf("external conversation ID is already associated with another conversation")
	}
	if r.store != nil {
		if err := r.store.UpsertAlias(alias, recordID); err != nil {
			return err
		}
	}
	r.identityIndex[alias] = recordID
	return nil
}

func (r *Registry) GetRouteOverride(recordID string) (*RouteOverride, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec := r.records[recordID]
	if rec == nil || rec.RouteOverride == nil {
		return nil, false
	}
	override := *rec.RouteOverride
	return &override, true
}

// LoadImageUnderstanding 读取单张图片已持久化的理解结果。
func (r *Registry) LoadImageUnderstanding(cacheKey string) (string, bool, error) {
	if r == nil || r.store == nil {
		return "", false, nil
	}
	return r.store.LoadImageUnderstanding(cacheKey)
}

// SaveImageUnderstanding 持久化单张图片的理解结果。
func (r *Registry) SaveImageUnderstanding(cacheKey string, result string) error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.UpsertImageUnderstanding(cacheKey, result)
}

func (r *Registry) cleanupLoop() {
	defer close(r.doneCh)
	ticker := time.NewTicker(defaultCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			r.cleanup()
		case <-r.stopCh:
			return
		}
	}
}

func (r *Registry) cleanup() {
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	for id, rec := range r.records {
		if rec == nil || now.Sub(rec.LastSeenAt) <= r.maxIdle {
			continue
		}
		delete(r.records, id)
		r.removeIdentityKeysLocked(id)
		if r.store != nil {
			if err := r.store.Delete(id); err != nil {
				log.Printf("[Conversation] 清理过期对话失败: %v", err)
			}
		}
	}
}

func (r *Registry) removeIdentityKeysLocked(recordID string) {
	for identityKey, currentRecordID := range r.identityIndex {
		if currentRecordID == recordID {
			delete(r.identityIndex, identityKey)
		}
	}
}

func buildIdentityKey(obs Observation) string {
	convID := strings.TrimSpace(obs.ConversationID)
	if convID != "" {
		return explicitIdentityKey(obs.APIKind, convID)
	}

	// 无可靠会话标识时不能依据用户、模型或首条提示词猜测，
	// 否则不同 agent 对话会被错误合并。
	token := generateID("anon")
	kind := firstNonEmpty(strings.TrimSpace(obs.APIKind), "unknown")
	return strings.ToLower(strings.Join([]string{kind, token}, "|"))
}

func explicitIdentityKey(apiKind string, value string) string {
	kind := firstNonEmpty(strings.TrimSpace(apiKind), "unknown")
	return strings.ToLower(strings.Join([]string{kind, strings.TrimSpace(value)}, "|"))
}

func cloneRecord(src *Record) *Record {
	if src == nil {
		return nil
	}
	dst := *src
	if src.RouteOverride != nil {
		override := *src.RouteOverride
		dst.RouteOverride = &override
	}
	if src.LastResolved != nil {
		resolved := *src.LastResolved
		dst.LastResolved = &resolved
	}
	if src.Prompts != nil {
		dst.Prompts = make([]string, len(src.Prompts))
		copy(dst.Prompts, src.Prompts)
	}
	if src.ImageFingerprints != nil {
		dst.ImageFingerprints = append([]string{}, src.ImageFingerprints...)
	}
	return &dst
}

func sortRecords(records []*Record) {
	// 按 ID 固定顺序，避免 LastSeenAt/状态变化导致列表跳动
	sort.Slice(records, func(i, j int) bool {
		return records[i].ID < records[j].ID
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPromptFromObservation(obs Observation) string {
	prompts := promptsFromObservation(obs)
	if len(prompts) > 0 {
		return prompts[0]
	}
	return truncate(obs.FirstPrompt, 300)
}

func promptsFromObservation(obs Observation) []string {
	values := make([]string, 0, len(obs.Prompts)+1)
	if obs.FirstPrompt != "" {
		values = append(values, obs.FirstPrompt)
	}
	values = append(values, obs.Prompts...)
	return cleanPromptList(values, 3)
}

func appendPrompts(rec *Record, prompts ...string) {
	if rec == nil || len(rec.Prompts) >= 3 {
		return
	}
	for _, prompt := range cleanPromptList(prompts, 3) {
		if len(rec.Prompts) >= 3 {
			return
		}
		exists := false
		for _, current := range rec.Prompts {
			if current == prompt {
				exists = true
				break
			}
		}
		if !exists {
			rec.Prompts = append(rec.Prompts, prompt)
		}
	}
}

func appendImageFingerprints(rec *Record, fingerprints ...string) {
	if rec == nil {
		return
	}
	seen := make(map[string]bool, len(rec.ImageFingerprints))
	for _, current := range rec.ImageFingerprints {
		seen[current] = true
	}
	for _, fingerprint := range fingerprints {
		fingerprint = strings.TrimSpace(fingerprint)
		if fingerprint == "" || seen[fingerprint] {
			continue
		}
		rec.ImageFingerprints = append(rec.ImageFingerprints, fingerprint)
		seen[fingerprint] = true
	}
}

func (r *Registry) persistLocked(rec *Record) {
	if r.store == nil || rec == nil {
		return
	}
	if err := r.store.Upsert(rec); err != nil {
		log.Printf("[Conversation] 保存对话失败: %v", err)
	}
}

func cleanPromptList(values []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	result := make([]string, 0, limit)
	seen := make(map[string]bool)
	for _, value := range values {
		prompt := truncate(value, 300)
		if prompt == "" || seen[prompt] {
			continue
		}
		seen[prompt] = true
		result = append(result, prompt)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func generateID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(buf))
}
