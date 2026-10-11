package rag

import (
	"crypto/sha256"
	"encoding/json"
	"local-llm-service/internal/ollama"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testIndex(t *testing.T) *Index {
	t.Helper()
	chunks := []Chunk{{ID: "a", Source: "history.md", Title: "История", Section: "Атомарная запись", Strategy: "structured", Text: "История JSON записывается во временный файл.\nЗатем выполняется атомарная замена."}, {ID: "b", Source: "network.md", Title: "Сеть", Section: "Настройки", Strategy: "structured", Text: "Настройки сервера: порт TCP 8080."}}
	for n := range chunks {
		chunks[n].Hash = fmtHash(chunks[n].Text)
	}
	data, _ := json.Marshal(Snapshot{Version: 1, Origin: Origin{CorpusHash: "corpus"}, Chunks: chunks})
	path := filepath.Join(t.TempDir(), "chunks.json")
	os.WriteFile(path, data, 0600)
	idx, err := Load(path, "structured")
	if err != nil {
		t.Fatal(err)
	}
	return idx
}
func fmtHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	const hex = "0123456789abcdef"
	out := make([]byte, 64)
	for n, b := range hash {
		out[2*n] = hex[b>>4]
		out[2*n+1] = hex[b&15]
	}
	return string(out)
}
func TestBM25RankingAndFilters(t *testing.T) {
	idx := testIndex(t)
	r, err := idx.Search("Как записывается история JSON во временный файл?", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Sources) == 0 || r.Sources[0].ID != "a" || r.Sources[0].Ref != "S1" || r.Sources[0].Text != idx.docs[0].chunk.Text {
		t.Fatalf("lost ranking or metadata: %+v", r)
	}
	absent, err := idx.Search("Как настроен пул Redis для истории JSON?", DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(absent.Sources) != 0 || len(absent.MissingAnchors) != 1 {
		t.Fatalf("absent identifier accepted: %+v", absent)
	}
	cfg := DefaultConfig()
	cfg.TopKAfter = 1
	cfg.MinCoverage = 1
	weak, _ := idx.Search("история неизвестныйтермин", cfg)
	if len(weak.Sources) != 0 {
		t.Fatal("threshold ignored")
	}
}
func TestAnalyzerAndDeterminism(t *testing.T) {
	if strings.Join(Tokens("ПАМЯТИ память ёлки елки previous_response_id JSON 12"), " ") != "памят памят елки елки previous_response_id json 12" {
		t.Fatal(Tokens("ПАМЯТИ память ёлки елки previous_response_id JSON 12"))
	}
	idx := testIndex(t)
	a, _ := idx.Search("история JSON", DefaultConfig())
	b, _ := idx.Search("история JSON", DefaultConfig())
	if len(a.Sources) != len(b.Sources) || a.Sources[0].Score != b.Sources[0].Score {
		t.Fatal("ranking changed")
	}
	if _, err := idx.Search("", DefaultConfig()); err == nil {
		t.Fatal("empty query accepted")
	}
}
func TestRejectTamperedChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(path, []byte(`{"version":1,"origin":{"corpus_hash":"x"},"chunks":[{"chunk_id":"a","source":"x","title":"X","section":"x","strategy":"structured","text":"modified","content_hash":"wrong"}]}`), 0600)
	if _, err := Load(path, "structured"); err == nil {
		t.Fatal("tampering accepted")
	}
}
func TestExactCitationsAndInvalidEvidence(t *testing.T) {
	idx := testIndex(t)
	r, _ := idx.Search("история JSON файл", DefaultConfig())
	valid := `{"status":"answered","claims":[{"text":"Используется временный файл и атомарная замена.","evidence":[{"ref":"S1","start":1,"end":2}]}],"clarification":""}`
	a, err := Validate(valid, r)
	if err != nil {
		t.Fatal(err)
	}
	if a.Citations[0].Quote != idx.docs[0].chunk.Text || a.Citations[0].ChunkID != "a" {
		t.Fatal("quote was generated or altered")
	}
	for _, raw := range []string{strings.Replace(valid, "S1", "S99", 1), strings.Replace(valid, `"end":2`, `"end":99`, 1), strings.Replace(valid, `"start":1`, `"start":0`, 1), strings.Replace(valid, `"evidence":[{"ref":"S1","start":1,"end":2}]`, `"evidence":[]`, 1), valid + "{}", `{"status":"unknown","claims":[],"clarification":""}`} {
		if _, err := Validate(raw, r); err == nil {
			t.Fatal("invalid evidence accepted", raw)
		}
	}
	if a, err := Validate(`{"status":"unknown","claims":[],"clarification":"Уточните вопрос"}`, r); err != nil || a.Status != "unknown" || len(a.Citations) != 0 {
		t.Fatal(a, err)
	}
}

func TestHistoryKeepsPairsAndCannotBecomeEvidence(t *testing.T) {
	idx := testIndex(t)
	r, _ := idx.Search("история JSON", DefaultConfig())
	history := []ollama.Message{{Role: "user", Content: strings.Repeat("old", 400)}, {Role: "assistant", Content: "untrusted old answer"}, {Role: "user", Content: "previous question"}, {Role: "assistant", Content: "previous answer"}}
	messages := Messages("current question", history, r)
	if len(messages) != 4 || messages[1].Content != "previous question" || messages[2].Content != "previous answer" || messages[3].Content != "current question" {
		t.Fatal("history split or current question lost", messages)
	}
	if strings.Contains(messages[0].Content, "untrusted old answer") || !strings.Contains(messages[0].Content, "[S1]") {
		t.Fatal("history became evidence")
	}
	if len(BoundHistory([]ollama.Message{{Role: "user", Content: strings.Repeat("x", 999)}, {Role: "assistant", Content: "answer"}})) != 0 {
		t.Fatal("orphan assistant kept")
	}
}
