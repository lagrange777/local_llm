package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

type Request struct {
	Model     string  `json:"model"`
	Prompt    string  `json:"prompt"`
	Stream    bool    `json:"stream"`
	Think     bool    `json:"think"`
	KeepAlive string  `json:"keep_alive"`
	Options   Options `json:"options"`
}

type Options struct {
	Seed        int     `json:"seed,omitempty"`
	Temperature float64 `json:"temperature"`
	NumCtx      int     `json:"num_ctx"`
	NumPredict  int     `json:"num_predict"`
}

type Response struct {
	Thinking        string `json:"thinking,omitempty"`
	Model           string `json:"model"`
	CreatedAt       string `json:"created_at"`
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	TotalDuration   int64  `json:"total_duration"`
	LoadDuration    int64  `json:"load_duration"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	EvalDuration    int64  `json:"eval_duration"`
}

func (c Client) Generate(ctx context.Context, request Request) (Response, error) {
	var response Response
	body, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return response, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return response, fmt.Errorf("Ollama request: %w", err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return response, err
	}
	if res.StatusCode != http.StatusOK {
		return response, fmt.Errorf("Ollama HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return response, fmt.Errorf("decode Ollama: %w", err)
	}
	if !response.Done || strings.TrimSpace(response.Response) == "" {
		return response, fmt.Errorf("Ollama returned an incomplete or empty answer")
	}
	if response.DoneReason == "length" {
		return response, fmt.Errorf("Ollama answer reached the token limit")
	}
	return response, nil
}
