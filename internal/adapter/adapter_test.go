package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gwerrors "go-gateway/internal/errors"
	"go-gateway/internal/model"
)

func TestOpenAIAdapter_ChatCompletion_Success(t *testing.T) {
	// 模拟 OpenAI 兼容上游
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected /v1/chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Authorization header, got %s", r.Header.Get("Authorization"))
		}

		resp := model.ChatCompletionResponse{
			ID:      "chatcmpl-123",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-4o",
			Choices: []model.Choice{
				{
					Index: 0,
					Message: model.ChatMessage{
						Role:    "assistant",
						Content: "Hello!",
					},
					FinishReason: "stop",
				},
			},
			Usage: &model.Usage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	adp := NewOpenAIAdapter(ts.URL, "test-key")
	req := &model.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []model.ChatMessage{
			{Role: "user", Content: "hi"},
		},
	}

	resp, err := adp.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Choices[0].Message.Content != "Hello!" {
		t.Errorf("expected 'Hello!', got %s", resp.Choices[0].Message.Content)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("expected 15 tokens, got %d", resp.Usage.TotalTokens)
	}
}

func TestOpenAIAdapter_ChatCompletion_UpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer ts.Close()

	adp := NewOpenAIAdapter(ts.URL, "bad-key")
	req := &model.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []model.ChatMessage{
			{Role: "user", Content: "hi"},
		},
	}

	_, err := adp.ChatCompletion(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var upErr *gwerrors.UpstreamError
	if !asUpstreamErr(err, &upErr) {
		t.Fatalf("expected *errors.UpstreamError, got %T: %v", err, err)
	}
	if upErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", upErr.StatusCode)
	}
	if !upErr.IsClientError() {
		t.Error("expected IsClientError() to return true for 401")
	}
}

func TestOpenAIAdapter_ChatCompletion_ServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"internal error"}}`))
	}))
	defer ts.Close()

	adp := NewOpenAIAdapter(ts.URL, "test-key")
	req := &model.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []model.ChatMessage{
			{Role: "user", Content: "hi"},
		},
	}

	_, err := adp.ChatCompletion(context.Background(), req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var upErr *gwerrors.UpstreamError
	if !asUpstreamErr(err, &upErr) {
		t.Fatalf("expected *errors.UpstreamError, got %T: %v", err, err)
	}
	if upErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", upErr.StatusCode)
	}
	if upErr.IsClientError() {
		t.Error("expected IsClientError() to return false for 500")
	}
}

func TestOpenAIAdapter_GetProviderType(t *testing.T) {
	adp := NewOpenAIAdapter("http://localhost", "key")
	if adp.GetProviderType() != "openai" {
		t.Errorf("expected 'openai', got %s", adp.GetProviderType())
	}
}

func TestOpenAIAdapter_ChatCompletionStream_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("streaming not supported")
		}

		chunk := model.StreamChunk{
			ID:      "chatcmpl-123",
			Object:  "chat.completion.chunk",
			Created: 1234567890,
			Model:   "gpt-4o",
			Choices: []model.StreamChoice{
				{
					Index: 0,
					Delta: model.Delta{
						Role:    "assistant",
						Content: "Hello",
					},
				},
			},
		}
		data, _ := json.Marshal(chunk)
		w.Write([]byte("data: " + string(data) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	adp := NewOpenAIAdapter(ts.URL, "test-key")
	req := &model.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []model.ChatMessage{
			{Role: "user", Content: "hi"},
		},
	}

	ch, err := adp.ChatCompletionStream(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	eventCount := 0
	for event := range ch {
		if event.Err != nil {
			t.Fatalf("unexpected stream error: %v", event.Err)
		}
		if event.Chunk != nil && len(event.Chunk.Choices) > 0 {
			eventCount++
		}
	}
	if eventCount != 1 {
		t.Errorf("expected 1 content event, got %d", eventCount)
	}
}

func TestAnthropicAdapter_GetProviderType(t *testing.T) {
	adp := NewAnthropicAdapter("http://localhost", "key")
	if adp.GetProviderType() != "anthropic" {
		t.Errorf("expected 'anthropic', got %s", adp.GetProviderType())
	}
}

func TestUpstreamError_IsClientError(t *testing.T) {
	tests := []struct {
		statusCode int
		isClient   bool
	}{
		{400, true},
		{401, true},
		{403, true},
		{404, true},
		{429, true},
		{499, true},
		{500, false},
		{502, false},
		{503, false},
	}

	for _, tt := range tests {
		upErr := &gwerrors.UpstreamError{StatusCode: tt.statusCode}
		got := upErr.IsClientError()
		if got != tt.isClient {
			t.Errorf("StatusCode %d: expected IsClientError=%v, got %v", tt.statusCode, tt.isClient, got)
		}
	}
}

func TestUpstreamError_ErrorMethod(t *testing.T) {
	upErr := &gwerrors.UpstreamError{StatusCode: 401, Body: "unauthorized"}
	expected := "upstream status 401: unauthorized"
	if upErr.Error() != expected {
		t.Errorf("expected %q, got %q", expected, upErr.Error())
	}
}

// asUpstreamErr 是 errors.As 的便捷包装，用于测试中判断错误类型。
func asUpstreamErr(err error, target **gwerrors.UpstreamError) bool {
	// 手动递归检查错误链
	for err != nil {
		if ue, ok := err.(*gwerrors.UpstreamError); ok {
			*target = ue
			return true
		}
		// 检查是否实现了 Unwrap()
		type unwrapper interface {
			Unwrap() error
		}
		if unw, ok := err.(unwrapper); ok {
			err = unw.Unwrap()
		} else {
			break
		}
	}
	return false
}