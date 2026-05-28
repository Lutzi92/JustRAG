package eval

import (
	"math"
	"testing"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func truthSet(ids ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// --- Recall ---

func TestRecall_AllRelevantRetrieved(t *testing.T) {
	retrieved := []string{"f1", "f2"}
	truth := truthSet("f1", "f2")
	got := Recall(retrieved, truth)
	if !approxEqual(got, 1.0) {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestRecall_Partial(t *testing.T) {
	retrieved := []string{"f1", "fx"}
	truth := truthSet("f1", "f2")
	got := Recall(retrieved, truth)
	if !approxEqual(got, 0.5) {
		t.Errorf("expected 0.5, got %f", got)
	}
}

func TestRecall_NoneRetrieved(t *testing.T) {
	retrieved := []string{"fx", "fy"}
	truth := truthSet("f1", "f2")
	got := Recall(retrieved, truth)
	if !approxEqual(got, 0.0) {
		t.Errorf("expected 0.0, got %f", got)
	}
}

func TestRecall_EmptyRetrieved(t *testing.T) {
	retrieved := []string{}
	truth := truthSet("f1", "f2")
	got := Recall(retrieved, truth)
	if !approxEqual(got, 0.0) {
		t.Errorf("expected 0.0, got %f", got)
	}
}

func TestRecall_EmptyTruthReturnsOne(t *testing.T) {
	retrieved := []string{"f1"}
	truth := truthSet()
	got := Recall(retrieved, truth)
	if !approxEqual(got, 1.0) {
		t.Errorf("expected 1.0 (empty truth convention), got %f", got)
	}
}

// --- Precision ---

func TestPrecision_AllRelevant(t *testing.T) {
	retrieved := []string{"f1", "f2"}
	truth := truthSet("f1", "f2")
	got := Precision(retrieved, truth)
	if !approxEqual(got, 1.0) {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestPrecision_DuplicateFilesCountEachTime(t *testing.T) {
	// retrieved=[f1,f1,fx], truth={f1} => hits=2, len=3 => 2/3
	retrieved := []string{"f1", "f1", "fx"}
	truth := truthSet("f1")
	got := Precision(retrieved, truth)
	expected := 2.0 / 3.0
	if !approxEqual(got, expected) {
		t.Errorf("expected %f, got %f", expected, got)
	}
}

func TestPrecision_EmptyRetrieved(t *testing.T) {
	retrieved := []string{}
	truth := truthSet("f1")
	got := Precision(retrieved, truth)
	if !approxEqual(got, 0.0) {
		t.Errorf("expected 0.0, got %f", got)
	}
}

// --- ReciprocalRank ---

func TestReciprocalRank_FirstPosition(t *testing.T) {
	retrieved := []string{"f1", "fx"}
	truth := truthSet("f1")
	got := ReciprocalRank(retrieved, truth)
	if !approxEqual(got, 1.0) {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestReciprocalRank_SecondPosition(t *testing.T) {
	retrieved := []string{"fx", "f1"}
	truth := truthSet("f1")
	got := ReciprocalRank(retrieved, truth)
	if !approxEqual(got, 0.5) {
		t.Errorf("expected 0.5, got %f", got)
	}
}

func TestReciprocalRank_NoMatch(t *testing.T) {
	retrieved := []string{"fx", "fy"}
	truth := truthSet("f1")
	got := ReciprocalRank(retrieved, truth)
	if !approxEqual(got, 0.0) {
		t.Errorf("expected 0.0, got %f", got)
	}
}

// --- NDCG ---

func TestNDCG_PerfectRanking(t *testing.T) {
	retrieved := []string{"f1", "f2"}
	truth := truthSet("f1", "f2")
	got := NDCG(retrieved, truth)
	if !approxEqual(got, 1.0) {
		t.Errorf("expected 1.0, got %f", got)
	}
}

func TestNDCG_AllIrrelevant(t *testing.T) {
	retrieved := []string{"fx", "fy"}
	truth := truthSet("f1", "f2")
	got := NDCG(retrieved, truth)
	if !approxEqual(got, 0.0) {
		t.Errorf("expected 0.0, got %f", got)
	}
}

func TestNDCG_PartialWithRankingEffect(t *testing.T) {
	// retrieved=[fx,f1], truth={f1}
	// DCG = 0 + 1/log2(3)
	// IDCG = 1/log2(2) = 1.0
	// nDCG = (1/log2(3)) / 1.0 = 1/log2(3)
	retrieved := []string{"fx", "f1"}
	truth := truthSet("f1")
	got := NDCG(retrieved, truth)
	expected := 1.0 / math.Log2(3)
	if !approxEqual(got, expected) {
		t.Errorf("expected %f, got %f", expected, got)
	}
}

// --- Aggregate ---

func makeReport(recall, precision, rr, ndcg float64, errStr string) QuestionReport {
	return QuestionReport{
		Metrics: PerQuestionMetrics{
			RecallAtK:      recall,
			PrecisionAtK:   precision,
			ReciprocalRank: rr,
			NDCGAtK:        ndcg,
		},
		Error: errStr,
	}
}

func TestAggregate_ComputesPercentiles(t *testing.T) {
	// 3 normal + 1 errored; errored excluded from Count=3
	// recalls: [0.0, 0.5, 1.0] -> sorted [0.0, 0.5, 1.0]
	// P50 (nearest-rank): ceil(50/100*3)-1 = ceil(1.5)-1 = 2-1 = 1 -> 0.5
	reports := []QuestionReport{
		makeReport(0.0, 0.0, 0.0, 0.0, ""),
		makeReport(0.5, 0.5, 0.5, 0.5, ""),
		makeReport(1.0, 1.0, 1.0, 1.0, ""),
		makeReport(0.0, 0.0, 0.0, 0.0, "some error"),
	}
	agg := Aggregate(reports, 10)
	if agg.Count != 3 {
		t.Errorf("expected Count=3, got %d", agg.Count)
	}
	if !approxEqual(agg.MeanRecall, 0.5) {
		t.Errorf("expected MeanRecall=0.5, got %f", agg.MeanRecall)
	}
	if !approxEqual(agg.MeanPrecision, 0.5) {
		t.Errorf("expected MeanPrecision=0.5, got %f", agg.MeanPrecision)
	}
	if !approxEqual(agg.P50Recall, 0.5) {
		t.Errorf("expected P50Recall=0.5, got %f", agg.P50Recall)
	}
	if !approxEqual(agg.MRR, 0.5) {
		t.Errorf("expected MRR=0.5, got %f", agg.MRR)
	}
	if !approxEqual(agg.MeanNDCG, 0.5) {
		t.Errorf("expected MeanNDCG=0.5, got %f", agg.MeanNDCG)
	}
	if agg.K != 10 {
		t.Errorf("expected K=10, got %d", agg.K)
	}
}

func TestAggregate_ZeroNonErroredReturnsZeros(t *testing.T) {
	reports := []QuestionReport{
		makeReport(0.0, 0.0, 0.0, 0.0, "err1"),
		makeReport(0.0, 0.0, 0.0, 0.0, "err2"),
	}
	agg := Aggregate(reports, 5)
	if agg.Count != 0 {
		t.Errorf("expected Count=0, got %d", agg.Count)
	}
	if !approxEqual(agg.MeanRecall, 0.0) {
		t.Errorf("expected MeanRecall=0.0, got %f", agg.MeanRecall)
	}
}

func TestAggregate_IncludesJudgeMeansWhenPresent(t *testing.T) {
	f1, f2 := 1.0, 0.5
	r1, r2 := 0.75, 0.5
	p1 := 1.0
	reports := []QuestionReport{
		{
			Metrics: PerQuestionMetrics{K: 10, RecallAtK: 1.0},
			Judge:   &JudgeMetrics{Faithfulness: &f1, AnswerRelevance: &r1, ContextPrecision: &p1},
		},
		{
			Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.5},
			Judge:   &JudgeMetrics{Faithfulness: &f2, AnswerRelevance: &r2},
		},
	}
	agg := Aggregate(reports, 10)
	if agg.MeanFaithfulness == nil || !approxEqual(*agg.MeanFaithfulness, 0.75) {
		t.Errorf("expected mean faithfulness 0.75, got %+v", agg.MeanFaithfulness)
	}
	if agg.MeanAnswerRelevance == nil || !approxEqual(*agg.MeanAnswerRelevance, 0.625) {
		t.Errorf("expected mean relevance 0.625, got %+v", agg.MeanAnswerRelevance)
	}
	if agg.MeanContextPrecision == nil || !approxEqual(*agg.MeanContextPrecision, 1.0) {
		t.Errorf("expected mean precision 1.0, got %+v", agg.MeanContextPrecision)
	}
	if agg.JudgedCount != 2 {
		t.Errorf("expected judged count 2, got %d", agg.JudgedCount)
	}
}

func TestAggregate_OmitsJudgeMeansWhenAbsent(t *testing.T) {
	reports := []QuestionReport{
		{Metrics: PerQuestionMetrics{K: 10, RecallAtK: 1.0}},
		{Metrics: PerQuestionMetrics{K: 10, RecallAtK: 0.5}},
	}
	agg := Aggregate(reports, 10)
	if agg.MeanFaithfulness != nil || agg.MeanAnswerRelevance != nil || agg.MeanContextPrecision != nil {
		t.Errorf("expected all judge means nil, got %+v", agg)
	}
	if agg.JudgedCount != 0 {
		t.Errorf("expected judged count 0, got %d", agg.JudgedCount)
	}
}

func TestAggregateByRoute_GroupsByQueryType(t *testing.T) {
	reports := []QuestionReport{
		{
			Question: Question{ID: "q1", QueryType: "lookup"},
			Metrics:  PerQuestionMetrics{K: 5, RecallAtK: 1.0, PrecisionAtK: 0.2, ReciprocalRank: 1.0, NDCGAtK: 1.0},
		},
		{
			Question: Question{ID: "q2", QueryType: "lookup"},
			Metrics:  PerQuestionMetrics{K: 5, RecallAtK: 0.5, PrecisionAtK: 0.1, ReciprocalRank: 0.5, NDCGAtK: 0.6},
		},
		{
			Question: Question{ID: "q3", QueryType: "enumeration"},
			Metrics:  PerQuestionMetrics{K: 5, RecallAtK: 0.0, PrecisionAtK: 0.0, ReciprocalRank: 0.0, NDCGAtK: 0.0},
		},
		{
			Question: Question{ID: "q4", QueryType: ""}, // unlabeled
			Metrics:  PerQuestionMetrics{K: 5, RecallAtK: 1.0, PrecisionAtK: 0.2, ReciprocalRank: 1.0, NDCGAtK: 1.0},
		},
	}

	byRoute := AggregateByRoute(reports, 5)

	if len(byRoute) != 3 {
		t.Fatalf("expected 3 route keys (lookup, enumeration, \"\"), got %d: %v", len(byRoute), keys(byRoute))
	}
	lookup := byRoute["lookup"]
	if lookup.Count != 2 {
		t.Errorf("lookup count = %d, want 2", lookup.Count)
	}
	if !approxEqual(lookup.MeanRecall, 0.75) {
		t.Errorf("lookup mean_recall = %f, want 0.75", lookup.MeanRecall)
	}
	enumeration := byRoute["enumeration"]
	if enumeration.Count != 1 {
		t.Errorf("enumeration count = %d, want 1", enumeration.Count)
	}
	if !approxEqual(enumeration.MeanRecall, 0.0) {
		t.Errorf("enumeration mean_recall = %f, want 0.0", enumeration.MeanRecall)
	}
	unlabeled := byRoute[""]
	if unlabeled.Count != 1 {
		t.Errorf("unlabeled count = %d, want 1", unlabeled.Count)
	}
}

func TestAggregateByRoute_SkipsErroredQuestions(t *testing.T) {
	reports := []QuestionReport{
		{
			Question: Question{ID: "q1", QueryType: "lookup"},
			Error:    "boom",
		},
		{
			Question: Question{ID: "q2", QueryType: "lookup"},
			Metrics:  PerQuestionMetrics{K: 5, RecallAtK: 1.0},
		},
	}
	byRoute := AggregateByRoute(reports, 5)
	if byRoute["lookup"].Count != 1 {
		t.Errorf("errored question should be skipped; lookup count = %d, want 1", byRoute["lookup"].Count)
	}
}

func keys(m map[string]AggregateMetrics) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
