package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server    ServerConfig              `yaml:"server"`
	Auth      AuthConfig                `yaml:"auth"`
	RateLimit map[string]RateLimitConfig `yaml:"rate_limit"`
	Providers []ProviderConfig          `yaml:"providers"`
	Routes    []RouteConfig             `yaml:"routes"`
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
	Key    string   `yaml:"key"`
	AppID  string   `yaml:"app_id"`
	Models []string `yaml:"models"`
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
	AppID     string        `yaml:"app_id"`
	Model     string        `yaml:"model"`
	Providers []RouteTarget `yaml:"providers"`
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
			if models, ok := providerModels[t.ProviderID]; ok && len(models) > 0 {
				if !models[r.Model] {
					return fmt.Errorf("route %s/%s: model %s not supported by provider %s", r.AppID, r.Model, r.Model, t.ProviderID)
				}
			}
		}
	}

	return nil
}