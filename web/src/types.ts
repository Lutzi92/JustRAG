export interface MessageSource {
    index?: number;  // 1-based citation number
    fileName: string;
    fileId?: string;
    content: string;
    score: number;
    pages?: number[];
    nodeKind?: string;
}

// CitationStatus is one entry in MessageVerification.citations — produced
// by the deterministic n-gram validator. `n` is the 1-based citation number
// as it appears in the answer ([3] → n=3). Discriminated union enforces
// the backend invariant: verified=true has no reason, verified=false
// always carries one of the stable keys "out_of_range" / "no_overlap".
export type CitationStatus =
    | { n: number; verified: true; method?: 'ngram' | 'semantic' }
    | { n: number; verified: false; reason: 'out_of_range' | 'no_overlap' };

// FlaggedClaimStatus is one entry produced by the Phase 3 §3.3
// factuality verifier. The `reason` is a closed enum mirroring the
// backend: unsupported / contradicted / out_of_scope. cited_n is the
// 1-based citation number the answer attached to this claim, or 0
// when the claim has no [N] citation.
export interface FlaggedClaimStatus {
    claim_text: string;
    reason: 'unsupported' | 'contradicted' | 'out_of_scope';
    cited_n?: number;
}

// FactualityVerification is the on-disk shape persisted under
// MessageVerification.factuality. A nil/undefined pointer means the
// verifier did NOT run; an empty flagged_claims array means it ran
// and the answer was clean.
export interface FactualityVerification {
    flagged_claims: FlaggedClaimStatus[];
}

// ChunkRelevanceStatus mirrors the backend ai.ChunkRelevance (ISREL):
// per-source relevance verdicts from the AP-D2 Self-RAG verifier.
export interface ChunkRelevanceStatus {
    n: number;
    verdict: 'relevant' | 'partially_relevant' | 'irrelevant';
}

// UsefulnessStatus mirrors the backend ai.UsefulnessVerdict (ISUSE):
// whether the answer actually addresses the question.
export interface UsefulnessStatus {
    verdict: 'yes' | 'partial' | 'no';
    reason: string;
}

// SelfRAGVerification is the AP-D2 unified-verifier shape persisted under
// MessageVerification.self_rag. When present it REPLACES `factuality` — the
// two are mutually exclusive in the post-response path — and the flagged
// claims live under `issup` (mirroring the backend JSON tags isrel/issup/isuse).
export interface SelfRAGVerification {
    isrel: ChunkRelevanceStatus[];
    issup: FlaggedClaimStatus[];
    isuse: UsefulnessStatus;
}

export interface MessageVerification {
    verified: boolean;          // LLM factchecker verdict; default false when factcheck didn't run
    score: number;              // 0-100; 0 when factcheck didn't run
    issues: string[];           // empty when factcheck didn't run
    citations?: CitationStatus[]; // present only when the n-gram validator ran
    factuality?: FactualityVerification | null; // present only when the verifier ran
    self_rag?: SelfRAGVerification | null; // present only when Self-RAG ran; replaces `factuality`
}

// TrajChunkRef is the per-step chunk preview surfaced to the trajectory
// panel. The full chunk content lives on the existing `sources` SSE event;
// this carries just enough metadata to render "found 3 chunks from these
// files" without doubling the SSE payload.
export interface TrajChunkRef {
    idx: number;
    file_name: string;
    score: number;
}

// TrajectoryEvent is the unified envelope for streaming agentic-chat
// progress to the frontend. The orchestrators (agentic_chat,
// plan_execute_chat) and the standard CRAG path each emit one event per
// decision point; the renderer aggregates them into a "Reasoning steps"
// panel above the answer.
//
// Stage values:
//   plan             Plan-and-Execute planner emitted a sub-query list
//   iterate          Plan-and-Execute iterate stage ran one round
//   hop              Agentic-chat ran one hop
//   decision         CRAG branch decision (proceed/rewrite/abstain) or other
//                    non-search reasoning step
//   answer           Orchestrator finished and is about to hand off to the LLM
//   refine_start     AP-A2 factuality gate decided to refine; carries the
//                    chosen mode (surgical|holistic) and the trigger count.
//                    Frontend paints "Korrigiert nach Faktencheck …" while the
//                    refine LLM call runs.
//   refine_complete  Refine call finished. Carries the word-level diff (added/
//                    removed/kept chunks), the mode that ran, and the v2
//                    verifier's residual claim count. ClaimsAfter = -1 is the
//                    sentinel for "v2 verifier errored — keep the refined
//                    text but flag the verdict as unknown".
export interface DiffChunk {
    kind: 'added' | 'removed' | 'kept';
    text: string;
}
export interface TrajectoryEvent {
    stage: 'plan' | 'iterate' | 'hop' | 'decision' | 'answer' | 'refine_start' | 'refine_complete' | string;
    step?: number;
    query?: string;
    queries?: string[];
    chunks?: TrajChunkRef[];
    decision?: string;
    reason?: string;
    findings?: number;
    mode?: 'surgical' | 'holistic' | string;
    diff?: DiffChunk[];
    claims_before?: number;
    claims_after?: number;
    // refine_complete: full refined answer with original whitespace/newlines.
    // Set whenever the refine pass actually changed the answer. Use this to
    // rebuild message content — the word-level diff is newline-lossy.
    refined_text?: string;
}

