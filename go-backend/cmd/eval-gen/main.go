// Command eval-gen synthesizes a draft golden set from a KB's corpus using
// LLM-generated questions. Unlike eval-genset (which maps an existing XLSX to
// current file UUIDs), eval-gen produces fresh questions directly from the
// chunks stored in the KB — useful for new KBs that have no pre-existing
// golden set.
//
// Usage:
//
//	eval-gen --kb-id <kb-uuid> [--lang de|en] [--lookup N] [--complex N] \
//	         [--enumeration N] [--multihop N] [--model <model>] [--out <path>]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/eval/gen"
	"github.com/justrag/go-backend/internal/vector"
)

func main() {
	kbID := flag.String("kb-id", "", "KB id to generate from (required)")
	out := flag.String("out", "", "output JSONL path (default eval/golden/generated-<kbShort>.jsonl)")
	lang := flag.String("lang", "de", "language code: de|en")
	lookup := flag.Int("lookup", 20, "lookup questions")
	complexN := flag.Int("complex", 10, "complex single-file questions")
	enumeration := flag.Int("enumeration", 5, "enumeration questions")
	multihop := flag.Int("multihop", 5, "multi-hop questions")
	maxFiles := flag.Int("max-files", 0, "cap files processed per mode (0 = all)")
	model := flag.String("model", "", "generation model override (default fast-tier)")
	flag.Parse()

	if *kbID == "" {
		fmt.Fprintln(os.Stderr, "error: --kb-id is required")
		os.Exit(2)
	}
	if *lang != "de" && *lang != "en" {
		fmt.Fprintln(os.Stderr, "error: --lang must be de or en")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fatal("config load", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := database.Connect(ctx, cfg.DB, cfg.VectorDB)
	if err != nil {
		fatal("database connect", err)
	}
	defer db.Main.Close()
	if db.Vector != db.Main {
		defer db.Vector.Close()
	}

	aiResolver := ai.NewConfigResolver(ai.NewStore(db.Main))
	chatStore := chat.NewStore(db.Main)

	searchSvc := vector.NewSearchService(db.Vector, db.Main, aiResolver,
		vector.WithSiteConfigReader(chatStore),
	)

	chunkSvc := vector.NewChunkService(db.Vector)

	g := gen.New(
		gen.NewDBCorpus(db.Main, chunkSvc),
		gen.NewAIQGen(aiResolver, *kbID, *lang, *model),
		gen.NewSearchNeighbors(searchSvc, 5),
		0.5,
	)
	questions, stats, err := g.GenerateAll(ctx, *kbID, *lang, gen.Counts{
		Lookup: *lookup, Complex: *complexN, Enumeration: *enumeration, MultiHop: *multihop,
		MaxFiles: *maxFiles,
	})
	if err != nil {
		fatal("generate", err)
	}
	if len(questions) == 0 {
		fatal("generate", fmt.Errorf("no questions produced (empty KB or all LLM calls failed)"))
	}

	outPath := *out
	if outPath == "" {
		short := *kbID
		if len(short) > 8 {
			short = short[:8]
		}
		outPath = "eval/golden/generated-" + short + ".jsonl"
	}
	f, err := os.Create(outPath)
	if err != nil {
		fatal("create output", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, q := range questions {
		if err := enc.Encode(&q); err != nil {
			fatal("encode", err)
		}
	}

	raw, _ := os.ReadFile(outPath)
	if _, err := eval.ParseGoldenSetJSONL(strings.NewReader(string(raw))); err != nil {
		fatal("output failed validation", err)
	}

	fmt.Printf("wrote %d questions to %s (lookup=%d complex=%d enumeration=%d multihop=%d)\n",
		len(questions), outPath, stats.Lookup, stats.Complex, stats.Enumeration, stats.MultiHop)
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "eval-gen: %s: %v\n", msg, err)
	os.Exit(1)
}
