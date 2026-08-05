// adapter 包定义了供应商适配器接口及实现。
// 通过 Adapter 模式隔离不同供应商的 API 差异，
// 网关内部统一使用 OpenAI 格式作为中间表示。
package adapter

import (
	"context"

	"go-gateway/internal/model"
)

// Adapter 是供应商适配器接口，所有供应商适配器必须实现此接口。
// 网关通过此接口统一调用不同供应商，无需关心底层协议差异。
type Adapter interface {
	// ChatCompletion 发送非流式请求，返回完整响应。
	ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error)

	// ChatCompletionStream 发送流式请求，返回 channel 逐块接收数据。
	// 调用方从 channel 读取 StreamEvent，channel 在流结束时自动关闭。
	ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error)

	// HealthCheck 检查上游供应商的连通性，返回 nil 表示健康。
	HealthCheck() error

	// GetProviderType 返回供应商类型标识（openai / anthropic / openai-compatible）。
	GetProviderType() string
}

// StreamEvent 是流式响应中的单个事件，包含一个数据块或错误。
type StreamEvent struct {
	Chunk *model.StreamChunk // 流式响应块，非 nil 时有效
	Err   error              // 错误信息，非 nil 时表示读取失败
}