export interface TableColumn {
    key: string;
    label: string;
    type: 'string' | 'number' | 'date';
    deriveFrom?: string;
}

export interface StructuredTable {
    columns: TableColumn[];
    rows: Array<Record<string, unknown>>;
    truncated: boolean;
    totalFiles: number;
}

export interface Message {
    id?: string;              // DB UUID, undefined during streaming
    parentMessageId?: string; // Parent in tree
    role: 'user' | 'ai';
    content: string;
    sources?: MessageSource[];
    structured_table?: StructuredTable | null;
    isEnhanced?: boolean;
    reasoning?: string;
    followUpQuestions?: string[];
    childIds?: string[];      // Computed client-side
    feedback?: 'positive' | 'negative' | null;
    feedbackComment?: string;
    feedbackUpdatedAt?: string;
    isDeepSearch?: boolean;
    verification?: MessageVerification | null;
    traceId?: string | null;
    // Attribution: the user-created team/agent that answered this turn (if
    // any). Populated from the backend's camelCase `teamId`/`agentId` on AI
    // message rows (omitted when nil) and stamped client-side from the
    // current agentSelection while streaming. Drives the small chip in the
    // AI-message chrome; a lookup miss (team/agent later deleted) renders
    // nothing rather than an empty label.
    teamId?: string | null;
    agentId?: string | null;
    // Streaming trajectory: one entry per orchestrator decision point
    // (plan/iterate/hop/decision/answer). Populated by useChatStream from
    // the `agentTrajectory` SSE events. Undefined when no events arrived
    // (legacy path or non-streaming).
    trajectory?: TrajectoryEvent[];
    // In-chat document comparison findings, populated by useChatStream from
    // the `comparisonFindings` SSE event during a comparison turn.
    comparisonFindings?: ComparisonFinding[];
}

export interface ComparisonFinding {
    mode: 'contradiction' | 'formal' | 'completeness';
    severity: 'high' | 'medium' | 'low';
    sectionIdx: number;
    uploadQuote: string;
    issue: string;
    citedFileIds: string[];
    citedQuote: string;
}

export interface BranchInfo {
    parentMessageId: string;
    siblingIds: string[];
    currentIndex: number;
    total: number;
}

export interface User {
    id: string;
    username: string;
    role: string;
    firstName?: string;
    lastName?: string;
    email?: string;
    authMethod?: string;
}

// KbRole is the caller's role on a KB, per kb_members: 'view' < 'edit' <
// 'admin' < 'owner'. A KnowledgeBase.myRole of undefined means the caller has
// no kb_members row — an implicit viewer on a published global KB.
export type KbRole = 'view' | 'edit' | 'admin' | 'owner';

// KbAssignableRole is the subset a share/invite dialog may grant, mirroring
// the backend's kbaccess.Assignable check on PUT /members/{userId} and
// POST /members/bulk. 'owner' is deliberately absent: ownership moves only
// through the explicit transfer endpoint, never through a role picker.
export type KbAssignableRole = 'view' | 'edit' | 'admin';

// KbMember is one row of GET /api/kb/{id}/members' `members` array (Task 9,
// consumed by MembersModal). firstName/lastName mirror the nullable DB
// columns; role is never absent — every member row has one.
export interface KbMember {
    userId: string;
    username: string;
    firstName?: string | null;
    lastName?: string | null;
    role: KbRole;
    createdAt: string;
}

