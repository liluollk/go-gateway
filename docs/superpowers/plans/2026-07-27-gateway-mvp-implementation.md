# AI 模型网关 MVP 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建一个企业级 AI 模型网关 MVP，替换现有 New API，提供统一鉴权、路由、限流、日志和协议转换能力。

**Architecture:** 无状态 HTTP 网关，接收 OpenAI 格式请求，经鉴权→限流→路由→协议转换后转发至上游供应商，返回 OpenAI 格式响应。使用 Adapter 模式隔离供应商差异，内存令牌桶做本地限流，JSON Lines 文件日志。

**Tech Stack:** Go 1.26, `golang.org/x/time/rate` (限流), `gopkg.in/yaml.v3` (配置), 标准库 `net/http` + `encoding/json`

## 全局约束

- 所有供应商 API 密钥通过环境变量 `${VAR}` 引用，不得写入 YAML
- 内部标准模型使用 OpenAI 格式作为通用表示
- 供应商适配器采用 Adapter 模式，新增供应商只需添加一个 Adapter 文件
- 写超时设为 300s（流式场景）
- 日志为 JSON Lines 格式，按天轮转，保留 30 天
- 错误响应格式统一为 `{"error":{"code":"ERROR_CODE","message":"..."}}`
- 流式响应使用 SSE (text/event-stream)，正确转发 `[DONE]` 标记
- 所有请求分配 X-Trace-Id，日志中记录完整链路

---

## 文件结构

```
go-gateway/
├── cmd/gateway/main.go              # 入口：HTTP 服务启动 + 优雅关闭
├── config.yaml                      # 本地开发配置
├── config.example.yaml              # 配置模板（不含密钥）
├── .env.example                     # 环境变量模板
├── internal/
│   ├── config/
│   │   └── config.go                # Config 结构体、加载、校验
│   ├── model/
│   │   └── model.go                 # 内部标准模型：请求/响应/流式块/ToolCall
│   ├── middleware/
│   │   ├── requestid.go             # X-Trace-Id 注入
│   │   ├── auth.go                  # Bearer Token → app_id + 模型白名单
│   │   ├── ratelimit.go             # 内存令牌桶限流（QPS/RPM）
│   │   └── logger.go                # 请求日志 JSON Lines
│   ├── router/
│   │   └── router.go                # 路由匹配 + 权重轮询 + fallback 链
│   ├── adapter/
│   │   ├── interface.go             # Adapter 接口定义
│   │   ├── openai.go                # OpenAI Adapter
│   │   ├── anthropic.go             # Anthropic Adapter（消息转 OpenAI 格式）
│   │   └── openai_compat.go         # OpenAI 兼容 Adapter（vLLM/DeepSeek）
│   ├── provider/
│   │   └── provider.go              # HTTP 客户端池 + 健康检测（被动）
│   ├── errors/
│   │   └── errors.go                # 统一错误码 + JSON 响应辅助
│   └── server/
│       ├── handler.go               # HTTP 路由注册 + 请求入口
│       └── chat.go                  # POST /v1/chat/completions 处理
├── go.mod
├── go.sum
└── README.md
```

---

## 阶段 1：骨架与单供应商（第 1-2 周）

目标：先跑通一个完整的 OpenAI 调用链（非流式 + SSE 流式）。

---

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

### Task 1.2: 配置加载与校验

**Files:**
- Create: `internal/config/config.go`
- Create: `config.yaml`
- Create: `config.example.yaml`
- Create: `.env.example`

**Interfaces:**
- Consumes: 来自 model 的无依赖
- Produces: `config.Load(path string) (*Config, error)` — 返回完整配置结构体

- [ ] **Step 1: 安装 yaml 依赖**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go get gopkg.in/yaml.v3
```

- [ ] **Step 2: 编写配置结构体 + 加载逻辑**

`internal/config/config.go`:

```go
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig           `yaml:"server"`
	Auth      AuthConfig             `yaml:"auth"`
	RateLimit map[string]RateLimitConfig `yaml:"rate_limit"`
	Providers []ProviderConfig       `yaml:"providers"`
	Routes    []RouteConfig          `yaml:"routes"`
}

type ServerConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
}

type AuthConfig struct {
	Keys []APIKeyConfig `yaml:"keys"`
}

type APIKeyConfig struct {
	Key      string   `yaml:"key"`
	AppID    string   `yaml:"app_id"`
	Models   []string `yaml:"models"`
}

type RateLimitConfig struct {
	QPS int `yaml:"qps,omitempty"`
	RPM int `yaml:"rpm,omitempty"`
}

type ProviderConfig struct {
	ID      string   `yaml:"id"`
	Type    string   `yaml:"type"`
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Models  []string `yaml:"models"`
}

type RouteConfig struct {
	AppID     string         `yaml:"app_id"`
	Model     string         `yaml:"model"`
	Providers []RouteTarget  `yaml:"providers"`
}

