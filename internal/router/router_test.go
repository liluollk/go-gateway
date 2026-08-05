package router

import (
	"testing"

	"go-gateway/internal/config"
)

func TestSelectProvider_SingleProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "openai-main", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"gpt-4o"}},
		},
		Routes: []config.RouteConfig{
			{
				Model: "gpt-4o",
				Providers: []config.RouteTarget{
					{ProviderID: "openai-main", Weight: 100},
				},
			},
		},
	}

	r := NewRouter(cfg)
	provider := r.SelectProvider("gpt-4o")
	if provider == nil {
		t.Fatal("expected provider, got nil")
	}
	if provider.ID != "openai-main" {
		t.Errorf("expected 'openai-main', got '%s'", provider.ID)
	}
}

func TestSelectProvider_ModelNotFound(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "openai-main", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"gpt-4o"}},
		},
		Routes: []config.RouteConfig{
			{
				Model: "gpt-4o",
				Providers: []config.RouteTarget{
					{ProviderID: "openai-main", Weight: 100},
				},
			},
		},
	}

	r := NewRouter(cfg)
	provider := r.SelectProvider("unknown-model")
	if provider != nil {
		t.Errorf("expected nil for unknown model, got '%s'", provider.ID)
	}
}

func TestSelectProvider_WeightedDistribution(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "p1", Type: "openai", BaseURL: "https://p1.com", Models: []string{"gpt-4o"}},
			{ID: "p2", Type: "openai", BaseURL: "https://p2.com", Models: []string{"gpt-4o"}},
		},
		Routes: []config.RouteConfig{
			{
				Model: "gpt-4o",
				Providers: []config.RouteTarget{
					{ProviderID: "p1", Weight: 7},
					{ProviderID: "p2", Weight: 3},
				},
			},
		},
	}

	r := NewRouter(cfg)
	counts := map[string]int{"p1": 0, "p2": 0}

	for i := 0; i < 1000; i++ {
		provider := r.SelectProvider("gpt-4o")
		if provider == nil {
			t.Fatal("expected provider, got nil")
		}
		counts[provider.ID]++
	}

	// 允许 10% 偏差
	if counts["p1"] < 600 || counts["p1"] > 800 {
		t.Errorf("p1 expected ~700, got %d (7:3 weight)", counts["p1"])
	}
	if counts["p2"] < 200 || counts["p2"] > 400 {
		t.Errorf("p2 expected ~300, got %d (7:3 weight)", counts["p2"])
	}
}

func TestGetFallbackProviders(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "p1", Type: "openai", BaseURL: "https://p1.com", Models: []string{"gpt-4o"}},
			{ID: "p2", Type: "openai", BaseURL: "https://p2.com", Models: []string{"gpt-4o"}},
			{ID: "p3", Type: "openai", BaseURL: "https://p3.com", Models: []string{"gpt-4o"}},
		},
		Routes: []config.RouteConfig{
			{
				Model: "gpt-4o",
				Providers: []config.RouteTarget{
					{ProviderID: "p1", Weight: 50},
					{ProviderID: "p2", Weight: 30},
					{ProviderID: "p3", Weight: 20},
				},
			},
		},
	}

	r := NewRouter(cfg)
	fallbacks := r.GetFallbackProviders("gpt-4o", "p1")
	if len(fallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks, got %d", len(fallbacks))
	}

	ids := map[string]bool{}
	for _, f := range fallbacks {
		ids[f.ID] = true
	}
	if !ids["p2"] || !ids["p3"] {
		t.Errorf("expected p2 and p3 in fallbacks, got %v", ids)
	}
	if ids["p1"] {
		t.Error("p1 should not be in fallbacks")
	}
}

func TestGetFallbackProviders_SingleProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.ProviderConfig{
			{ID: "only", Type: "openai", BaseURL: "https://only.com", Models: []string{"gpt-4o"}},
		},
		Routes: []config.RouteConfig{
			{
				Model: "gpt-4o",
				Providers: []config.RouteTarget{
					{ProviderID: "only", Weight: 100},
				},
			},
		},
	}

	r := NewRouter(cfg)
	fallbacks := r.GetFallbackProviders("gpt-4o", "only")
	if len(fallbacks) != 0 {
		t.Errorf("expected 0 fallbacks for single provider, got %d", len(fallbacks))
	}
}