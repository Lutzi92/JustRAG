// Package adkbridge connects Google's Agent Development Kit (adk-go) to the
// JustRAG model, tool and session layers. ADK sees only its own interfaces
// (model.LLM, tool.Tool, session.Service); the OpenAI-compatible client,
// concurrency limiter, call counter and reasoning switches stay in
// internal/ai and are reached through this package.
//
// Design: docs/superpowers/specs/2026-10-08-adk-go-adoption-plan.md.
// Nothing in this package is wired into a request path until Phase 2.
package adkbridge
