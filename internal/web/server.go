package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"local-llm-service/internal/ollama"
	"local-llm-service/internal/rag"
)

//go:embed assets/*
var assets embed.FS

type Config struct {
	Model       string
	Timeout     time.Duration
	Index       *rag.Index
	RAG         rag.Config
	CacheRAMMiB int
	ReportPath  string
}
type App struct {
	client ollama.Client
	config Config
	busy   chan struct{}
}
type chatInput struct {
	Messages []ollama.Message `json:"messages"`
	Think    bool             `json:"think"`
	RAG      bool             `json:"rag"`
}
type event struct {
	Type              string         `json:"type"`
	Retrieval         *rag.Retrieval `json:"retrieval,omitempty"`
	Answer            *rag.Answer    `json:"answer,omitempty"`
	Status            string         `json:"status,omitempty"`
	Mode              string         `json:"mode,omitempty"`
	Attempts          int            `json:"attempts"`
	FirstTokenSeconds float64        `json:"model_first_token_seconds"`
	LoadSeconds       float64        `json:"load_seconds"`
	PromptTokens      int            `json:"prompt_tokens"`
	EvalSeconds       float64        `json:"eval_seconds"`
	GenerationSeconds float64        `json:"generation_seconds"`
	Text              string         `json:"text,omitempty"`
	Model             string         `json:"model,omitempty"`
	Code              string         `json:"code,omitempty"`
	Seconds           float64        `json:"seconds,omitempty"`
	Tokens            int            `json:"tokens,omitempty"`
	MessagesUsed      int            `json:"messages_used,omitempty"`
	HistoryTrimmed    bool           `json:"history_trimmed,omitempty"`
}

