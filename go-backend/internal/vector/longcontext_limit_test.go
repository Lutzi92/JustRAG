package vector

import "testing"

func TestLongContextLimit(t *testing.T) {
	cases := []struct {
		name     string
		limit    int
		override int
		want     int
	}{
		{"no override uses constant", 10, 0, 200},
		{"override above constant wins", 10, 300, 300},
		{"limit already above the constant is kept", 250, 200, 250},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := longContextLimit(tc.limit, tc.override); got != tc.want {
				t.Fatalf("longContextLimit(%d, %d): want %d, got %d", tc.limit, tc.override, tc.want, got)
			}
		})
	}
}
