package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"local-llm-service/internal/ollama"
	"local-llm-service/internal/rag"
)

func TestOptimizationProfileActuallyReachesOllama(t *testing.T) {
	for _, id := range []string{"baseline", "optimized-8b", "instruct-4b-q4", "instruct-4b-q8"} {
		t.Run(id, func(t *testing.T) {
			var request ollama.ChatRequest
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewDecoder(r.Body).Decode(&request)
				raw := `{"status":"answered","claims":[{"text":"История JSON записывается во временный файл.","evidence":[{"ref":"S1","start":1,"end":1}]}],"clarification":""}`
				json.NewEncoder(w).Encode(ollama.ChatChunk{Message: ollama.Message{Content: raw}})
				json.NewEncoder(w).Encode(ollama.ChatChunk{Done: true, PromptEvalCount: 50, EvalCount: 20})
			}))
			defer upstream.Close()
			app := New(ollama.Client{BaseURL: upstream.URL}, Config{Model: "frozen", Index: ragFixture(t), RAG: rag.DefaultConfig(), Timeout: time.Second})
			body := `{"messages":[{"role":"user","content":"Как сохраняется история JSON?"}],"rag":true,"profile":"` + id + `"}`
			recorder := httptest.NewRecorder()
			app.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/chat", strings.NewReader(body)))
			if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"type":"done"`) {
				t.Fatal(recorder.Code, recorder.Body.String())
			}
			if id == "baseline" {
				if request.Options.NumCtx != 8192 || request.Options.NumPredict != 1024 || !strings.Contains(request.Messages[0].Content, "chunk_id=") {
					t.Fatal(request)
				}
			} else if request.Options.NumCtx != 4096 || request.Options.NumPredict != 384 || strings.Contains(request.Messages[0].Content, "chunk_id=") {
				t.Fatal(request)
			}
			if strings.HasPrefix(id, "instruct-") && request.Model == "frozen" {
				t.Fatal("profile did not change model")
			}
		})
	}
}
func TestOptimizationRejectsArbitraryProfileAndUnsupportedThinking(t *testing.T) {
	app := New(ollama.Client{BaseURL: "http://127.0.0.1:1"}, Config{Model: "frozen", Timeout: time.Second})
	for _, suffix := range []string{`"profile":"injected"`, `"profile":"instruct-4b-q4","think":true`} {
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}],`+suffix+`}`)))
		if recorder.Code != 400 {
			t.Fatal(recorder.Code, recorder.Body.String())
		}
	}
}

func TestRAGCapacityGuardHidesAnswerWhenPromptIsNearWindow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := `{"status":"answered","claims":[{"text":"История JSON записывается во временный файл.","evidence":[{"ref":"S1","start":1,"end":1}]}],"clarification":""}`
		json.NewEncoder(w).Encode(ollama.ChatChunk{Message: ollama.Message{Content: raw}})
		json.NewEncoder(w).Encode(ollama.ChatChunk{Done: true, PromptEvalCount: 3900, EvalCount: 20})
	}))
	defer upstream.Close()
	app := New(ollama.Client{BaseURL: upstream.URL}, Config{Model: "test", Index: ragFixture(t), RAG: rag.DefaultConfig(), Timeout: time.Second})
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"Как сохраняется история JSON?"}],"rag":true,"profile":"optimized-8b"}`)))
	events := decodeEvents(t, recorder.Body)
	for _, e := range events {
		if e.Type == "delta" || e.Type == "sources" || e.Type == "done" {
			t.Fatal("possibly truncated context published an answer")
		}
	}
	if events[len(events)-1].Code != "context_budget" {
		t.Fatal(events)
	}
}
