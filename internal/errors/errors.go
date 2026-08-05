// errors 包定义了网关的统一错误码和 HTTP 错误响应格式。
// 错误码遵循 OpenAI API 兼容格式，方便客户端统一处理。
package errors

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ErrorCode 是网关定义的错误码类型，兼容 OpenAI API 错误码规范。
type ErrorCode string

const (
	ErrInvalidRequest      ErrorCode = "INVALID_REQUEST"       // 请求参数不合法
	ErrInvalidAPIKey       ErrorCode = "INVALID_API_KEY"       // API Key 无效
	ErrModelNotAllowed     ErrorCode = "MODEL_NOT_ALLOWED"     // 模型不在白名单内
	ErrRateLimited         ErrorCode = "RATE_LIMITED"          // 触发限流
	ErrProviderError       ErrorCode = "PROVIDER_ERROR"        // 上游供应商返回错误
	ErrProviderUnavailable ErrorCode = "PROVIDER_UNAVAILABLE"  // 所有上游供应商不可用
	ErrUpstreamTimeout     ErrorCode = "UPSTREAM_TIMEOUT"      // 上游请求超时
)

// APIError 是网关统一错误结构，包含错误码和描述信息。
type APIError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// ErrorResponse 是返回给客户端的 HTTP 错误响应体，格式兼容 OpenAI API。
type ErrorResponse struct {
	Error APIError `json:"error"`
}

// ToHTTP 将 APIError 写入 HTTP 响应，设置 Content-Type 和状态码。
func (e *APIError) ToHTTP(w http.ResponseWriter, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{Error: *e})
}

// 以下是各错误类型的便捷构造函数。

func NewInvalidRequest(msg string) *APIError {
	return &APIError{Code: ErrInvalidRequest, Message: msg}
}

func NewInvalidAPIKey() *APIError {
	return &APIError{Code: ErrInvalidAPIKey, Message: "Invalid API Key"}
}

func NewModelNotAllowed(model string) *APIError {
	return &APIError{Code: ErrModelNotAllowed, Message: "Model not allowed: " + model}
}

func NewRateLimited() *APIError {
	return &APIError{Code: ErrRateLimited, Message: "Rate limit exceeded"}
}

func NewProviderError(detail string) *APIError {
	return &APIError{Code: ErrProviderError, Message: "Provider error: " + detail}
}

func NewProviderUnavailable() *APIError {
	return &APIError{Code: ErrProviderUnavailable, Message: "All providers unavailable"}
}

func NewUpstreamTimeout() *APIError {
	return &APIError{Code: ErrUpstreamTimeout, Message: "Upstream request timeout"}
}

// UpstreamError 表示上游供应商返回的错误，包含 HTTP 状态码和响应体。
// 用于区分客户端错误（4xx，不应重试）和服务端错误（5xx，可重试）。
type UpstreamError struct {
	StatusCode int
	Body       string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream status %d: %s", e.StatusCode, e.Body)
}

// IsClientError 判断上游错误是否为客户端错误（4xx），此类错误不应重试。
func (e *UpstreamError) IsClientError() bool {
	return e.StatusCode >= 400 && e.StatusCode < 500
}