export interface KnowledgeBase {
    id: string;
    name: string;
    description: string | null;
    userId: string | null;
    createdAt: string;
    ownerFirstName?: string | null;
    ownerLastName?: string | null;
    ownerUsername?: string | null;
    isPro: boolean;
    isGlobal?: boolean;
    isPublished?: boolean;
    headerText?: string | null;
    examplePrompts?: string | null;
    aiConfigId: string | null;
    chatModel: string | null;
    embeddingModel: string | null;
    rerankModel: string | null;
    ttsModel: string | null;
    language?: 'en' | 'de';
    systemPrompt?: string | null;
    // Card information-scent metadata (improvement #6) — returned by the KB list
    // endpoints so Home cards can show size + freshness without a per-card fetch.
    fileCount?: number;
    failedFileCount?: number;
    processingFileCount?: number;
    turnCount?: number;
    lastActivityAt?: string | null;
    // Caller's own role + total member count — returned by the same list
    // endpoints (Task 8). myRole is undefined for an implicit viewer with no
    // kb_members row (e.g. a published global KB nobody explicitly joined).
    myRole?: KbRole;
    memberCount?: number;
    // visibility is the stored truth since migration 0065; isGlobal above is
    // the derived mirror kept for API compatibility.
    visibility?: 'private' | 'public';
}

// A curator of a global KB: a kb_members row with role='admin'. `id` is the
// *user* id — the wire shape predates kb_members and the backend keeps it. It
// used to also declare a `userId` field the API never sent, so the remove
// button posted `undefined` as the user id.
export interface GlobalKbEditor {
    id: string;
    username: string;
    firstName?: string | null;
    lastName?: string | null;
    createdAt: string;
}

export interface SafeAIConfig {
    id: string;
    name: string;
    provider: string;
    is_active: boolean;
    chat_models: string[];
    embedding_models: string[];
    rerank_models: string[];
    tts_models: string[];
    reasoning_models: string[];
}

export interface FileEntry {
    id: string;
    name: string;
    type: string;
    status: 'pending' | 'processing' | 'completed' | 'error';
    progress: number;
    origin: string;
    createdAt: string;
    selected?: boolean;
    rssFeedId?: string;
    gitRepoSourceId?: string;
    errorStage?: string;
    errorMessage?: string;
    currentStage?: string;
    stageIndex?: number;
    stageTotal?: number;
}

export interface ChatEntry {
    id: string;
    kbId: string;
    userId: string;
    title: string;
    type?: 'chat' | 'research' | 'academic_research';
    createdAt: string;
    updatedAt: string;
    teamId?: string | null;
    agentId?: string | null;
}

export interface FlashcardItem {
    front: string;
    back: string;
}

export interface ChartContentData {
    description?: string;
    type: string;
    config: Record<string, unknown>;
    series?: Record<string, unknown>[];
}

export interface PresentationContentData {
    filePath?: string;
    markdown?: string;
    summary?: string;
}

export interface PodcastContentData {
    filePath?: string;
    audioPath?: string;
    script?: string;
}

export interface AnalysisContentData {
    text?: string;
    markdown?: string;
    summary?: string;
}

export interface AbstractContentData {
    text: string;
    abstractType: 'academic' | 'executive';
}

export interface QuizItem {
    question: string;
    options: string[];
    answerIndex: number;
    explanation?: string;
}

interface GeneratedContentBase {
    id: string;
    kbId: string;
    userId: string;
    title: string;
    createdAt: string;
}

export type GeneratedContent = GeneratedContentBase & (
    | { type: 'flashcards'; content: FlashcardItem[]; }
    | { type: 'presentation'; content: PresentationContentData; }
    | { type: 'chart'; content: ChartContentData; }
    | { type: 'podcast'; content: PodcastContentData; }
    | { type: 'analysis'; content: AnalysisContentData; }
    | { type: 'abstract'; content: AbstractContentData; }
    | { type: 'research'; content: AnalysisContentData; }
    | { type: 'briefing_doc'; content: AnalysisContentData; }
    | { type: 'faq'; content: AnalysisContentData; }
    | { type: 'study_guide'; content: AnalysisContentData; }
    | { type: 'timeline'; content: AnalysisContentData; }
    | { type: 'quiz'; content: QuizItem[]; }
);

export interface RssFeed {
    id: string;
    kbId: string;
    url: string;
    title: string | null;
    pollInterval: number;
    status: 'active' | 'paused' | 'error';
    errorMessage: string | null;
    consecutiveFailures: number;
    lastPolledAt: string | null;
    itemCount: number;
    fetchFullText: boolean;
    createdAt: string;
}

