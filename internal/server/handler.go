package server

import (
	"encoding/json"
	"net/http"

	"go-gateway/internal/config"
	"go-gateway/internal/errors"
	"go-gateway/internal/middleware"
)

type Handler struct {
	cfg *config.Config
}

func NewHandler(cfg *config.Config) *Handler {
	return &Handler{cfg: cfg}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// 从 config 构建 auth 配置
	authKeys := make([]struct {
		Key    string
		AppID  string
		Models []string
	}, len(h.cfg.Auth.Keys))
	for i, k := range h.cfg.Auth.Keys {
		authKeys[i] = struct {
			Key    string
			AppID  string
			Models []string
		}{Key: k.Key, AppID: k.AppID, Models: k.Models}
	}
	authCfg := middleware.NewAuthConfig(authKeys)
	authMiddleware := middleware.Auth(authCfg)

	mux.HandleFunc("/healthz", h.HealthCheck)                                                                 // 无鉴权
	mux.Handle("/v1/models", middleware.RequestID(authMiddleware(http.HandlerFunc(h.ListModels))))
	mux.Handle("/v1/chat/completions", middleware.RequestID(authMiddleware(http.HandlerFunc(h.ChatCompletion))))
}

func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := middleware.GetAuthModels(r.Context())
	if models == nil {
		errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
		return
	}

	type ModelInfo struct {
		ID     string `json:"id"`
		Object string `json:"object"`
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

func (h *Handler) ChatCompletion(w http.ResponseWriter, r *http.Request) {
	// TODO: 逐步实现 — Task 1.5, 1.6, 1.7
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
