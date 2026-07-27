package server

import (
	"net/http"

	"go-gateway/internal/config"
	"go-gateway/internal/middleware"
)

type Handler struct {
	cfg *config.Config
}

func NewHandler(cfg *config.Config) *Handler {
	return &Handler{cfg: cfg}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.HealthCheck)
	mux.Handle("/v1/models", middleware.RequestID(http.HandlerFunc(h.ListModels)))
	mux.Handle("/v1/chat/completions", middleware.RequestID(http.HandlerFunc(h.ChatCompletion)))
}

func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	// TODO: 需要 auth 中间件 — Task 1.4 完成后实现
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"object":"list","data":[]}`))
}

func (h *Handler) ChatCompletion(w http.ResponseWriter, r *http.Request) {
	// TODO: 逐步实现 — Task 1.5, 1.6, 1.7
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
