package metrics

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultRequestLogLimit     = 50
	defaultRequestLogRetention = 7
	requestLogQueueSize        = 4096
	requestLogFilePrefix       = "request-logs-"
	requestLogFileSuffix       = ".jsonl"
)

type RequestLogEntry struct {
	RequestID             string  `json:"requestId"`
	AttemptID             string  `json:"attemptId"`
	Timestamp             string  `json:"timestamp"`
	APIType               string  `json:"apiType"`
	Entry                 string  `json:"entry"`
	Status                string  `json:"status"`
	StatusCode            int     `json:"statusCode,omitempty"`
	Success               bool    `json:"success"`
	DurationMs            int64   `json:"durationMs"`
	FirstTokenMs          int64   `json:"firstTokenMs,omitempty"`
	Model                 string  `json:"model,omitempty"`
	ResolvedModel         string  `json:"resolvedModel,omitempty"`
	Transform             string  `json:"transform,omitempty"`
	InputTokens           int     `json:"inputTokens,omitempty"`
	OutputTokens          int     `json:"outputTokens,omitempty"`
	CacheCreationTokens   int     `json:"cacheCreationTokens,omitempty"`
	CacheReadTokens       int     `json:"cacheReadTokens,omitempty"`
	CacheCreation5mTokens int     `json:"cacheCreation5mTokens,omitempty"`
	CacheCreation1hTokens int     `json:"cacheCreation1hTokens,omitempty"`
	CacheTTL              string  `json:"cacheTTL,omitempty"`
	TPM                   float64 `json:"tpm,omitempty"`
	ChannelIndex          int     `json:"channelIndex"`
	ChannelName           string  `json:"channelName,omitempty"`
	BaseURL               string  `json:"baseUrl,omitempty"`
	KeyMask               string  `json:"keyMask,omitempty"`
	ErrorType             string  `json:"errorType,omitempty"`
	ErrorMessage          string  `json:"errorMessage,omitempty"`
	Retried               bool    `json:"retried"`
	Stream                bool    `json:"stream"`
	ConversationID        string  `json:"conversationId,omitempty"`
}

type RequestLogListOptions struct {
	APIType string
	Limit   int
}

type RequestLogStore struct {
	dir           string
	retentionDays int
	queue         chan RequestLogEntry
	done          chan struct{}
	closeOnce     sync.Once
	wg            sync.WaitGroup
}

func NewRequestLogStore(logDir string, retentionDays int) *RequestLogStore {
	if strings.TrimSpace(logDir) == "" {
		logDir = "logs"
	}
	if !filepath.IsAbs(logDir) {
		if abs, err := filepath.Abs(logDir); err == nil {
			logDir = abs
		}
	}
	if retentionDays <= 0 {
		retentionDays = defaultRequestLogRetention
	}
	_ = os.MkdirAll(logDir, 0755)

	store := &RequestLogStore{
		dir:           logDir,
		retentionDays: retentionDays,
		queue:         make(chan RequestLogEntry, requestLogQueueSize),
		done:          make(chan struct{}),
	}
	store.cleanupOldFiles()
	store.wg.Add(1)
	go store.run()
	return store
}

func (s *RequestLogStore) Record(entry RequestLogEntry) {
	if s == nil {
		return
	}
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().Format(time.RFC3339Nano)
	}
	entry.APIType = normalizeRequestLogAPIType(entry.APIType)
	entry.Entry = normalizeRequestLogEntry(entry.Entry, entry.APIType)
	if entry.TPM == 0 {
		entry.TPM = calculateRequestLogTPM(entry)
	}

	select {
	case s.queue <- entry:
	default:
	}
}

