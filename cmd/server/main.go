package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"local-llm-service/internal/ollama"
	"local-llm-service/internal/optimization"
	"local-llm-service/internal/rag"
	"local-llm-service/internal/web"
)

func main() {
	addr := env("WEB_ADDR", "127.0.0.1:8081")
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			log.Fatal(err)
		}
		client := http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://127.0.0.1:" + port + "/healthz")
		if err != nil {
			log.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			log.Fatal("unhealthy")
		}
		return
	}
	baseURL := env("OLLAMA_BASE_URL", "http://127.0.0.1:11434")
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		log.Fatal("OLLAMA_BASE_URL must be an HTTP(S) origin without credentials, query, or path")
	}
	model := env("OLLAMA_MODEL", "qwen3:8b")
	if strings.Contains(strings.ToLower(model), "cloud") || strings.ContainsAny(model, " /\\\t\r\n") {
		log.Fatal("OLLAMA_MODEL must name a local Ollama model")
	}
	timeout, err := time.ParseDuration(env("CHAT_TIMEOUT", "5m"))
	if err != nil || timeout < time.Second || timeout > 30*time.Minute {
		log.Fatal("CHAT_TIMEOUT must be between 1s and 30m")
	}
	// No environment proxy or alternate cloud provider is used.
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: timeout, MaxIdleConns: 10, IdleConnTimeout: 90 * time.Second}
	client := ollama.Client{BaseURL: baseURL, HTTP: &http.Client{Transport: transport}}
	index, err := rag.Load(env("RAG_INDEX_PATH", "documents/chunks.json"), env("RAG_STRATEGY", "structured"))
	if err != nil {
		log.Fatalf("load local RAG index: %v", err)
	}
	ragConfig := rag.DefaultConfig()
	for name, target := range map[string]*int{"RAG_TOP_K_BEFORE": &ragConfig.TopKBefore, "RAG_TOP_K_AFTER": &ragConfig.TopKAfter, "RAG_CONTEXT_CHARACTERS": &ragConfig.ContextCharacters} {
		if value := os.Getenv(name); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				log.Fatal(name, " must be an integer")
			}
			*target = parsed
		}
	}
	if value := os.Getenv("RAG_MIN_COVERAGE"); value != "" {
		ragConfig.MinCoverage, err = strconv.ParseFloat(value, 64)
		if err != nil {
			log.Fatal("invalid RAG_MIN_COVERAGE")
		}
	}
	if err := ragConfig.Validate(); err != nil {
		log.Fatal(err)
	}
	log.Printf("Loaded %d %s chunks; corpus=%s", index.Count(), index.Strategy, index.Origin.CorpusHash)
	cacheMiB, err := strconv.Atoi(env("OLLAMA_PROMPT_CACHE_MIB", "512"))
	if err != nil || cacheMiB < 0 || cacheMiB > 8192 {
		log.Fatal("OLLAMA_PROMPT_CACHE_MIB must be 0-8192")
	}
	defaultProfile := env("RAG_PROFILE", "baseline")
	if _, err := optimization.Resolve(defaultProfile, model); err != nil {
		log.Fatal("Unknown RAG_PROFILE")
	}
	app := web.New(client, web.Config{DefaultProfile: defaultProfile, OptimizationReportPath: env("OPTIMIZATION_REPORT_PATH", "artifacts/optimization29/comparison.json"), CacheRAMMiB: cacheMiB, Model: model, Timeout: timeout, Index: index, RAG: ragConfig, ReportPath: env("RAG_REPORT_PATH", "artifacts/rag28/comparison.json")})
	server := &http.Server{Addr: addr, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server.BaseContext = func(net.Listener) context.Context { return ctx }
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Printf("Local LLM web chat: %s (model %s)", addr, model)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
