package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"go-gateway/internal/config"
	gerrors "go-gateway/internal/errors"
	"go-gateway/internal/metrics"
	"go-gateway/internal/middleware"
	"go-gateway/internal/model"
)

// ChatCompletion 处理 /v1/chat/completions 请求。
// 请求流程：
// 1. 验证请求方法和请求体
// 2. 校验模型白名单
// 3. 根据 stream 参数分流到非流式或流式处理
func (h *Handler) ChatCompletion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		gerrors.NewInvalidRequest("method not allowed").ToHTTP(w, http.StatusMethodNotAllowed)
		return
	}

	// 读取并验证请求体
	body, err := io.ReadAll(r.Body)
	if err != nil {
		gerrors.NewInvalidRequest("failed to read request body").ToHTTP(w, http.StatusBadRequest)
		return
	}

	var req model.ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		gerrors.NewInvalidRequest("invalid JSON: "+err.Error()).ToHTTP(w, http.StatusBadRequest)
		return
	}

	if req.Model == "" {
		gerrors.NewInvalidRequest("model is required").ToHTTP(w, http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		gerrors.NewInvalidRequest("messages is required").ToHTTP(w, http.StatusBadRequest)
		return
	}

	// 检查模型白名单（从 context 中获取 Auth 中间件注入的模型列表）
	allowedModels := middleware.GetAuthModels(r.Context())
	modelAllowed := false
	for _, m := range allowedModels {
		if m == req.Model {
			modelAllowed = true
			break
		}
	}
	if !modelAllowed {
		gerrors.NewModelNotAllowed(req.Model).ToHTTP(w, http.StatusForbidden)
		return
	}

	// 非流式请求
	if !req.Stream {
		h.handleNonStreaming(w, r, &req)
		return
	}

	// 流式请求（SSE）
	h.handleStreaming(w, r, &req)
}

