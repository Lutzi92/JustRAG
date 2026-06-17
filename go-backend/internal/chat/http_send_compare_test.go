package chat

import "testing"

func TestWillRunComparison(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		attID   string
		modes   []string
		want    bool
	}{
		{"happy", true, "att1", []string{"formal"}, true},
		{"gate off", false, "att1", []string{"formal"}, false},
		{"no attachment", true, "", []string{"formal"}, false},
		{"no modes", true, "att1", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := willRunComparison(c.enabled, c.attID, c.modes); got != c.want {
				t.Fatalf("willRunComparison=%v want %v", got, c.want)
			}
		})
	}
}
