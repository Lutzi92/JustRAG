//go:build integration

package agui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"

	"github.com/justrag/go-backend/internal/adkbridge"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/mcp"
)

const testApp = "justrag"

// ---- database (copied from adkbridge's package-private test helpers) ----

var schemaSeq atomic.Int64

// isolatedPool returns a pool whose search_path is a fresh schema with the
// Up sections of the named migrations applied, dropped on cleanup.
func isolatedPool(t *testing.T, migrations ...string) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("agui integration tests require DB_* env (main Postgres)")
	}
	base := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	schema := fmt.Sprintf("agui_test_%d_%d", time.Now().UnixNano(), schemaSeq.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	// A schema-local knowledge_bases stub shadows public's (search_path), so
	// the migrations' kb_id FK targets it and seedKB never inserts into or
	// deletes from public.knowledge_bases, whose cascades lock tables other
	// test binaries are migrating concurrently (40P01).
	if _, err := pool.Exec(ctx, `CREATE TABLE knowledge_bases (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL)`); err != nil {
		t.Fatalf("create knowledge_bases stub: %v", err)
	}
	for _, m := range migrations {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "main", m))
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		up := strings.SplitN(string(raw), "-- +goose Down", 2)[0]
		// Test binaries of other packages add FKs to and delete from
		// public.users concurrently; a deadlock there is retried.
		for attempt := 1; ; attempt++ {
			_, err := pool.Exec(ctx, up)
			var pgErr *pgconn.PgError
			if err != nil && attempt < 5 && errors.As(err, &pgErr) && pgErr.Code == "40P01" {
				time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
				continue
			}
			if err != nil {
				t.Fatalf("apply %s: %v", m, err)
			}
			break
		}
	}
	return pool
}

// seedKB inserts into the schema-local knowledge_bases stub (see
// isolatedPool); it is dropped with the schema.
func seedKB(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO knowledge_bases (name) VALUES ($1) RETURNING id`, "agui-test-"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	return id
}

func seedUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, username, password_hash) VALUES ($1, $2, 'x')`, id, "agui-test-"+id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
	return id
}

// ---- fake model (copied from adkbridge/model_test.go, plus a per-turn delay) ----

type fakeTurn struct {
	chunks []ai.StreamChunk
	delay  time.Duration // sleep before each chunk; honours ctx cancellation
	err    error         // returned by StreamChatCompletion instead of a stream
}

type fakeClient struct {
	mu    sync.Mutex
	turns []fakeTurn
}

func (f *fakeClient) next() fakeTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.turns) == 0 {
		return fakeTurn{chunks: []ai.StreamChunk{{Content: "(no more turns)", FinishReason: "stop", Done: true}}}
	}
	t := f.turns[0]
	f.turns = f.turns[1:]
	return t
}

func (f *fakeClient) ChatCompletion(_ context.Context, _ *ai.ChatRequest) (*ai.ChatResponse, error) {
	var ch ai.ChatChoice
	for _, c := range f.next().chunks {
		ch.Message.Content += c.Content
		for _, d := range c.ToolCallDeltas {
			ch.Message.ToolCalls = append(ch.Message.ToolCalls, ai.ToolCall{ID: d.ID, Type: "function",
				Function: ai.ToolCallFunction{Name: d.Name, Arguments: d.Arguments}})
		}
		if c.FinishReason != "" {
			ch.FinishReason = c.FinishReason
		}
	}
	return &ai.ChatResponse{Choices: []ai.ChatChoice{ch}}, nil
}

func (f *fakeClient) StreamChatCompletion(ctx context.Context, _ ai.ChatRequest) (<-chan ai.StreamChunk, error) {
	turn := f.next()
	if turn.err != nil {
		return nil, turn.err
	}
	ch := make(chan ai.StreamChunk)
	go func() {
		defer close(ch)
		for _, c := range turn.chunks {
			if turn.delay > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(turn.delay):
				}
			}
			select {
			case <-ctx.Done():
				return
			case ch <- c:
			}
		}
	}()
	return ch, nil
}

// toolTurn streams a tool call split across two deltas, as vLLM does.
func toolTurn(id, name, args string) fakeTurn {
	half := len(args) / 2
	return fakeTurn{chunks: []ai.StreamChunk{
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, ID: id, Name: name, Arguments: args[:half]}}},
		{ToolCallDeltas: []ai.ToolCallDelta{{Index: 0, Arguments: args[half:]}}},
		{FinishReason: "tool_calls", Done: true},
	}}
}

func textTurn(s string) fakeTurn {
	return fakeTurn{chunks: []ai.StreamChunk{{Content: s}, {FinishReason: "stop", Done: true}}}
}

