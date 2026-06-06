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
	defaultCleanupInterval  = 10 * time.Minute
)

type Observation struct {
	APIKind       string
	Model         string
	Stream        bool
	ConversationID string
	FallbackKey   string
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
	ID            string         `json:"id"`
	APIKind       string         `json:"apiKind"`
	LastModel     string         `json:"lastModel,omitempty"`
	Stream        bool           `json:"stream"`
	FirstSeenAt   time.Time      `json:"firstSeenAt"`
	LastSeenAt    time.Time      `json:"lastSeenAt"`
	RequestCount  int64          `json:"requestCount"`
	ErrorCount    int64          `json:"errorCount"`
	LastError     string         `json:"lastError,omitempty"`
	RouteOverride *RouteOverride `json:"routeOverride,omitempty"`
	LastResolved  *ChannelRef    `json:"lastResolved,omitempty"`

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
			ID:           generateID("conv"),
			APIKind:      firstNonEmpty(obs.APIKind, "unknown"),
			LastModel:    obs.Model,
			Stream:       obs.Stream,
			FirstSeenAt:  now,
			LastSeenAt:   now,
			identityKey:  identityKey,
		}
		r.records[rec.ID] = rec
		r.identityIndex[identityKey] = rec.ID
	}

	rec.APIKind = firstNonEmpty(obs.APIKind, rec.APIKind)
	rec.LastModel = firstNonEmpty(obs.Model, rec.LastModel)
	rec.Stream = obs.Stream
	rec.LastSeenAt = now
	rec.RequestCount++

	return cloneRecord(rec)
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
	rec.ErrorCount++
	rec.LastError = truncate(errorMessage, 500)
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
