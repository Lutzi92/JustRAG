package vector

import (
	"reflect"
	"testing"
)

// Oracle: hand-written expected orders. The preferred dimension goes first
// only when such a table exists; the rest are de-duplicated and ascending, so
// the fallback order does not depend on pg_tables' row order.
func TestExcerptTableOrder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dims      []int
		preferred int
		want      []int
	}{
		{"preferred moved to front", []int{1536, 4096, 768}, 4096, []int{4096, 768, 1536}},
		{"unknown preference", []int{1536, 768}, 0, []int{768, 1536}},
		{"preferred table missing", []int{1536, 768}, 1024, []int{768, 1536}},
		{"duplicates dropped", []int{1536, 1536, 768}, 1536, []int{1536, 768}},
	} {
		if got := excerptTableOrder(tc.dims, tc.preferred); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
