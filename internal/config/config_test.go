package config

import (
	"os"
	"path/filepath"
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

func TestLoad_MissingConfigFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("expected error for missing config file, got nil")
	}
}

func TestLoad_InvalidProviderType(t *testing.T) {
	yamlContent := `
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 300s
auth:
  keys:
    - key: sk-test
      app_id: app-test
providers:
  - id: bad-provider
    type: invalid-type
    base_url: https://example.com
    api_key: test-key
routes:
  - app_id: app-test
    model: test-model
    providers:
      - provider_id: bad-provider
        weight: 100
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for invalid provider type, got nil")
	}
}

func TestLoad_NonExistentProviderInRoute(t *testing.T) {
	yamlContent := `
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 300s
auth:
  keys:
    - key: sk-test
      app_id: app-test
providers:
  - id: existing-provider
    type: openai
    base_url: https://example.com
    api_key: test-key
    models:
      - test-model
routes:
  - app_id: app-test
    model: test-model
    providers:
      - provider_id: nonexistent-provider
        weight: 100
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for non-existent provider in route, got nil")
	}
}

func TestLoad_UnresolvedEnvVar(t *testing.T) {
	yamlContent := `
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 300s
auth:
  keys:
    - key: sk-test
      app_id: app-test
providers:
  - id: openai-main
    type: openai
    base_url: https://api.openai.com
    api_key: ${NONEXISTENT_ENV_VAR}
    models:
      - test-model
routes:
  - app_id: app-test
    model: test-model
    providers:
      - provider_id: openai-main
        weight: 100
`
	// Ensure the env var is NOT set
	os.Unsetenv("NONEXISTENT_ENV_VAR")

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for unresolved env var, got nil")
	}
}

func TestLoad_ModelNotSupportedByProvider(t *testing.T) {
	yamlContent := `
server:
  port: 8080
  read_timeout: 30s
  write_timeout: 300s
auth:
  keys:
    - key: sk-test
      app_id: app-test
providers:
  - id: openai-main
    type: openai
    base_url: https://api.openai.com
    api_key: test-key
    models:
      - gpt-4o
routes:
  - app_id: app-test
    model: unsupported-model
    providers:
      - provider_id: openai-main
        weight: 100
`
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for unsupported model, got nil")
	}
}
