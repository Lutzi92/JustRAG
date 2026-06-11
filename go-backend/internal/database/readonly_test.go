package database

import "testing"

func TestBuildReadOnlyPoolConfigDefaults(t *testing.T) {
	cfg, err := buildReadOnlyPoolConfig("postgres://ro:pw@localhost:5432/justrag")
	if err != nil {
		t.Fatalf("buildReadOnlyPoolConfig: %v", err)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "5000" {
		t.Errorf("statement_timeout = %q, want \"5000\"", got)
	}
	if got := cfg.ConnConfig.RuntimeParams["default_transaction_read_only"]; got != "on" {
		t.Errorf("default_transaction_read_only = %q, want \"on\"", got)
	}
	if cfg.MaxConns != 4 {
		t.Errorf("MaxConns = %d, want 4", cfg.MaxConns)
	}
}

func TestBuildReadOnlyPoolConfigOperatorOverrides(t *testing.T) {
	dsn := "postgres://ro:pw@localhost:5432/justrag?pool_max_conns=8&statement_timeout=30000&default_transaction_read_only=off"
	cfg, err := buildReadOnlyPoolConfig(dsn)
	if err != nil {
		t.Fatalf("buildReadOnlyPoolConfig: %v", err)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "30000" {
		t.Errorf("statement_timeout = %q, want operator value \"30000\"", got)
	}
	if got := cfg.ConnConfig.RuntimeParams["default_transaction_read_only"]; got != "off" {
		t.Errorf("default_transaction_read_only = %q, want operator value \"off\"", got)
	}
	if cfg.MaxConns != 8 {
		t.Errorf("MaxConns = %d, want operator value 8", cfg.MaxConns)
	}
}
