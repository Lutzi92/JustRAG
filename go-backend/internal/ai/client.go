package ai

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/justrag/go-backend/internal/httpclient"
)

// streamScannerBufPool recycles the 64 KB initial buffer that bufio.Scanner
// uses to read SSE frames. A fresh make([]byte, 64*1024) per streaming
// session pushed ~64 KB of garbage onto each goroutine that wraps up,
// which compounded into measurable GC pressure under bursty concurrent
// streaming. Pooling the buffer lets connections released by one stream
// be reused by the next.
//
// The pool holds *[]byte so Get/Put avoid the boxing allocation that
// `any` of a value-type slice header would otherwise incur. The 1 MB
// max-token cap passed to scanner.Buffer is unchanged. Note: when a frame
// exceeds 64 KB, bufio.Scanner allocates a larger buffer INTERNALLY — it
// never writes back through our *[]byte — so the pool always recycles the
// original 64 KB array. Oversized frames simply pay a one-off allocation;
// the pool only amortizes the common small-frame case.
var streamScannerBufPool = sync.Pool{
	New: func() any { b := make([]byte, 64*1024); return &b },
}

// ssePrefix is the literal "data: " prefix every SSE data line must
// start with. Module-level []byte avoids re-allocating the converted
// literal on every bytes.HasPrefix / bytes.TrimPrefix call in the
// streaming loop.
var ssePrefix = []byte("data: ")

// sseDoneToken is the sentinel SSE payload that ends a stream. Same
// rationale as ssePrefix — keep the comparison allocation-free.
var sseDoneToken = []byte("[DONE]")

const defaultBaseURL = "https://api.openai.com/v1/"

// Client is a typed HTTP client for OpenAI-compatible APIs.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new Client. If baseURL is empty, it defaults to the
// OpenAI API base URL. A trailing slash is always ensured.
func NewClient(baseURL, apiKey string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: httpclient.New(0), // no top-level timeout; per-request contexts handle timeouts
	}
}

// clientCache memoises Client structs keyed by (baseURL, apiKey). Client has
// no mutable post-construction state and httpclient.New returns *http.Client
// values that share the package-level sharedTransport — so re-creating a
// Client per LLM call only allocated the wrapper struct, never a fresh
// transport. Eliminating those throwaway allocs across the embedding,
// completion, rerank, enumeration, stream, and health code paths removes a
// minor but steady source of GC pressure under high LLM-call concurrency.
//
// Cache shape: the key is `baseURL + "\x00" + apiKey`. The NUL separator
// prevents a (baseURL="a", apiKey="b") / (baseURL="a:b", apiKey="") collision
// since `:` can legally appear inside an apiKey value.
//
// Cache eviction: none. The (baseURL, apiKey) pair-space is bounded by the
// number of configured AI providers per deployment (small, typically 1–3),
// so unbounded growth is not a concern in production. Tests may create
// fresh httptest.NewServer URLs per case, growing the cache during the
// suite — but the cached struct itself is ~3 pointers and the transport is
// shared, so this is bounded harmless growth.
var clientCache sync.Map

// CachedClient returns a memoised Client for (baseURL, apiKey), constructing
// one on the first call and reusing it thereafter. Use this from production
// call sites instead of NewClient when the Client would otherwise be created
// and discarded per request. Tests should keep using NewClient so each test
// gets a deterministic, isolated Client.
func CachedClient(baseURL, apiKey string) *Client {
	key := baseURL + "\x00" + apiKey
	if v, ok := clientCache.Load(key); ok {
		return v.(*Client)
	}
	// LoadOrStore handles the case where another goroutine populated the
	// same key between our Load and Store — both callers return the same
	// canonical Client and the loser's NewClient result is GC'd.
	c := NewClient(baseURL, apiKey)
	actual, _ := clientCache.LoadOrStore(key, c)
	return actual.(*Client)
}

// ---------------------------------------------------------------------------
// Request / Response types
// ---------------------------------------------------------------------------