// ============================================================================
// Confluence Types
// ============================================================================

export interface ConfluenceConnection {
    id: string;
    userId: string;
    token: string; // masked
    displayName: string | null;
    status: 'active' | 'error';
    errorMessage: string | null;
    lastVerifiedAt: string | null;
    createdAt: string;
}

export interface ConfluenceSource {
    id: string;
    kbId: string;
    connectionId: string;
    spaceKey: string;
    rootPageId: string | null;
    rootPageTitle: string | null;
    includeAttachments: boolean;
    syncInterval: number | null;
    status: 'active' | 'syncing' | 'error' | 'paused';
    errorMessage: string | null;
    consecutiveFailures: number;
    lastSyncedAt: string | null;
    pageCount: number;
    syncProgress: number;
    syncTotal: number;
    createdAt: string;
}

export interface GitRepoSource {
    id: string;
    kbId: string;
    repoUrl: string;
    isPrivate: boolean;
    branch: string | null;
    hasToken: boolean;
    status: 'active' | 'syncing' | 'error' | 'paused';
    errorMessage: string | null;
    consecutiveFailures: number;
    lastSyncedAt: string | null;
    lastCommitSha: string | null;
    fileCount: number;
    syncProgress: number;
    syncTotal: number;
    createdAt: string;
}

export interface ConfluenceSpace {
    key: string;
    name: string;
    type: string;
}

export interface ConfluencePage {
    id: string;
    title: string;
    version?: { number: number; when: string };
}

export interface ConfluencePageWithPath {
    id: string;
    title: string;
    ancestorTitles: string[];
}

export interface ConfluenceConnectionInfo {
    connection: ConfluenceConnection | null;
    baseUrl: string | null;
    enabled: boolean;
}

// ============================================================================
// Data Explorer Types
// ============================================================================

export interface FileSchema {
    columns: { name: string; type: string }[];
    rowCount: number;
    preview: Record<string, unknown>[];
}

export interface DataExplorerConfig {
    chartType: 'bar' | 'line' | 'area' | 'pie' | 'scatter';
    title: string;
    description?: string;
    query: {
        groupBy?: string;
        groupByTransform?: { fn: 'DATE_TRUNC'; unit: 'year' | 'quarter' | 'month' | 'week' | 'day'; alias?: string };
        aggregations: { column: string; fn: 'SUM' | 'AVG' | 'COUNT' | 'MIN' | 'MAX'; alias?: string }[];
        filters?: { column: string; op: string; value: string | number | boolean }[];
        orderBy?: { column: string; direction: 'ASC' | 'DESC' };
        limit?: number;
    };
    display: {
        xAxis?: string;
        yAxis?: string[];
        nameKey?: string;
        valueKey?: string;
    };
}

export interface DataExplorerQueryResult {
    columns: { name: string; type: string }[];
    rows: Record<string, unknown>[];
    totalRows: number;
    truncated: boolean;
}

export interface AcademicPaper {
    id: string;
    title: string;
    authors: string[];
    year: number;
    journal?: string;
    volume?: string;
    issue?: string;
    pages?: string;
    abstract?: string;
    doi?: string;
    url: string;
    pdfUrl?: string;
    citationCount?: number;
    harvardCitation: string;
}

export interface AcademicFinding {
    content: string;
    papers: AcademicPaper[];
    relevanceScore: number;
}

export type KbConfigFieldType = 'bool' | 'int' | 'float' | 'string' | 'enum';

export interface KbConfigField {
  key: string;
  type: KbConfigFieldType;
  group: string;
  label: string;
  help: string;
  min?: number;
  max?: number;
  enum?: string[];
  requiresReingest?: boolean;
}

export interface KbConfigValue {
  override: string | null;
  global: string | null;
  effective: string | null;
}

export interface KbSettingsResponse {
  registry: KbConfigField[];
  values: Record<string, KbConfigValue>;
}

// GET /api/kb/catalog row — the discovery-surface view of a published global
// KB: name/description plus the caller's own subscribed flag, so the catalog
// modal can render a subscribe/unsubscribe toggle without a second request.
export interface KbCatalogEntry {
    id: string;
    name: string;
    description?: string | null;
    subscribed: boolean;
    categoryIds: string[];
}

