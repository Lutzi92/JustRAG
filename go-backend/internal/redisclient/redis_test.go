package redisclient_test

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/redisclient"
)

func TestNewClient_InvalidAddress(t *testing.T) {
	cfg := config.RedisConfig{
		Host: "nonexistent-host",
		Port: 9999,
		DB:   0,
	}

	client := redisclient.New(cfg)
	err := client.Ping(context.Background())
	if err == nil {
		t.Fatal("expected ping to fail for invalid address")
	}
}

func TestNewClient_Options(t *testing.T) {
	cfg := config.RedisConfig{
		Host:     "localhost",
		Port:     6379,
		Password: "testpass",
		DB:       3,
	}

	client := redisclient.New(cfg)
	opts := client.Options()

	if opts.Addr != "localhost:6379" {
		t.Errorf("expected addr localhost:6379, got %s", opts.Addr)
	}
	if opts.Password != "testpass" {
		t.Errorf("expected password testpass, got %s", opts.Password)
	}
	if opts.DB != 3 {
		t.Errorf("expected DB 3, got %d", opts.DB)
	}
}
