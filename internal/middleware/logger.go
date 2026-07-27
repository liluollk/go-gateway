package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type LogEntry struct {
	Timestamp        time.Time `json:"timestamp"`
	TraceID          string    `json:"trace_id"`
	AppID            string    `json:"app_id"`
	Method           string    `json:"method"`
	Path             string    `json:"path"`
	StatusCode       int       `json:"status_code"`
	LatencyMs        int64     `json:"latency_ms"`
	Model            string    `json:"model,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty"`
	TotalTokens      int       `json:"total_tokens,omitempty"`
	Error            string    `json:"error,omitempty"`
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	rw.body.Write(b)
	return rw.ResponseWriter.Write(b)
}

type Logger struct {
	mu      sync.Mutex
	dir     string
	file    *os.File
	encoder *json.Encoder
}

func NewLogger(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	l := &Logger{dir: dir}
	if err := l.rotate(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		l.file.Close()
	}

	filename := filepath.Join(l.dir, fmt.Sprintf("gateway-%s.log", time.Now().Format("2006-01-02")))
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	l.file = f
	l.encoder = json.NewEncoder(f)
	return nil
}

func (l *Logger) Write(entry *LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 检查日期是否变更，变更则轮转
	if l.file != nil {
		info, _ := l.file.Stat()
		if info != nil {
			today := time.Now().Format("2006-01-02")
			expected := fmt.Sprintf("gateway-%s.log", today)
			if info.Name() != expected {
				l.rotate()
			}
		}
	}

	l.encoder.Encode(entry)
}

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
	}
}

// LoggingMiddleware 包装一个 handler，记录请求日志
func (l *Logger) LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		entry := &LogEntry{
			Timestamp:  start,
			TraceID:    GetTraceID(r.Context()),
			AppID:      GetAuthAppID(r.Context()),
			Method:     r.Method,
			Path:       r.URL.Path,
			StatusCode: rw.statusCode,
			LatencyMs:  time.Since(start).Milliseconds(),
		}

		l.Write(entry)
	})
}
