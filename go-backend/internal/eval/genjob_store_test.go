package eval

import "testing"

func TestGenJobParamsJSON(t *testing.T) {
	p := GenJobParams{
		Lang:     "de",
		Name:     "draft-1",
		MaxFiles: 10,
		Model:    "",
		Counts:   GenCounts{Lookup: 20, Complex: 10, Enumeration: 5, MultiHop: 5},
	}
	b, err := p.toJSON()
	if err != nil {
		t.Fatalf("toJSON: %v", err)
	}
	got, err := parseGenJobParams(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != p {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, p)
	}
}