// ChatMessage is one message in a chat completion thread.
//
// Tool-calling fields (ToolCalls, ToolCallID, Name) are set only on
// assistant messages that invoked tools and on `role:"tool"` messages
// carrying tool results. Existing role:"user"/"system"/"assistant"
// content messages leave them zero-valued — they marshal away via omitempty.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	// MultiContent, when non-empty, replaces Content with an OpenAI-style
	// multimodal content-part array (text + image_url). Marshaled via the
	// custom MarshalJSON in content.go. Leave nil for text-only messages so
	// request bytes stay identical to the pre-vision path.
	MultiContent []ChatContentPart `json:"-"`
}

// ChatTool is one entry in the OpenAI-compatible tools catalog. Type is
// always "function" for our use; structured outputs and built-in OpenAI
// tools (web_search_preview etc.) are out of scope.
type ChatTool struct {
	Type     string           `json:"type"` // always "function"
	Function ChatToolFunction `json:"function"`
}

// ChatToolFunction is the schema-side definition the model sees: a name,
// a one-paragraph description, and a JSON-Schema-shaped Parameters blob.
// Parameters is a raw message so callers pass the MCP tool's existing
// input_schema through verbatim.
type ChatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall is one assistant-emitted function call. Arguments is a JSON-
// encoded string per the OpenAI shape (NOT a structured object) — vLLM
// and LiteLLM both forward it that way; downstream dispatch parses it
// against the tool's schema.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // always "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the name and arguments of an assistant-emitted
// function call.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatRequest is the request body for the chat completions endpoint.
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	Temperature    *float64        `json:"temperature,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	// MaxTokens caps completion length. Zero means "provider default".
	// Used by the Qwen3-Reranker yes/no scorer (max_tokens=1) so we pay
	// for a single generation step per (query, doc) pair.
	MaxTokens int `json:"max_tokens,omitempty"`
	// Logprobs requests token-level log probabilities. Required alongside
	// TopLogprobs when the caller needs to inspect alternatives that weren't
	// the greedy pick — the Qwen3-Reranker path softmaxes the "yes"/"no"
	// logprobs to derive a calibrated relevance score.
	Logprobs bool `json:"logprobs,omitempty"`
	// TopLogprobs asks the provider to return the N most likely tokens at
	// each position, each with its logprob. vLLM and sglang cap this around
	// 20; OpenAI caps it at 20 as well. Callers must set Logprobs=true for
	// this field to take effect.
	TopLogprobs int `json:"top_logprobs,omitempty"`
	// Tools is the OpenAI-compatible function-calling catalog. When non-empty,
	// the model may emit `tool_calls` in its response instead of (or alongside)
	// `content`. Tools, ToolChoice, ChatMessage.ToolCalls, ChatMessage.ToolCallID,
	// and ChatChoice.FinishReason form the answer-time tool-calling surface
	// (chat_answer_tools_enabled). Leave nil to preserve pre-tool-calling
	// request bytes for legacy call sites.
	Tools []ChatTool `json:"tools,omitempty"`
	// ToolChoice controls when the model may call tools. Accepts "auto" (default
	// when Tools is set), "none" (force a content-only completion), or a
	// structured forced choice `{type:"function",function:{name}}`. Typed as
	// `any` so the OpenAI variants round-trip without a sum-type stand-in.
	ToolChoice any `json:"tool_choice,omitempty"`
}

// ResponseFormat requests a structured output from the model. OpenAI's
// Structured Outputs and vLLM's guided_json backend both accept this shape
// — vLLM's guided decoding generates tokens constrained to the schema, so
// the response is guaranteed to be valid JSON. LiteLLM (our proxy)
// forwards response_format to vLLM without rewriting. Use Type="json_schema"
// with a schema for strict validation, or Type="json_object" for a weaker
// "any JSON" contract (less reliable for deeply nested shapes).
type ResponseFormat struct {
	Type       string              `json:"type"`                  // "json_schema" or "json_object"
	JSONSchema *ResponseJSONSchema `json:"json_schema,omitempty"` // required when Type == "json_schema"
}

// ResponseJSONSchema is the schema wrapper expected by OpenAI-compatible
// Structured Outputs. Strict=true disables generation paths outside the
// schema; recommended for extraction workloads where partial/invalid JSON
// would force a fallback parser.
type ResponseJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema"`
}

