package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go-gateway/internal/config"
	"go-gateway/internal/errors"
	"go-gateway/internal/middleware"
	"go-gateway/internal/model"
)

func (h *Handler) ChatCompletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errors.NewInvalidRequest("method not allowed").ToHTTP(w, http.StatusMethodNotAllowed)
		return
	}

	// 读取并验证请求体
	body, err := io.ReadAll(r.Body)
	if err != nil {
		errors.NewInvalidRequest("failed to read request body").ToHTTP(w, http.StatusBadRequest)
		return
	}

	var req model.ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		errors.NewInvalidRequest("invalid JSON: "+err.Error()).ToHTTP(w, http.StatusBadRequest)
		return
	}

	if req.Model == "" {
		errors.NewInvalidRequest("model is required").ToHTTP(w, http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		errors.NewInvalidRequest("messages is required").ToHTTP(w, http.StatusBadRequest)
		return
	}

	// 检查模型白名单
	appID := middleware.GetAuthAppID(r.Context())
	allowedModels := middleware.GetAuthModels(r.Context())
	modelAllowed := false
	for _, m := range allowedModels {
		if m == req.Model {
			modelAllowed = true
			break
		}
	}
	if !modelAllowed {
		errors.NewModelNotAllowed(req.Model).ToHTTP(w, http.StatusForbidden)
		return
	}

	// 非流式
	if !req.Stream {
		h.handleNonStreaming(w, r, &req, appID)
		return
	}

	// 流式 — Task 1.6 实现
	h.handleStreaming(w, r, &req, appID)
}

func (h *Handler) handleNonStreaming(w http.ResponseWriter, r *http.Request, req *model.ChatCompletionRequest, appID string) {
	// 查找匹配的 provider（Phase 1 简单取第一个匹配的）
	provider := h.findProvider(req.Model)
	if provider == nil {
		errors.NewProviderUnavailable().ToHTTP(w, http.StatusServiceUnavailable)
		return
	}

	// 转发到上游
	upstreamResp, err := h.callOpenAI(r.Context(), provider, req)
	if err != nil {
		errors.NewProviderError(err.Error()).ToHTTP(w, http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(upstreamResp)
}

func (h *Handler) findProvider(model string) *config.ProviderConfig {
	// Phase 1 简单逻辑：遍历 routes 匹配 model，返回第一个 provider
	// Phase 2 由 router 模块替换
	for _, route := range h.cfg.Routes {
		if route.Model == model {
			if len(route.Providers) > 0 {
				for _, p := range h.cfg.Providers {
					if p.ID == route.Providers[0].ProviderID {
						return &p
					}
				}
			}
		}
	}
	return nil
}

func (h *Handler) callOpenAI(ctx context.Context, provider *config.ProviderConfig, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.BaseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp model.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &chatResp, nil
}

func (h *Handler) handleStreaming(w http.ResponseWriter, r *http.Request, req *model.ChatCompletionRequest, appID string) {
	provider := h.findProvider(req.Model)
	if provider == nil {
		errors.NewProviderUnavailable().ToHTTP(w, http.StatusServiceUnavailable)
		return
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		errors.NewProviderError("marshal request: "+err.Error()).ToHTTP(w, http.StatusBadGateway)
		return
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, provider.BaseURL+"/v1/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		errors.NewProviderError("create request: "+err.Error()).ToHTTP(w, http.StatusBadGateway)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)

	client := &http.Client{Timeout: 300 * time.Second}
	upstreamResp, err := client.Do(httpReq)
	if err != nil {
		errors.NewProviderError("upstream request: "+err.Error()).ToHTTP(w, http.StatusBadGateway)
		return
	}
	defer upstreamResp.Body.Close()

	if upstreamResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(upstreamResp.Body)
		errors.NewProviderError(fmt.Sprintf("upstream status %d: %s", upstreamResp.StatusCode, string(body))).ToHTTP(w, http.StatusBadGateway)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		errors.NewProviderError("streaming not supported").ToHTTP(w, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	reader := bufio.NewReader(upstreamResp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// 确保发送 [DONE]
				w.Write([]byte("data: [DONE]\n\n"))
				flusher.Flush()
			}
			return
		}

		_, writeErr := w.Write([]byte(line))
		if writeErr != nil {
			// 客户端断开连接，取消上游请求
			return
		}
		flusher.Flush()
	}
}
