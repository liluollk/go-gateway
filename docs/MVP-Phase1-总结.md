# MVP Phase 1 完成总结

> 状态：Phase 1 全部完成 ✅
> 日期：2026-07-28

---

## 一、项目概述

**目标**：构建企业级 AI 模型网关的 MVP 骨架，替换现有 New API，跑通一条完整的 OpenAI 调用链（鉴权 → 路由 → 转发 → 响应）。

**技术栈**：Go 1.26，标准库 `net/http` + `encoding/json`，`gopkg.in/yaml.v3`（配置解析）

---

## 二、已完成模块

### 2.1 项目结构

```
go-gateway/
├── cmd/gateway/main.go              # 入口：加载配置 → 注册路由 → 启动服务 → 优雅关闭
├── config.yaml                      # 本地开发配置
├── config.example.yaml              # 配置模板（不含真实密钥）
├── .env.example                     # 环境变量模板
├── internal/
│   ├── config/
│   │   ├── config.go                # Config 结构体 + YAML 解析 + 环境变量替换 + 校验
│   │   └── config_test.go           # 配置加载测试
│   ├── model/
│   │   └── model.go                 # 内部标准模型：ChatRequest / ChatResponse / StreamChunk
│   ├── middleware/
│   │   ├── requestid.go             # X-Trace-Id 注入（客户端传了就用，没传自动生成）
│   │   ├── auth.go                  # Bearer Token 鉴权 + 模型白名单注入上下文
│   │   ├── ratelimit.go             # 内存令牌桶限流（QPS/RPM）
│   │   ├── logger.go                # JSON Lines 日志，按天轮转，保留 30 天
│   │   └── middleware_test.go       # 中间件测试
│   ├── router/
│   │   ├── router.go                # 路由匹配：Key + Model → Provider 列表
│   │   └── router_test.go           # 路由测试
│   ├── adapter/
│   │   ├── interface.go             # Adapter 接口定义（Chat / HealthCheck）
│   │   ├── openai.go                # OpenAI Adapter
│   │   ├── anthropic.go             # Anthropic Adapter（消息转 OpenAI 格式）
│   │   └── adapter_test.go          # 适配器测试
│   ├── errors/
│   │   └── errors.go                # 统一错误码 + JSON 响应
│   └── server/
│       ├── handler.go               # HTTP 路由注册 + 中间件链组装
│       └── chat.go                  # /v1/chat/completions（非流式 + 流式）
├── tests/
│   ├── integration/
│   │   ├── phase1_test.go           # 集成测试
│   │   └── phase2_test.go           # 集成测试
│   └── bench/
│       └── bench_test.go            # 性能压测
├── go.mod
├── go.sum
└── README.md
```

### 2.2 模块清单

| # | 模块 | 文件 | 说明 |
|---|------|------|------|
| 1 | 入口 | `cmd/gateway/main.go` | 配置加载 → 日志初始化 → 路由注册 → 启动服务 → 优雅关闭 |
| 2 | 配置 | `internal/config/config.go` | YAML 解析 + `${ENV_VAR}` 环境变量替换 + 启动时校验 |
| 3 | 模型 | `internal/model/model.go` | OpenAI 标准格式：ChatRequest/ChatResponse/StreamChunk/Delta |
| 4 | 错误 | `internal/errors/errors.go` | 7 种错误码 + 统一 JSON 响应格式 |
| 5 | 追踪 | `internal/middleware/requestid.go` | 自动生成 X-Trace-Id，客户端可自定义 |
| 6 | 鉴权 | `internal/middleware/auth.go` | Bearer Token → 查配置 → 模型白名单注入上下文 |
| 7 | 限流 | `internal/middleware/ratelimit.go` | 内存令牌桶，按 Key 独立限流（QPS/RPM） |
| 8 | 日志 | `internal/middleware/logger.go` | JSON Lines 格式，按天轮转，保留 30 天 |
| 9 | 路由 | `internal/router/router.go` | Key + Model → Provider 匹配 |
| 10 | 适配器 | `internal/adapter/` | Adapter 模式：OpenAI + Anthropic，隔离供应商差异 |
| 11 | 处理 | `internal/server/handler.go` | 路由注册 + 中间件链组装 |
| 12 | 转发 | `internal/server/chat.go` | 非流式（JSON 原样转发）+ 流式（SSE 逐行转发） |

---

## 三、核心架构

### 3.1 中间件链（洋葱模型）

```
请求进来
  │
  ▼
┌─────────────┐
│ RequestID    │  ← 贴追踪号（客户端传了就用，没传自动生成）
└──────┬──────┘
       ▼
┌─────────────┐
│ Auth         │  ← 验证 Bearer Token → 注入 Key + 模型白名单到上下文
└──────┬──────┘
       ▼
┌─────────────┐
│ RateLimit    │  ← 令牌桶限流，超限返回 429
└──────┬──────┘
       ▼
┌─────────────┐
│ Logger       │  ← 记录请求耗时、状态码、Token 用量
└──────┬──────┘
       ▼
┌─────────────┐
│ Handler      │  ← 真正处理：ChatCompletion / ListModels
└─────────────┘
```

### 3.2 注册的端点