// slowTextTurn streams n content chunks 5 ms apart.
func slowTextTurn(n int) fakeTurn {
	chunks := make([]ai.StreamChunk, 0, n+1)
	for i := 0; i < n; i++ {
		chunks = append(chunks, ai.StreamChunk{Content: "x"})
	}
	chunks = append(chunks, ai.StreamChunk{FinishReason: "stop", Done: true})
	return fakeTurn{chunks: chunks, delay: 5 * time.Millisecond}
}

// ---- fixture ----

type fixture struct {
	srv          *httptest.Server
	runs         *adkbridge.RunStore
	pool         *pgxpool.Pool
	imports      *atomic.Int64
	userA, userB string
	searchArgs   atomic.Value // json.RawMessage of the latest kb_search dispatch
}

func newFixture(t *testing.T, turns ...fakeTurn) *fixture {
	return newFixtureTTL(t, 24*time.Hour, turns...)
}

func newFixtureTTL(t *testing.T, ttl time.Duration, turns ...fakeTurn) *fixture {
	t.Helper()
	return newFixtureHooks(t, ttl, nil, turns...)
}

func newFixtureHooks(t *testing.T, ttl time.Duration, hooks TurnHooks, turns ...fakeTurn) *fixture {
	t.Helper()
	return newFixtureFull(t, ttl, hooks, 0, turns...)
}

func newFixtureFull(t *testing.T, ttl time.Duration, hooks TurnHooks, runTimeout time.Duration, turns ...fakeTurn) *fixture {
	t.Helper()
	pool := isolatedPool(t, "0082_adk_sessions.sql", "0083_agent_runs.sql")
	f := &fixture{pool: pool, imports: new(atomic.Int64), userA: seedUser(t, pool), userB: seedUser(t, pool)}

	search := adkbridge.NewTool(adkbridge.ToolSpec{Name: "kb_search", Description: "search",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
		Policy:      adkbridge.PolicyFor("kb_search")},
		func(_ context.Context, _ string, _ string, args json.RawMessage) (mcp.ToolResult, error) {
			f.searchArgs.Store(args)
			return mcp.ToolResult{Text: "Treffer [1]"}, nil
		})
	imp := adkbridge.NewTool(adkbridge.ToolSpec{Name: "confluence_import", Description: "import",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"spaceKey":{"type":"string"}}}`),
		Policy:      adkbridge.PolicyFor("confluence_import")},
		func(context.Context, string, string, json.RawMessage) (mcp.ToolResult, error) {
			f.imports.Add(1)
			return mcp.ToolResult{Text: "Import gestartet"}, nil
		})
	a, err := llmagent.New(llmagent.Config{
		Name:        "kb_agent",
		Model:       adkbridge.NewModel(&fakeClient{turns: turns}, "m"),
		Instruction: "Answer from the knowledge base.",
		Tools:       []tool.Tool{search, imp},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions := adkbridge.NewPGSessionService(pool)
	r, err := runner.New(runner.Config{AppName: testApp, Agent: a, SessionService: sessions, AutoCreateSession: true})
	if err != nil {
		t.Fatal(err)
	}
	f.runs = adkbridge.NewRunStore(pool, ttl)
	f.srv = httptest.NewServer(NewHandler(Config{
		AppName:    testApp,
		Runner:     r,
		Sessions:   sessions,
		Runs:       f.runs,
		Hooks:      hooks,
		RunTimeout: runTimeout,
		Scope: func(r *http.Request) (adkbridge.Scope, error) {
			u := r.Header.Get("X-Test-User")
			if u == "" {
				return adkbridge.Scope{}, ErrUnauthorized
			}
			return adkbridge.Scope{UserID: u, KBID: r.Header.Get("X-Test-KB"), Role: "edit"}, nil
		},
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func post(t *testing.T, f *fixture, user string, body map[string]any) (int, []map[string]any) {
	t.Helper()
	return postCtx(context.Background(), t, f, user, body)
}

// postKB posts as user with the run scoped to kbID.
func postKB(t *testing.T, f *fixture, user, kbID string, body map[string]any) (int, []map[string]any) {
	t.Helper()
	return postReq(context.Background(), t, f, user, kbID, body)
}

// postCtx posts body and parses the "data: " lines of the SSE response. A
// cancelled ctx (client disconnect) returns whatever was read so far.
func postCtx(ctx context.Context, t *testing.T, f *fixture, user string, body map[string]any) (int, []map[string]any) {
	t.Helper()
	return postReq(ctx, t, f, user, "", body)
}

func postReq(ctx context.Context, t *testing.T, f *fixture, user, kbID string, body map[string]any) (int, []map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.srv.URL, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	if kbID != "" {
		req.Header.Set("X-Test-KB", kbID)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil
		}
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var evs []map[string]any
	var respBuf bytes.Buffer
	sc := bufio.NewScanner(io.TeeReader(resp.Body, &respBuf))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad SSE data %q: %v", line, err)
		}
		evs = append(evs, ev)
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		t.Fatalf("read SSE: %v", err)
	}
	lastBody = respBuf.String()
	return resp.StatusCode, evs
}

// lastBody is the raw body of the most recent post (tests are not parallel).
var lastBody string

func userMsg(thread, text string) map[string]any {
	return map[string]any{"threadId": thread, "runId": uuid.NewString(),
		"messages": []map[string]any{{"id": uuid.NewString(), "role": "user", "content": text}}}
}

func resume(thread, id, st string) map[string]any {
	return map[string]any{"threadId": thread, "runId": uuid.NewString(), "messages": []map[string]any{},
		"resume": []map[string]any{{"interruptId": id, "status": st}}}
}

func last(evs []map[string]any) map[string]any {
	if len(evs) == 0 {
		return map[string]any{}
	}
	return evs[len(evs)-1]
}

// interrupts returns the outcome interrupts of a RUN_FINISHED event.
func interrupts(ev map[string]any) []map[string]any {
	out, _ := ev["outcome"].(map[string]any)
	list, _ := out["interrupts"].([]any)
	res := make([]map[string]any, 0, len(list))
	for _, i := range list {
		if m, ok := i.(map[string]any); ok {
			res = append(res, m)
		}
	}
	return res
}

// status returns the newest run's status for thread.
func status(t *testing.T, f *fixture, thread string) string {
	t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status FROM agent_runs WHERE thread_id=$1 ORDER BY created_at DESC LIMIT 1`, thread).Scan(&s); err != nil {
		t.Fatalf("status: %v", err)
	}
	return s
}