// ChatChoice is a single completion choice returned by the API.
//
// The Message field carries two reasoning fields for provider compatibility:
//   - ReasoningContent (json:"reasoning_content") — used by DeepSeek and QwQ.
//   - Reasoning (json:"reasoning") — used by vLLM and some OpenAI-compatible providers.
//
// When both are present, ReasoningContent takes precedence (see extractReasoning
// in completion.go). Callers should generally use the result of extractReasoning
// rather than inspecting these fields directly; if reading them manually, check
// ReasoningContent first, then fall back to Reasoning.
type ChatChoice struct {
	Message struct {
		Content          string     `json:"content,omitempty"`
		ReasoningContent string     `json:"reasoning_content,omitempty"`
		Reasoning        string     `json:"reasoning,omitempty"`
		ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	// Logprobs is the OpenAI-compatible token-level logprob payload. Only
	// populated when the request set Logprobs=true. Nil otherwise.
	Logprobs     *ChatLogprobs `json:"logprobs,omitempty"`
	FinishReason string        `json:"finish_reason,omitempty"`
}

// ChatLogprobs is the wrapper object OpenAI/vLLM return when Logprobs is
// requested. It carries one entry per generated token.
type ChatLogprobs struct {
	Content []ChatLogprobToken `json:"content"`
}

// ChatLogprobToken is a single generated token and the N most likely
// alternatives at that position (present when top_logprobs > 0).
type ChatLogprobToken struct {
	Token       string             `json:"token"`
	Logprob     float64            `json:"logprob"`
	TopLogprobs []ChatLogprobEntry `json:"top_logprobs,omitempty"`
}

// ChatLogprobEntry is one of the top-K alternatives at a given token
// position, with its log probability.
type ChatLogprobEntry struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

// ChatUsage carries token counters returned in a chat completion response.
//
// Cache-hit telemetry is reported under two non-overlapping shapes by
// different providers:
//   - OpenAI: nested `prompt_tokens_details.cached_tokens`
//   - DeepSeek: top-level `prompt_cache_hit_tokens`
//
// Use ChatUsage.CachedTokens() to read the normalized value.
type ChatUsage struct {
	PromptTokens         int `json:"prompt_tokens,omitempty"`
	CompletionTokens     int `json:"completion_tokens,omitempty"`
	TotalTokens          int `json:"total_tokens,omitempty"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"` // DeepSeek
	PromptTokensDetails  struct {
		CachedTokens int `json:"cached_tokens,omitempty"` // OpenAI
	} `json:"prompt_tokens_details,omitempty"`
}

// CachedTokens returns the cached-prompt-token count, preferring OpenAI's
// nested prompt_tokens_details.cached_tokens and falling back to DeepSeek's
// top-level prompt_cache_hit_tokens. Returns zero when neither provider
// reported a cache hit.
func (u ChatUsage) CachedTokens() int {
	if u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return u.PromptCacheHitTokens
}

// ChatResponse is the response from the chat completions endpoint.
type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
	Usage   ChatUsage    `json:"usage,omitempty"`
}

