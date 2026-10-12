// Package optimization defines bounded, reproducible local inference profiles.
package optimization

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"local-llm-service/internal/ollama"
)

type Profile struct {
	ID             string         `json:"id"`
	Label          string         `json:"label"`
	Model          string         `json:"model"`
	Options        ollama.Options `json:"options"`
	Prompt         string         `json:"prompt"`
	ThinkSupported bool           `json:"think_supported"`
}

func Profiles(baseline string) []Profile {
	if baseline == "" {
		baseline = "qwen3:8b"
	}
	return []Profile{
		{ID: "baseline", Label: "Исходная 8B Q4", Model: baseline, Options: ollama.Options{Temperature: 0, Seed: 42, NumCtx: 8192, NumPredict: 1024}, Prompt: "baseline", ThinkSupported: true},
		{ID: "optimized-8b", Label: "Настроенная 8B Q4", Model: baseline, Options: ollama.Options{Temperature: 0, Seed: 42, NumCtx: 4096, NumPredict: 384}, Prompt: "optimized", ThinkSupported: true},
		{ID: "optimized-8b-t02", Label: "8B · temperature 0.2 (подбор)", Model: baseline, Options: ollama.Options{Temperature: 0.2, Seed: 42, NumCtx: 4096, NumPredict: 384}, Prompt: "optimized", ThinkSupported: true},
		{ID: "instruct-4b-q8", Label: "4B Instruct · Q8", Model: "rag29-instruct-4b-q8", Options: ollama.Options{Temperature: 0, Seed: 42, NumCtx: 4096, NumPredict: 384}, Prompt: "optimized"},
		{ID: "instruct-4b-q4", Label: "4B Instruct · Q4", Model: "rag29-instruct-4b-q4", Options: ollama.Options{Temperature: 0, Seed: 42, NumCtx: 4096, NumPredict: 384}, Prompt: "optimized"},
	}
}
func Resolve(id, baseline string) (Profile, error) {
	for _, p := range Profiles(baseline) {
		if p.ID == id {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("unknown optimization profile")
}
func Fingerprint(baseline string) string {
	b, _ := json.Marshal(Profiles(baseline))
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
