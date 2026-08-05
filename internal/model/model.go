// model 包定义了与 OpenAI API 兼容的请求/响应数据结构。
// 用于 Chat Completion 接口的序列化/反序列化，支持流式和非流式两种模式。
package model

import "encoding/json"

// Role 表示消息角色，遵循 OpenAI Chat Completion API 规范。
type Role string

const (
	RoleSystem    Role = "system"    // 系统提示词
	RoleUser      Role = "user"      // 用户消息
	RoleAssistant Role = "assistant" // 模型回复
	RoleTool      Role = "tool"      // 工具调用结果
)

// ChatMessage 表示对话中的一条消息。
type ChatMessage struct {
	Role             Role               `json:"role"`
	Content          string             `json:"content"`
	ReasoningContent string             `json:"reasoning_content,omitempty"` // 思考过程内容（DeepSeek V4 等模型）
	ToolCallID       string             `json:"tool_call_id,omitempty"`      // 工具调用 ID（tool 角色使用）
	ToolCalls        []InternalToolCall `json:"tool_calls,omitempty"`        // 模型请求的工具调用列表
	Name             string             `json:"name,omitempty"`              // 可选的消息发送者名称
}

// InternalToolCall 表示模型发起的工具调用请求。
type InternalToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`     // 固定为 "function"
	Function InternalFunction `json:"function"` // 函数名称和参数
}

// InternalFunction 表示工具调用的函数信息。
type InternalFunction struct {
	Name      string `json:"name"`      // 函数名
	Arguments string `json:"arguments"` // JSON 格式的函数参数
}

// InternalToolDef 表示工具定义（在请求中声明可用的工具）。
type InternalToolDef struct {
	Type     string          `json:"type"`     // 固定为 "function"
	Function json.RawMessage `json:"function"` // 函数定义，使用 RawMessage 保留原始 JSON
}

// ChatCompletionRequest 是 Chat Completion API 的请求体。
// 兼容 OpenAI /v1/chat/completions 接口格式。
type ChatCompletionRequest struct {
	Model       string            `json:"model"`
	Messages    []ChatMessage     `json:"messages"`
	Stream      bool              `json:"stream,omitempty"`      // 是否使用 SSE 流式返回
	MaxTokens   int               `json:"max_tokens,omitempty"`  // 最大生成 token 数
	Temperature float64           `json:"temperature,omitempty"` // 采样温度 (0-2)
	TopP        float64           `json:"top_p,omitempty"`       // 核采样参数
	Tools       []json.RawMessage `json:"tools,omitempty"`       // 可用工具列表（透传）
	ToolChoice  interface{}       `json:"tool_choice,omitempty"` // 工具选择策略（透传）
	Stop        interface{}       `json:"stop,omitempty"`        // 停止词（透传）
}

// Usage 表示 Token 用量统计。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`     // 输入消耗的 token 数
	CompletionTokens int `json:"completion_tokens"` // 输出消耗的 token 数
	TotalTokens      int `json:"total_tokens"`      // 总 token 数
}

// ChatCompletionResponse 是非流式 Chat Completion 的响应体。
type ChatCompletionResponse struct {
	ID                string   `json:"id"`
	Object            string   `json:"object"`             // 固定为 "chat.completion"
	Created           int64    `json:"created"`            // Unix 时间戳
	Model             string   `json:"model"`              // 实际使用的模型名
	Choices           []Choice `json:"choices"`            // 回复选项列表
	Usage             *Usage   `json:"usage,omitempty"`    // Token 用量（非流式返回）
	SystemFingerprint string   `json:"system_fingerprint,omitempty"` // 系统指纹
}

// Choice 表示非流式响应中的一个回复选项。
type Choice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`       // 完整的回复消息
	FinishReason string      `json:"finish_reason"` // 结束原因：stop / length / tool_calls
}

// StreamChunk 是流式响应中 SSE 事件的数据结构（即 "data: {...}" 中的 JSON）。
type StreamChunk struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`             // 固定为 "chat.completion.chunk"
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []StreamChoice `json:"choices"`
	Usage             *Usage         `json:"usage,omitempty"`              // Token 用量（流式最后一个 chunk 包含）
	SystemFingerprint string         `json:"system_fingerprint,omitempty"`
}

// StreamChoice 表示流式响应中的一个增量选项。
type StreamChoice struct {
	Index        int     `json:"index"`
	Delta        Delta   `json:"delta"`                   // 增量内容（非完整消息）
	FinishReason *string `json:"finish_reason,omitempty"` // 结束时才有值
}

// Delta 表示流式响应中的增量内容片段。
type Delta struct {
	Role             string             `json:"role,omitempty"`              // 仅第一个 chunk 包含
	Content          string             `json:"content,omitempty"`           // 增量文本内容
	ReasoningContent string             `json:"reasoning_content,omitempty"` // 增量思考内容（DeepSeek V4 等模型）
	ToolCalls        []InternalToolCall `json:"tool_calls,omitempty"`        // 增量工具调用
}