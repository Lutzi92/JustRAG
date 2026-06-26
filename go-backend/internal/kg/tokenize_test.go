package kg

import (
	"reflect"
	"testing"
)

func TestTokenizeForGraph(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "   ", nil},
		{"drops short tokens", "a to be PPM", []string{"PPM"}},
		{"splits hyphen, dedups", "PPM-Team and the PPM-Team", []string{"PPM", "Team", "and", "the"}},
		{"keeps order first-seen", "Berlin Berlin Munich", []string{"Berlin", "Munich"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TokenizeForGraph(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("TokenizeForGraph(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