func runStatus(t *testing.T, f *fixture, runID string) string {
	t.Helper()
	var s string
	err := f.pool.QueryRow(context.Background(), `SELECT status FROM agent_runs WHERE id=$1`, runID).Scan(&s)
	if err != nil {
		return ""
	}
	return s
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// ---- tests ----

func TestLookupRunCompletes(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "kb_search", `{"query":"q"}`), textTurn("Antwort"))
	code, evs := post(t, f, f.userA, map[string]any{"threadId": "t1", "runId": uuid.NewString(),
		"messages": []map[string]any{{"id": "m1", "role": "user", "content": "Frage"}}})
	if code != 200 || last(evs)["type"] != "RUN_FINISHED" {
		t.Fatalf("code=%d last=%v", code, last(evs))
	}
	if status(t, f, "t1") != "completed" {
		t.Fatal("run row not completed")
	}
}

func TestApprovalInterruptAndResume(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("Import läuft"))
	_, evs := post(t, f, f.userA, userMsg("t1", "Importiere HRZ"))
	in := interrupts(last(evs))
	if len(in) != 1 || in[0]["reason"] != "tool_approval" || f.imports.Load() != 0 {
		t.Fatalf("interrupts=%v imports=%d", in, f.imports.Load())
	}
	if status(t, f, "t1") != "interrupted" {
		t.Fatal("paused run row not interrupted")
	}
	// The advertised expiry is the store's TTL from the pause (24h here).
	exp, err := time.Parse(time.RFC3339, fmt.Sprint(in[0]["expiresAt"]))
	if err != nil || exp.Location() != time.UTC || time.Until(exp) < 23*time.Hour || time.Until(exp) > 25*time.Hour {
		t.Fatalf("expiresAt = %v (%v)", in[0]["expiresAt"], err)
	}
	code, evs := post(t, f, f.userA, resume("t1", in[0]["id"].(string), "resolved"))
	if code != 200 || f.imports.Load() != 1 || last(evs)["type"] != "RUN_FINISHED" {
		t.Fatalf("code=%d imports=%d last=%v", code, f.imports.Load(), last(evs))
	}
	if status(t, f, "t1") != "completed" {
		t.Fatal("resumed run row not completed")
	}
}

func TestResumeOtherUsersThreadIs404(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("x"))
	_, evs := post(t, f, f.userA, userMsg("t1", "Importiere HRZ"))
	id := interrupts(last(evs))[0]["id"].(string)
	if code, _ := post(t, f, f.userB, resume("t1", id, "resolved")); code != 404 {
		t.Fatalf("code = %d", code)
	}
	if f.imports.Load() != 0 {
		t.Fatal("action ran for the wrong user")
	}
}

