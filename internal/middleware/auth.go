// middleware 包含网关的 HTTP 中间件：鉴权、日志、请求追踪。
// 中间件通过责任链模式组合，在 handler.go 的 RegisterRoutes 中按顺序包装。
package middleware

import (
	"context"
	"net/http"

	"go-gateway/internal/errors"
)

// authKey 是 context 中存储鉴权信息的 key 类型，避免与其他包冲突。
type authKey string

const (
	// AuthModelsKey 是 context 中存储允许访问模型列表的 key。
	AuthModelsKey authKey = "auth_models"
)

// AuthConfig 鉴权配置，存储所有合法 API Key 及其对应的模型白名单。
type AuthConfig struct {
	Keys map[string]struct {
		Models []string
	}
}

// NewAuthConfig 从配置切片构建 AuthConfig 的快速查找 map。
func NewAuthConfig(keys []struct {
	Key    string
	Models []string
}) *AuthConfig {
	cfg := &AuthConfig{Keys: make(map[string]struct {
		Models []string
	})}
	for _, k := range keys {
		cfg.Keys[k.Key] = struct {
			Models []string
		}{Models: k.Models}
	}
	return cfg
}

// Auth 返回一个 HTTP 中间件，用于验证请求的 Bearer Token。
// 验证通过后，将允许的模型列表注入到 request context 中。
// 验证失败则返回 401 Unauthorized。
func Auth(cfg *AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ExtractAPIKey(r)
			if token == "" {
				errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
				return
			}

			entry, ok := cfg.Keys[token]
			if !ok {
				errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
				return
			}

			// 鉴权通过，将模型白名单注入 context，供下游 handler 使用
			ctx := context.WithValue(r.Context(), AuthModelsKey, entry.Models)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetAuthModels 从 context 中提取当前请求允许访问的模型列表。
// 如果 context 中不存在，返回 nil。
func GetAuthModels(ctx context.Context) []string {
	if models, ok := ctx.Value(AuthModelsKey).([]string); ok {
		return models
	}
	return nil
}