package conversation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxIdle         = 24 * time.Hour
	defaultCleanupInterval = 10 * time.Minute
)

type Observation struct {
	APIKind        string
	Model          string
	Stream         bool
	ConversationID string
	FallbackKey    string
	FirstPrompt    string
	Prompts        []string
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
}

func NewRegistry() *Registry {
	r := &Registry{
		records:       make(map[string]*Record),
		identityIndex: make(map[string]string),
		maxIdle:       defaultMaxIdle,
		stopCh:        make(chan struct{}),
	}
	go r.cleanupLoop()
	return r
}

func (r *Registry) Stop() {
	close(r.stopCh)
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
	rec.Stream = obs.Stream
	if rec.ActiveRequests < 0 {
		rec.ActiveRequests = 0
	}
	rec.ActiveRequests++
	rec.IsSending = rec.ActiveRequests > 0
	rec.LastSeenAt = now
	rec.LastRequestAt = now
	rec.RequestCount++

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
	return cloneRecord(rec), nil
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

func (r *Registry) cleanupLoop() {
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
		delete(r.identityIndex, rec.identityKey)
	}
}

func buildIdentityKey(obs Observation) string {
	token := firstNonEmpty(strings.TrimSpace(obs.ConversationID), strings.TrimSpace(obs.FallbackKey))
	if token == "" {
		token = generateID("anon")
	}
	kind := firstNonEmpty(strings.TrimSpace(obs.APIKind), "unknown")
	return strings.ToLower(strings.Join([]string{kind, token}, "|"))
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
	return &dst
}

func sortRecords(records []*Record) {
	for i := 0; i < len(records); i++ {
		for j := i + 1; j < len(records); j++ {
			if records[j].LastSeenAt.After(records[i].LastSeenAt) {
				records[i], records[j] = records[j], records[i]
			}
		}
	}
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
