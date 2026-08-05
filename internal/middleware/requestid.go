package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
)

// contextKey 是 context 中存储值的 key 类型，避免与其他包冲突。
type contextKey string

// TraceIDKey 是 context 中存储 Trace ID 的 key。
const TraceIDKey contextKey = "trace_id"

// generateTraceID 生成一个 32 位十六进制随机字符串作为 Trace ID。
// 使用 crypto/rand 保证全局唯一性；若随机数生成失败，降级使用时间戳。
func generateTraceID() string {
	b := make([]byte, 16) // 16 字节 = 32 位十六进制
	if _, err := rand.Read(b); err != nil {
		// 极端情况：随机数生成失败，用纳秒时间戳兜底
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// RequestID 返回一个 HTTP 中间件，为每个请求分配或传递 Trace ID。
// 优先级：请求头 X-Trace-Id > 自动生成。
// Trace ID 会注入到 context 和响应头 X-Trace-Id 中，实现全链路追踪。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 优先使用客户端传入的 Trace ID
		traceID := r.Header.Get("X-Trace-Id")
		if traceID == "" {
			traceID = generateTraceID()
		}
		// 注入 context 和响应头
		ctx := context.WithValue(r.Context(), TraceIDKey, traceID)
		w.Header().Set("X-Trace-Id", traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetTraceID 从 context 中提取 Trace ID。
func GetTraceID(ctx context.Context) string {
	if id, ok := ctx.Value(TraceIDKey).(string); ok {
		return id
	}
	return ""
}