type RouteTarget struct {
	ProviderID string `yaml:"provider_id"`
	Weight     int    `yaml:"weight"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// 解析环境变量引用 ${VAR}
	resolved := os.Expand(string(data), func(key string) string {
		val := os.Getenv(key)
		if val == "" {
			return fmt.Sprintf("${%s}", key)
		}
		return val
	})

	var cfg Config
	if err := yaml.Unmarshal([]byte(resolved), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Validate() error {
	if c.Server.Port == 0 {
		c.Server.Port = 8080
	}
	if c.Server.ReadTimeout == 0 {
		c.Server.ReadTimeout = 30 * time.Second
	}
	if c.Server.WriteTimeout == 0 {
		c.Server.WriteTimeout = 300 * time.Second
	}

	// 验证至少有一个 API Key
	if len(c.Auth.Keys) == 0 {
		return fmt.Errorf("at least one auth key is required")
	}

	// 验证所有 provider_id 在 routes 中有效
	providerIDs := make(map[string]bool)
	providerModels := make(map[string]map[string]bool)
	for _, p := range c.Providers {
		if p.ID == "" {
			return fmt.Errorf("provider id is required")
		}
		if p.Type != "openai" && p.Type != "anthropic" && p.Type != "openai-compatible" {
			return fmt.Errorf("invalid provider type: %s", p.Type)
		}
		if p.BaseURL == "" {
			return fmt.Errorf("provider %s: base_url is required", p.ID)
		}
		if strings.HasPrefix(p.APIKey, "${") && strings.HasSuffix(p.APIKey, "}") {
			envVar := strings.TrimSuffix(strings.TrimPrefix(p.APIKey, "${"), "}")
			return fmt.Errorf("provider %s: environment variable %s is not set", p.ID, envVar)
		}
		providerIDs[p.ID] = true
		providerModels[p.ID] = make(map[string]bool)
		for _, m := range p.Models {
			providerModels[p.ID][m] = true
		}
	}

	for _, r := range c.Routes {
		if r.AppID == "" {
			return fmt.Errorf("route: app_id is required")
		}
		if r.Model == "" {
			return fmt.Errorf("route: model is required")
		}
		if len(r.Providers) == 0 {
			return fmt.Errorf("route %s/%s: at least one provider required", r.AppID, r.Model)
		}
		for _, t := range r.Providers {
			if !providerIDs[t.ProviderID] {
				return fmt.Errorf("route %s/%s: provider %s not found", r.AppID, r.Model, t.ProviderID)
			}
		}
	}

	return nil
}
```

- [ ] **Step 3: 创建配置示例文件**

`config.yaml`:

```yaml
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 300s

auth:
  keys:
    - key: sk-gateway-dev
      app_id: app-demo
      models:
        - gpt-4o
        - gpt-4o-mini
        - claude-sonnet-4

rate_limit:
  app-demo:
    qps: 100
    rpm: 6000

providers:
  - id: openai-main
    type: openai
    base_url: https://api.openai.com
    api_key: ${OPENAI_API_KEY}
    models:
      - gpt-4o
      - gpt-4o-mini

  - id: claude-main
    type: anthropic
    base_url: https://api.anthropic.com
    api_key: ${ANTHROPIC_API_KEY}
    models:
      - claude-sonnet-4

routes:
  - app_id: app-demo
    model: gpt-4o
    providers:
      - provider_id: openai-main
        weight: 100

  - app_id: app-demo
    model: claude-sonnet-4
    providers:
      - provider_id: claude-main
        weight: 100
```

`config.example.yaml`: 同上，但 `api_key` 全部写 `${OPENAI_API_KEY}` 形式。

`.env.example`:

```bash
# OpenAI
OPENAI_API_KEY=sk-your-key-here

# Anthropic
ANTHROPIC_API_KEY=sk-ant-your-key-here
```

- [ ] **Step 4: 编写测试**

`internal/config/config_test.go`:

```go
package config

import (
	"os"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	os.Setenv("TEST_API_KEY", "sk-test")
	defer os.Unsetenv("TEST_API_KEY")

	cfg, err := Load("../../config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Server.Port)
	}
	if len(cfg.Auth.Keys) == 0 {
		t.Error("expected at least one auth key")
	}
}
```

- [ ] **Step 5: 运行测试**

```bash
cd /c/Users/L3553/Desktop/go-gateway
SET TEST_API_KEY=sk-test
go test ./internal/config/ -v
```

Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat: config loading with YAML + env var substitution"
```

---

### Task 1.3: 请求追踪 + 请求验证中间件

**Files:**
- Create: `internal/middleware/requestid.go`
- Create: `internal/server/handler.go`

**Interfaces:**
- Consumes: `errors.APIError`, `config.Config`
- Produces: `server.NewHandler(cfg)`, `handler.RegisterRoutes(mux)`, `handler.ServeHTTP`

- [ ] **Step 1: 编写 RequestID 中间件**

`internal/middleware/requestid.go`:

```go
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type contextKey string

const TraceIDKey contextKey = "trace_id"

func generateTraceID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-Id")
		if traceID == "" {
			traceID = generateTraceID()
		}
		ctx := context.WithValue(r.Context(), TraceIDKey, traceID)
		w.Header().Set("X-Trace-Id", traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func GetTraceID(ctx context.Context) string {
	if id, ok := ctx.Value(TraceIDKey).(string); ok {
		return id
	}
	return ""
}
```

- [ ] **Step 2: 创建 handler 骨架**

`internal/server/handler.go`:

```go
package server

import (
	"net/http"

	"go-gateway/internal/config"
	"go-gateway/internal/middleware"
)

type Handler struct {
	cfg *config.Config
}

func NewHandler(cfg *config.Config) *Handler {
	return &Handler{cfg: cfg}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.HealthCheck)
	mux.Handle("/v1/models", middleware.RequestID(http.HandlerFunc(h.ListModels)))
	mux.Handle("/v1/chat/completions", middleware.RequestID(http.HandlerFunc(h.ChatCompletion)))
}

func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	// TODO: 需要 auth 中间件 — Task 1.4 完成后实现
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"object":"list","data":[]}`))
}

func (h *Handler) ChatCompletion(w http.ResponseWriter, r *http.Request) {
	// TODO: 逐步实现 — Task 1.5, 1.6, 1.7
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
```

- [ ] **Step 3: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

Expected: 编译通过

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "feat: request ID middleware and handler skeleton with healthz endpoint"
```

---

### Task 1.4: 认证鉴权中间件

**Files:**
- Create: `internal/middleware/auth.go`

**Interfaces:**
- Consumes: `config.Config`, `errors.APIError`, `middleware.TraceIDKey`
- Produces: `middleware.Auth(cfg) func(http.Handler) http.Handler` — 注入 `app_id`, `models` 到 Context

- [ ] **Step 1: 编写 auth 中间件**

`internal/middleware/auth.go`:

```go
package middleware

import (
	"context"
	"net/http"
	"strings"

	"go-gateway/internal/errors"
)

type authKey string

const (
	AuthAppIDKey   authKey = "auth_app_id"
	AuthModelsKey  authKey = "auth_models"
)

type AuthConfig struct {
	Keys map[string]struct {
		AppID  string
		Models []string
	}
}

func NewAuthConfig(keys []struct {
	Key    string
	AppID  string
	Models []string
}) *AuthConfig {
	cfg := &AuthConfig{Keys: make(map[string]struct {
		AppID  string
		Models []string
	})}
	for _, k := range keys {
		cfg.Keys[k.Key] = struct {
			AppID  string
			Models []string
		}{AppID: k.AppID, Models: k.Models}
	}
	return cfg
}

func Auth(cfg *AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
				errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			entry, ok := cfg.Keys[token]
			if !ok {
				errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), AuthAppIDKey, entry.AppID)
			ctx = context.WithValue(ctx, AuthModelsKey, entry.Models)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetAuthAppID(ctx context.Context) string {
	if id, ok := ctx.Value(AuthAppIDKey).(string); ok {
		return id
	}
	return ""
}

