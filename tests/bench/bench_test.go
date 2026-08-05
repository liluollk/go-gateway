package bench

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

func setupBenchServer(b *testing.B) *httptest.Server {
	os.Setenv("OPENAI_API_KEY", "sk-bench")
	os.Setenv("ANTHROPIC_API_KEY", "sk-bench")
	defer os.Unsetenv("OPENAI_API_KEY")
	defer os.Unsetenv("ANTHROPIC_API_KEY")

	cfg, err := config.Load("../../config.yaml")
	if err != nil {
		b.Fatalf("config load: %v", err)
	}
	logger, err := middleware.NewLogger("../../bench_logs")
	if err != nil {
		b.Fatalf("logger: %v", err)
	}
	handler := server.NewHandler(cfg, logger)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return httptest.NewServer(mux)
}

func BenchmarkHealthz(b *testing.B) {
	ts := setupBenchServer(b)
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		resp, err := client.Get(ts.URL + "/healthz")
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

func BenchmarkAuth(b *testing.B) {
	ts := setupBenchServer(b)
	defer ts.Close()

	reqBody := map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	body, _ := json.Marshal(reqBody)

	client := &http.Client{Timeout: 5 * time.Second}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-gateway-dev")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

func BenchmarkConcurrentRequests(b *testing.B) {
	ts := setupBenchServer(b)
	defer ts.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	reqBody := map[string]interface{}{
		"model":    "gpt-4o",
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	}
	body, _ := json.Marshal(reqBody)

	var wg sync.WaitGroup
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer sk-gateway-dev")
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}