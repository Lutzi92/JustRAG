package eval

import "testing"

func qr(expected, classified, errStr string, agent bool) QuestionReport {
	r := QuestionReport{
		Question: Question{QueryType: expected},
		Error:    errStr,
	}
	if agent {
		r.Agent = &AgentTrace{ClassifiedQueryType: classified}
	}
	return r
}

func TestRoutingAccuracy_AllMatch(t *testing.T) {
	reports := []QuestionReport{
		qr("lookup", "lookup", "", true),
		qr("enumeration", "enumeration", "", true),
		qr("complex_reasoning", "complex_reasoning", "", true),
	}
	got := RoutingAccuracy(reports)
	if got == nil {
		t.Fatal("expected non-nil report")
	}
	if got.Scored != 3 || got.Correct != 3 {
		t.Fatalf("scored=%d correct=%d, want 3/3", got.Scored, got.Correct)
	}
	if got.Accuracy != 1.0 {
		t.Fatalf("accuracy=%v, want 1.0", got.Accuracy)
	}
}

func TestRoutingAccuracy_Mixed(t *testing.T) {
	reports := []QuestionReport{
		qr("lookup", "lookup", "", true),                       // correct
		qr("lookup", "complex_reasoning", "", true),            // wrong
		qr("enumeration", "lookup", "", true),                  // wrong
		qr("complex_reasoning", "complex_reasoning", "", true), // correct
	}
	got := RoutingAccuracy(reports)
	if got.Scored != 4 || got.Correct != 2 {
		t.Fatalf("scored=%d correct=%d, want 4/2", got.Scored, got.Correct)
	}
	if got.Accuracy != 0.5 {
		t.Fatalf("accuracy=%v, want 0.5", got.Accuracy)
	}
	lk := got.PerExpected["lookup"]
	if lk.Scored != 2 || lk.Correct != 1 {
		t.Fatalf("lookup bucket scored=%d correct=%d, want 2/1", lk.Scored, lk.Correct)
	}
}

func TestRoutingAccuracy_GlobalSynthesisNormalizes(t *testing.T) {
	// global_synthesis is a golden-only label; it shares the
	// complex_reasoning dispatch path so it must count as a match.
	reports := []QuestionReport{
		qr("global_synthesis", "complex_reasoning", "", true),
	}
	got := RoutingAccuracy(reports)
	if got == nil || got.Scored != 1 || got.Correct != 1 {
		t.Fatalf("got %+v, want scored=1 correct=1", got)
	}
}

func TestRoutingAccuracy_SkipsUnscorable(t *testing.T) {
	reports := []QuestionReport{
		qr("", "lookup", "", true),           // unlabeled golden
		qr("lookup", "", "", true),           // no classified type
		qr("lookup", "lookup", "boom", true), // errored
		qr("lookup", "lookup", "", false),    // nil Agent (retrieval-only)
	}
	if got := RoutingAccuracy(reports); got != nil {
		t.Fatalf("expected nil (nothing scorable), got %+v", got)
	}
}