func TestDoubleResumeRunsActionOnce(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("x"), textTurn("y"))
	_, evs := post(t, f, f.userA, userMsg("t1", "Importiere HRZ"))
	id := interrupts(last(evs))[0]["id"].(string)
	c1, _ := post(t, f, f.userA, resume("t1", id, "resolved"))
	c2, _ := post(t, f, f.userA, resume("t1", id, "resolved"))
	if c1 != 200 || c2 != 404 || f.imports.Load() != 1 {
		t.Fatalf("c1=%d c2=%d imports=%d", c1, c2, f.imports.Load())
	}
}

func TestExpiredResumeIs409(t *testing.T) {
	f := newFixtureTTL(t, -time.Minute, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`))
	_, evs := post(t, f, f.userA, userMsg("t1", "Importiere HRZ"))
	id := interrupts(last(evs))[0]["id"].(string)
	if code, _ := post(t, f, f.userA, resume("t1", id, "resolved")); code != 409 || f.imports.Load() != 0 {
		t.Fatalf("code=%d imports=%d", code, f.imports.Load())
	}
}

func TestNewMessageWhileInterruptedIs409(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`))
	post(t, f, f.userA, userMsg("t1", "Importiere HRZ"))
	if code, _ := post(t, f, f.userA, userMsg("t1", "noch was")); code != 409 {
		t.Fatalf("code = %d", code)
	}
}

// A resume naming an interrupt that is not open fails before the claim, so
// the real pending interrupt stays claimable.
func TestMalformedResumeIs400AndKeepsInterrupt(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("Import läuft"))
	_, evs := post(t, f, f.userA, userMsg("t1", "Importiere HRZ"))
	id := interrupts(last(evs))[0]["id"].(string)
	if code, _ := post(t, f, f.userA, resume("t1", "not-an-interrupt", "resolved")); code != 400 {
		t.Fatalf("malformed resume code = %d", code)
	}
	if status(t, f, "t1") != "interrupted" {
		t.Fatal("malformed resume consumed the pending interrupt")
	}
	code, evs := post(t, f, f.userA, resume("t1", id, "resolved"))
	if code != 200 || f.imports.Load() != 1 || last(evs)["type"] != "RUN_FINISHED" {
		t.Fatalf("code=%d imports=%d last=%v", code, f.imports.Load(), last(evs))
	}
}

func TestClientDisconnectMarksRunCancelled(t *testing.T) {
	f := newFixture(t, slowTextTurn(200)) // 200 chunks, 5ms apart
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runID := uuid.NewString()
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	postCtx(ctx, t, f, f.userA, map[string]any{"threadId": "t1", "runId": runID,
		"messages": []map[string]any{{"id": "m1", "role": "user", "content": "lang"}}})
	waitFor(t, func() bool { return runStatus(t, f, runID) == "cancelled" })
}

// Final review item 5: a run past its time budget (RunTimeout, the agent
// chat's chat_turn_budget_seconds) is stopped and failed, and the still
// connected client is told so with RUN_ERROR.
func TestRunTimeoutFailsRunWithRunError(t *testing.T) {
	f := newFixtureFull(t, 24*time.Hour, nil, 60*time.Millisecond, slowTextTurn(200)) // ~1 s of streaming
	runID := uuid.NewString()
	start := time.Now()
	code, evs := post(t, f, f.userA, map[string]any{"threadId": "t1", "runId": runID,
		"messages": []map[string]any{{"id": "m1", "role": "user", "content": "lang"}}})
	if code != 200 || last(evs)["type"] != "RUN_ERROR" || last(evs)["message"] != "run failed" {
		t.Fatalf("code=%d last=%v", code, last(evs))
	}
	if d := time.Since(start); d > 700*time.Millisecond {
		t.Fatalf("run not stopped at its budget: took %v", d)
	}
	if st := runStatus(t, f, runID); st != "failed" {
		t.Fatalf("run status = %q", st)
	}
}

func TestUnauthenticatedIs401(t *testing.T) {
	f := newFixture(t)
	if code, _ := post(t, f, "", userMsg("t1", "x")); code != 401 {
		t.Fatalf("code = %d", code)
	}
}

