package eval

import (
	"math"
	"sort"
)

// Recall returns the fraction of truth file ids that appear anywhere in retrieved.
// Empty truth yields 1.0 (nothing to miss). Duplicate retrieved entries count once.
func Recall(retrieved []string, truth map[string]struct{}) float64 {
	if len(truth) == 0 {
		return 1.0
	}
	found := 0
	seen := make(map[string]struct{}, len(truth))
	for _, f := range retrieved {
		if _, ok := truth[f]; !ok {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		found++
	}
	return float64(found) / float64(len(truth))
}

// Precision returns the fraction of retrieved entries whose file id is in truth.
// Precision is over the chunk list: duplicates each count toward the denominator.
func Precision(retrieved []string, truth map[string]struct{}) float64 {
	if len(retrieved) == 0 {
		return 0
	}
	hits := 0
	for _, f := range retrieved {
		if _, ok := truth[f]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(retrieved))
}

// ReciprocalRank returns 1/(1-indexed rank of first match); 0 when no match.
func ReciprocalRank(retrieved []string, truth map[string]struct{}) float64 {
	for i, f := range retrieved {
		if _, ok := truth[f]; ok {
			return 1.0 / float64(i+1)
		}
	}
	return 0
}

// NDCG returns normalized discounted cumulative gain with binary relevance.
// Gain_i = 1 if file in truth else 0; DCG_i = gain_i / log2(i+2).
// IDCG is the ideal ranking of the intersection placed at the top.
func NDCG(retrieved []string, truth map[string]struct{}) float64 {
	if len(retrieved) == 0 {
		return 0
	}
	dcg := 0.0
	relevantCount := 0
	seen := make(map[string]struct{}, len(truth))
	for i, f := range retrieved {
		if _, ok := truth[f]; !ok {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		relevantCount++
		dcg += 1.0 / math.Log2(float64(i)+2.0)
	}
	if relevantCount == 0 {
		return 0
	}
	idcg := 0.0
	for i := 0; i < relevantCount; i++ {
		idcg += 1.0 / math.Log2(float64(i)+2.0)
	}
	return dcg / idcg
}

// Aggregate computes mean + p50/p95 metrics across non-errored QuestionReport entries.
func Aggregate(reports []QuestionReport, k int) AggregateMetrics {
	var recalls, precisions, rrs, ndcgs []float64
	for _, r := range reports {
		if r.Error != "" {
			continue
		}
		recalls = append(recalls, r.Metrics.RecallAtK)
		precisions = append(precisions, r.Metrics.PrecisionAtK)
		rrs = append(rrs, r.Metrics.ReciprocalRank)
		ndcgs = append(ndcgs, r.Metrics.NDCGAtK)
	}
	agg := AggregateMetrics{K: k, Count: len(recalls)}
	if agg.Count == 0 {
		return agg
	}
	agg.MeanRecall = mean(recalls)
	agg.MeanPrecision = mean(precisions)
	agg.MRR = mean(rrs)
	agg.MeanNDCG = mean(ndcgs)
	agg.P50Recall = percentile(recalls, 50)
	agg.P95Recall = percentile(recalls, 95)

	var faiths, rels, precs []float64
	for _, r := range reports {
		if r.Error != "" {
			continue
		}
		if r.Judge == nil {
			continue
		}
		if r.Judge.Faithfulness != nil {
			faiths = append(faiths, *r.Judge.Faithfulness)
		}
		if r.Judge.AnswerRelevance != nil {
			rels = append(rels, *r.Judge.AnswerRelevance)
		}
		if r.Judge.ContextPrecision != nil {
			precs = append(precs, *r.Judge.ContextPrecision)
		}
		agg.JudgedCount++
	}
	if len(faiths) > 0 {
		m := mean(faiths)
		agg.MeanFaithfulness = &m
	}
	if len(rels) > 0 {
		m := mean(rels)
		agg.MeanAnswerRelevance = &m
	}
	if len(precs) > 0 {
		m := mean(precs)
		agg.MeanContextPrecision = &m
	}

	return agg
}

// AggregateByRoute groups reports by Question.QueryType and computes
// AggregateMetrics per route. Errored questions are skipped (same policy
// as Aggregate). Unlabeled questions (QueryType == "") group under the
// empty-string key, which callers can render as "unlabeled" when needed.
func AggregateByRoute(reports []QuestionReport, k int) map[string]AggregateMetrics {
	buckets := make(map[string][]QuestionReport)
	for _, r := range reports {
		buckets[r.Question.QueryType] = append(buckets[r.Question.QueryType], r)
	}
	out := make(map[string]AggregateMetrics, len(buckets))
	for route, bucket := range buckets {
		out[route] = Aggregate(bucket, k)
	}
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// percentile uses the nearest-rank method.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := make([]float64, len(xs))
	copy(cp, xs)
	sort.Float64s(cp)
	rank := int(math.Ceil(p/100.0*float64(len(cp)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(cp) {
		rank = len(cp) - 1
	}
	return cp[rank]
}
