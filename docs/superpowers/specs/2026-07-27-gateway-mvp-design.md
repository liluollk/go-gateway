# AI 模型网关 MVP 设计文档

> 日期：2026-07-27 | 版本：2.0 | 状态：定稿

---

## 1. 背景

企业 AI 日消耗 **60 亿+ Token**（¥60 万+/天），服务于 **1,600+ 员工**，日均 **200 万+** 次请求。当前使用 **New API**（OneAPI 衍生版，AGPL-3.0）作为接入层，但存在成本不可控、无 Token 优化、无智能路由、缓存命中率低、Prompt 管理缺失、开源协议风险等问题。

**战略方向**：已从"推广普及"转向"精细化治理与降本增效"。采用**小步快跑**策略——先做 MVP 替换核心代理，再叠加智能能力。

---

## 2. MVP 目标

**核心目标**：客户端通过统一网关 API Key，调用一个或多个 OpenAI 兼容的模型服务，获得普通或流式响应。网关提供鉴权、路由、限流、日志等基础治理能力。

### 验收标准

| 指标 | 目标 | 验证方式 |
|------|------|---------|
| 协议兼容 | 100% 兼容 OpenAI `/v1/chat/completions`（含流式 + Tool Calling） | OpenAI SDK 调用 |
| 多供应商 | 支持 OpenAI + Anthropic + ≥1 个兼容供应商 | 配置切换验证 |
| 业务迁移 | 现有业务不改代码、只改 base_url | 真实业务接入 |
| 代理延迟 | <50ms p99（排除供应商时间） | 压测对比 |
| 单节点吞吐 | 500+ QPS | 压测报告 |
| 请求成功率 | >99.9%（排除供应商故障） | 监控 |

---

## 3. 系统架构

### 执行链路

```
请求 (OpenAI 格式)
    │
    ▼
① 请求体校验 ── JSON 解析 + 必填字段 → 400 INVALID_REQUEST
    │
    ▼
② 认证鉴权 ── Bearer Token → app_id + 模型白名单
    │
    ▼
③ 本地限流 ── 内存令牌桶，按 app_id 独立 QPS/RPM → 429 RATE_LIMITED
    │
    ▼
④ 路由分发 ── 权重轮询 + fallback 链
    │
    ▼
⑤ 协议转换 ── 内部标准 ↔ 供应商 API
    ├─ OpenAI Adapter → OpenAI API
    ├─ Anthropic Adapter → Anthropic API
    └─ OpenAICompatible Adapter → vLLM / DeepSeek 等
    │
    ▼
⑥ 日志采集 ── JSON Lines 文件，按天轮转
    │
    ▼
响应 (OpenAI 格式)
```

### 模块职责

| 模块 | 职责 | 关键设计 |
|------|------|---------|
| Config | 加载配置 + 启动时校验 | YAML + 环境变量（密钥） |
| Request Validator | JSON 解析 + 必填字段检查 | 进入中间件链前完成 |
| Auth | API Key → app_id + 模型白名单 | 内存 map 匹配，注入 Context |
| Rate Limiter | 防止单应用打爆供应商 | `x/time/rate` 令牌桶，QPS/RPM OR 逻辑 |
| Router | 选择目标供应商 + 故障切换 | 权重轮询 → fallback 链 → 被动健康检测 |
| Adapter | 内部标准 ↔ 供应商协议 | 面向同一内部模型编程，新增供应商只需加一个 Adapter |
| Logger | 审计 + 计量记录 | JSON Lines 文件，按天轮转 |
| Health | 探活 + 模型列表 | GET /healthz + GET /v1/models |

---

## 4. 分阶段执行

### 阶段 1：骨架与单供应商（第 1-2 周）

**目标**：先跑通一个完整的调用链。

**接口**：
- `GET /healthz`（无鉴权）
- `GET /v1/models`（需鉴权）
- `POST /v1/chat/completions` 非流式 + SSE 流式（需鉴权）

**交付清单**：

1. Go 项目 + HTTP 服务（可配置端口，默认 8080）+ 优雅关闭
2. 配置加载：YAML + 环境变量（`${VAR}` 语法），启动时校验
3. 请求体校验：合法 JSON、必填字段（model / messages / stream）
4. 认证鉴权：Bearer Token → app_id + 模型白名单
5. 一个 OpenAI 兼容 Provider：拼接上游、注入 Key、处理错误
6. 非流式响应转发：读取 usage，记录 token 用量
7. SSE 流式响应转发：逐块 Flush，正确转发 [DONE]，客户端断开时取消上游
8. 请求追踪与日志：X-Trace-Id，记录 trace_id / 路径 / 耗时 / 状态码 / usage

