package ai

import "testing"

func TestSelectRerankStrategy(t *testing.T) {
	if got := selectRerankStrategy("Qwen3-Reranker-8B", RerankOptions{UseChatTemplate: true}); got != strategyQwen3ChatTemplate {
		t.Fatalf("qwen3 + opt-in → chat-template strategy, got %v", got)
	}
	if got := selectRerankStrategy("Qwen3-Reranker-8B", RerankOptions{UseChatTemplate: false}); got != strategyRerankEndpoint {
		t.Fatalf("no opt-in → /rerank endpoint, got %v", got)
	}
	if got := selectRerankStrategy("cohere-rerank-v3", RerankOptions{UseChatTemplate: true}); got != strategyRerankEndpoint {
		t.Fatalf("non-qwen3 must never use chat template, got %v", got)
	}
}
