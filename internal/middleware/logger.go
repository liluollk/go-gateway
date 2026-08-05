package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogEntry 是一条请求日志记录，包含请求元信息、响应状态和 Token 用量。
type LogEntry struct {
	Timestamp        time.Time `json:"timestamp"`         // 请求开始时间
	TraceID          string    `json:"trace_id"`          // 全链路追踪 ID
	Method           string    `json:"method"`            // HTTP 方法
	Path             string    `json:"path"`              // 请求路径
	StatusCode       int       `json:"status_code"`       // HTTP 响应状态码
	LatencyMs        int64     `json:"latency_ms"`        // 请求耗时（毫秒）
	Model            string    `json:"model,omitempty"`   // 请求的模型名
	PromptTokens     int       `json:"prompt_tokens,omitempty"`     // 输入 token 数
	CompletionTokens int       `json:"completion_tokens,omitempty"` // 输出 token 数
	TotalTokens      int       `json:"total_tokens,omitempty"`      // 总 token 数
	Error            string    `json:"error,omitempty"`   // 错误信息（如有）
}

// responseWriter 包装 http.ResponseWriter，用于捕获响应状态码。
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader 重写以捕获状态码，再调用原始 ResponseWriter。
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush 实现 http.Flusher 接口，将底层缓冲数据发送到客户端。
// 如果底层 ResponseWriter 不支持 Flusher，则无操作。
func (rw *responseWriter) Flush() {
	if flusher, ok := rw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Logger 是网关的请求日志记录器，以 JSON 格式写入日志文件，按天自动轮转。
type Logger struct {
	mu      sync.Mutex    // 保护并发写入和文件轮转
	dir     string        // 日志文件目录
	file    *os.File      // 当前日志文件
	encoder *json.Encoder // JSON 编码器（每行一条 JSON 记录）
}

// NewLogger 创建日志记录器，在指定目录下按日期创建日志文件。
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

// rotate 公开的轮转方法，加锁后调用 rotateLocked。
func (l *Logger) rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotateLocked()
}

// rotateLocked 执行实际的日志文件轮转：关闭旧文件，创建当天日期的新文件。
func (l *Logger) rotateLocked() error {
	if l.file != nil {
		l.file.Close()
	}

	// 文件命名格式：gateway-2006-01-02.log
	filename := filepath.Join(l.dir, fmt.Sprintf("gateway-%s.log", time.Now().Format("2006-01-02")))
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	l.file = f
	l.encoder = json.NewEncoder(f)
	return nil
}

// Write 写入一条日志记录。写入前自动检测日期是否变更，变更则触发轮转。
func (l *Logger) Write(entry *LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 检查日期是否变更，变更则轮转到新文件
	if l.file != nil {
		info, _ := l.file.Stat()
		if info != nil {
			today := time.Now().Format("2006-01-02")
			expected := fmt.Sprintf("gateway-%s.log", today)
			if info.Name() != expected {
				l.rotateLocked()
			}
		}
	}

	l.encoder.Encode(entry)
}

// Close 关闭日志文件。
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
	}
}

// LoggingMiddleware 返回一个 HTTP 中间件，记录每个请求的耗时和状态码。
// 日志在请求处理完成后写入，因此可以捕获最终状态码和延迟。
func (l *Logger) LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// 用自定义 responseWriter 包装，捕获状态码
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		entry := &LogEntry{
			Timestamp:  start,
			TraceID:    GetTraceID(r.Context()),
			Method:     r.Method,
			Path:       r.URL.Path,
			StatusCode: rw.statusCode,
			LatencyMs:  time.Since(start).Milliseconds(),
		}

		l.Write(entry)
	})
}