// handleNonStreaming 处理非流式 Chat Completion 请求。
// 流程：路由选择 → 重试调用 → 失败则 fallback → 返回响应。
func (h *Handler) handleNonStreaming(w http.ResponseWriter, r *http.Request, req *model.ChatCompletionRequest) {
	startTime := time.Now()
	provider := h.router.SelectProvider(req.Model)
	if provider == nil {
		metrics.RequestsTotal.WithLabelValues(req.Model, "503", "none").Inc()
		metrics.ErrorsTotal.WithLabelValues(req.Model, "no_provider").Inc()
		gerrors.NewProviderUnavailable().ToHTTP(w, http.StatusServiceUnavailable)
		return
	}

	resp, err := h.tryNonStreamingCall(provider, r, req)
	if err != nil {
		fallbacks := h.router.GetFallbackProviders(req.Model, provider.ID)
		for _, fb := range fallbacks {
			resp, err = h.tryNonStreamingCall(fb, r, req)
			if err == nil {
				provider = fb
				break
			}
		}
	}

	if err != nil {
		log.Printf("all providers failed for model %s: %v", req.Model, err)
		metrics.RequestsTotal.WithLabelValues(req.Model, "502", provider.ID).Inc()
		metrics.ErrorsTotal.WithLabelValues(req.Model, "upstream_error").Inc()
		gerrors.NewProviderError("all providers failed").ToHTTP(w, http.StatusBadGateway)
		return
	}

	// 记录请求指标
	duration := time.Since(startTime).Seconds()
	metrics.RequestsTotal.WithLabelValues(req.Model, "200", provider.ID).Inc()
	metrics.RequestDuration.WithLabelValues(req.Model, provider.ID).Observe(duration)

	// 记录 Token 用量
	if resp.Usage != nil {
		metrics.TokensConsumed.WithLabelValues(req.Model, "prompt").Add(float64(resp.Usage.PromptTokens))
		metrics.TokensConsumed.WithLabelValues(req.Model, "completion").Add(float64(resp.Usage.CompletionTokens))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// tryNonStreamingCall 尝试调用单个供应商的非流式接口，支持重试。
// 重试策略：仅对 5xx 和网络错误重试，4xx 不重试（不可恢复）。
// 指数退避：initialBackoff * 2^retry，最大不超过 30s。
func (h *Handler) tryNonStreamingCall(provider *config.ProviderConfig, r *http.Request, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	adp, ok := h.adapters[provider.ID]
	if !ok {
		return nil, fmt.Errorf("adapter not found for provider %s", provider.ID)
	}

	maxRetries := h.cfg.Retry.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 0
	}
	initialBackoff := h.cfg.Retry.InitialBackoff
	if initialBackoff <= 0 {
		initialBackoff = time.Second
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(float64(initialBackoff) * math.Pow(2, float64(attempt-1)))
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			select {
			case <-time.After(backoff):
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}

		resp, err := adp.ChatCompletion(r.Context(), req)
		if err == nil {
			return resp, nil
		}

		lastErr = err
		var upErr *gerrors.UpstreamError
		if errors.As(err, &upErr) && upErr.IsClientError() {
			return nil, err
		}
	}
	return nil, lastErr
}

// handleStreaming 处理流式 Chat Completion 请求（SSE）。
// 流程：路由选择 → 获取 Adapter → 调用 Adapter.ChatCompletionStream → 逐块 SSE 转发。
// 注意：流式请求不支持 fallback（已开始发送数据后无法切换供应商）。
func (h *Handler) handleStreaming(w http.ResponseWriter, r *http.Request, req *model.ChatCompletionRequest) {
	startTime := time.Now()
	provider := h.router.SelectProvider(req.Model)
	if provider == nil {
		metrics.RequestsTotal.WithLabelValues(req.Model, "503", "none").Inc()
		metrics.ErrorsTotal.WithLabelValues(req.Model, "no_provider").Inc()
		gerrors.NewProviderUnavailable().ToHTTP(w, http.StatusServiceUnavailable)
		return
	}

	adp, ok := h.adapters[provider.ID]
	if !ok {
		metrics.RequestsTotal.WithLabelValues(req.Model, "503", "none").Inc()
		metrics.ErrorsTotal.WithLabelValues(req.Model, "no_adapter").Inc()
		gerrors.NewProviderUnavailable().ToHTTP(w, http.StatusServiceUnavailable)
		return
	}

	ch, err := adp.ChatCompletionStream(r.Context(), req)
	if err != nil {
		log.Printf("upstream stream request failed: %v", err)
		metrics.RequestsTotal.WithLabelValues(req.Model, "502", provider.ID).Inc()
		metrics.ErrorsTotal.WithLabelValues(req.Model, "stream_error").Inc()
		gerrors.NewProviderError("upstream request failed").ToHTTP(w, http.StatusBadGateway)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		metrics.RequestsTotal.WithLabelValues(req.Model, "500", provider.ID).Inc()
		metrics.ErrorsTotal.WithLabelValues(req.Model, "no_flusher").Inc()
		gerrors.NewProviderError("streaming not supported").ToHTTP(w, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	var usage *model.Usage
	for event := range ch {
		if event.Err != nil {
			log.Printf("stream read error: %v", event.Err)
			metrics.ErrorsTotal.WithLabelValues(req.Model, "stream_read").Inc()
			return
		}
		// 收集最后的 Usage 信息
		if event.Chunk.Usage != nil {
			usage = event.Chunk.Usage
		}
		data, err := json.Marshal(event.Chunk)
		if err != nil {
			log.Printf("stream marshal error: %v", err)
			continue
		}
		_, writeErr := fmt.Fprintf(w, "data: %s\n\n", data)
		if writeErr != nil {
			return
		}
		flusher.Flush()
	}

	// 记录流式请求指标
	duration := time.Since(startTime).Seconds()
	metrics.RequestsTotal.WithLabelValues(req.Model, "200", provider.ID).Inc()
	metrics.RequestDuration.WithLabelValues(req.Model, provider.ID).Observe(duration)
	if usage != nil {
		metrics.TokensConsumed.WithLabelValues(req.Model, "prompt").Add(float64(usage.PromptTokens))
		metrics.TokensConsumed.WithLabelValues(req.Model, "completion").Add(float64(usage.CompletionTokens))
	}

	w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}