### 阶段 2：多供应商与工程化（第 3-4 周）

**目标**：扩展到多个供应商，加入限流、路由和错误处理。

**交付清单**：

1. Adapter 接口 + 重构 OpenAI Adapter
2. Anthropic Adapter（攻克流式格式差异）
3. OpenAI 兼容 Adapter（vLLM / DeepSeek 等）
4. 路由分发：按 app_id + model 匹配，权重轮询 + fallback 链 + 超时控制
5. 本地限流：内存令牌桶，按 app_id 独立，QPS/RPM 双维度
6. 重试策略：2 次重试，指数退避（500ms 起）

### 阶段 3：完善与验收（第 5 周）

**交付清单**：

1. 集成测试覆盖所有接口和错误场景
2. 性能压测 500+ QPS，验证延迟 <50ms p99
3. go vet + 单元测试通过
4. 迁移指南 + 安全审计 + README

---

## 5. 配置设计

五个区块：

- **server**：监听端口、HTTP 读超时和写超时（写超时需考虑流式场景，设为 300s）
- **auth**：API Key 列表，每个 Key 绑定 app_id 和模型白名单
- **rate_limit**：按 app_id 设置 QPS/RPM（OR 逻辑，未配置的应用默认不限流）
- **providers**：供应商列表，每个条目包含 id、type（openai/anthropic/openai-compatible）、base_url、API Key（通过环境变量引用）、模型列表
- **routes**：路由规则，按 app_id + model 匹配，支持权重和 fallback

敏感信息通过 `${ENV_VAR}` 引用环境变量，不写进 YAML。启动时校验所有 route 引用的 provider_id 和 model 是否有效。

---

## 6. 项目结构

```
go-gateway/
├── cmd/gateway/main.go
├── config.yaml / config.example.yaml / .env.example
├── internal/
│   ├── config/          # 配置加载 + 校验
│   ├── model/           # 内部标准模型
│   ├── middleware/       # auth / ratelimit / logger / requestid
│   ├── router/          # 路由分发
│   ├── adapter/         # interface + openai / anthropic / openai_compat
│   ├── provider/        # 连接池 + 健康检测
│   └── errors/          # 统一错误码
├── go.mod
└── README.md
```

---

## 7. 错误码

| HTTP | 错误码 | 含义 |
|------|--------|------|
| 400 | INVALID_REQUEST | 请求格式错误 |
| 401 | INVALID_API_KEY | API Key 无效 |
| 403 | MODEL_NOT_ALLOWED | 该应用无权使用此模型 |
| 429 | RATE_LIMITED | 被限流 |
| 502 | PROVIDER_ERROR | 供应商返回错误 |
| 503 | PROVIDER_UNAVAILABLE | 所有供应商不可用 |
| 504 | UPSTREAM_TIMEOUT | 供应商超时 |

---

## 8. 迁移策略

1. **并行部署**：新网关与现有 New API 并行运行，业务方只改 base_url
2. **灰度验证**：1-2 个低风险业务先切，观察 1-2 天
3. **样本对比**：迁移前抓取 New API 的响应样本，确保新网关返回格式逐字一致
4. **全量切换**：验证通过后通知所有业务方，New API 保留 1 周备用

---

## 9. 明确不做（保持 MVP 克制）

| 版本 | 内容 |
|------|------|
| **V2** | 语义缓存、Prompt 管理与压缩 |
| **V3** | 成本管控/预算预警、智能路由、管理后台、分布式限流、全链路追踪、Docker/K8s 部署 |

---

## 10. 风险

| 风险 | 缓解 |
|------|------|
| Anthropic Streaming 格式差异大 | 阶段 1 专注 OpenAI，阶段 2 专门攻克 |
| 业务方迁移阻力 | 100% 协议兼容 + 平滑迁移 + 样本对比 |
| 供应商 API 变更 | Adapter 模式隔离 |
| 单节点性能不达标 | 架构无状态，预留水平扩展 |
| 范围膨胀 | 阶段划分明确，每个阶段独立可验收 |