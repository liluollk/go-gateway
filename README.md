# AI 模型网关 (go-gateway)

企业级 AI 模型调用统一接入层，提供鉴权、路由、限流、协议转换和日志能力。

## 快速开始

### 前置条件

- Go 1.26+
- 上游供应商 API Key（OpenAI / Anthropic）

### 安装

```bash
git clone <repo>
cd go-gateway

# 配置环境变量
cp .env.example .env
# 编辑 .env 填入真实的 API Key

# 配置网关
cp config.example.yaml config.yaml
# 编辑 config.yaml 配置路由、限流和应用
```

### 运行

```bash
source .env           # Linux/Mac
# 或 .env 文件内容手动 export

go build -o gateway ./cmd/gateway/
./gateway
```

服务默认监听 `http://localhost:8080`。

---

## 接口

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|:--:|------|
| GET | `/healthz` | 否 | 健康检查，返回 `{"status":"ok"}` |
| GET | `/v1/models` | 是 | 返回当前 API Key 允许访问的模型列表 |
| POST | `/v1/chat/completions` | 是 | 聊天补全（支持非流式 JSON 和 SSE 流式） |

### 请求示例

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-gateway-dev" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [{"role": "user", "content": "你好"}]
  }'
```

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-gateway-dev" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

---

## 架构

```
请求 → RequestID → Auth → RateLimit → Logger → Router → Adapter → 上游 API
```

| 模块 | 职责 |
|------|------|
| **RequestID** | 每个请求注入 `X-Trace-Id`，全链路追踪 |
| **Auth** | Bearer Token 鉴权 + 模型白名单 |
| **RateLimit** | 令牌桶算法，QPS + RPM 双维度限流 |
| **Logger** | JSON Lines 格式日志，按天轮转，保留 30 天 |
| **Router** | 权重轮询选择供应商，主供应商失败自动 fallback |
| **Adapter** | 隔离供应商差异，自动转换 OpenAI ↔ Anthropic 格式 |

详细设计见 [设计文档](docs/superpowers/specs/2026-07-27-gateway-mvp-design.md)。

---

## 配置

### 环境变量

| 变量 | 说明 |
|------|------|
| `OPENAI_API_KEY` | OpenAI API 密钥 |
| `ANTHROPIC_API_KEY` | Anthropic API 密钥 |

### 配置文件

参考 `config.example.yaml`，关键配置项：

```yaml
# 供应商
providers:
  - id: openai-main
    type: openai
    base_url: https://api.openai.com
    api_key: ${OPENAI_API_KEY}        # 环境变量引用

  - id: claude-main
    type: anthropic
    base_url: https://api.anthropic.com
    api_key: ${ANTHROPIC_API_KEY}

# 路由：模型 → 供应商映射
routes:
  - model: gpt-4o
    providers:
      - provider_id: openai-main
        weight: 100

# 限流：按 API Key 配置
rate_limit:
  sk-gateway-dev:
    qps: 100
    rpm: 6000

# 重试
retry:
  max_retries: 2
  initial_backoff: 1s
```

---

## 测试

```bash
# 单元测试
go test ./internal/...

# 集成测试（无需真实 API Key）
go test -tags=integration -v ./tests/integration/...

# 性能压测
cd tests/bench && go test -bench=Benchmark -benchtime=3s -benchmem
```

---

## 迁移

从 New API 迁移到本网关，见 [MIGRATION.md](MIGRATION.md)。

---

## 项目结构

```
go-gateway/
├── cmd/gateway/main.go              # 入口
├── config.yaml                      # 配置
├── internal/
│   ├── adapter/                     # 供应商适配器（OpenAI / Anthropic）
│   ├── config/                      # 配置加载
│   ├── errors/                      # 错误码
│   ├── middleware/                  # 中间件（Auth / Logger / RateLimit / RequestID）
│   ├── model/                       # 内部数据模型
│   ├── router/                      # 路由分发
│   └── server/                      # HTTP 处理器
└── tests/
    ├── integration/                 # 集成测试
    └── bench/                       # 性能压测
```