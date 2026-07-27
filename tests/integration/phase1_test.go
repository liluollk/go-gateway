//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go-gateway/internal/config"
	"go-gateway/internal/middleware"
	"go-gateway/internal/server"
)

func setupTestServer(t *testing.T) *server.Handler {
	os.Setenv("OPENAI_API_KEY", "sk-test-openai")
	os.Setenv("ANTHROPIC_API_KEY", "sk-test-anthropic")
	defer os.Unsetenv("OPENAI_API_KEY")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

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
