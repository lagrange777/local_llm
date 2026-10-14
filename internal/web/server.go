package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"local-llm-service/internal/ollama"
	"local-llm-service/internal/rag"
)

//go:embed assets/*
var assets embed.FS

type Config struct {
	Model                  string
	Timeout                time.Duration
	Index                  *rag.Index
	RAG                    rag.Config
	CacheRAMMiB            int
	ReportPath             string
	OptimizationReportPath string
	DefaultProfile         string
	Service                *ServiceConfig
}
type App struct {
	client ollama.Client
	config Config
	busy   chan struct{}
	access *privateAccess
}
type chatInput struct {
	Messages []ollama.Message `json:"messages"`
	Think    bool             `json:"think"`
	RAG      bool             `json:"rag"`
	Profile  string           `json:"profile,omitempty"`
}
type event struct {
	Type              string         `json:"type"`
	Retrieval         *rag.Retrieval `json:"retrieval,omitempty"`
	Answer            *rag.Answer    `json:"answer,omitempty"`
	Status            string         `json:"status,omitempty"`
	Profile           string         `json:"profile,omitempty"`
	PromptHash        string         `json:"prompt_sha256,omitempty"`
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
	if config.Service != nil {
		app.access = newAccess(*config.Service)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/status", app.status)
	mux.HandleFunc("GET /api/service/status", app.serviceStatus)
	mux.HandleFunc("POST /api/chat", app.chat)
	mux.HandleFunc("GET /api/optimization/profiles", app.profiles)
	mux.HandleFunc("GET /api/optimization/models", app.models)
	mux.HandleFunc("GET /api/optimization/report", app.optimizationReport)
	mux.HandleFunc("GET /optimization", func(w http.ResponseWriter, r *http.Request) {
		data, _ := assets.ReadFile("assets/optimization.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})
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
	for _, name := range []string{"app.js", "style.css", "comparison.js", "optimization.js", "login.js"} {
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
	handler := app.privateWrap(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		handler.ServeHTTP(w, r)
	})
}
func (a *App) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id := r.URL.Query().Get("profile")
	profile, err := a.resolveProfile(id)
	if err != nil {
		writeJSON(w, 400, map[string]string{"message": "Unknown profile"})
		return
	}
	status, err := a.client.Status(ctx, profile.Model)
	if err != nil {
		code, message := publicError(ctx, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "model": profile.Model, "code": code, "message": message})
		return
	}
	data := map[string]any{"ready": true, "model": profile.Model, "profile": profile.ID, "think_supported": profile.ThinkSupported, "generation": profile.Options, "prompt_sha256": rag.PromptHash(profile.Prompt), "profiles_sha256": a.profilesHash(), "version": status.Version, "rag_ready": a.config.Index != nil, "prompt_cache_mib": a.config.CacheRAMMiB}
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
	origin := ""
	if a.access != nil {
		origin = a.access.config.PublicOrigin
	}
	if !sameOrigin(r, origin) {
		writeJSON(w, 403, map[string]string{"code": "origin", "message": "Запрос разрешён только из интерфейса приложения."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input chatInput
	if err := decoder.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, 413, map[string]string{"code": "body_limit", "message": "Запрос превышает 64 КиБ."})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Некорректный запрос или слишком большой диалог."})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Ожидается один JSON-запрос."})
		return
	}
	messages, trimmed, err := prepareMessagesFor(input.Messages, a.access == nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	if input.RAG && a.config.Index == nil {
		writeJSON(w, 503, map[string]string{"message": "Локальный индекс не загружен."})
		return
	}
	profile, err := a.resolveProfile(input.Profile)
	if err != nil {
		writeJSON(w, 400, map[string]string{"message": "Unknown profile"})
		return
	}
	if input.Think && !profile.ThinkSupported {
		writeJSON(w, 400, map[string]string{"message": "Этот профиль поддерживает только ответы без рассуждений."})
		return
	}
	if a.access != nil && estimatedTokens(messages)+profile.Options.NumPredict >= profile.Options.NumCtx {
		writeJSON(w, 400, map[string]string{"code": "context_budget", "message": "Диалог превышает бюджет контекста. Сократите вопрос или начните новый диалог."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.config.Timeout)
	defer cancel()
	controller := http.NewResponseController(w)
	controller.SetWriteDeadline(time.Now().Add(a.config.Timeout + 10*time.Second))
	if a.access != nil {
		release, code := a.access.acquire(ctx, a.busy)
		if code != "" {
			if code == "rate_limit" {
				_, retry := a.access.chatRate.snapshot(time.Now())
				reject429(w, code, max(1, retry))
			} else if code == "queue_full" {
				reject429(w, code, 2)
			} else if r.Context().Err() == nil {
				writeJSON(w, 504, map[string]string{"code": "timeout", "message": "Истекло время ожидания в очереди."})
			}
			return
		}
		defer release()
		defer func() {
			if r.Context().Err() != nil {
				a.access.canceled.Add(1)
			}
		}()
	} else {
		select {
		case a.busy <- struct{}{}:
			defer func() { <-a.busy }()
		default:
			reject429(w, "queue_full", 2)
			return
		}
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("X-Accel-Buffering", "no")
	encoder := json.NewEncoder(w)
	send := func(item event) error {
		if a.access != nil {
			if item.Type == "done" {
				a.access.completed.Add(1)
				a.access.outputTokens.Add(int64(item.Tokens))
				log.Printf("private_generation tokens=%d seconds=%.3f", item.Tokens, item.Seconds)
			}
			if item.Type == "error" {
				a.access.failed.Add(1)
			}
		}
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
	if err := send(event{Type: "start", Mode: mode, Model: profile.Model, Profile: profile.ID, PromptHash: rag.PromptHash(profile.Prompt), MessagesUsed: used, HistoryTrimmed: trimmed}); err != nil {
		return
	}
	if input.RAG {
		err = a.ragChat(ctx, input, messages, profile, send)
		if err != nil && r.Context().Err() == nil {
			log.Printf("rag failed code=%s", func() string { code, _ := publicError(ctx, err); return code }())
			code, message := publicError(ctx, err)
			send(event{Type: "error", Code: code, Text: message})
		}
		return
	}
	started := time.Now()
	request := ollama.ChatRequest{Model: profile.Model, Messages: messages, Think: input.Think, KeepAlive: "10m", Options: profile.Options}
	firstToken := 0.0
	var buffered []ollama.ChatChunk
	bufferBytes := 0
	consume := func(chunk ollama.ChatChunk) error {
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
	}
	err = a.client.Chat(ctx, request, func(chunk ollama.ChatChunk) error {
		if a.access == nil {
			return consume(chunk)
		}
		if firstToken == 0 && chunk.Message.Content != "" {
			firstToken = time.Since(started).Seconds()
		}
		bufferBytes += len(chunk.Message.Content) + len(chunk.Message.Thinking)
		if bufferBytes > 2<<20 || len(buffered) > 8192 {
			return ollama.ErrTruncated
		}
		if chunk.Done && chunk.PromptEvalCount+profile.Options.NumPredict >= profile.Options.NumCtx {
			return rag.ErrPromptTooLarge
		}
		buffered = append(buffered, chunk)
		return nil
	})
	if err == nil && a.access != nil {
		for _, chunk := range buffered {
			if err = consume(chunk); err != nil {
				break
			}
		}
	}
	if err != nil && r.Context().Err() == nil {
		log.Printf("chat failed code=%s", func() string { code, _ := publicError(ctx, err); return code }())
		code, message := publicError(ctx, err)
		send(event{Type: "error", Code: code, Text: message})
	}
}
func prepareMessages(input []ollama.Message) ([]ollama.Message, bool, error) {
	return prepareMessagesFor(input, true)
}
func prepareMessagesFor(input []ollama.Message, allowTrim bool) ([]ollama.Message, bool, error) {
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
	for allowTrim && total > 6000 && len(messages) > 1 {
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
