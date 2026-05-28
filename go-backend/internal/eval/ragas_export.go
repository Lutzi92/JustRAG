package eval

// ExportRAGAS serializes an eval Report into the RAGAS framework's
// dataset shape: a JSON array of objects with keys
// {question, answer, contexts, ground_truth}. The "contexts" array
// comes from QuestionReport.Contents, populated by the runner +
// post-hoc --judge loop in cmd/eval/main.go.
//
// ground_truth is empty in this version: Question doesn't yet have a
// golden_answer field. Adding one is tracked as a future improvement
// in the Phase 3 §G plan.
//
// Output is deterministic: the order matches Report.Questions, and a
// nil Contents slice yields an empty []string (NOT nil) so downstream
// JSON encoders produce `"contexts":[]` rather than `"contexts":null`.
func ExportRAGAS(rep Report) []map[string]any {
	out := make([]map[string]any, 0, len(rep.Questions))
	for _, qr := range rep.Questions {
		ctx := qr.Contents
		if ctx == nil {
			ctx = []string{}
		}
		answer := ""
		if qr.Judge != nil {
			answer = qr.Judge.Answer
		}
		out = append(out, map[string]any{
			"question":     qr.Question.Question,
			"answer":       answer,
			"contexts":     ctx,
			"ground_truth": "",
		})
	}
	return out
}
