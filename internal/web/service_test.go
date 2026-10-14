package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"local-llm-service/internal/ollama"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func privateConfig() *ServiceConfig {
	return &ServiceConfig{Token: testKey, PublicOrigin: "http://127.0.0.1:8081", RequestsPerMinute: 6, QueueCapacity: 2, MaxContext: 4096, MaxOutput: 384}
}
func privateRequest(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestPrivateAuthenticationAndRevocation(t *testing.T) {
	h := New(ollama.Client{}, Config{Service: privateConfig(), Timeout: time.Second})
	for _, path := range []string{"/api/service/status", "/api/optimization/profiles", "/api/optimization/report", "/api/rag/search", "/api/chat", "/app.js"} {
		w := privateRequest(t, h, "GET", path, "", "")
		if w.Code != 401 && w.Code != 303 {
			t.Fatalf("unauthorized %s: %d", path, w.Code)
		}
	}
	if w := privateRequest(t, h, "GET", "/healthz", "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := privateRequest(t, h, "GET", "/api/service/status", "", "incorrect"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	login := privateRequest(t, h, "POST", "/api/auth/login", `{"token":"`+testKey+`"}`, "")
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	if strings.Contains(cookies[0].Value, testKey) {
		t.Fatal("API key leaked into cookie")
	}
	request := httptest.NewRequest("GET", "/api/service/status", nil)
	request.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 200 || strings.Contains(w.Body.String(), testKey) {
		t.Fatal("session failed or secret leaked")
	}
	logout := httptest.NewRequest("POST", "/api/auth/logout", nil)
	logout.AddCookie(cookies[0])
	h.ServeHTTP(httptest.NewRecorder(), logout)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatal("logged-out session reused")
	}
}
func TestPrivateConfigFailsClosed(t *testing.T) {
	c := privateConfig()
	for _, mutate := range []func(*ServiceConfig){func(c *ServiceConfig) { c.Token = "short" }, func(c *ServiceConfig) { c.PublicOrigin = "http://remote.example" }, func(c *ServiceConfig) { c.PublicOrigin = "https://host/path" }, func(c *ServiceConfig) { c.MaxOutput = c.MaxContext }, func(c *ServiceConfig) { c.QueueCapacity = 100 }, func(c *ServiceConfig) { c.RequestsPerMinute = 0 }} {
		copy := *c
		mutate(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	t.Setenv("PRIVATE_SERVICE", "true")
	t.Setenv("SERVICE_TOKEN_FILE", "/missing-secret")
	if _, err := LoadServiceConfig(); err == nil {
		t.Fatal("missing secret accepted")
	}
	c.PublicOrigin = "https://server.tail.example"
	c.CookieSecure = true
	if c.Validate() != nil {
		t.Fatal("valid HTTPS config rejected")
	}
}
func TestPrivateWindowLimiter(t *testing.T) {
	p := newAccess(*privateConfig())
	now := time.Unix(1000, 0)
	for i := 0; i < 6; i++ {
		if ok, _ := p.chatRate.take(now); !ok {
			t.Fatal("early limit")
		}
	}
	if ok, retry := p.chatRate.take(now.Add(30 * time.Second)); ok || retry != 30 {
		t.Fatal("wrong rolling limit")
	}
	if ok, _ := p.chatRate.take(now.Add(time.Minute)); !ok {
		t.Fatal("limit never reset")
	}
	for i := 0; i < 10; i++ {
		p.loginRate.take(now)
	}
	if ok, _ := p.loginRate.take(now); ok {
		t.Fatal("login brute force unbounded")
	}
}
func TestPrivateQueueBoundedAndCanceledSlotReused(t *testing.T) {
	p := newAccess(*privateConfig())
	active := make(chan struct{}, 1)
	first, code := p.acquire(context.Background(), active)
	if code != "" {
		t.Fatal(code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	released := make(chan string, 2)
	for i := 0; i < 2; i++ {
		go func() {
			r, c := p.acquire(ctx, active)
			if r != nil {
				r()
			}
			released <- c
		}()
	}
	deadline := time.Now().Add(time.Second)
	for p.queued.Load() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("queue did not fill")
		}
		time.Sleep(time.Millisecond)
	}
	if r, c := p.acquire(context.Background(), active); r != nil || c != "queue_full" {
		t.Fatal("queue overflow accepted")
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case c := <-released:
			if c != "queue_canceled" {
				t.Fatal(c)
			}
		case <-time.After(time.Second):
			t.Fatal("queue cancellation leaked")
		}
	}
	first()
	if p.queued.Load() != 0 || p.active.Load() != 0 {
		t.Fatal("slots leaked")
	}
	r, c := p.acquire(context.Background(), active)
	if c != "" {
		t.Fatal(c)
	}
	r()
}
func TestPrivateLimitsBeforeUpstreamAndProfileCaps(t *testing.T) {
	h := New(ollama.Client{BaseURL: "http://127.0.0.1:1"}, Config{Service: privateConfig(), Timeout: time.Second})
	body := `{"messages":[{"role":"user","content":"` + strings.Repeat(string(rune(0x1f600)), 4000) + `"}]}`
	w := privateRequest(t, h, "POST", "/api/chat", body, testKey)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "context_budget") {
		t.Fatal(w.Code, w.Body.String())
	}
	// Valid JSON requires reading over the entire body limit.
	w = privateRequest(t, h, "POST", "/api/chat", `{"messages":[{"role":"user","content":"`+strings.Repeat("x", 65537)+`"}]}`, testKey)
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
	request := httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+testKey)
	request.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	w = privateRequest(t, h, "GET", "/api/optimization/profiles", "", testKey)
	var data struct {
		Profiles []struct {
			Options ollama.Options `json:"options"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	for _, p := range data.Profiles {
		if p.Options.NumCtx > 4096 || p.Options.NumPredict > 384 {
			t.Fatal("profile bypass")
		}
	}
}
func TestPrivatePlainPromptNearCapacityDoesNotPublishAnswer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"message":{"content":"UNSAFE"}}
{"done":true,"done_reason":"stop","prompt_eval_count":4000}
`))
	}))
	defer upstream.Close()
	h := New(ollama.Client{BaseURL: upstream.URL}, Config{Service: privateConfig(), Timeout: time.Second})
	w := privateRequest(t, h, "POST", "/api/chat", `{"messages":[{"role":"user","content":"hi"}]}`, testKey)
	if strings.Contains(w.Body.String(), "UNSAFE") || !strings.Contains(w.Body.String(), "context_budget") {
		t.Fatal(w.Body.String())
	}
}
func TestPrivateGenerationRateRejectedWithoutUpstream(t *testing.T) {
	called := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"message":{"content":"ok"}}
{"done":true,"done_reason":"stop"}
`))
	}))
	defer upstream.Close()
	h := New(ollama.Client{BaseURL: upstream.URL}, Config{Service: privateConfig(), Timeout: time.Second})
	for i := 0; i < 7; i++ {
		w := privateRequest(t, h, "POST", "/api/chat", `{"messages":[{"role":"user","content":"hello"}]}`, testKey)
		if i < 6 && w.Code != 200 {
			t.Fatal(w.Code)
		}
		if i == 6 && (w.Code != 429 || w.Header().Get("Retry-After") == "") {
			t.Fatal("missing 429 retry")
		}
	}
	if called != 6 {
		t.Fatal("rate limit bypass", called)
	}
}

func TestPrivateCookieCannotResetBearerQuota(t *testing.T) {
	h := New(ollama.Client{BaseURL: "http://127.0.0.1:1"}, Config{Service: privateConfig(), Timeout: time.Millisecond})
	// A failed upstream attempt still consumes the owner's quota.
	for i := 0; i < 6; i++ {
		privateRequest(t, h, "POST", "/api/chat", `{"messages":[{"role":"user","content":"hello"}]}`, testKey)
	}
	login := privateRequest(t, h, "POST", "/api/auth/login", `{"token":"`+testKey+`"}`, "")
	r := httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	r.AddCookie(login.Result().Cookies()[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "rate_limit") {
		t.Fatal("new cookie bypassed quota", w.Body.String())
	}
}
func TestPrivateExpiredSessionRejected(t *testing.T) {
	p := newAccess(*privateConfig())
	cookie := "random-session"
	p.sessions[sha256.Sum256([]byte(cookie))] = time.Now().Add(-time.Second)
	r := httptest.NewRequest("GET", "/api/status", nil)
	r.AddCookie(&http.Cookie{Name: "local_llm_session", Value: cookie})
	if p.authenticated(r) {
		t.Fatal("expired session accepted")
	}
}