func (s *RequestLogStore) List(opts RequestLogListOptions) ([]RequestLogEntry, error) {
	if s == nil {
		return nil, nil
	}
	limit := opts.Limit
	if limit <= 0 || limit > defaultRequestLogLimit {
		limit = defaultRequestLogLimit
	}
	filter := normalizeRequestLogAPIType(opts.APIType)

	var entries []RequestLogEntry
	now := time.Now()
	for dayOffset := 0; dayOffset < s.retentionDays; dayOffset++ {
		date := now.AddDate(0, 0, -dayOffset).Format("2006-01-02")
		path := filepath.Join(s.dir, requestLogFilePrefix+date+requestLogFileSuffix)
		dayEntries, err := readRequestLogFile(path, filter)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for i := len(dayEntries) - 1; i >= 0; i-- {
			entries = append(entries, dayEntries[i])
			if len(entries) >= limit {
				return entries, nil
			}
		}
	}
	return entries, nil
}

func (s *RequestLogStore) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		close(s.done)
		s.wg.Wait()
	})
}

func (s *RequestLogStore) run() {
	defer s.wg.Done()

	var currentDate string
	var file *os.File
	var encoder *json.Encoder
	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer cleanupTicker.Stop()
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()
	writeEntry := func(entry RequestLogEntry) {
		date := requestLogDate(entry.Timestamp)
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		if date != currentDate {
			if file != nil {
				_ = file.Close()
			}
			path := filepath.Join(s.dir, requestLogFilePrefix+date+requestLogFileSuffix)
			_ = os.MkdirAll(s.dir, 0755)
			nextFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				file = nil
				encoder = nil
				currentDate = ""
				return
			}
			file = nextFile
			encoder = json.NewEncoder(file)
			currentDate = date
		}
		if encoder != nil {
			_ = encoder.Encode(entry)
		}
	}

	for {
		select {
		case entry := <-s.queue:
			writeEntry(entry)
		case <-cleanupTicker.C:
			s.cleanupOldFiles()
		case <-s.done:
			for {
				select {
				case entry := <-s.queue:
					writeEntry(entry)
				default:
					return
				}
			}
		}
	}
}

func (s *RequestLogStore) cleanupOldFiles() {
	if s == nil {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -(s.retentionDays - 1)).Format("2006-01-02")
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, requestLogFilePrefix) || !strings.HasSuffix(name, requestLogFileSuffix) {
			continue
		}
		date := strings.TrimSuffix(strings.TrimPrefix(name, requestLogFilePrefix), requestLogFileSuffix)
		if date < cutoff {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

func readRequestLogFile(path string, filter string) ([]RequestLogEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []RequestLogEntry
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var entry RequestLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entry.APIType = normalizeRequestLogAPIType(entry.APIType)
		entry.Entry = normalizeRequestLogEntry(entry.Entry, entry.APIType)
		if filter != "" && entry.APIType != filter {
			continue
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp < entries[j].Timestamp
	})
	return entries, nil
}

func requestLogDate(timestamp string) string {
	if timestamp == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		return t.Format("2006-01-02")
	}
	if len(timestamp) >= len("2006-01-02") {
		return timestamp[:len("2006-01-02")]
	}
	return ""
}

func normalizeRequestLogAPIType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "claude", "message", "messages":
		return "messages"
	case "codex", "response", "responses":
		return "responses"
	case "gemini":
		return "gemini"
	case "chat", "openai":
		return "chat"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeRequestLogEntry(entry string, apiType string) string {
	entry = strings.ToLower(strings.TrimSpace(entry))
	if entry != "" {
		return entry
	}
	switch normalizeRequestLogAPIType(apiType) {
	case "messages":
		return "claude"
	case "responses":
		return "codex"
	case "gemini":
		return "gemini"
	case "chat":
		return "chat"
	default:
		return apiType
	}
}

func calculateRequestLogTPM(entry RequestLogEntry) float64 {
	totalTokens := entry.InputTokens + entry.OutputTokens
	if totalTokens <= 0 || entry.DurationMs <= 0 {
		return 0
	}
	return float64(totalTokens) * 60000 / float64(entry.DurationMs)
}