// ToolCallDelta is one fragment of a streamed tool call. The OpenAI
// streaming protocol splits a single tool_call across many chunks: the
// first delta for an `index` carries id+name+the leading slice of
// arguments; subsequent deltas for the same index carry only the
// continuation of arguments. Callers (chat/answer_tools.go) accumulate
// per-index by concatenating Arguments and keeping the first non-empty
// ID/Name they see.
type ToolCallDelta struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"-"` // hoisted from function.name during parsing
	Arguments string `json:"-"` // hoisted from function.arguments during parsing
}

// StreamChunk is a single chunk delivered over a streaming chat completion.
type StreamChunk struct {
	Content          string
	ReasoningContent string
	Reasoning        string
	Done             bool

	// ToolCallDeltas is the per-chunk slice of tool-call fragments. Empty
	// on chunks that carry plain content. Order within the slice follows
	// the upstream chunk's order; callers must respect Index across chunks
	// to handle the rare parallel-tool-call case.
	ToolCallDeltas []ToolCallDelta
	// FinishReason is the provider-supplied reason for this round ending:
	// "stop" (model emitted answer), "tool_calls" (model wants tools run),
	// "length" (max_tokens hit). Only the final chunk carries it; intermediate
	// chunks leave it empty.
	FinishReason string

	// Err is non-nil when the stream terminated abnormally — a transport
	// error mid-stream (connection reset) or an SSE frame exceeding the
	// scanner cap (bufio.ErrTooLong). Only ever set on a chunk with
	// Done=true. Content delivered before this chunk is TRUNCATED output:
	// callers must not persist it as a complete AI message.
	Err error
}

// streamChatRequest is an internal wrapper that adds stream:true to a ChatRequest
// for serialisation without modifying the ChatRequest type itself.
type streamChatRequest struct {
	ChatRequest
	Stream bool `json:"stream"`
}

// streamDelta is the delta object inside a streaming choice.
type streamDelta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// streamChoice is a single choice in a streaming response chunk.
type streamChoice struct {
	Delta        streamDelta `json:"delta"`
	Index        int         `json:"index"`
	FinishReason string      `json:"finish_reason"`
}

// streamResponse is the JSON payload for each SSE data line.
type streamResponse struct {
	Choices []streamChoice `json:"choices"`
}

// EmbeddingRequest is the request body for the embeddings endpoint.
type EmbeddingRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"` // string or []string
	// LateChunking, when true, asks the provider to encode the concatenated
	// input list with a long-context embedder and return one mean-pooled
	// vector per element (Jina-style "late chunking"). Forwarded as
	// `late_chunking: true` in the JSON body. Providers that don't
	// recognise the field ignore it and return standard embeddings.
	LateChunking bool `json:"late_chunking,omitempty"`
}

// EmbeddingData holds a single embedding vector with its index.
type EmbeddingData struct {
	Index     int       `json:"index"`
	Embedding []float64 `json:"embedding"`
}

// EmbeddingResponse is the response from the embeddings endpoint.
type EmbeddingResponse struct {
	Data []EmbeddingData `json:"data"`
}

// RerankRequest is the request body for the rerank endpoint.
type RerankRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            int      `json:"top_n,omitempty"`
	ReturnDocuments bool     `json:"return_documents"`
}

// RerankResult holds a single reranked document result.
// Both relevance_score and score are parsed because different providers use
// different field names; use NormalizedScore to read whichever is populated.
type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
	Score          float64 `json:"score"`
}

// NormalizedScore returns RelevanceScore when positive, otherwise Score.
// Mirrors the ChatUsage.CachedTokens() pattern for provider-shape normalization.
func (r RerankResult) NormalizedScore() float64 {
	if r.RelevanceScore > 0 {
		return r.RelevanceScore
	}
	return r.Score
}

// RerankResponse is the response from the rerank endpoint.
type RerankResponse struct {
	Results []RerankResult `json:"results"`
}

// APIError is returned when the API responds with a non-2xx status code.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error %d: %s", e.StatusCode, e.Body)
}

// ---------------------------------------------------------------------------
// Public methods
// ---------------------------------------------------------------------------

