package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeRow returns canned scan values (or a canned error) for one QueryRow.
type fakeRow struct {
	val bool
	err error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("fakeRow: expected exactly one destination")
	}
	p, ok := dest[0].(*bool)
	if !ok {
		return errors.New("fakeRow: destination is not *bool")
	}
	*p = r.val
	return nil
}

// fakeProbe answers the three VerifyReadOnly queries. writeOK controls whether
// the write probe "succeeds" (a misconfigured pool); the bool fields answer the
// superuser / dangerous-function / dangerous-role checks in call order.
type fakeProbe struct {
	writeOK    bool
	superuser  bool
	dangerFunc bool
	dangerRole bool
	queryErr   error
}

func (f *fakeProbe) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	if f.writeOK {
		return pgconn.CommandTag{}, nil
	}
	return pgconn.CommandTag{}, errors.New("ERROR: cannot execute CREATE TABLE in a read-only transaction (SQLSTATE 25006)")
}

func (f *fakeProbe) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if f.queryErr != nil {
		return fakeRow{err: f.queryErr}
	}
	switch {
	case strings.Contains(sql, "rolsuper"):
		return fakeRow{val: f.superuser}
	case strings.Contains(sql, "has_function_privilege"):
		return fakeRow{val: f.dangerFunc}
	case strings.Contains(sql, "pg_has_role"):
		return fakeRow{val: f.dangerRole}
	}
	return fakeRow{err: errors.New("fakeProbe: unexpected query: " + sql)}
}

func TestVerifyReadOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		probe   *fakeProbe
		wantErr string // substring; empty means success expected
	}{
		{
			name:  "least-privilege role passes",
			probe: &fakeProbe{},
		},
		{
			name:    "write succeeds is disqualifying",
			probe:   &fakeProbe{writeOK: true},
			wantErr: "accepted a write",
		},
		{
			name:    "superuser is disqualifying",
			probe:   &fakeProbe{superuser: true},
			wantErr: "superuser",
		},
		{
			name:    "execute on file functions is disqualifying",
			probe:   &fakeProbe{dangerFunc: true},
			wantErr: "EXECUTE on server-side file/program functions",
		},
		{
			name:    "privileged role membership is disqualifying",
			probe:   &fakeProbe{dangerRole: true},
			wantErr: "member of a privileged role",
		},
		{
			name:    "query failure surfaces as an error",
			probe:   &fakeProbe{queryErr: errors.New("boom")},
			wantErr: "boom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := VerifyReadOnly(context.Background(), tc.probe)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("VerifyReadOnly() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("VerifyReadOnly() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("VerifyReadOnly() = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
