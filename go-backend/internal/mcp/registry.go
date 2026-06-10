package mcp

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/safego"
)

// Registry is the per-process tool catalog. Three tiers, checked in
// priority order on every lookup:
//
//  1. builtin     — global, in-process tools (kb_search, web_search, …)
//  2. perKB       — per-KB-only remote tools (reserved for a future
//     per-KB admin scope; currently unused)
//  3. globalRemote — admin-configured remote MCP servers that apply to
//     every KB. This is the deployment-wide list a global
//     `mcp_servers` site_config feeds.
//
// Lookup precedence (Get): built-ins win over per-KB which win over
// global-remote. The collision rule prevents a misconfigured remote
// from shadowing a built-in (operators sometimes name their probe
// servers "kb_search").
//
// Concurrency: the registry is read on the chat hot path and refreshed
// whenever an admin changes `mcp_servers` via the admin UI. RWMutex
// (not Mutex) because reads dominate.
type Registry struct {
	mu               sync.RWMutex
	builtin          map[string]Tool               // global built-ins
	perKB            map[string]map[string]Tool    // kbID -> toolName -> Tool (reserved)
	clients          map[string]map[string]*Client // kbID -> serverName -> Client (reserved)
	globalRemote     map[string]Tool               // toolName -> Tool (deployment-wide)
	globalClients    map[string]*Client            // serverName -> Client
	globalRemoteSpec []ServerSpec                  // last-configured spec, for /status reads
}

// NewRegistry creates an empty registry. Callers register built-ins
// during startup before any KB hits the chat path.
func NewRegistry() *Registry {
	return &Registry{
		builtin:       make(map[string]Tool),
		perKB:         make(map[string]map[string]Tool),
		clients:       make(map[string]map[string]*Client),
		globalRemote:  make(map[string]Tool),
		globalClients: make(map[string]*Client),
	}
}

// RegisterBuiltin adds a tool that is visible to every KB. Built-ins
// take precedence over remote tools with the same name (per
// `Configure`'s collision rule). This must be called during startup;
// the chat path does not call it.
func (r *Registry) RegisterBuiltin(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Origin = "builtin"
	r.builtin[t.Name] = t
}

// Configure (re)installs the per-KB tool list from the supplied server
// specs. Each spec yields one Client; the client's `tools/list` reply is
// folded into the per-KB map. Existing clients for servers that disappear
// from the spec list are dropped (their goroutines were never spawned —
// the http.Client is GC'd with the *Client struct).
//
// On any single-server failure (validation, initialize, list_tools) the
// error is logged and that server's tools are skipped; other servers
// continue to register. Returning a hard error here would let one
// misconfigured server take the whole KB offline, which is the wrong
// trade for a v1 admin-knob feature.
func (r *Registry) Configure(ctx context.Context, kbID string, specs []ServerSpec) {
	results := r.probeServers(ctx, specs, "mcp.configure", []any{"kb_id", kbID})

	tools := make(map[string]Tool)
	clients := make(map[string]*Client, len(specs))
	for _, p := range results {
		if p.skipped || p.client == nil {
			continue
		}
		clients[p.spec.Name] = p.client
		for n, t := range p.tools {
			tools[n] = t
		}
	}

	r.mu.Lock()
	r.perKB[kbID] = tools
	r.clients[kbID] = clients
	r.mu.Unlock()
}

// probeResult is one entry per input spec (in input order) from
// probeServers. `skipped` means validation failed — caller should not
// include this spec in any post-probe tracking. `client == nil` means
// validation passed but the live probe failed; caller may still record
// the spec (e.g. for /status reporting an unreachable-but-configured
// server).
type probeResult struct {
	spec    ServerSpec
	client  *Client
	tools   map[string]Tool
	skipped bool
}