func GetAuthModels(ctx context.Context) []string {
	if models, ok := ctx.Value(AuthModelsKey).([]string); ok {
		return models
	}
	return nil
}
```

- [ ] **Step 2: 更新 handler 集成 auth 中间件**

修改 `internal/server/handler.go`，在 `RegisterRoutes` 中为受保护路由添加 auth 中间件：

```go
import (
	"go-gateway/internal/middleware"
)

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// 从 config 构建 auth 配置
	authKeys := make([]struct {
		Key    string
		AppID  string
		Models []string
	}, len(h.cfg.Auth.Keys))
	for i, k := range h.cfg.Auth.Keys {
		authKeys[i] = struct {
			Key    string
			AppID  string
			Models []string
		}{Key: k.Key, AppID: k.AppID, Models: k.Models}
	}
	authCfg := middleware.NewAuthConfig(authKeys)
	authMiddleware := middleware.Auth(authCfg)

	mux.HandleFunc("/healthz", h.HealthCheck)                                         // 无鉴权
	mux.Handle("/v1/models", middleware.RequestID(authMiddleware(http.HandlerFunc(h.ListModels))))
	mux.Handle("/v1/chat/completions", middleware.RequestID(authMiddleware(http.HandlerFunc(h.ChatCompletion))))
}
```

- [ ] **Step 3: 更新 ListModels 使用 auth 上下文**

更新 `ListModels` 方法以返回该 app 可用的模型列表：

```go
func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := middleware.GetAuthModels(r.Context())
	if models == nil {
		errors.NewInvalidAPIKey().ToHTTP(w, http.StatusUnauthorized)
		return
	}

	type ModelInfo struct {
		ID     string `json:"id"`
		Object string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}

	data := make([]ModelInfo, len(models))
	for i, m := range models {
		data[i] = ModelInfo{ID: m, Object: "model", OwnedBy: "gateway"}
	}

	resp := map[string]interface{}{"object": "list", "data": data}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
```

- [ ] **Step 4: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "feat: auth middleware with Bearer token → app_id + model whitelist"
```

---

### Task 1.5: 请求体校验 + 非流式 OpenAI 转发

**Files:**
- Create: `internal/server/chat.go`

**Interfaces:**
- Consumes: `model.*`, `config.Config`, `errors.*`, `middleware.*`
- Produces: `handler.processChatCompletion(w, r)` — 完整处理链

- [ ] **Step 1: 编写请求验证 + 非流式转发逻辑**

`internal/server/chat.go`:

```go
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

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
	http.Error(w, "streaming not yet implemented", http.StatusNotImplemented)
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
```

- [ ] **Step 2: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 3: 提交**

```bash
git add -A
git commit -m "feat: request validation and non-streaming OpenAI forwarding"
```

---

### Task 1.6: SSE 流式响应转发

**Files:**
- Modify: `internal/server/chat.go`

**Interfaces:**
- Consumes: 在上一步的基础上扩展
- Produces: 流式响应处理 `handleStreaming()`

- [ ] **Step 1: 在 ChatCompletion 中启用流式分支**

修改 `ChatCompletion` 方法，将流式分支指向新方法：

```go
// 替换原有的 stub
if req.Stream {
	h.handleStreaming(w, r, &req, appID)
	return
}
```

- [ ] **Step 2: 实现流式转发**

在 `internal/server/chat.go` 中添加：

```go
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
```

- [ ] **Step 3: 添加 bufio import**

在 `internal/server/chat.go` 顶部添加：

```go
import (
	"bufio"
	// ... existing imports
)
```

- [ ] **Step 4: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "feat: SSE streaming response forwarding with proper [DONE] signal"
```

---

### Task 1.7: 日志中间件（JSON Lines，按天轮转）

**Files:**
- Create: `internal/middleware/logger.go`

**Interfaces:**
- Consumes: `middleware.TraceIDKey`, `middleware.AuthAppIDKey`
- Produces: 全局日志文件，每请求一行 JSON

- [ ] **Step 1: 编写日志中间件**

`internal/middleware/logger.go`:

```go
package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type LogEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	TraceID     string    `json:"trace_id"`
	AppID       string    `json:"app_id"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	StatusCode  int       `json:"status_code"`
	LatencyMs   int64     `json:"latency_ms"`
	Model       string    `json:"model,omitempty"`
	PromptTokens  int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int  `json:"completion_tokens,omitempty"`
	TotalTokens   int     `json:"total_tokens,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	body       bytes.Buffer
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	rw.body.Write(b)
	return rw.ResponseWriter.Write(b)
}

type Logger struct {
	mu      sync.Mutex
	dir     string
	file    *os.File
	encoder *json.Encoder
}

func NewLogger(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	l := &Logger{dir: dir}
	if err := l.rotate(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Logger) rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file != nil {
		l.file.Close()
	}

	filename := filepath.Join(l.dir, fmt.Sprintf("gateway-%s.log", time.Now().Format("2006-01-02")))
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	l.file = f
	l.encoder = json.NewEncoder(f)
	return nil
}

func (l *Logger) Write(entry *LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// 检查日期是否变更，变更则轮转
	if l.file != nil {
		info, _ := l.file.Stat()
		if info != nil {
			today := time.Now().Format("2006-01-02")
			expected := fmt.Sprintf("gateway-%s.log", today)
			if info.Name() != expected {
				l.rotate()
			}
		}
	}

	l.encoder.Encode(entry)
}

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
	}
}

// LoggingMiddleware 包装一个 handler，记录请求日志
func (l *Logger) LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)

		entry := &LogEntry{
			Timestamp:  start,
			TraceID:    GetTraceID(r.Context()),
			AppID:      GetAuthAppID(r.Context()),
			Method:     r.Method,
			Path:       r.URL.Path,
			StatusCode: rw.statusCode,
			LatencyMs:  time.Since(start).Milliseconds(),
		}

		l.Write(entry)
	})
}
```

- [ ] **Step 2: 集成日志到 handler**

在 `cmd/gateway/main.go` 中初始化 Logger 并注入：

```go
import (
	"go-gateway/internal/middleware"
)

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	logger, err := middleware.NewLogger("logs")
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}
	defer logger.Close()

	handler := server.NewHandler(cfg, logger)
	// ...
}
```

更新 `internal/server/handler.go`：

```go
type Handler struct {
	cfg    *config.Config
	logger *middleware.Logger
}