func New(client ollama.Client, config Config) http.Handler {
	app := &App{client: client, config: config, busy: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/status", app.status)
	mux.HandleFunc("POST /api/chat", app.chat)
	mux.HandleFunc("GET /api/rag/search", app.search)
	mux.HandleFunc("GET /api/rag/report", app.report)
	mux.HandleFunc("GET /comparison", func(w http.ResponseWriter, r *http.Request) {
		data, _ := assets.ReadFile("assets/comparison.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		data, _ := assets.ReadFile("assets/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
	for _, name := range []string{"app.js", "style.css", "comparison.js"} {
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			data, _ := assets.ReadFile("assets/" + name)
			contentType := "text/javascript; charset=utf-8"
			if name == "style.css" {
				contentType = "text/css; charset=utf-8"
			}
			w.Header().Set("Content-Type", contentType)
			w.Write(data)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		mux.ServeHTTP(w, r)
	})
}
func (a *App) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, err := a.client.Status(ctx, a.config.Model)
	if err != nil {
		code, message := publicError(ctx, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "model": a.config.Model, "code": code, "message": message})
		return
	}
	data := map[string]any{"ready": true, "model": a.config.Model, "version": status.Version, "rag_ready": a.config.Index != nil, "prompt_cache_mib": a.config.CacheRAMMiB}
	if a.config.Index != nil {
		data["rag_config"] = a.config.RAG
		data["corpus_hash"] = a.config.Index.Origin.CorpusHash
		data["index_sha256"] = a.config.Index.SHA256
		data["strategy"] = a.config.Index.Strategy
		data["chunks"] = a.config.Index.Count()
	}
	writeJSON(w, http.StatusOK, data)
}
func (a *App) chat(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "Запрос разрешён только из интерфейса приложения."})
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input chatInput
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Некорректный запрос или слишком большой диалог."})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Ожидается один JSON-запрос."})
		return
	}
	messages, trimmed, err := prepareMessages(input.Messages)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	if input.RAG && a.config.Index == nil {
		writeJSON(w, 503, map[string]string{"message": "Локальный индекс не загружен."})
		return
	}
	select {
	case a.busy <- struct{}{}:
		defer func() { <-a.busy }()
	default:
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"message": "Модель уже отвечает на другой запрос. Попробуйте после его завершения."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.config.Timeout)
	defer cancel()
	controller := http.NewResponseController(w)
	controller.SetWriteDeadline(time.Now().Add(a.config.Timeout + 10*time.Second))
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	encoder := json.NewEncoder(w)
	send := func(item event) error {
		if err := encoder.Encode(item); err != nil {
			return err
		}
		return controller.Flush()
	}
	mode := "plain"
	if input.RAG {
		mode = "rag"
	}
	used := len(messages) - 1
	if input.RAG {
		history := rag.BoundHistory(messages[1 : len(messages)-1])
		used = len(history) + 1
		trimmed = trimmed || len(history) < len(messages)-2
	}
	if err := send(event{Type: "start", Mode: mode, Model: a.config.Model, MessagesUsed: used, HistoryTrimmed: trimmed}); err != nil {
		return
	}
	if input.RAG {
		err = a.ragChat(ctx, input, messages, send)
		if err != nil && r.Context().Err() == nil {
			log.Printf("rag failed: %v", err)
			code, message := publicError(ctx, err)
			send(event{Type: "error", Code: code, Text: message})
		}
		return
	}
	started := time.Now()
	request := ollama.ChatRequest{Model: a.config.Model, Messages: messages, Think: input.Think, KeepAlive: "10m", Options: ollama.Options{Temperature: 0, Seed: 42, NumCtx: 8192, NumPredict: 1024}}
	firstToken := 0.0
	err = a.client.Chat(ctx, request, func(chunk ollama.ChatChunk) error {
		if chunk.Message.Thinking != "" {
			if err := send(event{Type: "thinking", Text: chunk.Message.Thinking}); err != nil {
				return err
			}
		}
		if chunk.Message.Content != "" {
			if firstToken == 0 {
				firstToken = time.Since(started).Seconds()
			}
			if err := send(event{Type: "delta", Text: chunk.Message.Content}); err != nil {
				return err
			}
		}
		if chunk.Done {
			return send(event{Type: "done", Model: chunk.Model, Seconds: time.Since(started).Seconds(), Tokens: chunk.EvalCount, FirstTokenSeconds: firstToken, GenerationSeconds: time.Since(started).Seconds(), Attempts: 1, LoadSeconds: float64(chunk.LoadDuration) / 1e9, PromptTokens: chunk.PromptEvalCount, EvalSeconds: float64(chunk.EvalDuration) / 1e9})
		}
		return nil
	})
	if err != nil && r.Context().Err() == nil {
		log.Printf("chat failed: %v", err)
		code, message := publicError(ctx, err)
		send(event{Type: "error", Code: code, Text: message})
	}
}
func prepareMessages(input []ollama.Message) ([]ollama.Message, bool, error) {
	if len(input) == 0 || len(input) > 21 || len(input)%2 == 0 {
		return nil, false, errors.New("Диалог должен заканчиваться вопросом и содержать не более 10 предыдущих обменов.")
	}
	messages := make([]ollama.Message, len(input))
	for i, m := range input {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		limit := 4000
		if role == "assistant" {
			limit = 12000
		}
		if m.Role != role || strings.TrimSpace(m.Content) == "" || utf8.RuneCountInString(m.Content) > limit || m.Thinking != "" {
			return nil, false, errors.New("Недопустимое сообщение: нужен непустой текст до 4000 символов и роли user/assistant по очереди.")
		}
		messages[i] = ollama.Message{Role: role, Content: strings.TrimSpace(m.Content)}
	}
	total := 0
	for _, m := range messages {
		total += utf8.RuneCountInString(m.Content)
	}
	trimmed := false
	for total > 6000 && len(messages) > 1 {
		total -= utf8.RuneCountInString(messages[0].Content) + utf8.RuneCountInString(messages[1].Content)
		messages = messages[2:]
		trimmed = true
	}
	system := ollama.Message{Role: "system", Content: "Ты полезный ассистент. Вопросы могут касаться проекта codex-chat-cli; не угадывай его настройки без документов. Отвечай кратко, не более трёх предложений, если пользователь не попросил развёрнутый ответ. Отвечай на языке последнего сообщения пользователя, если он не попросил иначе. Давай ясные и точные ответы, признавай неопределённость. Учитывай предыдущие сообщения диалога. Формулы и вычисления оформляй обычным текстом, без LaTeX."}
	return append([]ollama.Message{system}, messages...), trimmed, nil
}
func publicError(ctx context.Context, err error) (string, string) {
	if errors.Is(err, rag.ErrPromptTooLarge) {
		return "context_budget", "Вопрос и найденные документы не помещаются в контекст. Сократите вопрос или начните новый диалог."
	}
	if errors.Is(err, rag.ErrInvalidEvidence) {
		return "invalid_evidence", "Модель не смогла подтвердить ответ корректными ссылками и цитатами. Повторите или уточните вопрос."
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "Модель не успела ответить. Сократите вопрос или отключите рассуждения."
	}
	if errors.Is(err, ollama.ErrTruncated) {
		return "truncated", "Ответ достиг лимита длины. Сократите вопрос или попросите более краткий ответ."
	}
	var apiErr *ollama.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == http.StatusNotFound {
			return "missing_model", "Модель не установлена. Подготовьте её по инструкции запуска."
		}
		return "generation", "Ollama не смогла завершить ответ. Проверьте состояние модели и повторите запрос."
	}
	return "unavailable", "Нет связи с Ollama или поток ответа прерван. Проверьте, что локальная модель запущена."
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
