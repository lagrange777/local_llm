package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"local-llm-service/internal/ollama"
	"local-llm-service/internal/optimization"
)

type ServiceConfig struct {
	Token             string `json:"-"`
	PublicOrigin      string `json:"public_origin"`
	CookieSecure      bool   `json:"cookie_secure"`
	RequestsPerMinute int    `json:"requests_per_minute"`
	QueueCapacity     int    `json:"queue_capacity"`
	MaxContext        int    `json:"max_context_tokens"`
	MaxOutput         int    `json:"max_output_tokens"`
}

func LoadServiceConfig() (*ServiceConfig, error) {
	enabled, err := strconv.ParseBool(envService("PRIVATE_SERVICE", "false"))
	if err != nil {
		return nil, fmt.Errorf("PRIVATE_SERVICE must be boolean")
	}
	if !enabled {
		return nil, nil
	}
	data, err := os.ReadFile(os.Getenv("SERVICE_TOKEN_FILE"))
	if err != nil {
		return nil, fmt.Errorf("cannot read SERVICE_TOKEN_FILE")
	}
	c := &ServiceConfig{Token: strings.TrimSpace(string(data)), PublicOrigin: os.Getenv("SERVICE_PUBLIC_ORIGIN")}
	c.CookieSecure, err = strconv.ParseBool(envService("SERVICE_COOKIE_SECURE", "true"))
	if err != nil {
		return nil, fmt.Errorf("invalid SERVICE_COOKIE_SECURE")
	}
	for name, target := range map[string]*int{"SERVICE_RPM": &c.RequestsPerMinute, "SERVICE_QUEUE": &c.QueueCapacity, "SERVICE_MAX_CONTEXT": &c.MaxContext, "SERVICE_MAX_OUTPUT": &c.MaxOutput} {
		defaults := map[string]string{"SERVICE_RPM": "6", "SERVICE_QUEUE": "2", "SERVICE_MAX_CONTEXT": "4096", "SERVICE_MAX_OUTPUT": "384"}
		*target, err = strconv.Atoi(envService(name, defaults[name]))
		if err != nil {
			return nil, fmt.Errorf("%s must be integer", name)
		}
	}
	if err = c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}
func envService(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func (c ServiceConfig) Validate() error {
	if len(c.Token) < 32 || len(c.Token) > 256 || strings.ContainsAny(c.Token, " \t\n\r") {
		return fmt.Errorf("service token must contain 32-256 non-whitespace bytes")
	}
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("SERVICE_PUBLIC_ORIGIN must be an origin without path")
	}
	local := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local && !c.CookieSecure) {
		return fmt.Errorf("private service requires HTTPS; HTTP is allowed only for loopback tests with insecure cookie")
	}
	if !c.CookieSecure && !local {
		return fmt.Errorf("secure cookies are required outside loopback tests")
	}
	if c.RequestsPerMinute < 1 || c.RequestsPerMinute > 600 || c.QueueCapacity < 0 || c.QueueCapacity > 16 || c.MaxContext < 512 || c.MaxContext > 8192 || c.MaxOutput < 16 || c.MaxOutput > 1024 || c.MaxOutput >= c.MaxContext {
		return fmt.Errorf("invalid private service limits")
	}
	return nil
}

type windowLimiter struct {
	mu     sync.Mutex
	times  []time.Time
	limit  int
	window time.Duration
}

func (l *windowLimiter) take(now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(now)
	if len(l.times) >= l.limit {
		return false, max(1, int(math.Ceil(l.times[0].Add(l.window).Sub(now).Seconds())))
	}
	l.times = append(l.times, now)
	return true, 0
}
func (l *windowLimiter) expire(now time.Time) {
	i := 0
	for i < len(l.times) && !l.times[i].Add(l.window).After(now) {
		i++
	}
	l.times = append(l.times[:0], l.times[i:]...)
}
func (l *windowLimiter) snapshot(now time.Time) (int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(now)
	reset := 0
	if len(l.times) > 0 {
		reset = max(1, int(math.Ceil(l.times[len(l.times)-1].Add(l.window).Sub(now).Seconds())))
	}
	return l.limit - len(l.times), reset
}

