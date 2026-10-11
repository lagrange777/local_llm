// Package rag searches imported document chunks without remote services.
package rag

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Chunk struct {
	ID       string `json:"chunk_id"`
	Source   string `json:"source"`
	Title    string `json:"title"`
	Section  string `json:"section"`
	Strategy string `json:"strategy"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Text     string `json:"text"`
	Hash     string `json:"content_hash"`
}
type Origin struct {
	Repository     string  `json:"repository"`
	Index          string  `json:"index"`
	IndexSHA256    string  `json:"index_sha256"`
	CorpusHash     string  `json:"corpus_hash"`
	CreatedAt      string  `json:"created_at"`
	EmbeddingModel string  `json:"embedding_model_in_source"`
	Pages          float64 `json:"estimated_pages_at_400_words"`
}
type Snapshot struct {
	Version int     `json:"version"`
	Origin  Origin  `json:"origin"`
	Chunks  []Chunk `json:"chunks"`
}
type Config struct {
	TopKBefore        int     `json:"top_k_before"`
	TopKAfter         int     `json:"top_k_after"`
	MinCoverage       float64 `json:"min_coverage"`
	ContextCharacters int     `json:"context_characters"`
}

func DefaultConfig() Config { return Config{10, 4, 0.22, 5000} }
func (c Config) Validate() error {
	if c.TopKBefore < 1 || c.TopKBefore > 50 || c.TopKAfter < 1 || c.TopKAfter > c.TopKBefore || math.IsNaN(c.MinCoverage) || c.MinCoverage < 0 || c.MinCoverage > 1 || c.ContextCharacters < 1800 || c.ContextCharacters > 7000 {
		return fmt.Errorf("invalid BM25 configuration")
	}
	return nil
}

type document struct {
	chunk       Chunk
	frequencies map[string]int
	length      int
}
type Index struct {
	Origin    Origin `json:"origin"`
	SHA256    string `json:"sha256"`
	Strategy  string `json:"strategy"`
	docs      []document
	df        map[string]int
	avgLength float64
}
type Match struct {
	Chunk
	Ref      string  `json:"ref,omitempty"`
	Score    float64 `json:"score"`
	Coverage float64 `json:"coverage"`
	Decision string  `json:"decision"`
}
type Retrieval struct {
	Query          string   `json:"query"`
	Strategy       string   `json:"strategy"`
	CorpusHash     string   `json:"corpus_hash"`
	IndexSHA256    string   `json:"index_sha256"`
	Config         Config   `json:"config"`
	Candidates     []Match  `json:"candidates"`
	Sources        []Match  `json:"sources"`
	DurationMS     float64  `json:"duration_ms"`
	MissingAnchors []string `json:"missing_anchors,omitempty"`
}

func Load(path, strategy string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("index exceeds 16 MiB")
	}
	var snapshot Snapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Version != 1 || snapshot.Origin.CorpusHash == "" || (strategy != "structured" && strategy != "fixed") {
		return nil, fmt.Errorf("unsupported index or strategy")
	}
	idx := &Index{Origin: snapshot.Origin, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Strategy: strategy, df: map[string]int{}}
	seen := map[string]bool{}
	for _, chunk := range snapshot.Chunks {
		if chunk.ID == "" || seen[chunk.ID] || chunk.Source == "" || chunk.Title == "" || chunk.Section == "" || !utf8.ValidString(chunk.Text) || strings.TrimSpace(chunk.Text) == "" || fmt.Sprintf("%x", sha256.Sum256([]byte(chunk.Text))) != chunk.Hash {
			return nil, fmt.Errorf("invalid chunk metadata or content hash: %s", chunk.ID)
		}
		seen[chunk.ID] = true
		if chunk.Strategy != strategy {
			continue
		}
		terms := Tokens(chunk.Title + "\n" + chunk.Section + "\n" + chunk.Text)
		frequencies := map[string]int{}
		for _, term := range terms {
			frequencies[term]++
		}
		for term := range frequencies {
			idx.df[term]++
		}
		idx.docs = append(idx.docs, document{chunk, frequencies, len(terms)})
		idx.avgLength += float64(len(terms))
	}
	if len(idx.docs) == 0 {
		return nil, fmt.Errorf("no chunks for strategy %s", strategy)
	}
	idx.avgLength /= float64(len(idx.docs))
	return idx, nil
}
func (i *Index) Count() int { return len(i.docs) }
func (i *Index) idf(term string) float64 {
	df := float64(i.df[term])
	return math.Log(1 + (float64(len(i.docs))-df+0.5)/(df+0.5))
}
func (i *Index) Search(query string, cfg Config) (Retrieval, error) {
	started := time.Now()
	r := Retrieval{Query: query, Strategy: i.Strategy, CorpusHash: i.Origin.CorpusHash, IndexSHA256: i.SHA256, Config: cfg, Candidates: []Match{}, Sources: []Match{}}
	if err := cfg.Validate(); err != nil {
		return r, err
	}
	if strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 4000 {
		return r, fmt.Errorf("invalid retrieval question")
	}
	terms := map[string]bool{}
	totalWeight := 0.0
	for _, term := range Tokens(query) {
		if !terms[term] {
			terms[term] = true
			totalWeight += i.idf(term)
		}
	}
	// An absent explicit Latin name/identifier is a useful reason to abstain,
	// not evidence that the fact is absent from every possible document.
	for _, word := range words(query) {
		explicit := strings.Contains(word, "_")
		for _, ch := range word {
			if ch >= 'A' && ch <= 'Z' {
				explicit = true
			}
		}
		tokens := Tokens(word)
		if explicit && len(tokens) > 0 && i.df[tokens[0]] == 0 {
			r.MissingAnchors = append(r.MissingAnchors, word)
		}
	}
	orderedTerms := make([]string, 0, len(terms))
	for term := range terms {
		orderedTerms = append(orderedTerms, term)
	}
	sort.Strings(orderedTerms)
	if totalWeight > 0 {
		for _, d := range i.docs {
			score, covered := 0.0, 0.0
			hits := 0
			for _, term := range orderedTerms {
				tf := float64(d.frequencies[term])
				if tf == 0 {
					continue
				}
				hits++
				idf := i.idf(term)
				covered += idf
				score += idf * (tf * 2.2) / (tf + 1.2*(0.25+0.75*float64(d.length)/i.avgLength))
			}
			if score > 0 {
				decision := "selected"
				if covered/totalWeight < cfg.MinCoverage || (len(terms) > 1 && hits < 2) {
					decision = "below_threshold"
				}
				if len(r.MissingAnchors) > 0 {
					decision = "missing_anchor"
				}
				r.Candidates = append(r.Candidates, Match{Chunk: d.chunk, Score: score, Coverage: covered / totalWeight, Decision: decision})
			}
		}
	}
	sort.Slice(r.Candidates, func(a, b int) bool {
		if r.Candidates[a].Score != r.Candidates[b].Score {
			return r.Candidates[a].Score > r.Candidates[b].Score
		}
		return r.Candidates[a].ID < r.Candidates[b].ID
	})
	if len(r.Candidates) > cfg.TopKBefore {
		r.Candidates = r.Candidates[:cfg.TopKBefore]
	}
	budget := cfg.ContextCharacters
	for n := range r.Candidates {
		candidate := &r.Candidates[n]
		if candidate.Decision != "selected" {
			continue
		}
		if len(r.Sources) >= cfg.TopKAfter {
			candidate.Decision = "top_k"
			continue
		}
		// Keep complete chunks so line references and exact quotations stay valid.
		size := utf8.RuneCountInString(candidate.Text)
		if size > budget {
			candidate.Decision = "context_budget"
			continue
		}
		candidate.Ref = fmt.Sprintf("S%d", len(r.Sources)+1)
		budget -= size
		r.Sources = append(r.Sources, *candidate)
	}
	r.DurationMS = float64(time.Since(started).Microseconds()) / 1000
	return r, nil
}
func words(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields("а без бы был была были в во все для до его ее если есть же за и из или к как какая какой какие когда ли на не но о об от по при с со так то у что это через нужно the a an is are was were how what why which of for to in on and or does do can") {
		m[w] = true
	}
	return m
}()

// Light suffix normalization, not a full morphological analyzer. Identifiers
// and numbers remain intact. The same analyzer is used for documents and queries.
func Tokens(text string) []string {
	var out []string
	for _, word := range words(text) {
		word = strings.ReplaceAll(strings.ToLower(word), "ё", "е")
		if stopwords[word] {
			continue
		}
		russian := true
		for _, r := range word {
			if r < 'а' || r > 'я' {
				russian = false
				break
			}
		}
		if russian && utf8.RuneCountInString(word) > 4 {
			for _, suffix := range []string{"иями", "ями", "иями", "ение", "ения", "ений", "ются", "ется", "ого", "ему", "ому", "ыми", "ими", "ами", "ией", "ией", "ые", "ие", "ой", "ый", "ий", "ая", "яя", "ое", "ее", "ую", "юю", "ов", "ев", "ам", "ям", "ах", "ях", "ет", "ют", "ит", "ии", "ия", "ий", "ы", "и", "а", "я", "у", "ю", "е", "о", "ь"} {
				if strings.HasSuffix(word, suffix) && utf8.RuneCountInString(word)-utf8.RuneCountInString(suffix) >= 3 {
					word = strings.TrimSuffix(word, suffix)
					break
				}
			}
		}
		if word != "" {
			out = append(out, word)
		}
	}
	return out
}
