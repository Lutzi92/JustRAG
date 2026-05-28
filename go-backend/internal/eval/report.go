package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// WriteJSONReport writes rep as pretty-printed JSON to w.
func WriteJSONReport(w io.Writer, rep Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// WriteHumanSummary writes a one-screen summary of rep to w. Stable format
// suitable for CI log scraping as well as human eyeballing.
func WriteHumanSummary(w io.Writer, rep Report) error {
	_, err := fmt.Fprintf(w, `RAG retrieval evaluation report
  generated_at = %s
  golden_path  = %s
  k            = %d
  questions    = %d
  errors      = %d

Aggregate (k=%d, count=%d):
  mean_recall    = %.3f
  mean_precision = %.3f
  mrr            = %.3f
  mean_ndcg      = %.3f
  p50_recall     = %.3f
  p95_recall     = %.3f
`,
		rep.GeneratedAt.UTC().Format(time.RFC3339),
		rep.GoldenPath,
		rep.K,
		len(rep.Questions),
		rep.Errors,
		rep.K,
		rep.Aggregate.Count,
		rep.Aggregate.MeanRecall,
		rep.Aggregate.MeanPrecision,
		rep.Aggregate.MRR,
		rep.Aggregate.MeanNDCG,
		rep.Aggregate.P50Recall,
		rep.Aggregate.P95Recall,
	)
	if err != nil {
		return err
	}
	if rep.Aggregate.MeanFaithfulness != nil || rep.Aggregate.MeanAnswerRelevance != nil || rep.Aggregate.MeanContextPrecision != nil {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Judge (judged_count=%d):\n", rep.Aggregate.JudgedCount)
		if rep.Aggregate.MeanFaithfulness != nil {
			fmt.Fprintf(w, "  mean_faithfulness       = %.3f\n", *rep.Aggregate.MeanFaithfulness)
		}
		if rep.Aggregate.MeanAnswerRelevance != nil {
			fmt.Fprintf(w, "  mean_answer_relevance   = %.3f\n", *rep.Aggregate.MeanAnswerRelevance)
		}
		if rep.Aggregate.MeanContextPrecision != nil {
			fmt.Fprintf(w, "  mean_context_precision  = %.3f\n", *rep.Aggregate.MeanContextPrecision)
		}
	}
	if len(rep.RouteAggregates) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Per route:")
		// Sort route names for stable output.
		routes := make([]string, 0, len(rep.RouteAggregates))
		for r := range rep.RouteAggregates {
			routes = append(routes, r)
		}
		sort.Strings(routes)
		for _, r := range routes {
			label := r
			if label == "" {
				label = "unlabeled"
			}
			a := rep.RouteAggregates[r]
			fmt.Fprintf(w, "  %-20s count=%-3d mean_recall=%.3f mean_precision=%.3f mrr=%.3f ndcg=%.3f\n",
				label, a.Count, a.MeanRecall, a.MeanPrecision, a.MRR, a.MeanNDCG)
		}
	}
	if len(rep.OrchestratorAggregates) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Orchestrators:")
		names := make([]string, 0, len(rep.OrchestratorAggregates))
		for n := range rep.OrchestratorAggregates {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			label := n
			if label == "" {
				label = "unlabeled"
			}
			a := rep.OrchestratorAggregates[n]
			fmt.Fprintf(w, "  %-20s count=%-3d mean_recall=%.3f mean_precision=%.3f mrr=%.3f ndcg=%.3f\n",
				label, a.Count, a.MeanRecall, a.MeanPrecision, a.MRR, a.MeanNDCG)
		}
	}
	if rep.DepthBuckets != nil {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Depth buckets (k=%d, min_total_chunks=%d, eligible_questions=%d):\n",
			rep.DepthBuckets.K, rep.DepthBuckets.MinTotalChunks, rep.DepthBuckets.EligibleQuestions)
		for _, b := range rep.DepthBuckets.Buckets {
			relPct := 0.0
			if b.Total > 0 {
				relPct = float64(b.RelevantHits) / float64(b.Total) * 100
			}
			fmt.Fprintf(w, "  %-7s total=%-4d relevant=%-4d non_relevant=%-4d (%5.1f%% relevant)\n",
				b.Bucket, b.Total, b.RelevantHits, b.NonRelevantHits, relPct)
		}
	}
	return nil
}
