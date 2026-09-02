package logger

import (
	"bytes"
	"strings"
	"sync"
	"time"
)

type lineWriter struct {
	store *Store
	mu    sync.Mutex
	buf   []byte
}

func newLineWriter(store *Store) *lineWriter {
	return &lineWriter{store: store}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	if w == nil || w.store == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		index := bytes.IndexByte(w.buf, '\n')
		if index < 0 {
			break
		}
		line := string(w.buf[:index])
		w.buf = w.buf[index+1:]
		w.store.RecordAppLog(parseStdLogLine(line))
	}
	return len(p), nil
}

func parseStdLogLine(line string) AppLog {
	line = strings.TrimRight(line, "\r")
	entry := AppLog{Message: line}
	const prefixLayout = "2006/01/02 15:04:05.000000"
	if len(line) >= len(prefixLayout) {
		if parsed, err := time.ParseInLocation(prefixLayout, line[:len(prefixLayout)], time.Local); err == nil {
			entry.UnixMilli = parsed.UnixMilli()
			entry.Timestamp = parsed.UTC().Format(time.RFC3339Nano)
			entry.Message = strings.TrimSpace(line[len(prefixLayout):])
		}
	}
	if entry.UnixMilli == 0 {
		now := time.Now()
		entry.UnixMilli = now.UnixMilli()
		entry.Timestamp = now.UTC().Format(time.RFC3339Nano)
	}
	entry.Tag, entry.Message = splitLogTag(entry.Message)
	return entry
}

func splitLogTag(message string) (string, string) {
	message = strings.TrimSpace(message)
	if !strings.HasPrefix(message, "[") {
		return "", message
	}
	end := strings.IndexByte(message, ']')
	if end <= 1 {
		return "", message
	}
	return message[1:end], strings.TrimSpace(message[end+1:])
}
