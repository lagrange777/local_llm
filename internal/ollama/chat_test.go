package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChatStreamingAndExplicitModes(t *testing.T) {
	for _, think := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[think], func(t *testing.T) {
			allowFinal := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request ChatRequest
				if r.URL.Path != "/api/chat" || r.Method != "POST" {
					t.Error("wrong chat endpoint")
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Think != think || !request.Stream || len(request.Messages) != 1 || request.Messages[0].Content != "2+2" {
					t.Errorf("unexpected request: %+v", request)
				}
				encoder := json.NewEncoder(w)
				if think {
					encoder.Encode(ChatChunk{Message: Message{Thinking: "reasoning"}})
				}
				encoder.Encode(ChatChunk{Message: Message{Content: "4"}})
				w.(http.Flusher).Flush()
				select {
				case <-allowFinal:
				case <-r.Context().Done():
					return
				}
				encoder.Encode(ChatChunk{Done: true, DoneReason: "stop", EvalCount: 2})
			}))
			defer server.Close()
			var answer, thought strings.Builder
			gotDelta := false
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := (Client{BaseURL: server.URL}).Chat(ctx, ChatRequest{Think: think, Messages: []Message{{Role: "user", Content: "2+2"}}}, func(chunk ChatChunk) error {
				answer.WriteString(chunk.Message.Content)
				thought.WriteString(chunk.Message.Thinking)
				if chunk.Message.Content != "" {
					gotDelta = true
					close(allowFinal)
				}
				if chunk.Done && !gotDelta {
					t.Error("final arrived before streaming delta")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if answer.String() != "4" || (thought.Len() > 0) != think {
				t.Errorf("answer/thinking mixed: %s / %s", answer.String(), thought.String())
			}
		})
	}
}
func TestChatFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"missing model", 404, `{"error":"model not found"}`},
		{"malformed stream", 200, "oops\n"},
		{"early eof", 200, `{"message":{"content":"partial"},"done":false}` + "\n"},
		{"empty", 200, `{"done":true,"done_reason":"stop"}` + "\n"},
		{"token limit", 200, `{"message":{"content":"partial"},"done":true,"done_reason":"length"}` + "\n"},
		{"stream error", 200, `{"error":"failed to load"}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer server.Close()
			err := (Client{BaseURL: server.URL}).Chat(context.Background(), ChatRequest{}, func(ChatChunk) error { return nil })
			if err == nil {
				t.Fatal("expected failure")
			}
			if tc.name == "token limit" && !errors.Is(err, ErrTruncated) {
				t.Fatalf("wrong token-limit error: %v", err)
			}
			if tc.status == 404 {
				var api *APIError
				if !errors.As(err, &api) || api.Status != 404 {
					t.Fatalf("wrong API error: %v", err)
				}
			}
		})
	}
}
func TestChatCancellationReachesUpstream(t *testing.T) {
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"message":{"content":"first"},"done":false}` + "\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := (Client{BaseURL: server.URL}).Chat(ctx, ChatRequest{}, func(ChatChunk) error { cancel(); return nil })
	if err == nil {
		t.Fatal("expected cancellation")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not cancel")
	}
}