// User B's own pause on t1 was swept, user A has a live pause on a thread
// with the same id. B's stale resume passes the (user-scoped) session check,
// so only ClaimResume's owner filter stands between B and A's paused run.
func TestResumeNeverClaimsAnotherUsersPause(t *testing.T) {
	f := newFixture(t,
		toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`),
		toolTurn("c2", "confluence_import", `{"spaceKey":"HRZ"}`))
	_, evs := post(t, f, f.userB, userMsg("t1", "Importiere HRZ"))
	idB := interrupts(last(evs))[0]["id"].(string)
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE agent_runs SET status='abandoned' WHERE user_id=$1 AND status='interrupted'`, f.userB); err != nil {
		t.Fatal(err)
	}
	post(t, f, f.userA, userMsg("t1", "Importiere HRZ"))
	if code, _ := post(t, f, f.userB, resume("t1", idB, "resolved")); code != 404 {
		t.Fatalf("code = %d", code)
	}
	var st string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status FROM agent_runs WHERE user_id=$1 ORDER BY created_at DESC LIMIT 1`, f.userA).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "interrupted" || f.imports.Load() != 0 {
		t.Fatalf("A's pause: status=%s imports=%d", st, f.imports.Load())
	}
}

// A model-provider failure ends the run as RUN_ERROR "run failed" with the
// row failed; the provider's error text never reaches the client.
func TestProviderErrorIsSanitisedRunError(t *testing.T) {
	f := newFixture(t, fakeTurn{err: errors.New("upstream 500: secret-db-detail")})
	runID := uuid.NewString()
	code, evs := post(t, f, f.userA, map[string]any{"threadId": "t1", "runId": runID,
		"messages": []map[string]any{{"id": "m1", "role": "user", "content": "Frage"}}})
	if code != 200 || last(evs)["type"] != "RUN_ERROR" || last(evs)["message"] != "run failed" {
		t.Fatalf("code=%d events=%v", code, evs)
	}
	if strings.Contains(lastBody, "secret-db-detail") {
		t.Fatalf("provider detail leaked: %s", lastBody)
	}
	if st := runStatus(t, f, runID); st != "failed" {
		t.Fatalf("run status = %q", st)
	}
}

// uuid.Parse accepts forms Postgres rejects; the id is canonicalised before
// any row is written, so the approval is not lost.
func TestResumeWithURNRunIDRunsAction(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("Import läuft"))
	_, evs := post(t, f, f.userA, userMsg("t1", "Importiere HRZ"))
	id := interrupts(last(evs))[0]["id"].(string)
	body := resume("t1", id, "resolved")
	runID := uuid.NewString()
	body["runId"] = "urn:uuid:" + runID
	code, evs := post(t, f, f.userA, body)
	if code != 200 || f.imports.Load() != 1 || last(evs)["type"] != "RUN_FINISHED" {
		t.Fatalf("code=%d imports=%d last=%v", code, f.imports.Load(), last(evs))
	}
	if st := runStatus(t, f, runID); st != "completed" {
		t.Fatalf("resumed run status = %q", st)
	}
}

// The new run row is written before the claim; a refused claim leaves it
// failed and the expired pause abandoned.
func TestRefusedResumeLeavesNewRunFailed(t *testing.T) {
	f := newFixtureTTL(t, -time.Minute, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`))
	pause := userMsg("t1", "Importiere HRZ")
	_, evs := post(t, f, f.userA, pause)
	id := interrupts(last(evs))[0]["id"].(string)
	body := resume("t1", id, "resolved")
	if code, _ := post(t, f, f.userA, body); code != 409 {
		t.Fatalf("code = %d", code)
	}
	if st := runStatus(t, f, body["runId"].(string)); st != "failed" {
		t.Fatalf("new run status = %q", st)
	}
	if st := runStatus(t, f, pause["runId"].(string)); st != "abandoned" {
		t.Fatalf("paused run status = %q", st)
	}
}

// An approval given in KB A cannot be spent in KB B: the claim is scoped to
// the KB the run paused in, so the resume finds nothing (404), the action
// does not run, and the pause stays claimable from KB A.
func TestResumeFromOtherKBIs404AndKeepsInterrupt(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("Import läuft"))
	kbA, kbB := seedKB(t, f.pool), seedKB(t, f.pool)
	pause := userMsg("t1", "Importiere HRZ")
	_, evs := postKB(t, f, f.userA, kbA, pause)
	id := interrupts(last(evs))[0]["id"].(string)

	if code, _ := postKB(t, f, f.userA, kbB, resume("t1", id, "resolved")); code != 404 || !strings.Contains(lastBody, "no_open_interrupt") {
		t.Fatalf("resume from KB B: code=%d body=%s", code, lastBody)
	}
	if f.imports.Load() != 0 {
		t.Fatal("action ran in the wrong KB")
	}
	if st := runStatus(t, f, pause["runId"].(string)); st != "interrupted" {
		t.Fatalf("pause consumed by the refused resume: status=%q", st)
	}
	code, evs := postKB(t, f, f.userA, kbA, resume("t1", id, "resolved"))
	if code != 200 || f.imports.Load() != 1 || last(evs)["type"] != "RUN_FINISHED" {
		t.Fatalf("resume from KB A: code=%d imports=%d last=%v", code, f.imports.Load(), last(evs))
	}
}

