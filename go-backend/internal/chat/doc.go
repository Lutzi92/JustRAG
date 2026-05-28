// Package chat owns the streaming chat handler and the orchestrators that
// sit between the HTTP layer and the retrieval/LLM packages.
//
// PrepareChatContext is the shared assembly path: it runs CRAG grading, the
// enumeration pre-pass, contextual-prefix folding, sandwich ordering, and
// trajectory event emission, then hands the prompt to ai.StreamCompletion.
//
// Four orchestrators wrap PrepareChatContext for complex_reasoning queries,
// in priority order — the first one whose site_config flag is on wins:
//
//  1. Supervisor (chat_supervisor_enabled): one classification call routes
//     to a specialist (RetrieverAgent or EnumeratorAgent), each issuing a
//     single search.
//  2. Plan-and-Execute (chat_plan_execute_enabled): Plan into 1–5 sub-queries
//     (optionally as a DAG with explicit edges), iterate up to a configured
//     hop budget, then generate.
//  3. Agentic (chat_agentic_enabled): hop-1 search with HyDE+MultiQuery,
//     critique LLM, optional follow-up hops up to chat_agentic_max_hops with
//     a quality-plateau early-stop.
//  4. Standard PrepareChatContext: the non-orchestrated path (still runs
//     CRAG + enumeration + contextual prefix + sandwich order).
//
// Trajectory events (plan / iterate / hop / decision / answer) stream over
// SSE so the frontend can render a "Reasoning steps" panel above the answer.
package chat