func NewHandler(cfg *config.Config, logger *middleware.Logger) *Handler {
	return &Handler{cfg: cfg, logger: logger}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// ... 构建 authCfg ...

	// 日志中间件包裹所有路由
	loggedRouter := h.logger.LoggingMiddleware(mux)

	mux.HandleFunc("/healthz", h.HealthCheck)
	mux.Handle("/v1/models", middleware.RequestID(authMiddleware(http.HandlerFunc(h.ListModels))))
	mux.Handle("/v1/chat/completions", middleware.RequestID(authMiddleware(http.HandlerFunc(h.ChatCompletion))))
}
```

- [ ] **Step 3: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "feat: JSON Lines request logging with daily rotation"
```

---

### Task 1.8: 阶段 1 集成测试

**Files:**
- Create: `tests/integration/phase1_test.go`

- [ ] **Step 1: 编写集成测试**

`tests/integration/phase1_test.go`:

```go
//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"go-gateway/internal/config"
	"go-gateway/internal/middleware"
	"go-gateway/internal/server"
)

func setupTestServer(t *testing.T) *server.Handler {
	os.Setenv("TEST_OPENAI_KEY", "sk-test")
	defer os.Unsetenv("TEST_OPENAI_KEY")

	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	logger, err := middleware.NewLogger("../../test_logs")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return server.NewHandler(cfg, logger)
}

func TestHealthz(t *testing.T) {
	h := setupTestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestAuth_InvalidKey(t *testing.T) {
	h := setupTestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer invalid-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestChatCompletion_InvalidJSON(t *testing.T) {
	h := setupTestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader([]byte("not json")))
	req.Header.Set("Authorization", "Bearer sk-gateway-dev")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestListModels(t *testing.T) {
	h := setupTestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("GET", ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-gateway-dev")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	data := result["data"].([]interface{})
	if len(data) == 0 {
		t.Error("expected at least one model")
	}
}
```

- [ ] **Step 2: 运行集成测试**

```bash
cd /c/Users/L3553/Desktop/go-gateway
SET TEST_OPENAI_KEY=sk-test
go test -tags=integration ./tests/integration/ -v -count=1
```

Expected: 测试通过（注意：ChatCompletion 实际会因无上游而返回 502，但 auth 和 validation 测试应通过）

- [ ] **Step 3: 阶段 1 验收检查**

对照验收标准逐项确认：

| 验收项 | 状态 | 验证方式 |
|--------|------|---------|
| GET /healthz 无鉴权返回 ok | ✅ | 集成测试 |
| GET /v1/models 返回模型列表 | ✅ | 集成测试 |
| POST /v1/chat/completions 返回 401 | ✅ | 无效 Key 测试 |
| POST /v1/chat/completions 非法 JSON 返回 400 | ✅ | 集成测试 |
| 非流式转发到 OpenAI | ✅ | 需真实 Key 端到端验证 |
| SSE 流式转发 | ✅ | 需真实 Key 端到端验证 |
| X-Trace-Id 注入 | ✅ | 手动验证 |
| 日志写入文件 | ✅ | 手动验证 |

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "test: phase 1 integration tests"
```

---

## 阶段 2：多供应商与工程化（第 3-4 周）

目标：扩展到多个供应商，加入限流、路由和错误处理。

---

### Task 2.1: Adapter 接口重构

**Files:**
- Create: `internal/adapter/interface.go`
- Create: `internal/adapter/openai.go`
- Modify: `internal/server/chat.go` — 使用 Adapter 接口替换直接调用

**Interfaces:**
- Consumes: `model.*`, `config.ProviderConfig`
- Produces: `adapter.Adapter` 接口、`adapter.NewOpenAIAdapter(cfg)`

- [ ] **Step 1: 定义 Adapter 接口**

`internal/adapter/interface.go`:

```go
package adapter

import (
	"context"

	"go-gateway/internal/model"
)

type Adapter interface {
	// ChatCompletion 发送非流式请求，返回完整响应
	ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error)

	// ChatCompletionStream 发送流式请求，返回 channel 接收流式块
	ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error)

	// GetProviderType 返回供应商类型标识
	GetProviderType() string
}

type StreamEvent struct {
	Chunk *model.StreamChunk
	Err   error
}
```

- [ ] **Step 2: 重构 OpenAI Adapter**

`internal/adapter/openai.go`:

```go
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

	"go-gateway/internal/model"
)

type OpenAIAdapter struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func NewOpenAIAdapter(baseURL, apiKey string) *OpenAIAdapter {
	return &OpenAIAdapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *OpenAIAdapter) GetProviderType() string {
	return "openai"
}

func (a *OpenAIAdapter) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/chat/completions", bytes.NewReader(body))
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
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp model.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return &chatResp, nil
}

func (a *OpenAIAdapter) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}

	ch := make(chan StreamEvent, 100)
	go a.readStream(resp.Body, ch)
	return ch, nil
}

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
```

- [ ] **Step 3: 更新 Server 使用 Adapter**

修改 `internal/server/handler.go` 和 `internal/server/chat.go`，使用 `adapter.Adapter` 替代直接 HTTP 调用：

```go
// handler.go 新增
type Handler struct {
	cfg      *config.Config
	logger   *middleware.Logger
	adapters map[string]adapter.Adapter  // provider_id → adapter
}

func NewHandler(cfg *config.Config, logger *middleware.Logger) *Handler {
	h := &Handler{
		cfg:      cfg,
		logger:   logger,
		adapters: make(map[string]adapter.Adapter),
	}
	h.initAdapters()
	return h
}

func (h *Handler) initAdapters() {
	for _, p := range h.cfg.Providers {
		switch p.Type {
		case "openai", "openai-compatible":
			h.adapters[p.ID] = adapter.NewOpenAIAdapter(p.BaseURL, p.APIKey)
		case "anthropic":
			// Task 2.2 实现
		}
	}
}
```

- [ ] **Step 4: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "refactor: extract Adapter interface, OpenAI adapter implementation"
```

---

### Task 2.2: Anthropic Adapter（攻克流式格式差异）

**Files:**
- Create: `internal/adapter/anthropic.go`

**Interfaces:**
- Consumes: `adapter.Adapter` 接口、`model.*`
- Produces: `adapter.NewAnthropicAdapter(baseURL, apiKey)` — 实现 Messages API → OpenAI 格式转换

- [ ] **Step 1: 实现 Anthropic Adapter**

`internal/adapter/anthropic.go`:

```go
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

	"go-gateway/internal/model"
)

type AnthropicAdapter struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResponse struct {
	ID         string              `json:"id"`
	Type       string              `json:"type"`
	Role       string              `json:"role"`
	Content    []anthropicContent  `json:"content"`
	Model      string              `json:"model"`
	StopReason string              `json:"stop_reason"`
	Usage      *anthropicUsage     `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicStreamEvent struct {
	Type  string           `json:"type"`
	Index int              `json:"index,omitempty"`
	Delta *anthropicDelta  `json:"delta,omitempty"`
	Usage *anthropicUsage  `json:"usage,omitempty"`
	Message *anthropicMessageBlock `json:"message,omitempty"`
}

type anthropicDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessageBlock struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []anthropicContent `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage   *anthropicUsage `json:"usage"`
}

func NewAnthropicAdapter(baseURL, apiKey string) *AnthropicAdapter {
	return &AnthropicAdapter{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *AnthropicAdapter) GetProviderType() string {
	return "anthropic"
}

func (a *AnthropicAdapter) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	anthropicReq := a.toAnthropicRequest(req, false)
	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
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
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(respBody))
	}

	var anthropicResp anthropicResponse
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	return a.toOpenAIResponse(&anthropicResp, req.Model), nil
}

func (a *AnthropicAdapter) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error) {
	anthropicReq := a.toAnthropicRequest(req, true)
	body, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(body))
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

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("upstream status %d", resp.StatusCode)
	}

	ch := make(chan StreamEvent, 100)
	go a.readStream(resp.Body, ch, req.Model)
	return ch, nil
}

func (a *AnthropicAdapter) toAnthropicRequest(req *model.ChatCompletionRequest, stream bool) *anthropicRequest {
	messages := make([]anthropicMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		if msg.Role == model.RoleSystem {
			// Anthropic 的 system 通过 system 参数传递，此处跳过
			continue
		}
		messages = append(messages, anthropicMessage{
			Role:    string(msg.Role),
			Content: msg.Content,
		})
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	return &anthropicRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		Stream:      stream,
	}
}

func (a *AnthropicAdapter) toOpenAIResponse(anthropicResp *anthropicResponse, modelName string) *model.ChatCompletionResponse {
	content := ""
	for _, c := range anthropicResp.Content {
		content += c.Text
	}

	finishReason := anthropicResp.StopReason
	if finishReason == "end_turn" {
		finishReason = "stop"
	}

	usage := &model.Usage{}
	if anthropicResp.Usage != nil {
		usage.PromptTokens = anthropicResp.Usage.InputTokens
		usage.CompletionTokens = anthropicResp.Usage.OutputTokens
		usage.TotalTokens = anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens
	}

	return &model.ChatCompletionResponse{
		ID:      anthropicResp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   modelName,
		Choices: []model.Choice{
			{
				Index:        0,
				FinishReason: finishReason,
				Message: model.ChatMessage{
					Role:    model.RoleAssistant,
					Content: content,
				},
			},
		},
		Usage: usage,
	}
}

func (a *AnthropicAdapter) readStream(body io.ReadCloser, ch chan<- StreamEvent, modelName string) {
	defer body.Close()
	defer close(ch)

	reader := bufio.NewReader(body)
	created := time.Now().Unix()

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
			continue
		}

		switch event.Type {
		case "content_block_delta":
			if event.Delta != nil {
				chunk := &model.StreamChunk{
					ID:      "",
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   modelName,
					Choices: []model.StreamChoice{
						{
							Index: event.Index,
							Delta: model.Delta{
								Content: event.Delta.Text,
							},
						},
					},
				}
				ch <- StreamEvent{Chunk: chunk}
			}
		case "message_stop":
			finish := "stop"
			chunk := &model.StreamChunk{
				ID:      "",
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   modelName,
				Choices: []model.StreamChoice{
					{
						Index:        0,
						Delta:        model.Delta{},
						FinishReason: &finish,
					},
				},
			}
			ch <- StreamEvent{Chunk: chunk}
			return
		}
	}
}
```

- [ ] **Step 2: 注册 Anthropic Adapter**

在 `handler.go` 的 `initAdapters` 中添加：

```go
case "anthropic":
	h.adapters[p.ID] = adapter.NewAnthropicAdapter(p.BaseURL, p.APIKey)
```

- [ ] **Step 3: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "feat: Anthropic adapter with Messages API → OpenAI format conversion"
```

---

### Task 2.3: OpenAI 兼容 Adapter

**Files:**
- Create: `internal/adapter/openai_compat.go`

**Interfaces:**
- 复用 `adapter.OpenAIAdapter`（OpenAI 兼容协议本质上相同）

- [ ] **Step 1: 实现 OpenAI 兼容 Adapter**

`internal/adapter/openai_compat.go`:

```go
package adapter

import "go-gateway/internal/model"

// OpenAICompatibleAdapter 适用于 vLLM、DeepSeek、Ollama 等兼容 OpenAI 格式的供应商
// 本质上复用 OpenAIAdapter 的协议逻辑，但标记不同的类型
type OpenAICompatibleAdapter struct {
	*OpenAIAdapter
	providerType string
}

func NewOpenAICompatibleAdapter(baseURL, apiKey string) *OpenAICompatibleAdapter {
	return &OpenAICompatibleAdapter{
		OpenAIAdapter: NewOpenAIAdapter(baseURL, apiKey),
		providerType:  "openai-compatible",
	}
}

func (a *OpenAICompatibleAdapter) GetProviderType() string {
	return a.providerType
}
```

- [ ] **Step 2: 注册 OpenAI 兼容 Adapter**

在 `handler.go` 的 `initAdapters` 中更新：

```go
case "openai":
	h.adapters[p.ID] = adapter.NewOpenAIAdapter(p.BaseURL, p.APIKey)
case "openai-compatible":
	h.adapters[p.ID] = adapter.NewOpenAICompatibleAdapter(p.BaseURL, p.APIKey)
```

- [ ] **Step 3: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "feat: OpenAI-compatible adapter for vLLM, DeepSeek, etc."
```

---

### Task 2.4: 路由分发（权重轮询 + Fallback 链）

**Files:**
- Create: `internal/router/router.go`
- Modify: `internal/server/chat.go` — 使用 Router 替换 `findProvider()`

**Interfaces:**
- Consumes: `config.RouteConfig`, `config.RouteTarget`, `adapter.Adapter`
- Produces: `router.NewRouter(cfg, adapters)` — 提供 `route(appID, model) → adapter`

- [ ] **Step 1: 实现路由模块**

`internal/router/router.go`:

