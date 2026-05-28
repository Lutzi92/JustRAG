// Package ai is the integration boundary for every LLM and embedding-model
// call the application makes. It speaks the OpenAI-compatible Chat Completions
// and Embeddings wire formats, but most production deployments target
// vLLM/SGLang/Llama.cpp/DeepSeek behind that contract rather than OpenAI itself.
//
// Responsibilities:
//
//   - Streaming and non-streaming chat completion with retry/backoff,
//     reasoning-channel extraction, and prompt-cache token accounting.
//   - Query- and document-side embeddings with optional asymmetric
//     instruction prefixes and an in-process cache.
//   - The orchestrator-facing helpers (NeedsMoreInfo, DecideNextAction,
//     PlanQueries, VerifyFactuality) used by the agentic / plan-execute /
//     supervisor chat orchestrators in package chat.
//   - ConfigResolver: per-KB resolution of model + endpoint + key from the
//     site_configs table, with singleflight de-duplication.
//
// Test injection: orchestrator helpers route their LLM call through
// (*ConfigResolver).completionFn, which consults a per-instance completionHook
// before falling through to GenerateCompletionWithModel. Tests construct a
// hook-bearing resolver via newTestResolverWithCompletion (test-only) so the
// stub is scoped to the test's own resolver instance — no package-level
// global, no t.Cleanup unwind across parallel tests.
package ai
