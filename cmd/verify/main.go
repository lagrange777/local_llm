package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"local-llm-service/internal/ollama"
)

type testCase struct {
	ID         string `json:"id"`
	Difficulty string `json:"difficulty"`
	Prompt     string `json:"prompt"`
	Expected   string `json:"expected"`
}
type result struct {
	Test            testCase        `json:"test"`
	Request         ollama.Request  `json:"request"`
	Answer          ollama.Response `json:"answer"`
	ElapsedSeconds  float64         `json:"elapsed_seconds"`
	TokensPerSecond float64         `json:"tokens_per_second"`
}

func main() {
	baseURL := flag.String("url", env("OLLAMA_BASE_URL", "http://127.0.0.1:11434"), "Ollama HTTP address")
	model := flag.String("model", env("OLLAMA_MODEL", "qwen3:8b"), "local model")
	output := flag.String("output", "artifacts/verification", "directory for actual responses")
	timeout := flag.Duration("timeout", 3*time.Minute, "timeout per request")
	think := flag.Bool("think", false, "enable model reasoning")
	flag.Parse()
	if err := run(*baseURL, *model, *output, *timeout, *think); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(baseURL, model, output string, timeout time.Duration, think bool) error {
	if err := os.MkdirAll(output, 0755); err != nil {
		return err
	}
	cases := []testCase{
		{"01-simple", "simple", "Назови столицу Франции. Ответь одним предложением на русском языке.", "Париж."},
		{"02-reasoning", "medium", "Покупатель берёт 3 товара по 1200 рублей каждый. Скидка 15% применяется ко всей сумме. Сколько он заплатит? Покажи вычисление и итог.", "3600 рублей до скидки; скидка 540 рублей; итог 3060 рублей."},
		{"03-code", "complex", "Напиши на Go функцию Unique(values []int) []int, которая удаляет дубликаты и сохраняет порядок первого появления. Только функция в блоке go, без package и import. Для [3, 1, 3, 2, 1] результат [3, 1, 2]. Пустой или nil ввод должен давать пустой результат. Сложность O(n) в среднем; используй map для проверки встреченных чисел.", "Корректная функция с map, сохранением порядка; тесты на дубликаты, отрицательные числа, nil и пустой срез проходят."},
	}
	results := make([]result, 0, len(cases))
	client := ollama.Client{BaseURL: baseURL}
	for _, tc := range cases {
		request := ollama.Request{Model: model, Prompt: tc.Prompt, Think: think, KeepAlive: "30m", Options: ollama.Options{Temperature: 0.6, NumCtx: 4096, NumPredict: 3072}}
		fmt.Printf("Running %s (%s)...\n", tc.ID, tc.Difficulty)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		started := time.Now()
		answer, err := client.Generate(ctx, request)
		elapsed := time.Since(started).Seconds()
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", tc.ID, err)
		}
		speed := 0.0
		if answer.EvalDuration > 0 {
			speed = float64(answer.EvalCount) / (float64(answer.EvalDuration) / 1e9)
		}
		item := result{tc, request, answer, elapsed, speed}
		if err := save(filepath.Join(output, tc.ID+".json"), item); err != nil {
			return err
		}
		results = append(results, item)
		fmt.Printf("%s\nTime: %.2fs; speed: %.2f tokens/s\n\n", answer.Response, elapsed, speed)
	}
	return save(filepath.Join(output, "results.json"), struct {
		BaseURL     string   `json:"base_url"`
		Model       string   `json:"model"`
		CompletedAt string   `json:"completed_at"`
		Results     []result `json:"results"`
	}{baseURL, model, time.Now().UTC().Format(time.RFC3339), results})
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