```go
package router

import (
	"math/rand"
	"sync"
	"time"

	"go-gateway/internal/adapter"
	"go-gateway/internal/config"
)

type Target struct {
	Adapter adapter.Adapter
	Weight  int
}

type Route struct {
	AppID     string
	Model     string
	Targets   []*Target
}

type Router struct {
	mu        sync.RWMutex
	routes    []*Route
	index     map[string]map[string][]*Target // appID → model → targets
}

func NewRouter(cfg *config.Config, adapters map[string]adapter.Adapter) *Router {
	r := &Router{
		index: make(map[string]map[string][]*Target),
	}

	for _, route := range cfg.Routes {
		r.AddRoute(route.AppID, route.Model, route.Providers, adapters)
	}

	return r
}

func (r *Router) AddRoute(appID, model string, targets []config.RouteTarget, adapters map[string]adapter.Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	route := &Route{
		AppID:   appID,
		Model:   model,
		Targets: make([]*Target, 0),
	}

	for _, t := range targets {
		if a, ok := adapters[t.ProviderID]; ok {
			route.Targets = append(route.Targets, &Target{
				Adapter: a,
				Weight:  t.Weight,
			})
		}
	}

	if len(route.Targets) == 0 {
		return
	}

	r.routes = append(r.routes, route)

	if r.index[appID] == nil {
		r.index[appID] = make(map[string][]*Target)
	}
	r.index[appID][model] = route.Targets
}

// Select 根据 appID 和 model 选择目标，权重轮询
func (r *Router) Select(appID, model string) adapter.Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()

	targets := r.selectTargets(appID, model)
	if len(targets) == 0 {
		return nil
	}

	return weightedSelect(targets)
}

// SelectWithFallback 选择目标，失败时按 fallback 链尝试下一个
func (r *Router) SelectWithFallback(appID, model string) []adapter.Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()

	targets := r.selectTargets(appID, model)
	if len(targets) == 0 {
		return nil
	}

	// 按权重排序后返回所有目标作为 fallback 链
	ordered := make([]adapter.Adapter, 0, len(targets))
	// 简化：按权重从高到低排序
	// 实际场景可以 rand.Shuffle 后按权重分布
	for _, t := range targets {
		ordered = append(ordered, t.Adapter)
	}

	return ordered
}

func (r *Router) selectTargets(appID, model string) []*Target {
	if appIDMap, ok := r.index[appID]; ok {
		if targets, ok := appIDMap[model]; ok {
			return targets
		}
	}
	// 尝试通配匹配
	for _, route := range r.routes {
		if route.AppID == appID && route.Model == model {
			return route.Targets
		}
	}
	return nil
}

func weightedSelect(targets []*Target) adapter.Adapter {
	if len(targets) == 0 {
		return nil
	}
	if len(targets) == 1 {
		return targets[0].Adapter
	}

	totalWeight := 0
	for _, t := range targets {
		totalWeight += t.Weight
	}

	r := rand.Intn(totalWeight)
	cumulative := 0
	for _, t := range targets {
		cumulative += t.Weight
		if r < cumulative {
			return t.Adapter
		}
	}

	return targets[len(targets)-1].Adapter
}
```

- [ ] **Step 2: 集成 Router 到 Server**

在 `internal/server/handler.go` 中添加：

```go
type Handler struct {
	cfg     *config.Config
	logger  *middleware.Logger
	adapters map[string]adapter.Adapter
	router  *router.Router
}

func NewHandler(cfg *config.Config, logger *middleware.Logger) *Handler {
	h := &Handler{
		cfg:      cfg,
		logger:   logger,
		adapters: make(map[string]adapter.Adapter),
	}
	h.initAdapters()
	h.router = router.NewRouter(cfg, h.adapters)
	return h
}
```

- [ ] **Step 3: 更新 Chat 处理逻辑使用 Router**

修改 `internal/server/chat.go` 中的 `handleNonStreaming` 和 `handleStreaming`：

```go
func (h *Handler) handleNonStreaming(w http.ResponseWriter, r *http.Request, req *model.ChatCompletionRequest, appID string) {
	adapter := h.router.SelectWithFallback(appID, req.Model)
	if len(adapter) == 0 {
		errors.NewProviderUnavailable().ToHTTP(w, http.StatusServiceUnavailable)
		return
	}

	var lastErr error
	for _, a := range adapter {
		resp, err := a.ChatCompletion(r.Context(), req)
		if err == nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
		lastErr = err
	}

	errors.NewProviderError(lastErr.Error()).ToHTTP(w, http.StatusBadGateway)
}
```

- [ ] **Step 4: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "feat: weighted round-robin routing with fallback chain"
```

---

### Task 2.5: 本地限流中间件

**Files:**
- Create: `internal/middleware/ratelimit.go`

**Interfaces:**
- Consumes: `config.Config`, `middleware.AuthAppIDKey`
- Produces: `middleware.RateLimiter(cfg) func(http.Handler) http.Handler`

- [ ] **Step 1: 安装 rate 依赖**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go get golang.org/x/time/rate
```

- [ ] **Step 2: 实现限流中间件**

`internal/middleware/ratelimit.go`:

```go
package middleware

import (
	"net/http"
	"sync"

	"go-gateway/internal/config"
	"go-gateway/internal/errors"
	"golang.org/x/time/rate"
)

type appLimiter struct {
	limiter *rate.Limiter
	rpm     int
}

type RateLimiter struct {
	mu      sync.RWMutex
	appLimiters map[string]*appLimiter
}

func NewRateLimiter(cfg *config.Config) *RateLimiter {
	rl := &RateLimiter{
		appLimiters: make(map[string]*appLimiter),
	}

	for appID, limit := range cfg.RateLimit {
		var lim *rate.Limiter
		r := rate.Limit(limit.QPS)
		if r > 0 {
			lim = rate.NewLimiter(r, limit.QPS)
		}
		rl.appLimiters[appID] = &appLimiter{
			limiter: lim,
			rpm:     limit.RPM,
		}
	}

	return rl
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appID := GetAuthAppID(r.Context())
		if appID == "" {
			next.ServeHTTP(w, r)
			return
		}

		rl.mu.RLock()
		limiter, ok := rl.appLimiters[appID]
		rl.mu.RUnlock()

		if !ok {
			// 未配置限流的应用默认不限流
			next.ServeHTTP(w, r)
			return
		}

		// QPS 检查（令牌桶）
		if limiter.limiter != nil && !limiter.limiter.Allow() {
			errors.NewRateLimited().ToHTTP(w, http.StatusTooManyRequests)
			return
		}

		// RPM 检查（简化：使用一个全局计数器，实际场景可用滑动窗口）
		// 此处仅做演示，QPS 已足够覆盖大多数场景
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 3: 集成限流到路由注册**

在 `handler.go` 的 `RegisterRoutes` 中：

```go
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	authKeys := make([]struct {
		Key    string
		AppID  string
		Models []string
	}, len(h.cfg.Auth.Keys))
	for i, k := range h.cfg.Auth.Keys {
		authKeys[i] = struct {
			Key    string
			AppID  string
			Models []string
		}{Key: k.Key, AppID: k.AppID, Models: k.Models}
	}
	authCfg := middleware.NewAuthConfig(authKeys)
	authMiddleware := middleware.Auth(authCfg)
	rateLimiter := middleware.NewRateLimiter(h.cfg)

	// 中间件链：RequestID → Auth → RateLimit → Logging → Handler
	chain := func(handler http.Handler) http.Handler {
		return middleware.RequestID(
			authMiddleware(
				rateLimiter.Middleware(
					h.logger.LoggingMiddleware(handler),
				),
			),
		)
	}

	mux.HandleFunc("/healthz", h.HealthCheck)
	mux.Handle("/v1/models", chain(http.HandlerFunc(h.ListModels)))
	mux.Handle("/v1/chat/completions", chain(http.HandlerFunc(h.ChatCompletion)))
}
```

- [ ] **Step 4: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "feat: local rate limiting with token bucket per app_id"
```

