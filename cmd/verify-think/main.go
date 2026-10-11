package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"local-llm-service/internal/ollama"
)

type question struct {
	ID       string `json:"id"`
	Prompt   string `json:"prompt"`
	Expected string `json:"expected"`
}
type result struct {
	Question       question        `json:"question"`
	Request        ollama.Request  `json:"request"`
	Answer         ollama.Response `json:"answer"`
	ElapsedSeconds float64         `json:"elapsed_seconds"`
	Passed         bool            `json:"passed"`
	Failures       []string        `json:"failures,omitempty"`
}

func main() {
	url := flag.String("url", env("OLLAMA_BASE_URL", "http://127.0.0.1:11434"), "Ollama URL")
	model := flag.String("model", env("OLLAMA_MODEL", "qwen3:8b"), "model to check")
	output := flag.String("output", "artifacts/think-modes", "result directory")
	flag.Parse()
	if err := run(*url, *model, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(url, model, output string) error {
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	questions := []question{
		{"fact", "Назови столицу Франции. Ответь одним словом по-русски.", "Париж"},
		{"math", "Сколько будет 17 * 23? Ответь только целым числом.", "391"},
		{"explanation", "Объясни по-русски, что такое RAG: опиши поиск информации во внешних источниках и генерацию ответа на её основе. Не более двух предложений.", "Объяснение поиска внешней информации и генерации ответа на её основе."},
	}
	client := ollama.Client{BaseURL: url}
	results := make([]result, 0, len(questions)*2)
	failures := 0
	for _, q := range questions {
		for _, think := range []bool{false, true} {
			request := ollama.Request{Model: model, Prompt: q.Prompt, Think: think, KeepAlive: "30m", Options: ollama.Options{Temperature: 0, Seed: 42, NumCtx: 4096, NumPredict: 2048}}
			fmt.Printf("%s, think=%t...\n", q.ID, think)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			started := time.Now()
			answer, err := client.Generate(ctx, request)
			elapsed := time.Since(started).Seconds()
			cancel()
			item := result{Question: q, Request: request, Answer: answer, ElapsedSeconds: elapsed, Passed: true}
			if err != nil {
				item.Failures = append(item.Failures, err.Error())
			}
			if strings.Contains(answer.Response, "<think>") || strings.Contains(answer.Response, "</think>") {
				item.Failures = append(item.Failures, "thinking tag leaked into answer")
			}
			if think && strings.TrimSpace(answer.Thinking) == "" {
				item.Failures = append(item.Failures, "thinking=true has no thinking output")
			}
			if !think && strings.TrimSpace(answer.Thinking) != "" {
				item.Failures = append(item.Failures, "thinking=false generated thinking")
			}
			normalized := strings.Trim(strings.TrimSpace(answer.Response), ".! \n")
			if q.ID != "explanation" && normalized != q.Expected {
				item.Failures = append(item.Failures, "answer did not match expected final result")
			}
			if q.ID == "explanation" {
				lower := strings.ToLower(answer.Response)
				if !strings.Contains(lower, "rag") || !strings.Contains(lower, "генерац") || !(strings.Contains(lower, "поиск") || strings.Contains(lower, "ищет") || strings.Contains(lower, "извлеч")) {
					item.Failures = append(item.Failures, "RAG answer did not contain expected concepts")
				}
				for _, marker := range []string{"хорошо, мне нужно", "давай подума", "okay, so", "let me think"} {
					if strings.Contains(lower, marker) {
						item.Failures = append(item.Failures, "self-talk in final answer")
					}
				}
			}
			if len(item.Failures) > 0 {
				item.Passed = false
				failures++
			}
			if err := save(filepath.Join(output, fmt.Sprintf("%s-think-%t.json", q.ID, think)), item); err != nil {
				return err
			}
			results = append(results, item)
			fmt.Printf("Passed=%t, time=%.2fs, tokens=%d, thinking chars=%d, answer=%s\n\n", item.Passed, elapsed, answer.EvalCount, len([]rune(answer.Thinking)), answer.Response)
		}
	}
	summary := struct {
		URL          string   `json:"base_url"`
		Model        string   `json:"model"`
		CompletedAt  string   `json:"completed_at"`
		Results      []result `json:"results"`
		FailureCount int      `json:"failure_count"`
	}{url, model, time.Now().UTC().Format(time.RFC3339), results, failures}
	if err := save(filepath.Join(output, "results.json"), summary); err != nil {
		return err
	}
	if failures > 0 {
		return fmt.Errorf("%d of %d mode checks failed", failures, len(results))
	}
	return nil
}
func save(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
