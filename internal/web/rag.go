package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"local-llm-service/internal/ollama"
	"local-llm-service/internal/optimization"
	"local-llm-service/internal/rag"
)

func (a *App) search(w http.ResponseWriter, r *http.Request) {
	if a.config.Index == nil {
		writeJSON(w, 503, map[string]string{"message": "Локальный индекс не загружен."})
		return
	}
	result, err := a.config.Index.Search(r.URL.Query().Get("query"), a.config.RAG)
	if err != nil {
		writeJSON(w, 400, map[string]string{"message": err.Error()})
		return
	}
	writeJSON(w, 200, result)
}
func (a *App) report(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(a.config.ReportPath)
	if err != nil {
		writeJSON(w, 404, map[string]string{"message": "Отчёт ещё не построен. Запустите scripts/evaluate-rag.py."})
		return
	}
	if len(data) > 16<<20 || !json.Valid(data) {
		writeJSON(w, 500, map[string]string{"message": "Некорректный отчёт."})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(data)
}
func (a *App) ragChat(ctx context.Context, input chatInput, messages []ollama.Message, profile optimization.Profile, send func(event) error) error {
	started := time.Now()
	query := messages[len(messages)-1].Content
	retrieval, err := a.config.Index.Search(query, a.config.RAG)
	if err != nil {
		return err
	}
	if err = send(event{Type: "retrieval", Retrieval: &retrieval}); err != nil {
		return err
	}
	loadSeconds, evalSeconds, promptTokens := 0.0, 0.0, 0
	finish := func(answer rag.Answer, tokens, attempts int, first float64, generation float64) error {
		if err := send(event{Type: "sources", Answer: &answer}); err != nil {
			return err
		}
		if err := send(event{Type: "delta", Text: answer.Text}); err != nil {
			return err
		}
		return send(event{Type: "done", Model: profile.Model, Profile: profile.ID, PromptHash: rag.PromptHash(profile.Prompt), Seconds: time.Since(started).Seconds(), Tokens: tokens, Status: answer.Status, Attempts: attempts, FirstTokenSeconds: first, GenerationSeconds: generation, LoadSeconds: loadSeconds, PromptTokens: promptTokens, EvalSeconds: evalSeconds})
	}
	if len(retrieval.Sources) == 0 {
		return finish(rag.Unknown(), 0, 0, 0, 0)
	}
	prompt := rag.MessagesFor(query, messages[1:len(messages)-1], retrieval, profile.Prompt)
	// Character budget is a heuristic, not an exact tokenizer. Refuse oversized
	// prompts instead of silently dropping the selected evidence.
	characters := 0
	for _, message := range prompt {
		characters += len([]rune(message.Content))
	}
	if profile.Prompt == "baseline" && characters+profile.Options.NumPredict > profile.Options.NumCtx {
		return rag.ErrPromptTooLarge
	}
	// Conservative byte heuristic, not a tokenizer. Actual near-capacity prompts
	// are rejected after generation too, before publishing any claims.
	estimate := 0
	for _, m := range prompt {
		estimate += (len(m.Content)+2)/3 + 16
	}
	if profile.Prompt != "baseline" && estimate+profile.Options.NumPredict > profile.Options.NumCtx {
		return rag.ErrPromptTooLarge
	}
	request := ollama.ChatRequest{Model: profile.Model, Messages: prompt, Think: input.Think, Format: rag.Format, KeepAlive: "10m", Options: profile.Options}
	tokens := 0
	first := 0.0
	generationStart := time.Now()
	for attempt := 1; attempt <= 2; attempt++ {
		var raw strings.Builder
		err = a.client.Chat(ctx, request, func(chunk ollama.ChatChunk) error {
			if first == 0 && chunk.Message.Content != "" {
				first = time.Since(started).Seconds()
			}
			raw.WriteString(chunk.Message.Content)
			if chunk.Message.Thinking != "" {
				if err := send(event{Type: "thinking", Text: chunk.Message.Thinking}); err != nil {
					return err
				}
			}
			if chunk.Done {
				if profile.Prompt != "baseline" && chunk.PromptEvalCount+profile.Options.NumPredict >= profile.Options.NumCtx {
					return rag.ErrPromptTooLarge
				}
				tokens += chunk.EvalCount
				loadSeconds += float64(chunk.LoadDuration) / 1e9
				evalSeconds += float64(chunk.EvalDuration) / 1e9
				promptTokens += chunk.PromptEvalCount
			}
			return nil
		})
		if err != nil {
			return err
		}
		answer, validationErr := rag.Validate(raw.String(), retrieval)
		if validationErr == nil {
			return finish(answer, tokens, attempt, first, time.Since(generationStart).Seconds())
		}
		if attempt == 2 {
			return fmt.Errorf("%w after repair: %v", rag.ErrInvalidEvidence, validationErr)
		}
		// Retry exactly once; neither the invalid claims nor invented quotes reach UI/history.
		request.Messages = append(prompt, ollama.Message{Role: "user", Content: "Предыдущий JSON не прошёл проверку: " + validationErr.Error() + ". Сформируй корректный ответ заново. Используй только существующие ref и номера строк. Если доказательств нет — unknown."})
	}
	return fmt.Errorf("unreachable")
}
