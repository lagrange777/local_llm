package web

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"local-llm-service/internal/ollama"
	"local-llm-service/internal/rag"
)

func ragFixture(t *testing.T) *rag.Index {
	t.Helper()
	text := "История JSON записывается во временный файл.\nПотом выполняется атомарная замена."
	chunk := rag.Chunk{ID: "original-chunk", Source: "history.md", Title: "История", Section: "Запись JSON", Strategy: "structured", Text: text, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(text)))}
	data, _ := json.Marshal(rag.Snapshot{Version: 1, Origin: rag.Origin{CorpusHash: "fixture"}, Chunks: []rag.Chunk{chunk}})
	path := filepath.Join(t.TempDir(), "chunks.json")
	os.WriteFile(path, data, 0600)
	idx, err := rag.Load(path, "structured")
	if err != nil {
		t.Fatal(err)
	}
	return idx
}
func TestRAGRepairBeforePublishingAnswer(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req ollama.ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Format) == 0 || !strings.Contains(req.Messages[0].Content, "original-chunk") {
			t.Error("missing schema or context")
		}
		raw := `{"status":"answered","claims":[{"text":"История сохраняется атомарно через временный файл.","evidence":[{"ref":"S1","start":1,"end":2}]}],"clarification":""}`
		if calls == 1 {
			raw = strings.Replace(raw, "S1", "FORGED", 1)
		}
		json.NewEncoder(w).Encode(ollama.ChatChunk{Message: ollama.Message{Content: raw}})
		json.NewEncoder(w).Encode(ollama.ChatChunk{Done: true, EvalCount: 20})
	}))
	defer upstream.Close()
	app := New(ollama.Client{BaseURL: upstream.URL}, Config{Model: "test", Timeout: time.Second, Index: ragFixture(t), RAG: rag.DefaultConfig()})
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"Как сохраняется история JSON через временный файл?"}],"rag":true}`)))
	if recorder.Code != 200 {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
	events := decodeEvents(t, recorder.Body)
	if calls != 2 {
		t.Fatal("repair not attempted", calls)
	}
	if strings.Contains(recorder.Body.String(), "FORGED") {
		t.Fatal("invalid answer leaked")
	}
	var citation bool
	for _, e := range events {
		if e.Answer != nil && len(e.Answer.Citations) > 0 {
			citation = e.Answer.Citations[0].Quote == "История JSON записывается во временный файл.\nПотом выполняется атомарная замена."
		}
	}
	last := events[len(events)-1]
	if !citation || last.Type != "done" || last.Attempts != 2 || last.Tokens != 40 {
		t.Fatal(events)
	}
}
func TestRAGEmptyContextSkipsModelAndUnavailableIndex(t *testing.T) {
	app := New(ollama.Client{BaseURL: "http://127.0.0.1:1"}, Config{Model: "test", Timeout: time.Second, Index: ragFixture(t), RAG: rag.DefaultConfig()})
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"Как настроен Redis для JSON истории?"}],"rag":true}`)))
	events := decodeEvents(t, recorder.Body)
	last := events[len(events)-1]
	if last.Type != "done" || last.Status != "unknown" || last.Attempts != 0 || last.Tokens != 0 {
		t.Fatal("empty context invoked model", events)
	}
	for _, e := range events {
		if e.Answer != nil && len(e.Answer.Citations) > 0 {
			t.Fatal("invented evidence")
		}
	}
	recorder = httptest.NewRecorder()
	New(ollama.Client{}, Config{}).ServeHTTP(recorder, httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}],"rag":true}`)))
	if recorder.Code != 503 {
		t.Fatal("missing index accepted")
	}
}
func TestRAGRejectsUnrepairableEvidence(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollama.ChatChunk{Message: ollama.Message{Content: `{"status":"answered","claims":[{"text":"UNVERIFIED","evidence":[]}],"clarification":""}`}})
		json.NewEncoder(w).Encode(ollama.ChatChunk{Done: true})
	}))
	defer upstream.Close()
	app := New(ollama.Client{BaseURL: upstream.URL}, Config{Model: "test", Timeout: time.Second, Index: ragFixture(t), RAG: rag.DefaultConfig()})
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"История JSON временный файл"}],"rag":true}`)))
	events := decodeEvents(t, recorder.Body)
	for _, e := range events {
		if e.Type == "delta" || e.Type == "done" {
			t.Fatal("unverified answer published")
		}
	}
	if events[len(events)-1].Code != "invalid_evidence" {
		t.Fatal(events)
	}
}

func TestRAGContextBudgetReportsActionableError(t *testing.T) {
	var chunks []rag.Chunk
	for n := 0; n < 3; n++ {
		text := "История JSON сохраняется.\n" + strings.Repeat("данные ", 200)
		chunks = append(chunks, rag.Chunk{ID: fmt.Sprint(n), Source: "history.md", Title: "История", Section: "Запись", Strategy: "structured", Text: text, Hash: fmt.Sprintf("%x", sha256.Sum256([]byte(text)))})
	}
	data, _ := json.Marshal(rag.Snapshot{Version: 1, Origin: rag.Origin{CorpusHash: "fixture"}, Chunks: chunks})
	path := filepath.Join(t.TempDir(), "chunks.json")
	os.WriteFile(path, data, 0600)
	idx, err := rag.Load(path, "structured")
	if err != nil {
		t.Fatal(err)
	}
	app := New(ollama.Client{BaseURL: "http://127.0.0.1:1"}, Config{Model: "test", Timeout: time.Second, Index: idx, RAG: rag.DefaultConfig()})
	body, _ := json.Marshal(chatInput{Messages: []ollama.Message{{Role: "user", Content: strings.Repeat("История JSON ", 300)}}, RAG: true})
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/chat", strings.NewReader(string(body))))
	events := decodeEvents(t, recorder.Body)
	if events[len(events)-1].Code != "context_budget" {
		t.Fatal(events[len(events)-1])
	}
}
