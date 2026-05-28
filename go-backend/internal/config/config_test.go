package config_test

import (
	"testing"

	"github.com/justrag/go-backend/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-that-is-at-least-32-characters-long")
	t.Setenv("NODE_ENV", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != 3000 {
		t.Errorf("expected Port=3000, got %d", cfg.Port)
	}
	if cfg.LegacyPort != 3001 {
		t.Errorf("expected LegacyPort=3001, got %d", cfg.LegacyPort)
	}
	if cfg.DB.Host != "localhost" {
		t.Errorf("expected DB.Host=localhost, got %s", cfg.DB.Host)
	}
	if cfg.DB.Port != 5432 {
		t.Errorf("expected DB.Port=5432, got %d", cfg.DB.Port)
	}
	if cfg.DB.MaxConns != 20 {
		t.Errorf("expected DB.MaxConns=20, got %d", cfg.DB.MaxConns)
	}
	if cfg.Redis.Host != "localhost" {
		t.Errorf("expected Redis.Host=localhost, got %s", cfg.Redis.Host)
	}
	if cfg.Redis.Port != 6379 {
		t.Errorf("expected Redis.Port=6379, got %d", cfg.Redis.Port)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("PORT", "8080")
	t.Setenv("LEGACY_PORT", "4000")
	t.Setenv("DB_HOST", "db.example.com")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("DB_USER", "myuser")
	t.Setenv("DB_PASSWORD", "mypass")
	t.Setenv("DB_NAME", "mydb")
	t.Setenv("DB_POOL_MAX", "50")
	t.Setenv("REDIS_HOST", "redis.example.com")
	t.Setenv("REDIS_PORT", "6380")
	t.Setenv("REDIS_PASSWORD", "redispass")
	t.Setenv("REDIS_DB", "2")
	t.Setenv("JWT_SECRET", "a-very-long-secret-that-is-at-least-32-characters")
	t.Setenv("ALLOWED_ORIGINS", "https://app.example.com,https://admin.example.com")
	t.Setenv("NODE_ENV", "production")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("expected Port=8080, got %d", cfg.Port)
	}
	if cfg.LegacyPort != 4000 {
		t.Errorf("expected LegacyPort=4000, got %d", cfg.LegacyPort)
	}
	if cfg.DB.Host != "db.example.com" {
		t.Errorf("expected DB.Host=db.example.com, got %s", cfg.DB.Host)
	}
	if cfg.Redis.Password != "redispass" {
		t.Errorf("expected Redis.Password=redispass, got %s", cfg.Redis.Password)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "https://app.example.com" {
		t.Errorf("expected 2 allowed origins, got %v", cfg.AllowedOrigins)
	}
}

func TestProductionRequiresJWTSecret(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	t.Setenv("JWT_SECRET", "short")
	t.Setenv("REDIS_PASSWORD", "somepass")
	t.Setenv("ALLOWED_ORIGINS", "https://example.com")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for short JWT_SECRET in production")
	}
}

func TestRejectsShortJWTSecretInDev(t *testing.T) {
	t.Setenv("NODE_ENV", "development")
	t.Setenv("JWT_SECRET", "short")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for short JWT_SECRET regardless of env")
	}
}

func TestRejectsEmptyJWTSecret(t *testing.T) {
	t.Setenv("NODE_ENV", "development")
	t.Setenv("JWT_SECRET", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when JWT_SECRET is empty")
	}
}

func TestProductionRequiresRedisPassword(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	t.Setenv("JWT_SECRET", "a-very-long-secret-that-is-at-least-32-characters")
	t.Setenv("REDIS_PASSWORD", "")
	t.Setenv("ALLOWED_ORIGINS", "https://example.com")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for empty REDIS_PASSWORD in production")
	}
}

func TestGoEnvTakesPrecedenceOverNodeEnv(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-that-is-at-least-32-characters-long")
	t.Setenv("GO_ENV", "production")
	t.Setenv("NODE_ENV", "development")
	t.Setenv("REDIS_PASSWORD", "somepass")
	t.Setenv("ALLOWED_ORIGINS", "https://example.com")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != "production" {
		t.Errorf("expected Env=production (from GO_ENV), got %s", cfg.Env)
	}
}

func TestProductionRequiresAllowedOrigins(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-that-is-at-least-32-characters-long")
	t.Setenv("NODE_ENV", "production")
	t.Setenv("REDIS_PASSWORD", "somepass")
	t.Setenv("ALLOWED_ORIGINS", "")

	if _, err := config.Load(); err == nil {
		t.Fatal("expected error when ALLOWED_ORIGINS is unset in production")
	}
}

func TestVectorDBFallsBackToMainDB(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-that-is-at-least-32-characters-long")
	t.Setenv("DB_HOST", "main-db")
	t.Setenv("DB_USER", "mainuser")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.VectorDB.Host != "main-db" {
		t.Errorf("expected VectorDB.Host to fall back to main DB host, got %s", cfg.VectorDB.Host)
	}
	if cfg.VectorDB.User != "mainuser" {
		t.Errorf("expected VectorDB.User to fall back to main DB user, got %s", cfg.VectorDB.User)
	}
}