type privateAccess struct {
	config       ServiceConfig
	tokenHash    [32]byte
	mu           sync.Mutex
	sessions     map[[32]byte]time.Time
	loginRate    windowLimiter
	chatRate     windowLimiter
	waiting      chan struct{}
	active       atomic.Int64
	queued       atomic.Int64
	accepted     atomic.Int64
	completed    atomic.Int64
	failed       atomic.Int64
	canceled     atomic.Int64
	limited      atomic.Int64
	overflow     atomic.Int64
	outputTokens atomic.Int64
}

func newAccess(c ServiceConfig) *privateAccess {
	if err := c.Validate(); err != nil {
		panic(err)
	}
	return &privateAccess{config: c, tokenHash: sha256.Sum256([]byte(c.Token)), sessions: make(map[[32]byte]time.Time), loginRate: windowLimiter{limit: 10, window: time.Minute}, chatRate: windowLimiter{limit: c.RequestsPerMinute, window: time.Minute}, waiting: make(chan struct{}, c.QueueCapacity)}
}
func (p *privateAccess) validToken(s string) bool {
	h := sha256.Sum256([]byte(s))
	return subtle.ConstantTimeCompare(h[:], p.tokenHash[:]) == 1
}
func (p *privateAccess) authenticated(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); h != "" {
		return strings.HasPrefix(h, "Bearer ") && p.validToken(strings.TrimPrefix(h, "Bearer "))
	}
	cookie, err := r.Cookie("local_llm_session")
	if err != nil {
		return false
	}
	h := sha256.Sum256([]byte(cookie.Value))
	p.mu.Lock()
	defer p.mu.Unlock()
	expiry, ok := p.sessions[h]
	if !ok {
		return false
	}
	if !expiry.After(time.Now()) {
		delete(p.sessions, h)
		return false
	}
	return true
}
func sameOrigin(r *http.Request, configured string) bool {
	s := r.Header.Get("Origin")
	if s == "" {
		return true
	}
	if configured != "" {
		return s == configured
	}
	u, err := url.Parse(s)
	return err == nil && u.Host == r.Host && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https")
}
func (p *privateAccess) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, p.config.PublicOrigin) {
		writeJSON(w, 403, map[string]string{"code": "origin", "message": "Вход разрешён только из интерфейса сервиса."})
		return
	}
	if ok, retry := p.loginRate.take(time.Now()); !ok {
		reject429(w, "login_rate_limit", retry)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		Token string `json:"token"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"message": "Некорректный запрос входа."})
		return
	}
	if d.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, 400, map[string]string{"message": "Ожидается один запрос входа."})
		return
	}
	if !p.validToken(body.Token) {
		writeJSON(w, 401, map[string]string{"message": "Неверный ключ доступа."})
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeJSON(w, 503, map[string]string{"message": "Вход временно недоступен."})
		return
	}
	session := hex.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(session))
	expiry := time.Now().Add(12 * time.Hour)
	p.mu.Lock()
	for key, exp := range p.sessions {
		if !exp.After(time.Now()) {
			delete(p.sessions, key)
		}
	}
	if len(p.sessions) >= 64 {
		p.mu.Unlock()
		writeJSON(w, 503, map[string]string{"message": "Достигнут лимит сессий."})
		return
	}
	p.sessions[hash] = expiry
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "local_llm_session", Value: session, Path: "/", Expires: expiry, MaxAge: 43200, HttpOnly: true, Secure: p.config.CookieSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"authenticated": true})
}
func (p *privateAccess) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, p.config.PublicOrigin) {
		writeJSON(w, 403, map[string]string{"message": "Недопустимый Origin."})
		return
	}
	if c, err := r.Cookie("local_llm_session"); err == nil {
		p.mu.Lock()
		delete(p.sessions, sha256.Sum256([]byte(c.Value)))
		p.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "local_llm_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: p.config.CookieSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"authenticated": false})
}
func reject429(w http.ResponseWriter, code string, retry int) {
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	writeJSON(w, 429, map[string]string{"code": code, "message": "Лимит запросов или очередь исчерпаны. Повторите позже."})
}
func (p *privateAccess) acquire(ctx context.Context, active chan struct{}) (func(), string) {
	if ok, _ := p.chatRate.take(time.Now()); !ok {
		p.limited.Add(1)
		return nil, "rate_limit"
	}
	select {
	case active <- struct{}{}:
	default:
		select {
		case p.waiting <- struct{}{}:
			p.queued.Add(1)
		default:
			p.overflow.Add(1)
			return nil, "queue_full"
		}
		select {
		case active <- struct{}{}:
			<-p.waiting
			p.queued.Add(-1)
		case <-ctx.Done():
			<-p.waiting
			p.queued.Add(-1)
			p.canceled.Add(1)
			return nil, "queue_canceled"
		}
	}
	if ctx.Err() != nil {
		<-active
		p.canceled.Add(1)
		return nil, "queue_canceled"
	}
	p.active.Add(1)
	p.accepted.Add(1)
	return func() { p.active.Add(-1); <-active }, ""
}
func (a *App) serviceStatus(w http.ResponseWriter, r *http.Request) {
	if a.access == nil {
		writeJSON(w, 200, map[string]bool{"private": false})
		return
	}
	p := a.access
	remaining, reset := p.chatRate.snapshot(time.Now())
	writeJSON(w, 200, map[string]any{"private": true, "limits": p.config, "max_request_bytes": 65536, "max_concurrent": 1, "timeout_seconds": a.config.Timeout.Seconds(), "active": p.active.Load(), "queued": p.queued.Load(), "remaining_requests": remaining, "window_reset_after_seconds": reset, "metrics": map[string]int64{"accepted": p.accepted.Load(), "completed": p.completed.Load(), "failed": p.failed.Load(), "canceled": p.canceled.Load(), "rate_limited": p.limited.Load(), "queue_full": p.overflow.Load(), "output_tokens": p.outputTokens.Load()}})
}
func (a *App) privateWrap(next http.Handler) http.Handler {
	if a.access == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		begin := time.Now()
		rec := &statusWriter{ResponseWriter: w}
		defer func() {
			log.Printf("private_http method=%s route=%s status=%d duration_ms=%d", r.Method, safeRoute(r.URL.Path), rec.statusCode(), time.Since(begin).Milliseconds())
		}()
		p := a.access
		switch {
		case r.Method == "GET" && r.URL.Path == "/healthz":
			next.ServeHTTP(rec, r)
			return
		case r.Method == "GET" && r.URL.Path == "/api/auth/status":
			writeJSON(rec, 200, map[string]bool{"private": true, "authenticated": p.authenticated(r)})
			return
		case r.Method == "POST" && r.URL.Path == "/api/auth/login":
			p.login(rec, r)
			return
		case r.Method == "GET" && (r.URL.Path == "/login" || (r.URL.Path == "/" && !p.authenticated(r))):
			data, _ := assets.ReadFile("assets/login.html")
			rec.Header().Set("Content-Type", "text/html; charset=utf-8")
			rec.Write(data)
			return
		case r.Method == "GET" && (r.URL.Path == "/style.css" || r.URL.Path == "/login.js"):
			next.ServeHTTP(rec, r)
			return
		}
		if !p.authenticated(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(rec, 401, map[string]string{"code": "unauthorized", "message": "Для доступа нужен ключ или вход в сервис."})
			} else {
				http.Redirect(rec, r, "/login", http.StatusSeeOther)
			}
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/auth/logout" {
			p.logout(rec, r)
			return
		}
		next.ServeHTTP(rec, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
		w.ResponseWriter.WriteHeader(n)
	}
}
func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) statusCode() int {
	if w.status == 0 {
		return 200
	}
	return w.status
}
func safeRoute(s string) string {
	switch s {
	case "/", "/login", "/healthz", "/api/chat", "/api/status", "/api/service/status", "/api/auth/login", "/api/auth/logout", "/api/auth/status":
		return s
	}
	return "other"
}
func estimatedTokens(messages []ollama.Message) int {
	total := 0
	for _, m := range messages {
		total += (len(m.Content)+2)/3 + 32
	}
	return total
}
func (a *App) boundedProfile(p optimization.Profile) optimization.Profile {
	if a.access != nil {
		p.Options.NumCtx = min(p.Options.NumCtx, a.access.config.MaxContext)
		p.Options.NumPredict = min(p.Options.NumPredict, a.access.config.MaxOutput)
	}
	return p
}
func (a *App) profileList() []optimization.Profile {
	items := optimization.Profiles(a.config.Model)
	for i, p := range items {
		items[i] = a.boundedProfile(p)
	}
	return items
}
func (a *App) profilesHash() string {
	b, _ := json.Marshal(a.profileList())
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
