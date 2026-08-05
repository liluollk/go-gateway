// middleware 包包含网关的 HTTP 中间件：鉴权、日志、请求追踪、限流。
package middleware

import (
	"net/http"
	"sync"

	"golang.org/x/time/rate"

	"go-gateway/internal/errors"
)

// RateLimiter 是本地内存令牌桶限流器，按 API Key 独立限流。
// 支持 QPS（每秒请求数）和 RPM（每分钟请求数）双维度，
// 任一维度触发限制即返回 429。
type RateLimiter struct {
	mu     sync.RWMutex
	limits map[string]*rateLimitPair // api_key → 限流配置
}

// rateLimitPair 存储单个 API Key 的 QPS 和 RPM 令牌桶。
type rateLimitPair struct {
	qpsLimiter *rate.Limiter
	rpmLimiter *rate.Limiter
}

// RateLimitConfig 是限流配置，用于初始化限流器。
type RateLimitConfig struct {
	QPS int
	RPM int
}

// NewRateLimiter 创建限流器实例，根据配置初始化各 API Key 的令牌桶。
// limits 的 key 为 API Key 值，value 为该 Key 的 QPS/RPM 限制。
// 未配置的 Key 不受限流控制。QPS 或 RPM 为 0 表示该维度不限流。
func NewRateLimiter(limits map[string]RateLimitConfig) *RateLimiter {
	rl := &RateLimiter{
		limits: make(map[string]*rateLimitPair),
	}
	for apiKey, cfg := range limits {
		pair := &rateLimitPair{}
		if cfg.QPS > 0 {
			pair.qpsLimiter = rate.NewLimiter(rate.Limit(cfg.QPS), cfg.QPS*2)
		}
		if cfg.RPM > 0 {
			pair.rpmLimiter = rate.NewLimiter(rate.Limit(cfg.RPM)/60, cfg.RPM/60*2+1)
		}
		rl.limits[apiKey] = pair
	}
	return rl
}

// Allow 检查指定 API Key 是否允许本次请求。
// 返回 true 表示允许，false 表示触发限流。
// QPS 或 RPM 为 0 的维度不检查。
func (rl *RateLimiter) Allow(apiKey string) bool {
	rl.mu.RLock()
	pair, ok := rl.limits[apiKey]
	rl.mu.RUnlock()

	if !ok {
		return true // 未配置限流的 Key 不受限制
	}

	if pair.qpsLimiter != nil && !pair.qpsLimiter.Allow() {
		return false
	}
	if pair.rpmLimiter != nil && !pair.rpmLimiter.Allow() {
		return false
	}
	return true
}

// RateLimit 返回一个 HTTP 中间件，用于限流检查。
// 需要在 Auth 中间件之后执行，以获取 API Key 信息。
func (rl *RateLimiter) RateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := ExtractAPIKey(r)
		if apiKey == "" {
			errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
			return
		}

		if !rl.Allow(apiKey) {
			errors.NewRateLimited().ToHTTP(w, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ExtractAPIKey 从请求头中提取 Bearer Token（API Key）。
// 供 Auth 和 RateLimit 中间件共用。
func ExtractAPIKey(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) < 7 || authHeader[:7] != "Bearer " {
		return ""
	}
	return authHeader[7:]
}