package web

import (
	"context"
	"encoding/json"
	"local-llm-service/internal/optimization"
	"local-llm-service/internal/rag"
	"net/http"
	"os"
	"time"
)

func (a *App) resolveProfile(id string) (optimization.Profile, error) {
	if id == "" {
		id = a.config.DefaultProfile
	}
	if id == "" {
		id = "baseline"
	}
	p, err := optimization.Resolve(id, a.config.Model)
	return a.boundedProfile(p), err
}
func (a *App) profiles(w http.ResponseWriter, r *http.Request) {
	p, _ := a.resolveProfile("")
	hashes := map[string]string{"baseline": rag.PromptHash("baseline"), "optimized": rag.PromptHash("optimized")}
	writeJSON(w, 200, map[string]any{"default_profile": p.ID, "profiles": a.profileList(), "fingerprint": a.profilesHash(), "prompt_hashes": hashes})
}
func (a *App) optimizationReport(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(a.config.OptimizationReportPath)
	if err != nil {
		writeJSON(w, 404, map[string]string{"message": "Сравнение оптимизации ещё не выполнено."})
		return
	}
	if len(data) > 16<<20 || !json.Valid(data) {
		writeJSON(w, 500, map[string]string{"message": "Некорректный отчёт."})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(data)
}

func (a *App) models(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	models, err := a.client.Installed(ctx)
	if err != nil {
		writeJSON(w, 503, map[string]string{"message": "Local models unavailable"})
		return
	}
	writeJSON(w, 200, models)
}
