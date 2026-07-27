### Task 1.1: 项目初始化 + 模块骨架

**Files:**
- Create: `go.mod`
- Create: `cmd/gateway/main.go`
- Create: `internal/errors/errors.go`
- Create: `internal/model/model.go`

**Interfaces:**
- Produces: 项目模块结构、错误码类型、内部模型类型

- [ ] **Step 1: 初始化 Go 模块**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go mod init go-gateway
```

- [ ] **Step 2: 创建统一错误码**

`internal/errors/errors.go`:

```go
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
```

- [ ] **Step 3: 创建内部标准模型**

`internal/model/model.go`:

```go
package model

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ChatMessage struct {
	Role       Role              `json:"role"`
	Content    string            `json:"content"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []InternalToolCall `json:"tool_calls,omitempty"`
	Name       string            `json:"name,omitempty"`
}

type InternalToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function InternalFunction `json:"function"`
}

type InternalFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type InternalToolDef struct {
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function"`
}

type ChatCompletionRequest struct {
	Model       string            `json:"model"`
	Messages    []ChatMessage     `json:"messages"`
	Stream      bool              `json:"stream,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Temperature float64           `json:"temperature,omitempty"`
	TopP        float64           `json:"top_p,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	ToolChoice  interface{}       `json:"tool_choice,omitempty"`
	Stop        interface{}       `json:"stop,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatCompletionResponse struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Created           int64             `json:"created"`
	Model             string            `json:"model"`
	Choices           []Choice          `json:"choices"`
	Usage             *Usage            `json:"usage,omitempty"`
	SystemFingerprint string            `json:"system_fingerprint,omitempty"`
}

type Choice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type StreamChunk struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	Choices           []StreamChoice `json:"choices"`
	SystemFingerprint string         `json:"system_fingerprint,omitempty"`
}

type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        Delta       `json:"delta"`
	FinishReason *string     `json:"finish_reason,omitempty"`
}

type Delta struct {
	Role      string            `json:"role,omitempty"`
	Content   string            `json:"content,omitempty"`
	ToolCalls []InternalToolCall `json:"tool_calls,omitempty"`
}
```

- [ ] **Step 4: 创建 main.go 骨架（含优雅关闭）**

`cmd/gateway/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-gateway/internal/config"
	"go-gateway/internal/server"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	handler := server.NewHandler(cfg)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	go func() {
		log.Printf("Gateway listening on :%d", cfg.Server.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced shutdown: %v", err)
	}
	log.Println("Server exited")
}
```

- [ ] **Step 5: 验证编译通过**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

Expected: 无错误输出，生成可执行文件。

- [ ] **Step 6: 提交**

```bash
git init
git add -A
git commit -m "feat: project scaffolding with error codes and internal model types"
```

---
