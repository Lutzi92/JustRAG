package chat

import (
	"context"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/prompts"
)

// aiDAGCriticAdapter wraps ai.CritiqueDAGProgress to satisfy the
// DAGCritic interface the executor consumes. Lives here in chat/
// (not ai/) because the chat package is the only consumer; ai/
// stays free of chat-package types.
//
// The fields the chat handler must inject before constructing the
// adapter:
//   - resolver: aiResolver from the handler
//   - kbID, lang: from the chat request
//   - modelOverride: chat_plan_execute_dag_iterative_model resolved
//     by the chat handler before plan-execute starts
type aiDAGCriticAdapter struct {
	resolver      *ai.ConfigResolver
	kbID          string
	lang          string
	modelOverride string
}

// Critique satisfies DAGCritic. Translates the chat-side shapes
// into the prompts-package shapes ai.CritiqueDAGProgress expects,
// then back. The translation is a flat field copy — if either
// shape ever drifts a compile-time error here flags it.
func (a *aiDAGCriticAdapter) Critique(
	ctx context.Context,
	question string,
	completed []DAGCompletedNode,
	pending []DAGPendingNode,
	existingIDs, completedIDs map[string]bool,
) (DAGCritiqueDecision, error) {
	completedPrompt := make([]prompts.CritiqueDAGCompletedNode, len(completed))
	for i, c := range completed {
		completedPrompt[i] = prompts.CritiqueDAGCompletedNode{
			NodeID:     c.NodeID,
			Query:      c.Query,
			TopExcerpt: c.TopExcerpt,
			Err:        c.Err,
		}
	}
	pendingPrompt := make([]prompts.CritiqueDAGPendingNode, len(pending))
	for i, p := range pending {
		pendingPrompt[i] = prompts.CritiqueDAGPendingNode{
			NodeID: p.NodeID,
			Query:  p.Query,
		}
	}
	res, err := ai.CritiqueDAGProgress(ctx, a.resolver, question, completedPrompt, pendingPrompt,
		existingIDs, completedIDs, a.kbID, a.lang, a.modelOverride)
	if err != nil {
		// On error the AI layer returns CritiqueResult{Action: continue}
		// so the executor's switch falls into the no-op branch.
		return DAGCritiqueDecision{Action: string(res.Action)}, err
	}
	return DAGCritiqueDecision{
		Action:       string(res.Action),
		NewNodes:     res.NewNodes,
		PruneNodeIDs: res.PruneNodeIDs,
		Reason:       res.Reason,
	}, nil
}

// newAIDAGCritic builds a critic-adapter for the chat handler. The
// model override resolution sits HERE (not inside the adapter
// constructor caller) because the AP-D3 fallback chain is
// documented: empty `chat_plan_execute_dag_iterative_model` falls
// back to `chat_plan_execute_model`, which falls back to the KB
// default. The chat handler resolves chat_plan_execute_model
// already; this helper applies the AP-D3 layer on top.
func newAIDAGCritic(resolver *ai.ConfigResolver, kbID, lang, model string) DAGCritic {
	return &aiDAGCriticAdapter{
		resolver:      resolver,
		kbID:          kbID,
		lang:          lang,
		modelOverride: model,
	}
}
