package rag

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"local-llm-service/internal/ollama"
)

type Evidence struct {
	Ref   string `json:"ref"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}
type Claim struct {
	Text     string     `json:"text"`
	Evidence []Evidence `json:"evidence"`
}
type Draft struct {
	Status        string  `json:"status"`
	Claims        []Claim `json:"claims"`
	Clarification string  `json:"clarification"`
}
type Citation struct {
	Ref     string `json:"ref"`
	ChunkID string `json:"chunk_id"`
	Source  string `json:"source"`
	Title   string `json:"title"`
	Section string `json:"section"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Quote   string `json:"quote"`
}
type Answer struct {
	Status    string     `json:"status"`
	Text      string     `json:"text"`
	Claims    []Claim    `json:"claims"`
	Citations []Citation `json:"citations"`
}

var ErrPromptTooLarge = errors.New("RAG prompt exceeds context budget")
var ErrInvalidEvidence = errors.New("citation validation failed")

const UnknownText = "Не знаю по доступным документам. Уточните вопрос или добавьте источник с нужной информацией."

var Format = json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["answered","unknown"]},"claims":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"},"evidence":{"type":"array","items":{"type":"object","properties":{"ref":{"type":"string"},"start":{"type":"integer"},"end":{"type":"integer"}},"required":["ref","start","end"],"additionalProperties":false}}},"required":["text","evidence"],"additionalProperties":false}},"clarification":{"type":"string"}},"required":["status","claims","clarification"],"additionalProperties":false}`)

func Unknown() Answer {
	return Answer{Status: "unknown", Text: UnknownText, Claims: []Claim{}, Citations: []Citation{}}
}

//go:embed prompts/*.txt
var prompts embed.FS

func PromptHash(style string) string {
	data, _ := prompts.ReadFile("prompts/" + style + ".txt")
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func Messages(query string, history []ollama.Message, r Retrieval) []ollama.Message {
	return MessagesFor(query, history, r, "baseline")
}
func MessagesFor(query string, history []ollama.Message, r Retrieval, style string) []ollama.Message {
	data, _ := prompts.ReadFile("prompts/" + style + ".txt")
	system := strings.TrimSpace(string(data))
	var context strings.Builder
	for _, s := range r.Sources {
		if style == "baseline" {
			fmt.Fprintf(&context, "\n[%s] source=%s; section=%s; chunk_id=%s\n", s.Ref, s.Source, s.Section, s.ID)
		} else {
			fmt.Fprintf(&context, "\n[%s] %s | %s\n", s.Ref, s.Source, s.Section)
		}
		for n, line := range strings.Split(s.Text, "\n") {
			fmt.Fprintf(&context, "%d: %s\n", n+1, line)
		}
	}
	msgs := []ollama.Message{{Role: "system", Content: system + "\nДОКУМЕНТЫ:\n" + context.String()}}
	msgs = append(msgs, BoundHistory(history)...)
	return append(msgs, ollama.Message{Role: "user", Content: query})
}
func Validate(raw string, r Retrieval) (Answer, error) {
	var d Draft
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return Answer{}, fmt.Errorf("invalid answer JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Answer{}, fmt.Errorf("extra answer JSON")
	}
	if d.Status == "unknown" {
		if len(d.Claims) != 0 || strings.TrimSpace(d.Clarification) == "" {
			return Answer{}, fmt.Errorf("unknown must have no claims and request clarification")
		}
		return Unknown(), nil
	}
	if d.Status != "answered" || len(d.Claims) < 1 || len(d.Claims) > 3 || d.Clarification != "" {
		return Answer{}, fmt.Errorf("answer must contain 1–3 supported claims")
	}
	byRef := map[string]Match{}
	for _, source := range r.Sources {
		byRef[source.Ref] = source
	}
	a := Answer{Status: d.Status, Claims: d.Claims, Citations: []Citation{}}
	used := map[string]bool{}
	var statements []string
	for _, claim := range d.Claims {
		if strings.TrimSpace(claim.Text) == "" || utf8.RuneCountInString(claim.Text) > 700 || len(claim.Evidence) < 1 || len(claim.Evidence) > 3 {
			return Answer{}, fmt.Errorf("claim requires short text and evidence")
		}
		var refs []string
		for _, e := range claim.Evidence {
			s, ok := byRef[e.Ref]
			lines := strings.Split(s.Text, "\n")
			if !ok || e.Start < 1 || e.End < e.Start || e.End > len(lines) || e.End-e.Start >= 4 {
				return Answer{}, fmt.Errorf("invalid source or line range")
			}
			quote := strings.Join(lines[e.Start-1:e.End], "\n")
			if strings.TrimSpace(quote) == "" || utf8.RuneCountInString(quote) > 1200 {
				return Answer{}, fmt.Errorf("invalid quotation")
			}
			key := fmt.Sprintf("%s:%d:%d", e.Ref, e.Start, e.End)
			if !used[key] {
				a.Citations = append(a.Citations, Citation{e.Ref, s.ID, s.Source, s.Title, s.Section, e.Start, e.End, quote})
				used[key] = true
			}
			refs = append(refs, "["+e.Ref+"]")
		}
		statements = append(statements, strings.TrimSpace(claim.Text)+" "+strings.Join(refs, " "))
	}
	a.Text = strings.Join(statements, "\n\n")
	return a, nil
}

// BoundHistory keeps complete pairs; retrieved sources remain the only evidence.
func BoundHistory(history []ollama.Message) []ollama.Message {
	count := 0
	start := len(history)
	for start > 0 {
		size := utf8.RuneCountInString(history[start-1].Content)
		if count+size > 1000 {
			break
		}
		count += size
		start--
	}
	// Avoid keeping an assistant message without its paired user turn.
	if start%2 == 1 {
		start++
	}
	return history[start:]
}
