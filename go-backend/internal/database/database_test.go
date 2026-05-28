package database_test

import (
	"net/url"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
)

func TestBuildConnString(t *testing.T) {
	cfg := config.DBConfig{
		Host:              "db.example.com",
		Port:              5432,
		User:              "myuser",
		Password:          "mypass",
		Name:              "mydb",
		MaxConns:          20,
		IdleTimeout:       30 * time.Second,
		ConnectionTimeout: 10 * time.Second,
		StatementTimeout:  120 * time.Second,
	}

	connStr := database.BuildConnString(cfg)

	if connStr != "postgres://myuser:mypass@db.example.com:5432/mydb?statement_timeout=120000&connect_timeout=10" {
		t.Errorf("unexpected connection string: %s", connStr)
	}
}

// TestBuildConnString_SpecialChars exercises the entire reason BuildConnString
// uses url.URL/url.UserPassword: passwords containing characters that break a
// naive fmt.Sprintf DSN (`@` separates user from host; `?` starts the query;
// `#` introduces a fragment; `:` separates user from password) must round-trip
// through url.Parse cleanly.
func TestBuildConnString_SpecialChars(t *testing.T) {
	cfg := config.DBConfig{
		Host:              "db.example.com",
		Port:              5432,
		User:              "admin",
		Password:          "p@ss:#w0?rd/&x",
		Name:              "mydb",
		ConnectionTimeout: 10 * time.Second,
		StatementTimeout:  120 * time.Second,
	}

	connStr := database.BuildConnString(cfg)

	parsed, err := url.Parse(connStr)
	if err != nil {
		t.Fatalf("BuildConnString produced an unparseable URL: %v\n  %s", err, connStr)
	}
	if parsed.User.Username() != "admin" {
		t.Errorf("expected user=admin, got %q", parsed.User.Username())
	}
	gotPass, hasPass := parsed.User.Password()
	if !hasPass {
		t.Fatalf("expected password to be present")
	}
	if gotPass != "p@ss:#w0?rd/&x" {
		t.Errorf("password did not round-trip: got %q", gotPass)
	}
	if parsed.Host != "db.example.com:5432" {
		t.Errorf("expected host=db.example.com:5432, got %q", parsed.Host)
	}
	if parsed.Path != "/mydb" {
		t.Errorf("expected path=/mydb, got %q", parsed.Path)
	}
}

func TestBuildPoolConfig(t *testing.T) {
	cfg := config.DBConfig{
		Host:              "localhost",
		Port:              5432,
		User:              "postgres",
		Password:          "postgres",
		Name:              "testdb",
		MaxConns:          10,
		MinConns:          4,
		IdleTimeout:       15 * time.Second,
		MaxConnLifetime:   30 * time.Minute,
		ConnectionTimeout: 5 * time.Second,
		StatementTimeout:  60 * time.Second,
	}

	poolCfg, err := database.BuildPoolConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if poolCfg.MaxConns != 10 {
		t.Errorf("expected MaxConns=10, got %d", poolCfg.MaxConns)
	}
	if poolCfg.MinConns != 4 {
		t.Errorf("expected MinConns=4, got %d", poolCfg.MinConns)
	}
	if poolCfg.MaxConnIdleTime != 15*time.Second {
		t.Errorf("expected idle timeout 15s, got %v", poolCfg.MaxConnIdleTime)
	}
	if poolCfg.MaxConnLifetime != 30*time.Minute {
		t.Errorf("expected max conn lifetime 30m, got %v", poolCfg.MaxConnLifetime)
	}
	if poolCfg.HealthCheckPeriod != 30*time.Second {
		t.Errorf("expected health check period 30s, got %v", poolCfg.HealthCheckPeriod)
	}
	if poolCfg.AfterConnect == nil {
		t.Errorf("expected AfterConnect hook to be set for pgvector iterative_scan")
	}
}
