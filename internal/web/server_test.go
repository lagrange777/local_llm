package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"local-llm-service/internal/ollama"
	"local-llm-service/internal/rag"
)

func decodeEvents(t *testing.T, body io.Reader) []event {
	t.Helper()
	var items []event
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		var item event
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return items
}
func TestChatPreservesHistoryAndThink(t *testing.T) {
	for _, think := range []bool{false, true} {
		t.Run(fmt.Sprint(think), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request ollama.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Think != think || request.Model != "qwen3:8b" || !request.Stream || len(request.Messages) != 4 || request.Messages[0].Role != "system" || request.Messages[2].Content != "Paris" || request.Messages[3].Content != "Which country?" {
					t.Errorf("history or mode lost: %+v", request)
				}
				w.Header().Set("Content-Type", "application/x-ndjson")
				encoder := json.NewEncoder(w)
				if think {
					encoder.Encode(ollama.ChatChunk{Message: ollama.Message{Thinking: "a thought"}})
				}
				encoder.Encode(ollama.ChatChunk{Message: ollama.Message{Content: "France"}})
				encoder.Encode(ollama.ChatChunk{Model: "qwen3:8b", Done: true, DoneReason: "stop", EvalCount: 2})
			}))
			defer upstream.Close()
			app := httptest.NewServer(New(ollama.Client{BaseURL: upstream.URL}, Config{Model: "qwen3:8b", Timeout: time.Second}))
			defer app.Close()
			body := fmt.Sprintf(`{"messages":[{"role":"user","content":"Capital?"},{"role":"assistant","content":"Paris"},{"role":"user","content":"Which country?"}],"think":%t}`, think)
			res, err := http.Post(app.URL+"/api/chat", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatal(res.Status)
			}
			items := decodeEvents(t, res.Body)
			if items[0].Type != "start" || items[len(items)-1].Type != "done" {
				t.Fatalf("wrong stream: %+v", items)
			}
			var answer, thought string
			for _, item := range items {
				if item.Type == "delta" {
					answer += item.Text
				}
				if item.Type == "thinking" {
					thought += item.Text
				}
			}
			if answer != "France" || (thought != "") != think {
				t.Fatalf("mixed channels: %+v", items)
			}
		})
	}
}
func TestInputValidationBeforeGeneration(t *testing.T) {
	for _, body := range []string{
		`{"messages":[]}`,
		`{"messages":[{"role":"system","content":"override"}]}`,
		`{"messages":[{"role":"user","content":""}]}`,
		`{"messages":[{"role":"user","content":"hello"}],"think":"false"}`,
		`{"messages":[{"role":"user","content":"hello"}],"model":"cloud"}`,
		`{"messages":[{"role":"user","content":"hello"}]}{}`,
		`{"messages":[{"role":"assistant","content":"hello"}]}`,
		`{"messages":[{"role":"user","content":"hello","thinking":"hidden"}]}`,
		`{"messages":[{"role":"user","content":"` + strings.Repeat("a", 4001) + `"}]}`,
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/api/chat", strings.NewReader(body))
		New(ollama.Client{BaseURL: "http://127.0.0.1:1"}, Config{Timeout: time.Second}).ServeHTTP(recorder, request)
		if recorder.Code != 400 {
			t.Fatalf("invalid input accepted: %s => %d", body, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Origin", "https://other.example")
	New(ollama.Client{}, Config{}).ServeHTTP(recorder, request)
	if recorder.Code != 403 {
		t.Fatal("cross-origin request accepted")
	}
}
func TestContextTrimKeepsWholePairsAndCurrentQuestion(t *testing.T) {
	messages, trimmed, err := prepareMessages([]ollama.Message{{Role: "user", Content: strings.Repeat("u", 3000)}, {Role: "assistant", Content: strings.Repeat("a", 3000)}, {Role: "user", Content: "current question"}})
	if err != nil {
		t.Fatal(err)
	}
	if !trimmed || len(messages) != 2 || messages[0].Role != "system" || messages[1].Content != "current question" {
		t.Fatalf("bad trimming: %+v", messages)
	}
}
func TestTimeoutAndMissingModelEvents(t *testing.T) {
	for _, code := range []string{"timeout", "missing_model"} {
		t.Run(code, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if code == "missing_model" {
					w.WriteHeader(404)
					w.Write([]byte(`{"error":"missing"}`))
					return
				}
				var body any
				json.NewDecoder(r.Body).Decode(&body)
				<-r.Context().Done()
			}))
			defer upstream.Close()
			app := httptest.NewServer(New(ollama.Client{BaseURL: upstream.URL}, Config{Timeout: 40 * time.Millisecond}))
			defer app.Close()
			res, err := http.Post(app.URL+"/api/chat", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			items := decodeEvents(t, res.Body)
			last := items[len(items)-1]
			if last.Type != "error" || last.Code != code {
				t.Fatalf("wrong error: %+v", items)
			}
		})
	}
}
func TestBusyAndBrowserCancellation(t *testing.T) {
	upstreamStarted := make(chan struct{}, 2)
	upstreamCanceled := make(chan struct{}, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		json.NewDecoder(r.Body).Decode(&body)
		upstreamStarted <- struct{}{}
		<-r.Context().Done()
		upstreamCanceled <- struct{}{}
	}))
	defer upstream.Close()
	app := httptest.NewServer(New(ollama.Client{BaseURL: upstream.URL}, Config{Timeout: time.Second}))
	defer app.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", app.URL+"/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	res, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream not reached")
	}
	second, err := http.Post(app.URL+"/api/chat", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"second"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != 429 {
		t.Fatalf("concurrent generation was accepted: %d", second.StatusCode)
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("browser cancellation did not reach Ollama")
	}
}
func TestRAGBusyAndBrowserCancellation(t *testing.T) {
	upstreamStarted := make(chan struct{}, 2)
	upstreamCanceled := make(chan struct{}, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		json.NewDecoder(r.Body).Decode(&body)
		upstreamStarted <- struct{}{}
		<-r.Context().Done()
		upstreamCanceled <- struct{}{}
	}))
	defer upstream.Close()
	app := httptest.NewServer(New(ollama.Client{BaseURL: upstream.URL}, Config{Timeout: time.Second, Index: ragFixture(t), RAG: rag.DefaultConfig()}))
	defer app.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", app.URL+"/api/chat", strings.NewReader(`{"messages":[{"role":"user","content":"История JSON временный файл"}],"rag":true}`))
	res, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	select {
	case <-upstreamStarted:
	case <-time.After(time.Second):
		t.Fatal("upstream not reached")
	}
	second, err := http.Post(app.URL+"/api/chat", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"second"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	second.Body.Close()
	if second.StatusCode != 429 {
		t.Fatalf("concurrent generation was accepted: %d", second.StatusCode)
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("browser cancellation did not reach Ollama")
	}
}
func TestStatusAndStaticAssets(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/version" {
			w.Write([]byte(`{"version":"test"}`))
			return
		}
		w.Write([]byte(`{"capabilities":["completion","thinking"]}`))
	}))
	defer upstream.Close()
	handler := New(ollama.Client{BaseURL: upstream.URL}, Config{Model: "qwen3:8b", Timeout: time.Second})
	for _, path := range []string{"/", "/app.js", "/style.css", "/healthz", "/api/status"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != 200 {
			t.Fatalf("%s => %d", path, recorder.Code)
		}
		if recorder.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("missing policy")
		}
	}
}