// A thread is bound to the KB of its first run; a new message from another
// KB (paused or not) is refused before any row is written.
func TestNewMessageFromOtherKBIs409(t *testing.T) {
	f := newFixture(t, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("x"))
	kbA, kbB := seedKB(t, f.pool), seedKB(t, f.pool)
	postKB(t, f, f.userA, kbA, userMsg("t1", "Importiere HRZ")) // paused in A
	if code, _ := postKB(t, f, f.userA, kbB, userMsg("t1", "noch was")); code != 409 || !strings.Contains(lastBody, "thread_kb_mismatch") {
		t.Fatalf("paused thread: code=%d body=%s", code, lastBody)
	}
	postKB(t, f, f.userA, kbA, userMsg("t2", "Frage")) // completes in A
	for _, kb := range []string{kbB, ""} {
		if code, _ := postKB(t, f, f.userA, kb, userMsg("t2", "Folgefrage")); code != 409 || !strings.Contains(lastBody, "thread_kb_mismatch") {
			t.Fatalf("completed thread, kb %q: code=%d body=%s", kb, code, lastBody)
		}
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_runs WHERE thread_id='t2'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("refused messages wrote rows: n=%d err=%v", n, err)
	}
}

// ---- turn hooks ----

type hookCall struct {
	name, threadID, runID, text string
	chatID                      string // Scope.ChatID the hook saw
	status                      adkbridge.RunStatus
	ctxErr                      error // ctx.Err() TurnFinished saw
}

// fakeHooks maps "" to "t-new", refuses "other" and "other-kb" with
// ErrThreadNotFound and accepts every other id as-is.
type fakeHooks struct {
	mu    sync.Mutex
	calls []hookCall
	// startErr, when set, is returned by TurnStarted.
	startErr error
	// finishErr, when set, is returned by TurnFinished (with its events).
	finishErr error
}

func (h *fakeHooks) add(c hookCall) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, c)
}

func (h *fakeHooks) snapshot() []hookCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookCall(nil), h.calls...)
}

func (h *fakeHooks) ResolveThread(_ context.Context, sc adkbridge.Scope, threadID, first string) (string, error) {
	h.add(hookCall{name: "resolve", threadID: threadID, text: first, chatID: sc.ChatID})
	switch threadID {
	case "":
		return "t-new", nil
	case "other", "other-kb":
		return "", ErrThreadNotFound
	}
	return threadID, nil
}

func (h *fakeHooks) TurnStarted(_ context.Context, sc adkbridge.Scope, threadID, runID, userText string) error {
	h.add(hookCall{name: "started", threadID: threadID, runID: runID, text: userText, chatID: sc.ChatID})
	return h.startErr
}

func (h *fakeHooks) TurnFinished(ctx context.Context, sc adkbridge.Scope, threadID string, out TurnOutput) ([]events.Event, error) {
	h.add(hookCall{name: "finished", threadID: threadID, runID: out.RunID, text: out.Text, chatID: sc.ChatID,
		status: out.Status, ctxErr: ctx.Err()})
	return []events.Event{events.NewCustomEvent("justrag.message.v1", events.WithValue(map[string]any{"id": "msg-1"}))}, h.finishErr
}

func hasType(evs []map[string]any, typ string) bool {
	for _, e := range evs {
		if e["type"] == typ {
			return true
		}
	}
	return false
}

func countRuns(t *testing.T, f *fixture) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM agent_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestThreadOfOtherUserIs404(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h, textTurn("x"))
	code, _ := post(t, f, f.userA, userMsg("other", "Frage"))
	if code != 404 || !strings.Contains(lastBody, `"thread_not_found"`) {
		t.Fatalf("code=%d body=%s", code, lastBody)
	}
	if n := countRuns(t, f); n != 0 {
		t.Fatalf("runs = %d", n)
	}
	if c := h.snapshot(); len(c) != 1 || c[0].name != "resolve" {
		t.Fatalf("calls = %+v", c)
	}
}

func TestThreadOfOtherKBIs404(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h, textTurn("x"))
	code, _ := postKB(t, f, f.userA, seedKB(t, f.pool), userMsg("other-kb", "Frage"))
	if code != 404 || !strings.Contains(lastBody, `"thread_not_found"`) {
		t.Fatalf("code=%d body=%s", code, lastBody)
	}
	if n := countRuns(t, f); n != 0 {
		t.Fatalf("runs = %d", n)
	}
}