// probeServers validates and probes every spec concurrently via
// safego.GoCtx (panic-isolated; admin path so latency-sensitive but not
// error-propagating — per-server failures are logged + skipped). GoCtx
// (not Go) so a recovered panic inside a probe carries the request_id /
// kb_id of the admin request that triggered it.
// Built-in collision filtering is applied here so callers can splice
// the result straight into their tier map.
//
// `logPrefix` distinguishes warnings emitted by Configure vs
// ConfigureGlobal ("mcp.configure" / "mcp.configure_global");
// `extraLogAttrs` are appended to every warning so call sites can
// attach kb_id et al. without re-implementing the loop.
func (r *Registry) probeServers(ctx context.Context, specs []ServerSpec, logPrefix string, extraLogAttrs []any) []probeResult {
	// Snapshot built-in names once instead of acquiring r.mu.RLock() for
	// every tool of every server (built-ins are startup-frozen). With N
	// remote tools across M servers this collapses N RLock cycles into 1.
	builtinNames := r.snapshotBuiltinNames()

	results := make([]probeResult, len(specs))
	var wg sync.WaitGroup
	// Each iteration's `i` and `s` are per-iteration variables (Go 1.22+
	// loop-scope semantics, confirmed by the module's `go 1.26` directive),
	// so each goroutine below writes to its own results[i] slot — no
	// concurrent slice-element write, no need for an inner shadow var.
	for i := range specs {
		s := specs[i]
		if err := s.Validate(); err != nil {
			args := append([]any{"name", s.Name, "error", err}, extraLogAttrs...)
			logctx.From(ctx).Warn(logPrefix+": invalid server spec", args...)
			results[i].skipped = true
			continue
		}
		results[i].spec = s
		wg.Add(1)
		// Probe each server concurrently — ListTools is an HTTP round
		// trip with `dialTimeout`. Sequentially this would be N×timeout
		// worst-case (any one slow server stalls the rest).
		safego.GoCtx(ctx, func() {
			defer wg.Done()
			c := NewClient(s.URL, s.Headers)
			listCtx, cancel := context.WithTimeout(ctx, dialTimeout)
			defer cancel()
			serverTools, err := c.ListTools(listCtx)
			if err != nil {
				args := append([]any{"name", s.Name, "url", s.URL, "error", err}, extraLogAttrs...)
				logctx.From(ctx).Warn(logPrefix+": tools/list failed; skipping server", args...)
				return
			}
			localTools := make(map[string]Tool, len(serverTools))
			origin := "remote:" + s.Name
			for _, t := range serverTools {
				if _, isBuiltin := builtinNames[t.Name]; isBuiltin {
					args := append([]any{"tool", t.Name, "server", s.Name}, extraLogAttrs...)
					logctx.From(ctx).Warn(logPrefix+": remote tool name shadows built-in; skipping", args...)
					continue
				}
				t.Origin = origin
				t.Handler = remoteHandler{client: c, toolName: t.Name}
				localTools[t.Name] = t
			}
			results[i].client = c
			results[i].tools = localTools
		})
	}
	wg.Wait()
	return results
}

// snapshotBuiltinNames returns a set of built-in tool names taken under
// the registry RLock once. Built-ins are registered at startup and never
// mutated thereafter, so callers in collision-check loops can read this
// snapshot without further locking.
func (r *Registry) snapshotBuiltinNames() map[string]struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]struct{}, len(r.builtin))
	for k := range r.builtin {
		out[k] = struct{}{}
	}
	return out
}

// List returns every tool the KB can call: built-ins ∪ per-KB ∪
// global-remote. The returned slice is a snapshot — callers may reorder,
// filter, or append to it without locking. The copy is SHALLOW: the Tool
// values share reference fields (notably InputSchema's json.RawMessage
// backing array) with the registry, so callers must not mutate a Tool's
// fields in place — replace the whole element instead.
func (r *Registry) List(kbID string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.builtin)+len(r.perKB[kbID])+len(r.globalRemote))
	for _, t := range r.builtin {
		out = append(out, t)
	}
	for _, t := range r.perKB[kbID] {
		out = append(out, t)
	}
	for _, t := range r.globalRemote {
		out = append(out, t)
	}
	return out
}

// Get resolves a tool by name. Precedence: built-ins (global, hard-
// shadowing) → per-KB → global-remote. Returns the tool plus a
// found=true flag.
func (r *Registry) Get(kbID, toolName string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.builtin[toolName]; ok {
		return t, true
	}
	if perKB, ok := r.perKB[kbID]; ok {
		if t, ok := perKB[toolName]; ok {
			return t, true
		}
	}
	if t, ok := r.globalRemote[toolName]; ok {
		return t, true
	}
	return Tool{}, false
}

