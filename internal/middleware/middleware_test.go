package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuth_ValidKey(t *testing.T) {
	cfg := NewAuthConfig([]struct {
		Key    string
		Models []string
	}{
		{Key: "sk-test", Models: []string{"gpt-4o", "claude-sonnet"}},
	})

	handler := Auth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		models := GetAuthModels(r.Context())
		if len(models) != 2 {
			t.Errorf("expected 2 models, got %d", len(models))
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer sk-test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAuth_MissingKey(t *testing.T) {
	cfg := NewAuthConfig([]struct {
		Key    string
		Models []string
	}{
		{Key: "sk-test", Models: []string{"gpt-4o"}},
	})

	handler := Auth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuth_InvalidKey(t *testing.T) {
	cfg := NewAuthConfig([]struct {
		Key    string
		Models []string
	}{
		{Key: "sk-test", Models: []string{"gpt-4o"}},
	})

	handler := Auth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer invalid-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuth_NoBearerPrefix(t *testing.T) {
	cfg := NewAuthConfig([]struct {
		Key    string
		Models []string
	}{
		{Key: "sk-test", Models: []string{"gpt-4o"}},
	})

	handler := Auth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "sk-test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestRateLimiter_Allow(t *testing.T) {
	limits := map[string]RateLimitConfig{
		"key-1": {QPS: 10, RPM: 100},
	}
	rl := NewRateLimiter(limits)

	ok := rl.Allow("key-1")
	if !ok {
		t.Error("expected first request to be allowed")
	}
}

func TestRateLimiter_Exhausted(t *testing.T) {
	limits := map[string]RateLimitConfig{
		"key-1": {QPS: 1, RPM: 100},
	}
	rl := NewRateLimiter(limits)

	// burst = QPS*2 = 2，所以前 2 个请求可通过，第 3 个拒绝
	ok := rl.Allow("key-1")
	if !ok {
		t.Fatal("first request should be allowed (burst=2)")
	}
	ok = rl.Allow("key-1")
	if !ok {
		t.Fatal("second request should be allowed (burst=2)")
	}

	ok = rl.Allow("key-1")
	if ok {
		t.Error("third request should be rejected (burst exhausted, QPS=1)")
	}
}

func TestRateLimiter_DifferentKeys(t *testing.T) {
	limits := map[string]RateLimitConfig{
		"key-1": {QPS: 1, RPM: 100},
		"key-2": {QPS: 1, RPM: 100},
	}
	rl := NewRateLimiter(limits)

	ok := rl.Allow("key-1")
	if !ok {
		t.Fatal("key-1: first request should be allowed")
	}

	ok = rl.Allow("key-2")
	if !ok {
		t.Fatal("key-2: first request should be allowed (independent limit)")
	}
}

func TestRateLimiter_ZeroQPS(t *testing.T) {
	limits := map[string]RateLimitConfig{
		"key-1": {QPS: 0, RPM: 0},
	}
	rl := NewRateLimiter(limits)

	ok := rl.Allow("key-1")
	if !ok {
		t.Error("zero QPS means unlimited, should allow")
	}
}

func TestRequestID_Generate(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := GetTraceID(r.Context())
		if traceID == "" {
			t.Error("trace ID should not be empty")
		}
		if len(traceID) != 32 {
			t.Errorf("expected 32-character trace ID, got %d: %s", len(traceID), traceID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestRequestID_Propagate(t *testing.T) {
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := GetTraceID(r.Context())
		if traceID != "abc123" {
			t.Errorf("expected propagated trace ID 'abc123', got %s", traceID)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Trace-Id", "abc123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestExtractAPIKey_Valid(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer sk-test-key")

	key := ExtractAPIKey(req)
	if key != "sk-test-key" {
		t.Errorf("expected 'sk-test-key', got '%s'", key)
	}
}

func TestExtractAPIKey_Empty(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	key := ExtractAPIKey(req)
	if key != "" {
		t.Errorf("expected empty string, got '%s'", key)
	}
}