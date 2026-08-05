package adapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	gwerrors "go-gateway/internal/errors"
	"go-gateway/internal/model"
)

// AnthropicAdapter 是 Anthropic Messages API 的适配器实现。
// 负责将内部 OpenAI 格式请求转换为 Anthropic 格式，转发后
// 将 Anthropic 响应转换回 OpenAI 格式。
type AnthropicAdapter struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// anthropicMessage 是 Anthropic 消息格式。
type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicRequest 是 Anthropic Messages API 请求体。
type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	TopP        float64            `json:"top_p,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

// anthropicContent 是 Anthropic 响应中的内容块。
type anthropicContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"` // 思考内容（DeepSeek V4 等模型）
}

// anthropicResponse 是非流式 Anthropic 响应。
type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []anthropicContent `json:"content"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Usage      *anthropicUsage    `json:"usage"`
}

// anthropicUsage 是 Anthropic Token 用量。
type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// anthropicStreamEvent 是 Anthropic SSE 事件的 JSON 结构。
type anthropicStreamEvent struct {
	Type    string                   `json:"type"`
	Delta   *anthropicDelta          `json:"delta,omitempty"`
	Usage   *anthropicUsage          `json:"usage,omitempty"`
	Message *anthropicMessageBlock   `json:"message,omitempty"`
}

// anthropicDelta 是流式增量内容。
type anthropicDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"` // 思考增量内容（DeepSeek V4 等模型）
}

// anthropicMessageBlock 是流式 message_start 中的消息块。
type anthropicMessageBlock struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []anthropicContent `json:"content"`
	Usage   *anthropicUsage    `json:"usage"`
}

// NewAnthropicAdapter 创建 Anthropic 适配器实例。
func NewAnthropicAdapter(baseURL, apiKey string) *AnthropicAdapter {
	return &AnthropicAdapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// GetProviderType 返回供应商类型标识。
func (a *AnthropicAdapter) GetProviderType() string {
	return "anthropic"
}

// HealthCheck 通过访问上游的 Anthropic Messages 端点检查连通性。
func (a *AnthropicAdapter) HealthCheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.baseURL+"/v1/messages", nil)
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	// Anthropic 对 GET 请求返回 405，但说明连通性没问题
	if resp.StatusCode >= 500 {
		return fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	return nil
}

// ChatCompletion 发送非流式请求，将 Anthropic 响应转换为 OpenAI 格式。
func (a *AnthropicAdapter) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	anthReq, err := a.convertRequest(req, false)
	if err != nil {
		return nil, fmt.Errorf("convert request: %w", err)
	}

	body, err := json.Marshal(anthReq)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

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

	var anthResp anthropicResponse
	if err := json.Unmarshal(respBody, &anthResp); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return a.convertResponse(&anthResp, req.Model), nil
}

// ChatCompletionStream 发送流式请求，返回 channel 逐块接收数据。
func (a *AnthropicAdapter) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error) {
	anthReq, err := a.convertRequest(req, true)
	if err != nil {
		return nil, fmt.Errorf("convert request: %w", err)
	}

	body, err := json.Marshal(anthReq)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

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
	go a.readStream(req.Model, resp.Body, ch)
	return ch, nil
}