// ConfigureGlobal (re)installs the deployment-wide remote-tool list
// from the supplied server specs. Mirrors Configure() but operates on
// the globalRemote tier — every KB sees these tools without per-KB
// configuration. Used by the admin UI's "save servers" action and at
// startup when reading the `mcp_servers` site_config.
//
// Like Configure(), per-server failures are logged + skipped (a single
// unreachable server must not take the whole feature offline). Tool-
// name collisions with built-ins are silently dropped — built-ins win.
func (r *Registry) ConfigureGlobal(ctx context.Context, specs []ServerSpec) {
	results := r.probeServers(ctx, specs, "mcp.configure_global", nil)

	tools := make(map[string]Tool)
	clients := make(map[string]*Client, len(specs))
	cleaned := make([]ServerSpec, 0, len(specs))
	for _, p := range results {
		if p.skipped {
			continue
		}
		// `cleaned` is the post-validate spec list; we record it whether
		// or not the live probe succeeded so /status can report an
		// unreachable-but-configured server.
		cleaned = append(cleaned, p.spec)
		if p.client == nil {
			continue
		}
		clients[p.spec.Name] = p.client
		for n, t := range p.tools {
			tools[n] = t
		}
	}

	r.mu.Lock()
	r.globalRemote = tools
	r.globalClients = clients
	r.globalRemoteSpec = cleaned
	r.mu.Unlock()
}

// ServerStatus is the per-server diagnostic the admin UI renders. It is
// the result of a fresh `tools/list` against the configured URL: when
// `Reachable` is false, `Error` carries the failure detail and `Tools`
// is empty.
type ServerStatus struct {
	Name      string        `json:"name"`
	URL       string        `json:"url"`
	Reachable bool          `json:"reachable"`
	Error     string        `json:"error,omitempty"`
	Tools     []ToolSummary `json:"tools,omitempty"`
}

// ToolSummary is the public, JSON-serializable view of one tool.
type ToolSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Origin      string `json:"origin"`
}

// GlobalStatus runs a live `tools/list` against every configured global
// remote server and returns one ServerStatus per server. It also
// returns the built-in tools so the admin UI can show the full set the
// agent will see at runtime. Caller-provided context governs timeouts.
//
// Probes run concurrently — sequentially the admin UI would block for
// N×dialTimeout worst-case on N unreachable servers. Result ordering
// matches the input spec list so the UI is stable across calls.
func (r *Registry) GlobalStatus(ctx context.Context) (builtins []ToolSummary, servers []ServerStatus) {
	r.mu.RLock()
	specsCopy := append([]ServerSpec(nil), r.globalRemoteSpec...)
	for _, t := range r.builtin {
		builtins = append(builtins, ToolSummary{Name: t.Name, Description: t.Description, Origin: "builtin"})
	}
	r.mu.RUnlock()

	servers = make([]ServerStatus, len(specsCopy))
	var wg sync.WaitGroup
	// Per-iteration `i` and `s` (Go 1.22+); each goroutine writes only
	// to its own servers[i] slot, so the per-element writes below are
	// race-free without an additional shadow variable.
	for i := range specsCopy {
		s := specsCopy[i]
		servers[i] = ServerStatus{Name: s.Name, URL: s.URL}
		wg.Add(1)
		safego.GoCtx(ctx, func() {
			defer wg.Done()
			c := NewClient(s.URL, s.Headers)
			listCtx, cancel := context.WithTimeout(ctx, dialTimeout)
			defer cancel()
			ts, err := c.ListTools(listCtx)
			if err != nil {
				servers[i].Error = err.Error()
				return
			}
			servers[i].Reachable = true
			summaries := make([]ToolSummary, 0, len(ts))
			for _, t := range ts {
				summaries = append(summaries, ToolSummary{
					Name:        t.Name,
					Description: t.Description,
					Origin:      "remote:" + s.Name,
				})
			}
			servers[i].Tools = summaries
		})
	}
	wg.Wait()
	return builtins, servers
}

// GlobalSpec returns a copy of the currently-configured server specs
// so the admin UI can pre-populate the editor without reading
// site_configs separately.
func (r *Registry) GlobalSpec() []ServerSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServerSpec, len(r.globalRemoteSpec))
	copy(out, r.globalRemoteSpec)
	return out
}

// remoteHandler is the ToolHandler implementation backing remote tools
// — it forwards Invoke to the MCP client's CallTool.
type remoteHandler struct {
	client   *Client
	toolName string
}

// Invoke satisfies ToolHandler.
func (h remoteHandler) Invoke(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	return h.client.CallTool(ctx, h.toolName, args)
}
