package middleware

import (
	"context"
	"net/http"
	"strings"

	"go-gateway/internal/errors"
)

type authKey string

const (
	AuthAppIDKey  authKey = "auth_app_id"
	AuthModelsKey authKey = "auth_models"
)

type AuthConfig struct {
	Keys map[string]struct {
		AppID  string
		Models []string
	}
}

func NewAuthConfig(keys []struct {
	Key    string
	AppID  string
	Models []string
}) *AuthConfig {
	cfg := &AuthConfig{Keys: make(map[string]struct {
		AppID  string
		Models []string
	})}
	for _, k := range keys {
		cfg.Keys[k.Key] = struct {
			AppID  string
			Models []string
		}{AppID: k.AppID, Models: k.Models}
	}
	return cfg
}

func Auth(cfg *AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
				errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			entry, ok := cfg.Keys[token]
			if !ok {
				errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), AuthAppIDKey, entry.AppID)
			ctx = context.WithValue(ctx, AuthModelsKey, entry.Models)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetAuthAppID(ctx context.Context) string {
	if id, ok := ctx.Value(AuthAppIDKey).(string); ok {
		return id
	}
	return ""
}

func GetAuthModels(ctx context.Context) []string {
	if models, ok := ctx.Value(AuthModelsKey).([]string); ok {
		return models
	}
	return nil
}