| 端点 | 鉴权 | 限流 | 日志 | 说明 |
|------|:--:|:--:|:--:|------|
| `/healthz` | ❌ | ❌ | ❌ | 健康检查 |
| `/v1/models` | ✅ | ✅ | ✅ | 列出当前 Key 可用的模型 |
| `/v1/chat/completions` | ✅ | ✅ | ✅ | 核心：Chat 请求转发 |

### 3.3 请求转发流程

```
客户端 ──POST /v1/chat/completions──▶ 网关
  Authorization: Bearer sk-gateway-dev
  {"model":"gpt-4o", "messages":[...], "stream":false}
                                        │
                        ① 解析 JSON → ChatRequest
                        ② 查白名单：model 是否允许
                        ③ 匹配路由：Key + Model → Provider
                        ④ 获取 Adapter：provider.type → openai/anthropic
                        ⑤ 调用 Adapter.Chat()，转发请求
                        ⑥ 返回响应给客户端
                                        │
                                        ▼
                               OpenAI / Anthropic
```

---

## 四、配置说明

### 4.1 config.yaml 结构

```yaml
server:              # 服务器配置
  port: 8080
  read_timeout: 30s
  write_timeout: 300s

auth:                # API Key 列表
  keys:
    - key: sk-gateway-dev
      models: [gpt-4o, gpt-4o-mini, claude-sonnet-4]

rate_limit:          # 限流（按 Key 独立配置）
  sk-gateway-dev:
    qps: 100
    rpm: 6000

providers:           # 上游供应商
  - id: openai-main
    type: openai
    base_url: https://api.openai.com
    api_key: ${OPENAI_API_KEY}    # 环境变量注入

routes:              # 路由规则（Key + Model → Provider）
  - key: sk-gateway-dev
    model: gpt-4o
    providers:
      - provider_id: openai-main
        weight: 100
```

### 4.2 环境变量

| 变量 | 用途 |
|------|------|
| `OPENAI_API_KEY` | OpenAI 供应商密钥 |
| `ANTHROPIC_API_KEY` | Anthropic 供应商密钥 |

---

## 五、关键设计决策

### 5.1 Adapter 模式

每个供应商实现统一的 `Adapter` 接口，网关内部只用 OpenAI 格式，适配器负责格式转换：

```
网关内部（OpenAI 格式）→ OpenAI Adapter → 原样转发 → OpenAI
网关内部（OpenAI 格式）→ Anthropic Adapter → 格式转换 → Anthropic
```

**好处**：新增供应商只需加一个 Adapter 文件，核心逻辑不用动。

### 5.2 请求体处理方式

当前代码**先解析 JSON 到结构体，再重新序列化后转发**。这意味着：

- ✅ 未定义的字段会被丢弃（如客户端传了未知参数）
- ✅ 结构体已有的字段完整保留（包括 `tools`、`tool_calls`）
- ⚠️ 非流式场景下，`content: null` 会变成 `content: ""`（不影响实际使用）

### 5.3 流式处理

非流式和流式走不同的处理路径：

| | 非流式 | 流式 |
|------|------|------|
| 转发方式 | `json.Marshal` → `http.Do` → `json.Unmarshal` → `json.Encode` | `json.Marshal` → `http.Do` → `bufio.Reader` 逐行转发 |
| 响应头 | `application/json` | `text/event-stream` |
| 超时 | 120s | 300s |

---

## 六、尚未完成

| 项目 | 说明 |
|------|------|
| 真实供应商验证 | 尚未用真实 OpenAI/Anthropic Key 测试 |
| Function Calling 透传验证 | 结构体已支持，但未实际测试 |
| 权重轮询 | 路由目前只取第一个 provider，未实现多 provider 负载均衡 |
| Fallback 链 | 主 provider 不可用时自动切换备选（未实现） |
| 部署 | 无 Dockerfile / docker-compose |
| 可观测性 | 无 Prometheus 指标 / Grafana 面板 |
| Token 用量统计 | 日志中记录了 `usage` 字段，但未做指标聚合 |

---

## 七、API 文档

### 调用方式

```bash
# 非流式
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-gateway-dev" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好"}],"stream":false}'

# 流式
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-gateway-dev" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好"}],"stream":true}'

# 模型列表
curl http://localhost:8080/v1/models \
  -H "Authorization: Bearer sk-gateway-dev"

# 健康检查
curl http://localhost:8080/healthz
```

### 错误响应格式

```json
{
  "error": {
    "code": "INVALID_API_KEY",
    "message": "Invalid API Key"
  }
}
```

| 错误码 | HTTP 状态 | 含义 |
|------|------|------|
| `INVALID_REQUEST` | 400 | 请求格式错误 |
| `INVALID_API_KEY` | 401 | API Key 无效 |
| `MODEL_NOT_ALLOWED` | 403 | 模型不在白名单 |
| `RATE_LIMITED` | 429 | 触发限流 |
| `PROVIDER_ERROR` | 502 | 上游供应商错误 |
| `PROVIDER_UNAVAILABLE` | 503 | 所有供应商不可用 |
| `UPSTREAM_TIMEOUT` | 504 | 上游超时 |

---

## 八、下一步

- **P0 验证**：用真实 API Key 测试 OpenAI / Anthropic 端点，验证流式和非流式
- **P1 部署**：Dockerfile + docker-compose + Prometheus + Grafana
- **P2 智能降本**：语义缓存、智能路由、模型降级