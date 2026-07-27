package errors

import (
	"encoding/json"
	"net/http"
)

type ErrorCode string

const (
	ErrInvalidRequest      ErrorCode = "INVALID_REQUEST"
	ErrInvalidAPIKey       ErrorCode = "INVALID_API_KEY"
	ErrModelNotAllowed     ErrorCode = "MODEL_NOT_ALLOWED"
	ErrRateLimited         ErrorCode = "RATE_LIMITED"
	ErrProviderError       ErrorCode = "PROVIDER_ERROR"
	ErrProviderUnavailable ErrorCode = "PROVIDER_UNAVAILABLE"
	ErrUpstreamTimeout     ErrorCode = "UPSTREAM_TIMEOUT"
)

type APIError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type ErrorResponse struct {
	Error APIError `json:"error"`
}

func (e *APIError) ToHTTP(w http.ResponseWriter, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{Error: *e})
}

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