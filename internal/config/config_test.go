package config

import (
	"os"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	os.Setenv("OPENAI_API_KEY", "sk-test-openai")
	os.Setenv("ANTHROPIC_API_KEY", "sk-test-anthropic")
	defer os.Unsetenv("OPENAI_API_KEY")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

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