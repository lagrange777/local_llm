package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"local-llm-service/internal/rag"
)

func main() {
	path := flag.String("index", "documents/chunks.json", "imported chunks")
	query := flag.String("query", "", "question")
	strategy := flag.String("strategy", "structured", "fixed or structured")
	questions := flag.String("questions", "", "JSON dataset")
	flag.Parse()
	idx, err := rag.Load(*path, *strategy)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg := rag.DefaultConfig()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if *questions != "" {
		data, err := os.ReadFile(*questions)
		if err != nil {
			panic(err)
		}
		var items []struct {
			ID    string `json:"id"`
			Query string `json:"query"`
		}
		if err := json.Unmarshal(data, &items); err != nil {
			panic(err)
		}
		for _, q := range items {
			r, err := idx.Search(q.Query, cfg)
			if err != nil {
				panic(err)
			}
			encoder.Encode(struct {
				ID        string        `json:"id"`
				Retrieval rag.Retrieval `json:"retrieval"`
			}{q.ID, r})
		}
		return
	}
	r, err := idx.Search(*query, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder.Encode(r)
}