// A resume is resolved too: a thread the caller may not use is 404 before
// any session or interrupt is looked at.
func TestResumeOnForeignThreadIs404(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h)
	code, _ := post(t, f, f.userA, resume("other", "i1", "resolved"))
	if code != 404 || !strings.Contains(lastBody, `"thread_not_found"`) {
		t.Fatalf("code=%d body=%s", code, lastBody)
	}
	if c := h.snapshot(); len(c) != 1 || c[0].text != "" {
		t.Fatalf("calls = %+v", c)
	}
}

func TestResumeWithHooksRequiresThread(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h)
	code, _ := post(t, f, f.userA, resume("", "i1", "resolved"))
	if code != 400 || !strings.Contains(lastBody, `"resume_requires_thread"`) || len(h.snapshot()) != 0 {
		t.Fatalf("code=%d body=%s calls=%+v", code, lastBody, h.snapshot())
	}
}

// No thread is created for a request that carries nothing to run.
func TestNoInputDoesNotResolveThread(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h)
	code, _ := post(t, f, f.userA, map[string]any{"threadId": "", "runId": uuid.NewString(), "messages": []map[string]any{}})
	if code != 400 || len(h.snapshot()) != 0 {
		t.Fatalf("code=%d calls=%+v", code, h.snapshot())
	}
}

func TestHooksCalledOnceInOrder(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h, toolTurn("c1", "kb_search", `{"query":"q"}`), textTurn("Antwort"))
	code, evs := post(t, f, f.userA, userMsg("", "Frage"))
	if code != 200 || last(evs)["type"] != "RUN_FINISHED" {
		t.Fatalf("code=%d last=%v", code, last(evs))
	}
	if evs[0]["type"] != "RUN_STARTED" || evs[0]["threadId"] != "t-new" || last(evs)["threadId"] != "t-new" {
		t.Fatalf("thread ids: %v / %v", evs[0], last(evs))
	}
	// The hook's events come right before RUN_FINISHED.
	if pen := evs[len(evs)-2]; pen["type"] != "CUSTOM" || pen["name"] != "justrag.message.v1" {
		t.Fatalf("penultimate = %v", pen)
	}
	c := h.snapshot()
	if len(c) != 3 || c[0].name != "resolve" || c[1].name != "started" || c[2].name != "finished" {
		t.Fatalf("calls = %+v", c)
	}
	runID, _ := evs[0]["runId"].(string)
	if c[0].threadID != "" || c[0].text != "Frage" {
		t.Fatalf("resolve = %+v", c[0])
	}
	if c[1].threadID != "t-new" || c[1].runID != runID || c[1].text != "Frage" || c[1].chatID != "t-new" {
		t.Fatalf("started = %+v", c[1])
	}
	if c[2].threadID != "t-new" || c[2].runID != runID || c[2].status != adkbridge.RunCompleted || c[2].text != "Antwort" {
		t.Fatalf("finished = %+v", c[2])
	}
	if status(t, f, "t-new") != "completed" {
		t.Fatal("run row not keyed by the resolved thread")
	}
}

func TestTurnFinishedOnDisconnect(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h, slowTextTurn(200))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	postCtx(ctx, t, f, f.userA, userMsg("", "lang"))
	waitFor(t, func() bool {
		c := h.snapshot()
		return len(c) == 3 && c[2].name == "finished"
	})
	c := h.snapshot()[2]
	if c.status != adkbridge.RunCancelled || c.ctxErr != nil {
		t.Fatalf("finished = %+v", c)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(h.snapshot()); n != 3 {
		t.Fatalf("calls = %d, want 3", n)
	}
}

func TestTurnFinishedOnInterruptAndResume(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("Import läuft"))
	_, evs := post(t, f, f.userA, userMsg("", "Importiere HRZ"))
	in := interrupts(last(evs))
	if len(in) != 1 {
		t.Fatalf("last = %v", last(evs))
	}
	if pen := evs[len(evs)-2]; pen["type"] != "CUSTOM" {
		t.Fatalf("hook events missing before interrupt RUN_FINISHED: %v", pen)
	}
	c := h.snapshot()
	if len(c) != 3 || c[2].status != adkbridge.RunInterrupted {
		t.Fatalf("calls = %+v", c)
	}
	id, _ := in[0]["id"].(string)
	code, evs := post(t, f, f.userA, resume("t-new", id, "resolved"))
	if code != 200 || f.imports.Load() != 1 || last(evs)["type"] != "RUN_FINISHED" {
		t.Fatalf("code=%d imports=%d last=%v", code, f.imports.Load(), last(evs))
	}
	c = h.snapshot()
	if len(c) != 6 || c[3].name != "resolve" || c[3].threadID != "t-new" || c[3].text != "" ||
		c[4].name != "started" || c[4].text != "" || c[5].status != adkbridge.RunCompleted {
		t.Fatalf("calls = %+v", c)
	}
}

