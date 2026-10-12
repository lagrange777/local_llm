package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var ErrTruncated = errors.New("model reached output token limit")

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("Ollama HTTP %d: %s", e.Status, e.Message) }

type Message struct {
	Role     string `json:"role"`
	Content  string `json:"content"`
	Thinking string `json:"thinking,omitempty"`
}
type ChatRequest struct {
	Model     string          `json:"model"`
	Messages  []Message       `json:"messages"`
	Stream    bool            `json:"stream"`
	Format    json.RawMessage `json:"format,omitempty"`
	Think     bool            `json:"think"`
	KeepAlive string          `json:"keep_alive"`
	Options   Options         `json:"options"`
}
type ChatChunk struct {
	Model           string  `json:"model"`
	Message         Message `json:"message"`
	Done            bool    `json:"done"`
	DoneReason      string  `json:"done_reason,omitempty"`
	EvalCount       int     `json:"eval_count,omitempty"`
	LoadDuration    int64   `json:"load_duration,omitempty"`
	PromptEvalCount int     `json:"prompt_eval_count,omitempty"`
	EvalDuration    int64   `json:"eval_duration,omitempty"`
	TotalDuration   int64   `json:"total_duration,omitempty"`
	Error           string  `json:"error,omitempty"`
}

// Chat forwards chunks as they arrive. Canceling ctx closes the upstream request.
func (c Client) Chat(ctx context.Context, request ChatRequest, consume func(ChatChunk) error) error {
	request.Stream = true
	res, err := c.request(ctx, http.MethodPost, "/api/chat", request)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(io.LimitReader(res.Body, 8<<20))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	hasAnswer := false
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var chunk ChatChunk
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			return fmt.Errorf("decode Ollama stream: %w", err)
		}
		if chunk.Error != "" {
			return errors.New(chunk.Error)
		}
		hasAnswer = hasAnswer || strings.TrimSpace(chunk.Message.Content) != ""
		if chunk.Done && chunk.DoneReason == "length" {
			return ErrTruncated
		}
		if chunk.Done && !hasAnswer {
			return errors.New("Ollama returned an empty answer")
		}
		if err := consume(chunk); err != nil {
			return err
		}
		if chunk.Done {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Ollama stream: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("Ollama stream ended before done")
}

type ModelStatus struct{ Version string }

func (c Client) Status(ctx context.Context, model string) (ModelStatus, error) {
	var status ModelStatus
	res, err := c.request(ctx, http.MethodGet, "/api/version", nil)
	if err != nil {
		return status, err
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&struct {
		Version *string `json:"version"`
	}{&status.Version})
	res.Body.Close()
	if err != nil {
		return status, err
	}
	res, err = c.request(ctx, http.MethodPost, "/api/show", map[string]string{"model": model})
	if err != nil {
		return status, err
	}
	defer res.Body.Close()
	var data struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&data); err != nil {
		return status, err
	}
	for _, capability := range data.Capabilities {
		if capability == "completion" {
			return status, nil
		}
	}
	return status, errors.New("selected model cannot generate answers")
}
func (c Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return nil, &APIError{Status: res.StatusCode, Message: strings.TrimSpace(string(data))}
	}
	return res, nil
}
