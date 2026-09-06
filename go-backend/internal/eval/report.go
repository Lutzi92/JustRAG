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

// ReadJSONReport is the inverse of WriteJSONReport. Used by `cmd/eval
// --baseline` to load a previously written report for comparison, and by
// the scheduled-regression check to load the current/predecessor reports.
// A payload that decodes cleanly but carries zero questions (e.g. `{}`, or
// any other JSON object missing the "questions" field) is rejected: it is
// almost certainly not a report at all — a truncated file, a different JSON
// shape, an empty object — and letting it through would hand callers a
// zero-valued Report that silently passes as a legitimate (empty) baseline
// rather than erroring, e.g. `cmd/eval --baseline wrong.json` would exit 0
// against a zero-valued gate instead of failing loudly.
func ReadJSONReport(r io.Reader) (Report, error) {
	var rep Report
	if err := json.NewDecoder(r).Decode(&rep); err != nil {
		return Report{}, fmt.Errorf("read json report: %w", err)
	}
	if len(rep.Questions) == 0 {
		return Report{}, fmt.Errorf("read json report: report has no questions")
	}
	return rep, nil
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
			fmt.Fprintf(w, "  mean_faithfulness       = %.3f (n=%d)\n", *rep.Aggregate.MeanFaithfulness, rep.Aggregate.FaithfulnessN)
		}
		if rep.Aggregate.MeanAnswerRelevance != nil {
			fmt.Fprintf(w, "  mean_answer_relevance   = %.3f (n=%d)\n", *rep.Aggregate.MeanAnswerRelevance, rep.Aggregate.AnswerRelevanceN)
		}
		if rep.Aggregate.MeanContextPrecision != nil {
			fmt.Fprintf(w, "  mean_context_precision  = %.3f (n=%d)\n", *rep.Aggregate.MeanContextPrecision, rep.Aggregate.ContextPrecisionN)
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
	if len(rep.TurnKindAggregates) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Per turn kind:")
		kinds := make([]string, 0, len(rep.TurnKindAggregates))
		for k := range rep.TurnKindAggregates {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			a := rep.TurnKindAggregates[k]
			fmt.Fprintf(w, "  %-20s count=%-3d mean_recall=%.3f mean_precision=%.3f mrr=%.3f ndcg=%.3f\n",
				k, a.Count, a.MeanRecall, a.MeanPrecision, a.MRR, a.MeanNDCG)
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
	if rep.RoutingAccuracy != nil {
		ra := rep.RoutingAccuracy
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Routing accuracy (query-type classification vs golden, scored=%d):\n", ra.Scored)
		fmt.Fprintf(w, "  accuracy = %.3f (%d/%d)\n", ra.Accuracy, ra.Correct, ra.Scored)
		expected := make([]string, 0, len(ra.PerExpected))
		for e := range ra.PerExpected {
			expected = append(expected, e)
		}
		sort.Strings(expected)
		for _, e := range expected {
			b := ra.PerExpected[e]
			acc := 0.0
			if b.Scored > 0 {
				acc = float64(b.Correct) / float64(b.Scored)
			}
			fmt.Fprintf(w, "  %-20s %.3f (%d/%d)\n", e, acc, b.Correct, b.Scored)
		}
	}
	// explicitTabularEligibility: per Ruling R75, a golden set that carries
	// Question.TabularExpected on any question switches the fire_rate
	// denominator from the query_type fallback to the explicit flag (see
	// tabularEligibilityIsExplicit / TabularRouterRates). When that rule is
	// in effect and it yields zero eligible (tabular_expected=true)
	// questions, TabularRouterFireRate is nil — but that nil is a genuine,
	// reportable "0 tabular_expected questions" fact about the golden set,
	// not the ordinary "nothing to report" silence the legacy query_type
	// rule's nil should stay as. Print it as n/a instead of dropping the
	// line (and, if it's the only tabular signal available, the whole
	// section) silently.
	explicitTabularEligibility := tabularEligibilityIsExplicit(rep.Questions)
	if rep.TabularRouterFireRate != nil || rep.TabularSQLErrorRate != nil || explicitTabularEligibility {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Tabular router:")
		switch {
		case rep.TabularRouterFireRate != nil:
			eligibilityDesc := "of lookup/complex_reasoning questions"
			if explicitTabularEligibility {
				eligibilityDesc = "of tabular_expected questions"
			}
			fmt.Fprintf(w, "  fire_rate      = %.3f (%s)\n", *rep.TabularRouterFireRate, eligibilityDesc)
		case explicitTabularEligibility:
			fmt.Fprintln(w, "  fire_rate      = n/a (0 tabular_expected questions)")
		}
		if rep.TabularSQLErrorRate != nil {
			fmt.Fprintf(w, "  sql_error_rate = %.3f (of fired questions)\n", *rep.TabularSQLErrorRate)
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