func TestRefusedResumeCallsTurnFinishedFailed(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`), textTurn("ok"))
	_, evs := post(t, f, f.userA, userMsg("", "Importiere HRZ"))
	in := interrupts(last(evs))
	if len(in) != 1 {
		t.Fatalf("last = %v", last(evs))
	}
	id, _ := in[0]["id"].(string)
	// The interrupt is open in this user's session but was recorded in the
	// default KB: claiming it from another KB is refused after Start.
	code, _ := postKB(t, f, f.userA, seedKB(t, f.pool), resume("t-new", id, "resolved"))
	if code != 404 {
		t.Fatalf("code = %d body=%s", code, lastBody)
	}
	c := h.snapshot()
	if len(c) != 6 || c[5].name != "finished" || c[5].status != adkbridge.RunFailed {
		t.Fatalf("calls = %+v", c)
	}
}

func TestTurnStartedFailureFailsRun(t *testing.T) {
	h := &fakeHooks{startErr: errors.New("db down")}
	f := newFixtureHooks(t, 24*time.Hour, h, textTurn("x"))
	code, _ := post(t, f, f.userA, userMsg("", "Frage"))
	if code != 500 || !strings.Contains(lastBody, `"internal_error"`) || strings.Contains(lastBody, "db down") {
		t.Fatalf("code=%d body=%s", code, lastBody)
	}
	if status(t, f, "t-new") != "failed" {
		t.Fatal("run row not failed")
	}
	c := h.snapshot()
	if len(c) != 3 || c[2].name != "finished" || c[2].status != adkbridge.RunFailed {
		t.Fatalf("calls = %+v", c)
	}
}

func TestScopeChatIDIsResolvedThread(t *testing.T) {
	h := &fakeHooks{}
	f := newFixtureHooks(t, 24*time.Hour, h, toolTurn("c1", "kb_search", `{"query":"q","chat_id":"forged"}`), textTurn("Antwort"))
	if code, _ := post(t, f, f.userA, userMsg("", "Frage")); code != 200 {
		t.Fatalf("code = %d", code)
	}
	raw, _ := f.searchArgs.Load().(json.RawMessage)
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("args %s: %v", raw, err)
	}
	if args["chat_id"] != "t-new" {
		t.Fatalf("chat_id = %v", args["chat_id"])
	}
}

// A completed answer the hooks failed to persist would vanish on reload, so
// the client is told the run failed; the run row stays completed.
func TestTurnFinishedErrorOnCompletedRunIsRunError(t *testing.T) {
	h := &fakeHooks{finishErr: errors.New("insert message: secret-db-detail")}
	f := newFixtureHooks(t, 24*time.Hour, h, textTurn("Antwort"))
	code, evs := post(t, f, f.userA, userMsg("", "Frage"))
	if code != 200 || last(evs)["type"] != "RUN_ERROR" || last(evs)["message"] != "run failed" {
		t.Fatalf("code=%d last=%v", code, last(evs))
	}
	if hasType(evs, "RUN_FINISHED") || hasType(evs, "CUSTOM") || strings.Contains(lastBody, "secret-db-detail") {
		t.Fatalf("body = %s", lastBody)
	}
	if status(t, f, "t-new") != "completed" {
		t.Fatal("run row must stay completed")
	}
}

// The interrupt is real and open in the DB: a hook error does not hide it.
func TestTurnFinishedErrorOnInterruptKeepsInterrupt(t *testing.T) {
	h := &fakeHooks{finishErr: errors.New("db down")}
	f := newFixtureHooks(t, 24*time.Hour, h, toolTurn("c1", "confluence_import", `{"spaceKey":"HRZ"}`))
	code, evs := post(t, f, f.userA, userMsg("", "Importiere HRZ"))
	if code != 200 || last(evs)["type"] != "RUN_FINISHED" || len(interrupts(last(evs))) != 1 {
		t.Fatalf("code=%d last=%v", code, last(evs))
	}
	if hasType(evs, "CUSTOM") || hasType(evs, "RUN_ERROR") {
		t.Fatalf("body = %s", lastBody)
	}
	if status(t, f, "t-new") != "interrupted" {
		t.Fatal("run row must stay interrupted")
	}
}