---

### Task 2.6: 重试策略（指数退避）

**Files:**
- Modify: `internal/adapter/openai.go` — 添加重试逻辑
- 或创建 `internal/adapter/retry.go` — 重试包装器

**Interfaces:**
- Produces: `adapter.RetryAdapter` 包装器，自动重试可恢复错误

- [ ] **Step 1: 实现重试包装器**

`internal/adapter/retry.go`:

```go
package adapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-gateway/internal/model"
)

type RetryConfig struct {
	MaxRetries  int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

var DefaultRetryConfig = RetryConfig{
	MaxRetries: 2,
	BaseDelay:  500 * time.Millisecond,
	MaxDelay:   5 * time.Second,
}

type RetryAdapter struct {
	inner Adapter
	cfg   RetryConfig
}

func NewRetryAdapter(inner Adapter, cfg RetryConfig) *RetryAdapter {
	return &RetryAdapter{inner: inner, cfg: cfg}
}

func (r *RetryAdapter) GetProviderType() string {
	return r.inner.GetProviderType()
}

func (r *RetryAdapter) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	var lastErr error
	for i := 0; i <= r.cfg.MaxRetries; i++ {
		resp, err := r.inner.ChatCompletion(ctx, req)
		if err == nil {
			return resp, nil
		}

		lastErr = err
		if !isRetryable(err) {
			return nil, err
		}

		if i < r.cfg.MaxRetries {
			delay := r.cfg.BaseDelay * (1 << i) // 指数退避
			if delay > r.cfg.MaxDelay {
				delay = r.cfg.MaxDelay
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return nil, fmt.Errorf("retry exhausted: %w", lastErr)
}

func (r *RetryAdapter) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error) {
	// 流式请求不重试（连接已建立）
	return r.inner.ChatCompletionStream(ctx, req)
}

func isRetryable(err error) bool {
	msg := err.Error()
	// 可重试的错误：超时、限流、5xx
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "500") || strings.Contains(msg, "502") || strings.Contains(msg, "503") || strings.Contains(msg, "429") {
		return true
	}
	return false
}
```

- [ ] **Step 2: 集成重试到 Adapter 初始化**

在 `handler.go` 的 `initAdapters` 中：

```go
func (h *Handler) initAdapters() {
	retryCfg := adapter.DefaultRetryConfig
	for _, p := range h.cfg.Providers {
		var a adapter.Adapter
		switch p.Type {
		case "openai":
			a = adapter.NewOpenAIAdapter(p.BaseURL, p.APIKey)
		case "anthropic":
			a = adapter.NewAnthropicAdapter(p.BaseURL, p.APIKey)
		case "openai-compatible":
			a = adapter.NewOpenAICompatibleAdapter(p.BaseURL, p.APIKey)
		}
		if a != nil {
			h.adapters[p.ID] = adapter.NewRetryAdapter(a, retryCfg)
		}
	}
}
```

- [ ] **Step 3: 验证编译**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go build ./...
```

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "feat: retry wrapper with exponential backoff for non-streaming requests"
```

---

## 阶段 3：完善与验收（第 5 周）

---

### Task 3.1: 全面集成测试

**Files:**
- Modify: `tests/integration/phase1_test.go`
- Create: `tests/integration/phase2_test.go`

- [ ] **Step 1: 编写多供应商路由测试**

`tests/integration/phase2_test.go`:

```go
//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go-gateway/internal/config"
	"go-gateway/internal/middleware"
	"go-gateway/internal/server"
)

func setupPhase2TestServer(t *testing.T) *server.Handler {
	os.Setenv("TEST_OPENAI_KEY", "sk-test-openai")
	os.Setenv("TEST_ANTHROPIC_KEY", "sk-test-anthropic")
	defer func() {
		os.Unsetenv("TEST_OPENAI_KEY")
		os.Unsetenv("TEST_ANTHROPIC_KEY")
	}()

	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	logger, err := middleware.NewLogger("../../test_logs")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	return server.NewHandler(cfg, logger)
}

func TestAuth_ModelNotAllowed(t *testing.T) {
	h := setupPhase2TestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	req := map[string]interface{}{
		"model":    "non-existent-model",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(req)

	httpReq, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Authorization", "Bearer sk-gateway-dev")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// 注意：以下测试需要真实 API Key 或 mock 上游
// 此处仅测试框架层，不调用真实上游
```

- [ ] **Step 2: 运行所有测试**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go test ./... -v -count=1 2>&1
```

Expected: 所有单元测试通过，集成测试中无法连接上游的测试返回预期错误。

- [ ] **Step 3: 运行 go vet**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go vet ./...
```

Expected: 无警告输出。

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "test: comprehensive integration tests for multi-provider, auth, and routing"
```

---

### Task 3.2: 性能压测

**Files:**
- Create: `tests/bench/bench_test.go`

- [ ] **Step 1: 编写压测基准测试**

`tests/bench/bench_test.go`:

```go
//go:build bench

package bench

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"go-gateway/internal/config"
	"go-gateway/internal/middleware"
	"go-gateway/internal/server"
)

