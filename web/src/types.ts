export interface MessageSource {
    index?: number;  // 1-based citation number
    fileName: string;
    fileId?: string;
    content: string;
    score: number;
    pages?: number[];
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
    // Streaming trajectory: one entry per orchestrator decision point
    // (plan/iterate/hop/decision/answer). Populated by useChatStream from
    // the `agentTrajectory` SSE events. Undefined when no events arrived
    // (legacy path or non-streaming).
    trajectory?: TrajectoryEvent[];
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

export interface StudioConfig {
    cards: boolean;
    presentation: boolean;
    podcast: boolean;
    chart: boolean;
    abstract: boolean;
    // Phase-2 study artifacts. Optional so legacy stored configs (which omit
    // them) keep working; absent ⇒ enabled by default in the sc spreads.
    briefingDoc?: boolean;
    faq?: boolean;
    studyGuide?: boolean;
    timeline?: boolean;
    quiz?: boolean;
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
    studioConfig?: StudioConfig | null;
    aiConfigId: string | null;
    chatModel: string | null;
    embeddingModel: string | null;
    rerankModel: string | null;
    ttsModel: string | null;
    language?: 'en' | 'de';
    systemPrompt?: string | null;
}

export interface GlobalKbEditor {
    id: string;
    userId: string;
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
    errorStage?: string;
    errorMessage?: string;
}

export interface ChatEntry {
    id: string;
    kbId: string;
    userId: string;
    title: string;
    type?: 'chat' | 'research' | 'academic_research';
    createdAt: string;
    updatedAt: string;
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
