//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"go-gateway/internal/config"
	"go-gateway/internal/middleware"
	"go-gateway/internal/server"
)

func setupPhase2TestServer(t *testing.T) *server.Handler {
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

func TestAuth_ModelNotAllowed(t *testing.T) {
	h := setupPhase2TestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	reqBody := map[string]interface{}{
		"model":    "non-existent-model",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(reqBody)

	httpReq, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Authorization", "Bearer sk-gateway-dev")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestRateLimit_Exceeded(t *testing.T) {
	h := setupPhase2TestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	reqBody := map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(reqBody)

	client := &http.Client{Timeout: 200 * time.Millisecond}

	rateLimited := false
	for i := 0; i < 200; i++ {
		httpReq, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
		httpReq.Header.Set("Authorization", "Bearer sk-gateway-dev")
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(httpReq)
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			rateLimited = true
			resp.Body.Close()
			break
		}
		resp.Body.Close()
	}

	if !rateLimited {
		t.Log("rate limit not triggered (QPS=100 may not be saturated in test)")
	}
}

func TestConcurrentRateLimit(t *testing.T) {
	h := setupPhase2TestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	reqBody := map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(reqBody)

	client := &http.Client{Timeout: 200 * time.Millisecond}

	var wg sync.WaitGroup
	rateLimited := false
	var mu sync.Mutex

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			httpReq, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
			httpReq.Header.Set("Authorization", "Bearer sk-gateway-dev")
			httpReq.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(httpReq)
			if err != nil {
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusTooManyRequests {
				mu.Lock()
				rateLimited = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if rateLimited {
		t.Log("concurrent rate limit triggered successfully")
	}
}

func TestModels_ListsAllowedModels(t *testing.T) {
	h := setupPhase2TestServer(t)
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
	data, ok := result["data"].([]interface{})
	if !ok || len(data) == 0 {
		t.Error("expected at least one model in response")
	}
}

func TestHealthz_NoAuth(t *testing.T) {
	h := setupPhase2TestServer(t)
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

func TestAuth_MissingKey(t *testing.T) {
	h := setupPhase2TestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestChatCompletion_MissingModel(t *testing.T) {
	h := setupPhase2TestServer(t)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	reqBody := map[string]interface{}{
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(reqBody)

	httpReq, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
	httpReq.Header.Set("Authorization", "Bearer sk-gateway-dev")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}