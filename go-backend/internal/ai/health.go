package ai

import (
	"context"
	"time"
)

// HealthResult holds the result of an AI provider health check.
type HealthResult struct {
	Status       string `json:"status"` // "healthy" or "unhealthy"
	LatencyMs    int64  `json:"latencyMs"`
	Error        string `json:"error,omitempty"`
	ProviderName string `json:"providerName,omitempty"`
}

// CheckProviderHealth resolves the global AI provider config and calls the
// models endpoint to verify connectivity. It never returns a non-nil error;
// all failure modes are encoded in HealthResult.Status.
func CheckProviderHealth(ctx context.Context, resolver *ConfigResolver) (*HealthResult, error) {
	cfg, err := resolver.Resolve(ctx, "")
	if err != nil {
		return &HealthResult{
			Status: "unhealthy",
			Error:  "Kein aktiver KI-Anbieter konfiguriert",
		}, nil
	}

	client := CachedClient(cfg.BaseURL, cfg.APIKey)

	start := time.Now()
	listErr := client.ListModels(ctx)
	latencyMs := time.Since(start).Milliseconds()

	if listErr != nil {
		return &HealthResult{
			Status:       "unhealthy",
			LatencyMs:    latencyMs,
			Error:        listErr.Error(),
			ProviderName: cfg.ProviderName,
		}, nil
	}

	return &HealthResult{
		Status:       "healthy",
		LatencyMs:    latencyMs,
		ProviderName: cfg.ProviderName,
	}, nil
}