// ChatCompletion calls the chat/completions endpoint with a 120s timeout.
func (c *Client) ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	var resp ChatResponse
	if err := c.doJSON(ctx, http.MethodPost, "chat/completions", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// StreamChatCompletion calls the chat/completions endpoint with stream:true and
// returns a channel of StreamChunk values. The final chunk always has Done:true.
// The caller must drain the channel to completion; cancelling ctx will cause the
// goroutine to terminate and close the channel.
func (c *Client) StreamChatCompletion(ctx context.Context, req ChatRequest) (<-chan StreamChunk, error) {
	body := streamChatRequest{ChatRequest: req, Stream: true}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal stream request: %w", err)
	}

	url := c.baseURL + "chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("ai: create stream request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: do stream request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(bodyBytes)}
	}

	// Buffer 1 lets the SSE-parser goroutine read the next chunk while the
	// downstream transformer (stream.go think-tag state machine) is still
	// working on the current one. Backpressure is preserved — the channel
	// fills after one chunk — but a slow transformer can't immediately stall
	// the JSON parser.
	ch := make(chan StreamChunk, 1)

	// Bare go (not safego.Go) on purpose: the recover/Body.Close/close(ch) LIFO
	// defer chain below must run entirely under this goroutine's control so the
	// panic-recovery can forward a terminal chunk while `ch` is still open. A
	// safego.Go wrapper would add an outer recovery layer that fires after
	// close(ch), unable to guarantee that ordering.
	go func() {
		// Defers execute LIFO. Registration order below is intentional and must
		// not be changed:
		//   1. close(ch)        — registered first, runs last
		//   2. resp.Body.Close  — registered second, runs middle
		//   3. recover()        — registered last, runs first on panic
		// On panic the recovery fires while ch is still open, so the
		// non-blocking select can forward a terminal chunk without racing a
		// concurrent close. The select's default branch prevents leaking this
		// goroutine if the consumer has already exited (mirrors stream.go).
		defer close(ch)
		defer resp.Body.Close()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("stream goroutine panic recovered",
					"error", r,
					"stack", string(debug.Stack()),
				)
				select {
				case ch <- StreamChunk{Done: true}:
				default:
				}
			}
		}()

		scanner := bufio.NewScanner(resp.Body)
		// Increase scanner buffer to 1 MB — the default 64 KB limit silently
		// truncates SSE frames with large reasoning/content deltas. Initial
		// 64 KB comes from streamScannerBufPool so we reuse buffers across
		// concurrent streaming sessions instead of allocating fresh per stream.
		bufPtr := streamScannerBufPool.Get().(*[]byte)
		// Always returns the original 64 KB array: bufio.Scanner grows its
		// buffer internally without writing back through bufPtr (see the
		// pool's doc comment).
		defer streamScannerBufPool.Put(bufPtr)
		scanner.Buffer((*bufPtr)[:0], 1024*1024)
		for scanner.Scan() {
			// Every send to ch must select on ctx.Done() — otherwise a
			// downstream consumer that exits (e.g. HTTP client disconnect)
			// will deadlock this goroutine on an unbuffered send and leak it
			// until the process restarts.
			//
			// The select below is non-blocking (default branch). That's
			// intentional and safe: scanner.Scan() is what would actually
			// block, and when the consumer/client disconnects, the HTTP/TLS
			// teardown closes resp.Body which causes Scan() to return false
			// and exit the loop. The ctx check is a fast best-effort poll
			// between lines; the truly interrupt-driven cancellation lives
			// on every send to ch below.
			select {
			case <-ctx.Done():
				select {
				case ch <- StreamChunk{Done: true}:
				default:
				}
				return
			default:
			}

			// scanner.Bytes() returns a slice into the scanner's internal
			// buffer — valid only until the next Scan() call, but that's
			// fine here because json.Unmarshal copies any data it retains
			// (string fields in streamResponse). Avoids the string→[]byte
			// allocation that `json.Unmarshal([]byte(scanner.Text()), …)`
			// forces on every one of the ~200–500 SSE frames a streaming
			// chat completion delivers.
			line := scanner.Bytes()

			// Skip blank lines.
			if len(line) == 0 {
				continue
			}

			// SSE lines must start with "data: ".
			if !bytes.HasPrefix(line, ssePrefix) {
				continue
			}

			payload := line[len(ssePrefix):]

			if bytes.Equal(payload, sseDoneToken) {
				select {
				case ch <- StreamChunk{Done: true}:
				case <-ctx.Done():
				}
				return
			}

			var sr streamResponse
			if err := json.Unmarshal(payload, &sr); err != nil {
				// Skip malformed lines.
				continue
			}

			if len(sr.Choices) == 0 {
				continue
			}

			delta := sr.Choices[0].Delta
			hasTools := len(delta.ToolCalls) > 0
			finishReason := sr.Choices[0].FinishReason
			if delta.Content == "" && delta.ReasoningContent == "" && delta.Reasoning == "" && !hasTools && finishReason == "" {
				continue
			}

			// Populate both Reasoning and ReasoningContent consistently:
			// use whichever field the provider populated.
			reasoning := delta.Reasoning
			if reasoning == "" {
				reasoning = delta.ReasoningContent
			}
			reasoningContent := delta.ReasoningContent
			if reasoningContent == "" {
				reasoningContent = delta.Reasoning
			}

			var toolDeltas []ToolCallDelta
			if hasTools {
				toolDeltas = make([]ToolCallDelta, 0, len(sr.Choices[0].Delta.ToolCalls))
				for _, tc := range sr.Choices[0].Delta.ToolCalls {
					toolDeltas = append(toolDeltas, ToolCallDelta{
						Index:     tc.Index,
						ID:        tc.ID,
						Type:      tc.Type,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					})
				}
			}

			select {
			case ch <- StreamChunk{
				Content:          delta.Content,
				ReasoningContent: reasoningContent,
				Reasoning:        reasoning,
				ToolCallDeltas:   toolDeltas,
				FinishReason:     finishReason,
			}:
			case <-ctx.Done():
				return
			}
		}

		// A scan failure (connection reset mid-stream, or a frame over the
		// 1 MB cap → bufio.ErrTooLong) ends the loop exactly like clean EOF.
		// Without checking scanner.Err() here, a truncated stream would be
		// reported as a clean completion and downstream callers would
		// persist the partial answer as a complete AI message.
		if err := scanner.Err(); err != nil {
			slog.Warn("stream scan error — content truncated", "error", err)
			select {
			case ch <- StreamChunk{Done: true, Err: fmt.Errorf("ai: stream read: %w", err)}:
			case <-ctx.Done():
			}
			return
		}

		// Scanner exhausted without [DONE] — send Done anyway.
		select {
		case ch <- StreamChunk{Done: true}:
		case <-ctx.Done():
		}
	}()

	return ch, nil
}

