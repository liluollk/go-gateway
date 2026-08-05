# Phase 2 学习总结：多供应商工程化

---

## 第一部分：大白话理解（快递站比喻）

### 先搞懂一个请求怎么走完

想象你是一个**快递站的站长**。客户只认你的地址，但你背后有顺丰、圆通、中通三家快递公司。

Phase 1 你只会用顺丰。Phase 2 你要学会用三家。

### 请求链路：客户 → 安检 → 限流 → 登记 → 你 → 快递公司 → 客户

#### 第一步：安检 = Auth 中间件

客户必须出示工牌（API Key），没有工牌不让进。从请求头 `Authorization: Bearer sk-xxx` 里取出 Key，查名单，不在名单里返回 401。

**文件：** [auth.go](file:///C:/Users/L3553/Desktop/go-gateway/internal/middleware/auth.go)

#### 第二步：限流 = RateLimit 中间件

每个客户一秒钟只能来 100 次，一分钟 6000 次。超过就拦下，返回 429。

**怎么实现？** 令牌桶算法——水桶底有个水龙头每秒滴 N 滴水，每来一个请求舀一勺，桶里没水了就拒绝。

**为什么有两个桶？** QPS 捅防瞬间突发，RPM 捅防总量超标，双保险。

**文件：** [ratelimit.go](file:///C:/Users/L3553/Desktop/go-gateway/internal/middleware/ratelimit.go)

#### 第三步：登记 = Logger 中间件

每个请求记一笔账：谁、几点、去了哪个模型、花了多久、成没成功。一行 JSON 写到 `logs/gateway-2026-07-29.log`，每天一个新文件，30 天后自动删。

**文件：** [logger.go](file:///C:/Users/L3553/Desktop/go-gateway/internal/middleware/logger.go)

#### 第四步：路由 + 转发 = Handler

**A. 选快递公司（Router）**

客户说"我要发 gpt-4o"，你查表：gpt-4o 有顺丰和圆通两家。权重轮询——顺丰权重 70，圆通权重 30，100 个请求里大概 70 个走顺丰，30 个走圆通。

**文件：** [router.go](file:///C:/Users/L3553/Desktop/go-gateway/internal/router/router.go)

**B. 转发请求（Adapter）**

选好顺丰后，把请求交给它。但顺丰和圆通的"面单格式"不一样。**Adapter 就是翻译器**——内部用统一格式（OpenAI），交给不同的供应商时翻译成各自的格式。

```go
type Adapter interface {
    ChatCompletion(请求) → 响应     // 非流式：一问一答
    ChatCompletionStream(请求) → 流 // 流式：逐字输出
}
```

**文件：** [interface.go](file:///C:/Users/L3553/Desktop/go-gateway/internal/adapter/interface.go)、[openai.go](file:///C:/Users/L3553/Desktop/go-gateway/internal/adapter/openai.go)

**C. 失败了怎么办（重试 + 换人）**

指数退避：失败 → 等 1s → 再试 → 失败 → 等 2s → 再试 → 失败 → 换供应商。

**4xx 错误不重试**（401/403 重试无意义），**流式不 fallback**（已经开始发数据了，无法切换）。

**文件：** [chat.go](file:///C:/Users/L3553/Desktop/go-gateway/internal/server/chat.go)

#### 第五步：Anthropic 特例——格式转换

OpenAI 和 Anthropic 的格式不一样，就像中文表格和英文表格。

**最大的区别——system prompt 放哪：**

```
OpenAI:  messages: [{role: "system", content: "你是助手"}, {role: "user", content: "你好"}]
Anthropic: system: "你是助手"（单独字段）, messages: [{role: "user", content: "你好"}]
```

`convertRequest` 方法把 OpenAI 格式里的 system 消息**抽出来**，放到 Anthropic 的 system 字段里。

**文件：** [anthropic.go](file:///C:/Users/L3553/Desktop/go-gateway/internal/adapter/anthropic.go)

---

## 第二部分：技术细节

### Phase 2 做了什么

| 模块 | 文件 | 大白话 |
|------|------|--------|
| **Adapter 接口** | `internal/adapter/interface.go` | 定义统一的"翻译器"标准 |
| **OpenAI Adapter** | `internal/adapter/openai.go` | 直连 OpenAI，格式一致无需转换 |
| **Anthropic Adapter** | `internal/adapter/anthropic.go` | 格式转换器：OpenAI ↔ Anthropic |
| **Router** | `internal/router/router.go` | 选厂家：权重轮询 + 失败换人 |
| **RateLimit** | `internal/middleware/ratelimit.go` | 限流：令牌桶，QPS + RPM 双维度 |
| **重试+Fallback** | `internal/server/chat.go` | 容错：指数退避 + 4xx 不重试 |

**核心思想：** 把"不同供应商的差异"关在 Adapter 里，上层只看到统一接口。

### OpenAI vs Anthropic 全部差异

#### HTTP 请求差异

| 差异点 | OpenAI | Anthropic |
|--------|--------|-----------|
| 端点 | `POST /v1/chat/completions` | `POST /v1/messages` |
| 认证头 | `Authorization: Bearer <key>` | `x-api-key: <key>` |
| 特有头 | 无 | `anthropic-version: 2023-06-01` |

#### 请求体格式差异

| 差异点 | OpenAI | Anthropic |
|--------|--------|-----------|
| System Prompt | 放在 `messages` 数组里 | 独立顶层 `system` 字段 |
| 多条 System | 支持多条 | 只取最后一条 |
| max_tokens | 可选 | 必填，默认 4096 |
| 消息角色 | system/user/assistant/tool | user/assistant |

#### 响应体格式差异

| 差异点 | OpenAI | Anthropic |
|--------|--------|-----------|
| 内容格式 | `choices[0].message.content`（纯字符串） | `content: [{type:"text", text:"..."}]`（数组） |
| 结束原因 | `finish_reason: "stop"` / `"length"` | `stop_reason: "end_turn"` / `"max_tokens"` |
| Token 用量 | `usage.prompt_tokens` / `completion_tokens` | `usage.input_tokens` / `output_tokens` |

#### 流式事件差异（最关键）

| 阶段 | OpenAI SSE | Anthropic SSE |
|------|-----------|--------------|
| 开始 | 无专门事件，直接发 `data:` | `event: message_start` + `data: {...}` |
| 内容 | `data: {"choices":[{"delta":{"content":"你"}}]}` | `event: content_block_delta` + `data: {...}` |
| 结束 | `data: {"choices":[{"finish_reason":"stop"}]}` | `event: message_delta` → `event: message_stop` |
| Usage | 在最后一个 chunk 里 | 在 `message_delta` 事件里 |

**流式转换映射：**

```
Anthropic 事件流                    →     OpenAI 格式
─────────────────────────────────────────────────────────
event: message_start               →     记录 message.id，不发送 chunk
event: content_block_delta          →     choices[0].delta.content = "你"
event: message_delta                →     choices[0].finish_reason = "stop"
event: message_stop                 →     关闭 channel
```

### 关键设计决策

| 决策 | 理由 |
|------|------|
| 统一用 OpenAI 格式做内部标准 | OpenAI 生态最广，其他厂商兼容成本低 |
| Adapter 接口只有两个方法 | 最小化接口，新供应商只需实现两个方法 |
| 重试和 Fallback 在 chat.go 同一层 | 避免双层叠加放大延迟 |
| 4xx 错误不重试 | 客户端错误（401/403）重试无意义 |
| 流式不 Fallback | 已经开始发送数据，无法切换供应商 |
| 令牌桶 QPS + RPM 双维度 | QPS 防突发，RPM 防总量超标 |
| 权重轮询用 atomic 计数器 | 无锁，高性能，Goroutine 安全 |

### Phase 1 vs Phase 2 对比

| 原来（Phase 1） | 现在（Phase 2） |
|------|------|
| 只有一个 OpenAI | 支持 OpenAI + Anthropic + 任意兼容供应商 |
| 直连，没有路由 | 权重轮询 + fallback |
| 没有限流 | QPS + RPM 令牌桶 |
| 没有重试 | 指数退避重试 + 4xx 不重试 |
| 没有日志 | JSON Lines + 按天轮转 |

---

## 完整请求链路图

```
POST /v1/chat/completions
  Body: {"model":"gpt-4o","messages":[{"role":"user","content":"你好"}]}
    │
    ▼
┌────────────────────────────────────────────────┐
│ 1. [Auth] 检查 API Key 是否在名单里             │
│    sk-gateway-dev → ✅ 允许访问 gpt-4o, claude   │
└────────────────────────────────────────────────┘
    │
    ▼
┌────────────────────────────────────────────────┐
│ 2. [RateLimit] 令牌桶检查                       │
│    QPS 桶: 每秒 100 个 → 有水 ✅                │
│    RPM 桶: 每分钟 6000 个 → 有水 ✅             │
└────────────────────────────────────────────────┘
    │
    ▼
┌────────────────────────────────────────────────┐
│ 3. [Logger] 开始计时，记录请求                   │
└────────────────────────────────────────────────┘
    │
    ▼
┌────────────────────────────────────────────────┐
│ 4. [Router] 选厂家                             │
│    config.yaml: gpt-4o → openai-main(权重100)   │
│    选中: openai-main                            │
└────────────────────────────────────────────────┘
    │
    ▼
┌────────────────────────────────────────────────┐
│ 5. [Adapter] 组装 HTTP 请求并发送               │
│                                                 │
│  ┌─ OpenAI ───────────────────────────────┐    │
│  │ ① json.Marshal(req) 直接序列化          │    │
│  │ ② POST https://api.openai.com/...       │    │
│  │ ③ Header: Authorization: Bearer sk-xxx  │    │
│  │ ④ 读响应 → json.Unmarshal → 返回        │    │
│  └─────────────────────────────────────────┘    │
│                                                 │
│  ┌─ Anthropic ────────────────────────────┐    │
│  │ ① convertRequest() OpenAI→Anthropic     │    │
│  │ ② POST https://api.anthropic.com/...    │    │
│  │ ③ Header: x-api-key: sk-xxx             │    │
│  │ ④ convertResponse() Anthropic→OpenAI    │    │
│  └─────────────────────────────────────────┘    │
└────────────────────────────────────────────────┘
    │
    ▼
┌────────────────────────────────────────────────┐
│ 6. [重试+Fallback] 失败了怎么办？               │
│    第1次 → 失败(503) → 等1s → 第2次 → 失败     │
│    → 第3次失败 → 换供应商 → 成功！              │
│    ⚠ 4xx 错误(401/403) 不重试，直接返回          │
└────────────────────────────────────────────────┘
    │
    ▼
┌────────────────────────────────────────────────┐
│ 7. [Logger] 记录: 200, 耗时 1.2s, 123 tokens   │
└────────────────────────────────────────────────┘
    │
    ▼
返回: {"choices":[{"message":{"content":"你好！..."}}]}
```