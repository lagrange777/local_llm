package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateHTTPContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/generate" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "test-model" || request.Prompt != "hello" || request.Stream || !request.Think {
			t.Errorf("unexpected body: %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"test-model","response":"hello!","done":true,"done_reason":"stop","eval_count":4}`))
	}))
	defer server.Close()
	got, err := (Client{BaseURL: server.URL + "/"}).Generate(context.Background(), Request{Model: "test-model", Prompt: "hello", Think: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Response != "hello!" || got.EvalCount != 4 {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestGenerateFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"missing model", 404, `{"error":"model not found"}`},
		{"invalid JSON", 200, "invalid"},
		{"empty answer", 200, `{"done":true,"response":""}`},
		{"unfinished", 200, `{"done":false,"response":"partial"}`},
		{"truncated", 200, `{"done":true,"response":"partial","done_reason":"length"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer server.Close()
			if _, err := (Client{BaseURL: server.URL}).Generate(context.Background(), Request{}); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestGenerateCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Client{BaseURL: "http://127.0.0.1:1"}).Generate(ctx, Request{})
	if err == nil {
		t.Fatal("expected cancellation")
	}
}