// Embedding calls the embeddings endpoint. The caller is responsible for
// setting any desired timeout on ctx. Results are sorted by Index.
func (c *Client) Embedding(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	var resp EmbeddingResponse
	if err := c.doJSON(ctx, http.MethodPost, "embeddings", req, &resp); err != nil {
		return nil, err
	}
	slices.SortFunc(resp.Data, func(a, b EmbeddingData) int {
		return cmp.Compare(a.Index, b.Index)
	})
	return &resp, nil
}

// Rerank calls the rerank endpoint with a 30s timeout.
func (c *Client) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var resp RerankResponse
	if err := c.doJSON(ctx, http.MethodPost, "rerank", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListModels calls the models endpoint with a 10s timeout and returns nil on
// success or an error otherwise.
func (c *Client) ListModels(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return c.doJSON(ctx, http.MethodGet, "models", nil, nil)
}

// ---------------------------------------------------------------------------
// Internal helper
// ---------------------------------------------------------------------------

// doJSONBufPool recycles the buffer used to encode request bodies so the
// Embedding / Rerank / ChatCompletion hot paths don't allocate a fresh
// []byte per call. The buffer is held until doJSON returns (after the
// response is fully read), so net/http never calls req.GetBody against a
// buffer that has been reset and reused by another goroutine.
var doJSONBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// doJSON performs a JSON HTTP request and decodes the response into result.
// If body is nil a request without a body is sent (suitable for GET).
// If result is nil the response body is discarded after status checking.
func (c *Client) doJSON(ctx context.Context, method, path string, body any, result any) error {
	// Optional process-wide cap on in-flight unary requests per endpoint
	// (AI_MAX_CONCURRENT_REQUESTS; no-op when unset). See concurrency.go for
	// why the streaming path is deliberately excluded.
	release, err := acquireProviderSlot(ctx, c.baseURL)
	if err != nil {
		return err
	}
	defer release()

	var reqBody io.Reader
	if body != nil {
		buf := doJSONBufPool.Get().(*bytes.Buffer)
		buf.Reset()
		// json.NewEncoder appends a trailing '\n' that json.Marshal omits;
		// HTTP JSON APIs tolerate the trailing whitespace.
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			doJSONBufPool.Put(buf)
			return fmt.Errorf("ai: marshal request: %w", err)
		}
		reqBody = buf
		defer doJSONBufPool.Put(buf)
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("ai: create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ai: do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Error path: read body fully for the error message.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{
			StatusCode: resp.StatusCode,
			Body:       string(errBody),
		}
	}

	// Stream-decode directly from the response body to avoid buffering
	// the entire response (especially large for embedding/rerank responses).
	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("ai: decode response: %w", err)
		}
	}

	return nil
}
