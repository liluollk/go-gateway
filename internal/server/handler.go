// server 包实现网关的 HTTP 处理器：路由注册、健康检查、模型列表、Chat Completion。
// Handler 是核心结构体，持有配置和日志引用，所有业务逻辑通过其方法实现。
package server

import (
	"encoding/json"
	"net/http"

	"go-gateway/internal/adapter"
	"go-gateway/internal/config"
	"go-gateway/internal/errors"
	"go-gateway/internal/metrics"
	"go-gateway/internal/middleware"
	"go-gateway/internal/router"
)

// Handler 是网关的 HTTP 请求处理器，封装了配置和日志依赖。
type Handler struct {
	cfg      *config.Config
	logger   *middleware.Logger
	router   *router.Router
	adapters map[string]adapter.Adapter // provider_id → adapter 映射
}

// NewHandler 创建 Handler 实例，并初始化路由和所有供应商适配器。
func NewHandler(cfg *config.Config, logger *middleware.Logger) *Handler {
	h := &Handler{
		cfg:      cfg,
		logger:   logger,
		router:   router.NewRouter(cfg),
		adapters: make(map[string]adapter.Adapter),
	}
	h.initAdapters()
	return h
}

// initAdapters 根据配置中的 provider 列表初始化对应的适配器实例。
func (h *Handler) initAdapters() {
	for _, p := range h.cfg.Providers {
		var adp adapter.Adapter
		switch p.Type {
		case "openai", "openai-compatible":
			adp = adapter.NewOpenAIAdapter(p.BaseURL, p.APIKey)
		case "anthropic":
			adp = adapter.NewAnthropicAdapter(p.BaseURL, p.APIKey)
		default:
			continue
		}

		h.adapters[p.ID] = adp
	}
}

// RegisterRoutes 在给定的 ServeMux 上注册所有路由。
// 中间件链顺序：RequestID → Auth → RateLimit → Logging → 业务 Handler
// 注意：/healthz 不需要鉴权和日志。
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// 从 config 构建 auth 配置
	authKeys := make([]struct {
		Key    string
		Models []string
	}, len(h.cfg.Auth.Keys))
	for i, k := range h.cfg.Auth.Keys {
		authKeys[i] = struct {
			Key    string
			Models []string
		}{Key: k.Key, Models: k.Models}
	}
	authCfg := middleware.NewAuthConfig(authKeys)
	authMiddleware := middleware.Auth(authCfg)

	// 从 config 构建限流配置
	rateLimits := make(map[string]middleware.RateLimitConfig)
	for key, cfg := range h.cfg.RateLimit {
		rateLimits[key] = middleware.RateLimitConfig{QPS: cfg.QPS, RPM: cfg.RPM}
	}
	rateLimiter := middleware.NewRateLimiter(rateLimits)
	rateLimitMiddleware := rateLimiter.RateLimit

	// /healthz：无鉴权、无日志，用于 K8s 探活
	mux.HandleFunc("/healthz", h.HealthCheck)
	// /metrics：Prometheus 指标端点，无鉴权
	mux.Handle("/metrics", metrics.Handler())
	// /v1/models：获取当前 API Key 允许访问的模型列表
	mux.Handle("/v1/models", middleware.RequestID(
		authMiddleware(
			rateLimitMiddleware(
				h.logger.LoggingMiddleware(
					http.HandlerFunc(h.ListModels),
				),
			),
		),
	))
	// /v1/chat/completions：Chat Completion 核心接口（支持流式和非流式）
	mux.Handle("/v1/chat/completions", middleware.RequestID(
		authMiddleware(
			rateLimitMiddleware(
				h.logger.LoggingMiddleware(
					http.HandlerFunc(h.ChatCompletion),
				),
			),
		),
	))
}

// HealthCheck 健康检查端点，返回 {"status":"ok"}。
func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	type ProviderStatus struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Healthy bool   `json:"healthy"`
	}

	providers := make([]ProviderStatus, 0, len(h.cfg.Providers))
	allHealthy := true

	for _, p := range h.cfg.Providers {
		healthy := h.checkProviderHealth(p.ID)
		if !healthy {
			allHealthy = false
		}
		providers = append(providers, ProviderStatus{
			ID:      p.ID,
			Type:    p.Type,
			Healthy: healthy,
		})

		// 更新 Prometheus 指标
		healthValue := 0.0
		if healthy {
			healthValue = 1.0
		}
		metrics.UpstreamHealth.WithLabelValues(p.ID).Set(healthValue)
	}

	status := "ok"
	httpStatus := http.StatusOK
	if !allHealthy {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}

	resp := map[string]interface{}{
		"status":    status,
		"providers": providers,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(resp)
}

// checkProviderHealth 检查单个供应商的连通性。
func (h *Handler) checkProviderHealth(providerID string) bool {
	adp, ok := h.adapters[providerID]
	if !ok {
		return false
	}
	return adp.HealthCheck() == nil
}

// ListModels 返回当前 API Key 有权访问的模型列表，格式兼容 OpenAI /v1/models。
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := middleware.GetAuthModels(r.Context())
	if models == nil {
		errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
		return
	}

	type ModelInfo struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}

	data := make([]ModelInfo, len(models))
	for i, m := range models {
		data[i] = ModelInfo{ID: m, Object: "model", OwnedBy: "gateway"}
	}

	resp := map[string]interface{}{"object": "list", "data": data}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}