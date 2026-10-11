package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExplicitThinkingModes(t *testing.T) {
	for _, think := range []bool{false, true} {
		t.Run(fmt.Sprint(think), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if string(body["think"]) != fmt.Sprint(think) {
					t.Errorf("think must be explicit %t, got %s", think, body["think"])
				}
				w.Header().Set("Content-Type", "application/json")
				response := Response{Model: "test", Response: "4", Done: true, DoneReason: "stop", EvalCount: 2}
				if think {
					response.Thinking = "test reasoning"
				}
				json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			response, err := (Client{BaseURL: server.URL}).Generate(context.Background(), Request{Model: "test", Prompt: "2+2", Think: think})
			if err != nil {
				t.Fatal(err)
			}
			if (strings.TrimSpace(response.Thinking) != "") != think {
				t.Fatalf("reasoning must be preserved independently of the answer: %+v", response)
			}
			if response.Response != "4" {
				t.Fatalf("unexpected answer: %s", response.Response)
			}
		})
	}
}
