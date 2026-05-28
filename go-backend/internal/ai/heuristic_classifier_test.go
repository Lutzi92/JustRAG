package ai

import "testing"

func TestHeuristicComplexity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
		want  ComplexityVerdict
	}{
		// Simple
		{"empty", "", ComplexitySimple},
		{"short factual", "Was ist §323c StGB?", ComplexitySimple},
		{"short english factual", "What is SSO?", ComplexitySimple},
		{"one word", "Kündigungsfrist", ComplexitySimple},
		{"six words no conjunction", "when does the semester break start", ComplexitySimple},

		// Complex — DE markers
		{"vergleich", "Vergleiche BGB und HGB", ComplexityComplex},
		{"unterschied", "Was ist der Unterschied zwischen A und B", ComplexityComplex},
		{"warum", "Warum gilt das?", ComplexityComplex},
		{"erkläre", "Erkläre mir die Regelung", ComplexityComplex},

		// Complex — EN markers
		{"compare", "compare X and Y", ComplexityComplex},
		{"how does", "how does RAG work", ComplexityComplex},
		{"why", "why is this the case", ComplexityComplex},
		{"difference", "what's the difference between A and B", ComplexityComplex},

		// Unknown — longer but no markers
		{"long no marker", "please retrieve all documents from the archive about semester fees collected in 2024", ComplexityUnknown},
		{"with conjunction no marker", "fees and deadlines for registration", ComplexityUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := HeuristicComplexity(tt.query)
			if got != tt.want {
				t.Errorf("HeuristicComplexity(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}