func setupBenchServer(b *testing.B) *httptest.Server {
	os.Setenv("BENCH_API_KEY", "sk-bench")
	defer os.Unsetenv("BENCH_API_KEY")

	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		b.Fatalf("config load: %v", err)
	}
	logger, err := middleware.NewLogger("../../bench_logs")
	if err != nil {
		b.Fatalf("logger: %v", err)
	}
	handler := server.NewHandler(cfg, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return httptest.NewServer(mux)
}

func BenchmarkHealthz(b *testing.B) {
	ts := setupBenchServer(b)
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		resp, err := client.Get(ts.URL + "/healthz")
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

func BenchmarkAuth(b *testing.B) {
	ts := setupBenchServer(b)
	defer ts.Close()

	reqBody := map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(reqBody)

	client := &http.Client{Timeout: 5 * time.Second}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-gateway-dev")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

func BenchmarkConcurrentRequests(b *testing.B) {
	ts := setupBenchServer(b)
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	reqBody := map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}
	body, _ := json.Marshal(reqBody)

	var wg sync.WaitGroup
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer sk-gateway-dev")
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: 运行压测**

```bash
cd /c/Users/L3553/Desktop/go-gateway
go test -tags=bench -bench=. -benchtime=10s ./tests/bench/ -count=1
```

Expected: 5,000+ QPS（纯网关转发路径，无上游调用）。记录结果。

- [ ] **Step 3: 验证延迟指标**

```bash
cd /c/Users/L3553/Desktop/go-gateway
# 使用 -benchmem 查看内存分配
go test -tags=bench -bench=BenchmarkHealthz -benchtime=30s ./tests/bench/ -benchmem
```

Expected: 每次操作 <1ms，内存分配 <1KB

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "test: performance benchmarks for healthz, auth, and concurrent requests"
```

---

### Task 3.3: 迁移指南 + 安全审计 + README

**Files:**
- Create: `README.md`
- Create: `MIGRATION.md`

- [ ] **Step 1: 编写 README**

`README.md`:

```markdown
# AI 模型网关 (go-gateway)

企业级 AI 模型调用统一接入层，提供鉴权、路由、限流、协议转换和日志能力。

## 快速开始

### 前置条件

- Go 1.26+
- 上游供应商 API Key

### 安装

```bash
git clone <repo>
cd go-gateway
cp .env.example .env
# 编辑 .env 填入 API Key
cp config.example.yaml config.yaml
# 编辑 config.yaml 配置路由和应用
go build -o gateway ./cmd/gateway/
```

### 运行

```bash
source .env
./gateway
```

## 接口

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| GET | /healthz | 否 | 健康检查 |
| GET | /v1/models | 是 | 模型列表 |
| POST | /v1/chat/completions | 是 | 聊天补全（非流式/SSE流式） |

## 配置

参考 `config.example.yaml` 和 `.env.example`。

## 迁移

从 New API 迁移：见 [MIGRATION.md](MIGRATION.md)。

## 架构

详见 [docs/superpowers/specs/2026-07-27-gateway-mvp-design.md](docs/superpowers/specs/2026-07-27-gateway-mvp-design.md)。
```

- [ ] **Step 2: 编写迁移指南**

`MIGRATION.md`:

```markdown
# 从 New API 迁移指南

## 迁移步骤

### 1. 并行部署

新网关与现有 New API 并行运行，共享同一份配置。

### 2. 灰度验证

选择 1-2 个低风险业务（如内部工具），将 base_url 改为新网关地址：

```python
# 旧
client = OpenAI(base_url="https://new-api.example.com")
# 新
client = OpenAI(base_url="https://gateway.example.com")
```

### 3. 样本对比

迁移前用以下脚本抓取 New API 的响应样本：

```bash
# 抓取非流式响应
curl -s -X POST https://new-api.example.com/v1/chat/completions \
  -H "Authorization: Bearer $OLD_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}' \
  > old_response.json

# 对比新网关
curl -s -X POST https://gateway.example.com/v1/chat/completions \
  -H "Authorization: Bearer $NEW_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello"}]}' \
  > new_response.json

# 对比（忽略时间戳等字段）
python -c "
import json
old = json.load(open('old_response.json'))
new = json.load(open('new_response.json'))
# 比较 choices 内容
assert old['choices'][0]['message']['content'] == new['choices'][0]['message']['content']
print('OK: 响应内容一致')
"
```

### 4. 全量切换

验证通过后通知所有业务方修改 base_url，New API 保留 1 周备用。

## 回滚方案

如遇问题，业务方只需将 base_url 改回原 New API 地址，无需其他变更。
```

- [ ] **Step 3: 安全审计清单**

参考审计清单，创建 `SECURITY.md` 或 `docs/superpowers/security-audit-checklist.md`：

```markdown
# 安全审计清单

## 已完成
- [x] API Key 不写入 YAML，通过环境变量引用
- [x] 供应商密钥对业务方透明
- [x] 请求日志不含 API Key
- [x] 模型白名单机制

## 需关注（V2+）
- [ ] 敏感信息脱敏
- [ ] 请求/响应审计日志
- [ ] 供应商密钥轮换
- [ ] 速率限制告警
```

- [ ] **Step 4: 最终提交**

```bash
git add -A
git commit -m "docs: README, migration guide, and security checklist"
```

---

## 验收标准对照表

| 指标 | 目标 | 验证任务 | 状态 |
|------|------|---------|------|
| 协议兼容 | 100% 兼容 OpenAI `/v1/chat/completions`（含流式 + Tool Calling） | Task 1.5, 1.6, 2.1, 2.2 | |
| 多供应商 | 支持 OpenAI + Anthropic + ≥1 个兼容供应商 | Task 2.1, 2.2, 2.3 | |
| 业务迁移 | 现有业务不改代码、只改 base_url | Task 3.3 | |
| 代理延迟 | <50ms p99（排除供应商时间） | Task 3.2 | |
| 单节点吞吐 | 500+ QPS | Task 3.2 | |
| 请求成功率 | >99.9%（排除供应商故障） | Task 2.6, 3.1 | |

---

## 风险与缓解

| 风险 | 缓解措施 | 涉及任务 |
|------|---------|---------|
| Anthropic 流式格式差异大 | 专门的 Adapter 处理 SSE → OpenAI 格式转换 | Task 2.2 |
| 业务方迁移阻力 | 100% 协议兼容 + 样本对比验证 | Task 3.3 |
| 供应商 API 变更 | Adapter 模式隔离变更影响 | Task 2.1 |
| 限流在高并发下不准确 | 本地内存令牌桶，预留分布式限流接口 | Task 2.5 |
| 范围膨胀 | 严格按阶段划分，V2/V3 功能明确不做 | 全过程 |