// GET /api/admin/kb-categories row — system-admin only; used to render the
// catalog's optional filter chips.
export interface KbCategory {
    id: string;
    name: string;
    sortOrder: number;
}

// --- KB workflow canvas (GET /api/kb/{id}/workflow) ---

export type WorkflowLane = 'lookup' | 'enumeration' | 'complex_reasoning';

/** Three-state, because some stages depend on query CONTENT, not query type. */
export type NodeActivation = 'active' | 'conditional' | 'inactive';

/** Where a resolved config value came from. */
export type ValueOrigin = 'kb' | 'global' | 'default';

export interface WorkflowNodeData {
  id: string;
  label: string;
  group: string;
  help: string;
  keys: string[];
  alwaysOn: boolean;
  llmCalls: number;
  latencyMs: number;
  activation: NodeActivation;
  /** 'flag_off' | 'lane_skipped' | 'orchestrator_bypass' | 'superseded_by:self_rag' */
  reason?: string;
  /** Long German prose explaining a conditional/inactive state. May be a paragraph. */
  condition?: string;
  values: Record<string, string>;
  origins: Record<string, ValueOrigin>;
  editable: boolean;
}

export interface WorkflowEdge {
  from: string;
  to: string;
  label: string;
  loop: boolean;
  maxIterations: number;
}

/**
 * What a KB's default agent/team binding can point AT — the two things that
 * are actually attachable, and the two the writer can address (`kind` picks
 * between PUT …/agents/{id} and PUT …/teams/{id}).
 *
 * Kept apart from AgentBindingKind below on purpose: an option carrying
 * "unknown" would be an option nobody can save, and typing them the same
 * would let one be handed to the writer.
 */
export type BindingTarget = 'agent' | 'team';

/**
 * The resolved state of the binding — FOUR values, mirroring
 * go-backend/internal/pipeline/BindingKind:
 *
 *  - 'agent' / 'team' — a default is bound, `id`/`name` name it;
 *  - ''              — nothing is bound (a claim about the KB);
 *  - 'unknown'       — the server TRIED to read the link tables and could
 *                      not. Deliberately NOT collapsed into '': "nothing
 *                      bound" is a claim, and a failed read is not entitled
 *                      to make it. See BindingUnknown's doc comment in
 *                      go-backend/internal/pipeline/binding.go.
 */
export type AgentBindingKind = BindingTarget | '' | 'unknown';

/**
 * One agent or team attached to the KB, as a candidate for the KB default.
 *
 * `disabled` means the agent/team itself is switched off (agents.is_enabled /
 * agent_teams.is_enabled). Such an option is still LISTED — the KB's current
 * default may be one of them, and an admin has to see what they are clearing —
 * but must not be offered as a NEW default: chat-time resolution filters
 * is_enabled, so binding one would produce a default that never applies.
 */
export interface AgentBindingOption {
  kind: BindingTarget;
  id: string;
  name: string;
  disabled: boolean;
  /**
   * A TEAM with no enabled member. Same consequence as `disabled` — chat-time
   * resolution drops the selection and the KB answers with the standard path —
   * but a separate flag, because the remedy differs: staff the team rather than
   * switch it on. Never true for an agent.
   */
  emptyTeam: boolean;
}

/**
 * GET /api/kb/{id}/workflow's `agentBinding`: what is bound right now, and
 * what could be bound instead.
 *
 * `id`/`name` are empty unless `kind` is 'agent' or 'team'. Which option is
 * the current binding is answered ONCE, here at the top level — the options
 * deliberately carry no isDefault flag of their own (two places answering
 * "what is bound?" is two places that can disagree), so the control compares
 * `option.id` against this `id`.
 */
export interface AgentBindingInfo {
  kind: AgentBindingKind;
  id: string;
  name: string;
  /**
   * A default IS bound (`kind`/`id`/`name` name it) and the agent or team it
   * points at is switched off, so nothing routes through it — the KB answers
   * with the standard path. The row survives and takes effect again the moment
   * anyone re-enables the agent, which is why the control shows it and offers
   * to clear it instead of hiding it.
   */
  disabled: boolean;
  /**
   * A TEAM is bound and it has no enabled member. The team itself is switched
   * ON, so `disabled` is false and nothing about it looks wrong — but
   * LoadTeamForChat returns zero members, the selection is dropped, and the KB
   * answers exactly as if nothing were bound.
   */
  emptyTeam: boolean;
  options: AgentBindingOption[];
}