// convertRequest 将 OpenAI 格式请求转换为 Anthropic 格式。
// 关键差异：
// 1. system 角色消息提取到独立的 system 字段
// 2. Anthropic 不支持多条 system 消息，只取最后一条
// 3. max_tokens 在 Anthropic 中必填，默认 4096
// 4. Anthropic 使用 x-api-key 头而非 Authorization: Bearer
func (a *AnthropicAdapter) convertRequest(req *model.ChatCompletionRequest, stream bool) (*anthropicRequest, error) {
	var messages []anthropicMessage
	var systemPrompt string

	for _, msg := range req.Messages {
		switch msg.Role {
		case model.RoleSystem:
			if msg.Content != "" {
				systemPrompt = msg.Content
			}
		case model.RoleUser, model.RoleAssistant:
			role := string(msg.Role)
			messages = append(messages, anthropicMessage{
				Role:    role,
				Content: msg.Content,
			})
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	return &anthropicRequest{
		Model:       req.Model,
		Messages:    messages,
		System:      systemPrompt,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      stream,
	}, nil
}

// convertResponse 将 Anthropic 非流式响应转换为 OpenAI 格式。
func (a *AnthropicAdapter) convertResponse(anthResp *anthropicResponse, modelName string) *model.ChatCompletionResponse {
	content := ""
	reasoning := ""
	for _, c := range anthResp.Content {
		switch c.Type {
		case "text":
			content += c.Text
		case "thinking":
			reasoning += c.Thinking
		}
	}

	finishReason := "stop"
	if anthResp.StopReason == "max_tokens" {
		finishReason = "length"
	} else if anthResp.StopReason == "tool_use" {
		finishReason = "tool_calls"
	}

	var usage *model.Usage
	if anthResp.Usage != nil {
		usage = &model.Usage{
			PromptTokens:     anthResp.Usage.InputTokens,
			CompletionTokens: anthResp.Usage.OutputTokens,
			TotalTokens:      anthResp.Usage.InputTokens + anthResp.Usage.OutputTokens,
		}
	}

	return &model.ChatCompletionResponse{
		ID:      anthResp.ID,
		Object:  "chat.completion",
		Model:   modelName,
		Choices: []model.Choice{{
			Index: 0,
			Message: model.ChatMessage{
				Role:             model.RoleAssistant,
				Content:          content,
				ReasoningContent: reasoning,
			},
			FinishReason: finishReason,
		}},
		Usage: usage,
	}
}

// readStream 在后台 goroutine 中读取 Anthropic SSE 流，转换为 OpenAI 格式后发送。
// Anthropic SSE 格式：
//   event: message_start
//   data: {"type":"message_start","message":{...}}
//   event: content_block_delta
//   data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"..."}}
//   event: message_delta
//   data: {"type":"message_delta","delta":{"stop_reason":"..."},"usage":{...}}
//   event: message_stop
//   data: {"type":"message_stop"}
func (a *AnthropicAdapter) readStream(modelName string, body io.ReadCloser, ch chan<- StreamEvent) {
	defer body.Close()
	defer close(ch)

	reader := bufio.NewReader(body)
	var msgID string = "chatcmpl-" + generateShortID()

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

		var event anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			ch <- StreamEvent{Err: fmt.Errorf("parse event: %w", err)}
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				msgID = event.Message.ID
			}

		case "content_block_delta":
			if event.Delta == nil {
				continue
			}
			switch event.Delta.Type {
			case "text_delta":
				ch <- StreamEvent{Chunk: &model.StreamChunk{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Model:   modelName,
					Choices: []model.StreamChoice{{
						Index: 0,
						Delta: model.Delta{
							Content: event.Delta.Text,
						},
					}},
				}}
			case "thinking_delta":
				ch <- StreamEvent{Chunk: &model.StreamChunk{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Model:   modelName,
					Choices: []model.StreamChoice{{
						Index: 0,
						Delta: model.Delta{
							ReasoningContent: event.Delta.Thinking,
						},
					}},
				}}
			}

		case "message_delta":
			finishReason := "stop"
			var usage *model.Usage
			if event.Usage != nil {
				usage = &model.Usage{
					PromptTokens:     event.Usage.InputTokens,
					CompletionTokens: event.Usage.OutputTokens,
					TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
				}
			}
			ch <- StreamEvent{Chunk: &model.StreamChunk{
				ID:      msgID,
				Object:  "chat.completion.chunk",
				Model:   modelName,
				Choices: []model.StreamChoice{{
					Index:        0,
					Delta:        model.Delta{},
					FinishReason: &finishReason,
				}},
				Usage: usage,
			}}

		case "message_stop":
			return
		}
	}
}

// generateShortID 生成一个短随机 ID，用于流式响应中的回退值。
func generateShortID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "chatcmpl-fallback"
	}
	return "chatcmpl-" + hex.EncodeToString(b)
}