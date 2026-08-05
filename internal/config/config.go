// config 包负责网关配置的加载、解析和校验。
// 配置文件为 YAML 格式，支持 ${ENV_VAR} 环境变量替换。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是网关的顶层配置，对应 config.yaml 的根结构。
type Config struct {
	Server    ServerConfig               `yaml:"server"`
	Auth      AuthConfig                 `yaml:"auth"`
	RateLimit map[string]RateLimitConfig `yaml:"rate_limit"` // key 为 API Key，按 Key 配置限流
	Retry     RetryConfig                `yaml:"retry"`      // 重试策略配置
	Providers []ProviderConfig           `yaml:"providers"`  // 上游模型供应商
	Routes    []RouteConfig              `yaml:"routes"`     // 模型→供应商的路由映射
}

// ServerConfig HTTP 服务器配置。
type ServerConfig struct {
	Port         int           `yaml:"port"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`  // 读取请求超时
	WriteTimeout time.Duration `yaml:"write_timeout"` // 写入响应超时（含流式传输）
}

// AuthConfig 鉴权配置，包含所有合法的 API Key 列表。
type AuthConfig struct {
	Keys []APIKeyConfig `yaml:"keys"`
}

// APIKeyConfig 单个 API Key 的配置，包含 Key 值及其允许访问的模型列表（白名单）。
type APIKeyConfig struct {
	Key    string   `yaml:"key"`
	Models []string `yaml:"models"` // 该 Key 允许访问的模型白名单
}

// RateLimitConfig 限流配置，支持 QPS 和 RPM 两种维度。
type RateLimitConfig struct {
	QPS int `yaml:"qps,omitempty"` // 每秒请求数上限
	RPM int `yaml:"rpm,omitempty"` // 每分钟请求数上限
}

// ProviderConfig 上游模型供应商配置，如 OpenAI、Anthropic 等。
type ProviderConfig struct {
	ID      string   `yaml:"id"`      // 供应商唯一标识
	Type    string   `yaml:"type"`    // 供应商类型：openai / anthropic / openai-compatible
	BaseURL string   `yaml:"base_url"` // API 基础地址
	APIKey  string   `yaml:"api_key"`  // 供应商密钥（支持 ${ENV_VAR} 环境变量）
	Models  []string `yaml:"models"`   // 该供应商支持的模型列表
}

// RouteConfig 路由配置，定义某个 API Key 下某个模型的路由目标。
// 同一个 Key + Model 可以配置多个 Provider，按权重分配流量。
type RouteConfig struct {
	Key       string        `yaml:"key"`
	Model     string        `yaml:"model"`
	Providers []RouteTarget `yaml:"providers"` // 候选供应商列表
}

// RouteTarget 路由目标，指向一个具体的供应商及权重。
type RouteTarget struct {
	ProviderID string `yaml:"provider_id"` // 指向 ProviderConfig.ID
	Weight     int    `yaml:"weight"`      // 权重，用于多供应商负载均衡
}

// RetryConfig 重试策略配置，控制上游请求失败时的重试行为。
// 仅对 5xx 和网络错误重试，4xx 不重试。
type RetryConfig struct {
	MaxRetries     int           `yaml:"max_retries"`     // 最大重试次数，默认 2
	InitialBackoff time.Duration `yaml:"initial_backoff"` // 初始退避时间，默认 1s
}

// Load 从指定路径加载 YAML 配置文件。
// 1. 读取原始文件内容
// 2. 将 ${ENV_VAR} 替换为环境变量值（未设置的环境变量保留原样，后续 Validate 会报错）
// 3. 解析为 Config 结构体
// 4. 执行配置校验
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// 解析环境变量引用 ${VAR}，如 ${OPENAI_API_KEY}
	resolved := os.Expand(string(data), func(key string) string {
		val := os.Getenv(key)
		if val == "" {
			// 环境变量未设置时保留原样，后续 Validate 会检测并报错
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

// Validate 校验配置的完整性和合法性。
// 校验内容：
// 1. 设置默认值（端口、超时）
// 2. 至少存在一个 API Key
// 3. Provider 类型合法、必要字段非空、环境变量已设置
// 4. Route 中引用的 Provider 存在且支持对应模型
func (c *Config) Validate() error {
	// 默认值设置
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

	// 验证所有 provider 的合法性，并构建 providerID 集合和模型映射
	providerIDs := make(map[string]bool)
	providerModels := make(map[string]map[string]bool) // providerID → model → true
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
		// 检测环境变量是否未解析（仍保留 ${VAR} 格式）
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

	// 验证路由配置：Provider 存在且支持对应模型
	for _, r := range c.Routes {
		if r.Key == "" {
			return fmt.Errorf("route: key is required")
		}
		if r.Model == "" {
			return fmt.Errorf("route: model is required")
		}
		if len(r.Providers) == 0 {
			return fmt.Errorf("route %s/%s: at least one provider required", r.Key, r.Model)
		}
		for _, t := range r.Providers {
			if !providerIDs[t.ProviderID] {
				return fmt.Errorf("route %s/%s: provider %s not found", r.Key, r.Model, t.ProviderID)
			}
			if models, ok := providerModels[t.ProviderID]; ok && len(models) > 0 {
				if !models[r.Model] {
					return fmt.Errorf("route %s/%s: model %s not supported by provider %s", r.Key, r.Model, r.Model, t.ProviderID)
				}
			}
		}
	}

	return nil
}