export interface OrchestratorCandidate {
  orchestrator: string;
  activation: NodeActivation;
  condition?: string;
}

// The workflow projection's `fields` map carries the SAME registry rows the
// flat settings panel gets: both are serialised from siteconfig.KBConfigField
// (go-backend/internal/siteconfig/registry.go), field for field, tag for tag.
// These were two hand-maintained mirrors of one Go struct — adding a registry
// field would have updated one and silently left the other stale, which is the
// exact drift this whole surface exists to prevent. Aliases, not copies: there
// is one shape, and it can only be described once.
//
// The workflow-local names are kept because the whole workflow/ directory
// imports them, and because a future divergence (should the projection ever
// send a narrower row) then has one obvious place to happen — replace the
// alias with a real declaration and the comment explaining why.
export type WorkflowFieldType = KbConfigFieldType;

export type WorkflowConfigField = KbConfigField;

export interface WorkflowGraph {
  lane: WorkflowLane;
  nodes: WorkflowNodeData[];
  edges: WorkflowEdge[];
  orchestrators: OrchestratorCandidate[];
  estLlmCalls: number;
  estLatencyMs: number;
  fields: Record<string, WorkflowConfigField>;
  /** The id recorded in the KB's `workflow_preset` marker, or "" if the KB
   * was never set up from a preset. Meaningless on its own — see
   * presetBaseKnown before reading it. */
  presetBase: string;
  /** False only when presetBase names a preset that no longer exists
   * (renamed or removed) — true for BOTH "no base at all" (presetBase === "")
   * and "based on a preset that still exists". A canvas that ignores this
   * third state cannot tell "you conform" (0 deviations, real bundle behind
   * it) from "there is nothing to conform to" (a stale id, no bundle at all)
   * — see go-backend/internal/pipeline/project.go's PresetBaseKnown doc. */
  presetBaseKnown: boolean;
  /** Bundle keys whose per-KB override no longer matches presetBase's
   * bundle. Only meaningful when presetBaseKnown is true and presetBase is
   * non-empty; empty otherwise. */
  deviations: string[];
  /** The KB's default agent/team binding plus the attachable set. A TOP-LEVEL
   * field, not something on the agent_binding node: `options` and `id` are
   * values the server's Project() has no argument for and never reads (see
   * AgentBindingInfo in go-backend/internal/pipeline/binding.go), and the
   * client mirrors that split rather than inventing a second home for it. */
  agentBinding: AgentBindingInfo;
}

// --- Workflow presets (GET /api/workflow/presets, POST/GET .../workflow/preset) ---

/** One preset's projected cost on one lane. Mirrors go-backend's LaneCost. */
export interface WorkflowLaneCost {
  estLlmCalls: number;
  estLatencyMs: number;
}

/**
 * A curated preset plus its cost, per lane. Costs is keyed by lane rather
 * than collapsed to one number because the lanes genuinely disagree on
 * price — on the complex lane in particular, "research" and "standard"
 * project the SAME total (NodeOrchestrator is a flat cost regardless of
 * which orchestrator wins), so a single number would make "Recherche" look
 * no more expensive than "Standard" for the one lane it exists for. See
 * go-backend/internal/pipeline/presets_cost.go's PricedPreset doc.
 */
export interface WorkflowPreset {
  id: string;
  label: string;
  description: string;
  bundle: Record<string, string>;
  costs: Record<WorkflowLane, WorkflowLaneCost>;
}

/** The result of applying (or previewing) a preset.
 *
 * Three numbers, because "what does this cost me?" has three honest answers
 * and only reporting the first understated every first-time apply (see
 * go-backend/internal/pipeline/preset_apply.go's ApplyResult):
 *
 *  - `overwrites` — bundle keys the KB EXPLICITLY set to a value the apply
 *    changes: exactly what the admin personally loses. Zero for a KB that
 *    never overrode anything, which is the common case.
 *  - `effective` — bundle keys whose EFFECTIVE value changes, i.e. what the
 *    KB will actually answer differently on. Not bounded by `overwrites`: a
 *    KB with no overrides still changes behaviour wherever the deployment
 *    global disagrees with the bundle.
 *  - `pinned` — how many settings the apply freezes as per-KB rows (the
 *    bundle size; the provenance marker is not counted). Those stop following
 *    the deployment defaults afterwards.
 */
export interface WorkflowPresetApplyResult {
  preset: string;
  label: string;
  overwrites: string[];
  effective: string[];
  pinned: number;
}
