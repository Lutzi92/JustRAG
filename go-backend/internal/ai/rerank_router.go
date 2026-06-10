package ai

type rerankStrategy int

const (
	strategyRerankEndpoint rerankStrategy = iota
	strategyQwen3ChatTemplate
)

// selectRerankStrategy centralizes reranker model-family routing so call sites
// stay generic. Today only Qwen3-Reranker (with explicit chat-template opt-in)
// diverges from the standard /rerank endpoint; new families add a case here,
// not another branch inside Rerank.
func selectRerankStrategy(rerankModel string, opts RerankOptions) rerankStrategy {
	if opts.UseChatTemplate && IsQwen3RerankerModel(rerankModel) {
		return strategyQwen3ChatTemplate
	}
	return strategyRerankEndpoint
}
