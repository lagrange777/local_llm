package main

import (
	"context"
	"errors"
	"local-llm-service/internal/ollama"
	"local-llm-service/internal/web"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
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
	app := web.New(client, web.Config{Model: model, Timeout: timeout})
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
