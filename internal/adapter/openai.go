package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	gwerrors "go-gateway/internal/errors"
	"go-gateway/internal/model"
)

// OpenAIAdapter 是 OpenAI API 的适配器实现。
// 负责将内部 OpenAI 格式请求转发到 OpenAI 兼容上游，并解析响应。
type OpenAIAdapter struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewOpenAIAdapter 创建 OpenAI 适配器实例。
// baseURL 为上游 API 基础地址（如 https://api.openai.com），
// apiKey 为供应商 API 密钥。
func NewOpenAIAdapter(baseURL, apiKey string) *OpenAIAdapter {
	return &OpenAIAdapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// GetProviderType 返回供应商类型标识。
func (a *OpenAIAdapter) GetProviderType() string {
	return "openai"
}

// HealthCheck 通过访问上游的 /models 端点检查连通性。
func (a *OpenAIAdapter) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.baseURL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	return nil
}

// ChatCompletion 发送非流式 Chat Completion 请求。
func (a *OpenAIAdapter) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	req.Stream = false
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &gwerrors.UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       string(respBody),
		}
	}

	var chatResp model.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return &chatResp, nil
}

// ChatCompletionStream 发送流式 Chat Completion 请求，返回 channel 逐块接收数据。
// 内部启动 goroutine 读取上游 SSE 流，解析为 StreamChunk 后通过 channel 发送。
func (a *OpenAIAdapter) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)

	// 流式请求使用更长的超时时间
	streamClient := &http.Client{Timeout: 300 * time.Second}
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, &gwerrors.UpstreamError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	ch := make(chan StreamEvent, 100)
	go a.readStream(resp.Body, ch)
	return ch, nil
}

// readStream 在后台 goroutine 中读取上游 SSE 流，解析为 StreamChunk 后发送到 channel。
// 读取完成后自动关闭 channel 和 response body。
func (a *OpenAIAdapter) readStream(body io.ReadCloser, ch chan<- StreamEvent) {
	defer body.Close()
	defer close(ch)

	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				ch <- StreamEvent{Err: fmt.Errorf("read stream: %w", err)}
			}
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return
		}

		var chunk model.StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			ch <- StreamEvent{Err: fmt.Errorf("parse chunk: %w", err)}
			continue
		}

		ch <- StreamEvent{Chunk: &chunk}
	}
}