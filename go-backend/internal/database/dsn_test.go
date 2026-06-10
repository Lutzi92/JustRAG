package database

import "testing"

func TestDSNHasPoolMaxConns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		dsn  string
		want bool
	}{
		{"postgres://u:p@host:5432/db?pool_max_conns=8", true},
		{"postgres://u:p@host:5432/db?sslmode=disable&pool_max_conns=8", true},
		{"postgres://u:p@host:5432/db", false},
		{"postgres://u:p@host:5432/db?sslmode=disable", false},
		// Parameter name inside a password must not count as an override.
		{"postgres://u:pool_max_conns@host:5432/db", false},
		{"host=localhost user=ro dbname=app pool_max_conns=8", true},
		{"host=localhost user=ro dbname=app", false},
		// Value merely containing the substring must not count.
		{"host=localhost user=ro password=xpool_max_conns=8x dbname=app", false},
	}
	for _, tc := range cases {
		if got := dsnHasPoolMaxConns(tc.dsn); got != tc.want {
			t.Errorf("dsnHasPoolMaxConns(%q) = %v, want %v", tc.dsn, got, tc.want)
		}
	}
}
