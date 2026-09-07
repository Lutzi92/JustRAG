// Pure TS mirror of go-backend/internal/chatpolicy (policy.go + tools.go) for
// client-side validation and a rule preview in AdminAgentTab. The SERVER
// stays the authority — internal/siteconfig runs the real Go validators at
// save time; this module exists only so an operator sees a mistake before
// they hit Save, and so the "which rule fires for this query type" question
// has an answer without a chat turn.
//
// These validators need not reproduce the Go validator's exact error text —
// only the same error CLASSES (not an array/object, too many rules, unknown
// orchestrator/mode/route/tool/query_type, duplicate tool, unknown field).

/** The closed set of orchestrator names a rule may name (chatpolicy.Orchestrators). */
export const ORCHESTRATORS = [
    'drift', 'longcontext', 'supervisor', 'plan_execute', 'plan_execute_dag', 'agentic', 'standard',
] as const;
export type Orchestrator = typeof ORCHESTRATORS[number];

/** The closed set of query-classifier labels a `when.query_type` may contain (chatpolicy.QueryTypes). */
const QUERY_TYPES = ['lookup', 'enumeration', 'complex_reasoning'] as const;

/** The closed set of routes a `chat_answer_tools_by_route` document may key on (chatpolicy.Routes). */
export const ROUTES = ['lookup', 'enumeration', 'complex_reasoning', 'global_synthesis'] as const;
export type Route = typeof ROUTES[number];

/** The closed set of built-in MCP tool names, in chatpolicy.KnownAnswerTools order. */
export const KNOWN_TOOLS = [
    'chunk_read', 'calculator', 'keyword_search', 'kb_search', 'web_search',
    'code_exec', 'memory_read', 'memory_write', 'recent_documents',
    'count_mentions', 'document_outline', 'sql_query', 'table_query', 'graph_search',
] as const;
export type KnownTool = typeof KNOWN_TOOLS[number];

const MAX_RULES = 32;

const WHEN_FIELDS = [
    'query_type', 'global_synthesis', 'enumeration', 'recency_listing',
    'has_file_selection', 'history_turns_gte', 'kb_ids',
] as const;
const RULE_FIELDS = ['when', 'orchestrator', 'mode'] as const;

export type PolicyRule = {
    when: {
        query_type?: string[];
        global_synthesis?: boolean;
        enumeration?: boolean;
        recency_listing?: boolean;
        has_file_selection?: boolean;
        history_turns_gte?: number;
        kb_ids?: string[];
    };
    orchestrator: string;
    mode: 'force' | 'prefer';
};

function isPlainObject(v: unknown): v is Record<string, unknown> {
    return typeof v === 'object' && v !== null && !Array.isArray(v);
}

/**
 * Validates a `chat_orchestrator_policy` document (a JSON array of rules).
 * An empty/whitespace-only value is the documented default ("no policy") —
 * mirrors ParseOrchestratorPolicy's empty-string handling in policy.go.
 */
export function validatePolicyJSON(raw: string): { rules: PolicyRule[]; errors: string[] } {
    const trimmed = raw.trim();
    if (trimmed === '') return { rules: [], errors: [] };
    if (trimmed[0] !== '[') {
        return { rules: [], errors: ['chat_orchestrator_policy must be a JSON array of rules'] };
    }

    let parsed: unknown;
    try {
        parsed = JSON.parse(trimmed);
    } catch (e) {
        return { rules: [], errors: [`invalid policy JSON: ${(e as Error).message}`] };
    }
    if (!Array.isArray(parsed)) {
        return { rules: [], errors: ['chat_orchestrator_policy must be a JSON array of rules'] };
    }

    const errors: string[] = [];
    if (parsed.length > MAX_RULES) {
        errors.push(`policy has ${parsed.length} rules, at most ${MAX_RULES} rules are allowed`);
    }

    const rules: PolicyRule[] = [];
    parsed.forEach((item, i) => {
        if (!isPlainObject(item)) {
            errors.push(`rule ${i}: must be an object`);
            return;
        }
        for (const k of Object.keys(item)) {
            if (!(RULE_FIELDS as readonly string[]).includes(k)) {
                errors.push(`rule ${i}: unknown field "${k}"`);
            }
        }

        const rawWhen = item.when;
        const when: PolicyRule['when'] = {};
        if (rawWhen !== undefined) {
            if (!isPlainObject(rawWhen)) {
                errors.push(`rule ${i}: when must be an object`);
            } else {
                for (const k of Object.keys(rawWhen)) {
                    if (!(WHEN_FIELDS as readonly string[]).includes(k)) {
                        errors.push(`rule ${i}: unknown when field "${k}"`);
                    }
                }
                if (rawWhen.query_type !== undefined) {
                    if (!Array.isArray(rawWhen.query_type) || !rawWhen.query_type.every(v => typeof v === 'string')) {
                        errors.push(`rule ${i}: query_type must be an array of strings`);
                    } else {
                        when.query_type = rawWhen.query_type as string[];
                        for (const qt of when.query_type) {
                            if (!(QUERY_TYPES as readonly string[]).includes(qt)) {
                                errors.push(`rule ${i}: unknown query_type "${qt}" (known: ${QUERY_TYPES.join(', ')})`);
                            }
                        }
                    }
                }
                if (rawWhen.global_synthesis !== undefined) {
                    if (typeof rawWhen.global_synthesis !== 'boolean') {
                        errors.push(`rule ${i}: global_synthesis must be a boolean`);
                    } else {
                        when.global_synthesis = rawWhen.global_synthesis;
                    }
                }
                if (rawWhen.enumeration !== undefined) {
                    if (typeof rawWhen.enumeration !== 'boolean') {
                        errors.push(`rule ${i}: enumeration must be a boolean`);
                    } else {
                        when.enumeration = rawWhen.enumeration;
                    }
                }
                if (rawWhen.recency_listing !== undefined) {
                    if (typeof rawWhen.recency_listing !== 'boolean') {
                        errors.push(`rule ${i}: recency_listing must be a boolean`);
                    } else {
                        when.recency_listing = rawWhen.recency_listing;
                    }
                }
                if (rawWhen.has_file_selection !== undefined) {
                    if (typeof rawWhen.has_file_selection !== 'boolean') {
                        errors.push(`rule ${i}: has_file_selection must be a boolean`);
                    } else {
                        when.has_file_selection = rawWhen.has_file_selection;
                    }
                }
                if (rawWhen.history_turns_gte !== undefined) {
                    if (typeof rawWhen.history_turns_gte !== 'number' || !Number.isInteger(rawWhen.history_turns_gte) || rawWhen.history_turns_gte < 0) {
                        errors.push(`rule ${i}: history_turns_gte must be a non-negative integer`);
                    } else {
                        when.history_turns_gte = rawWhen.history_turns_gte;
                    }
                }
                if (rawWhen.kb_ids !== undefined) {
                    if (!Array.isArray(rawWhen.kb_ids) || !rawWhen.kb_ids.every(v => typeof v === 'string')) {
                        errors.push(`rule ${i}: kb_ids must be an array of strings`);
                    } else {
                        when.kb_ids = rawWhen.kb_ids as string[];
                        when.kb_ids.forEach((id, j) => {
                            if (id.trim() === '') {
                                errors.push(`rule ${i}: kb_ids[${j}] must not be empty`);
                            }
                        });
                    }
                }
            }
        }

        const orchestrator = item.orchestrator;
        if (typeof orchestrator !== 'string' || !(ORCHESTRATORS as readonly string[]).includes(orchestrator)) {
            errors.push(`rule ${i}: unknown orchestrator ${JSON.stringify(orchestrator)} (known: ${ORCHESTRATORS.join(', ')})`);
        }

        const mode = item.mode;
        if (mode !== 'force' && mode !== 'prefer') {
            errors.push(`rule ${i}: unknown mode ${JSON.stringify(mode)} (known: force, prefer)`);
        }

        rules.push({
            when,
            orchestrator: typeof orchestrator === 'string' ? orchestrator : '',
            mode: mode === 'prefer' ? 'prefer' : 'force',
        });
    });

    return { rules, errors };
}

/**
 * Validates a `chat_answer_tools_by_route` document (a JSON object keyed by
 * route). An empty/whitespace-only value is the documented default ("no
 * restriction").
 */
export function validateToolsByRouteJSON(raw: string): { errors: string[] } {
    const trimmed = raw.trim();
    if (trimmed === '') return { errors: [] };
    if (trimmed[0] !== '{') {
        return { errors: ['chat_answer_tools_by_route must be a JSON object keyed by route'] };
    }

    let parsed: unknown;
    try {
        parsed = JSON.parse(trimmed);
    } catch (e) {
        return { errors: [`invalid tool-map JSON: ${(e as Error).message}`] };
    }
    if (!isPlainObject(parsed)) {
        return { errors: ['chat_answer_tools_by_route must be a JSON object keyed by route'] };
    }

    const errors: string[] = [];
    for (const [route, tools] of Object.entries(parsed)) {
        if (!(ROUTES as readonly string[]).includes(route)) {
            errors.push(`unknown route "${route}" (known: ${ROUTES.join(', ')})`);
            continue;
        }
        if (!Array.isArray(tools)) {
            errors.push(`route "${route}": must be an array of tool names`);
            continue;
        }
        const seen = new Set<string>();
        for (const name of tools) {
            if (typeof name !== 'string' || !(KNOWN_TOOLS as readonly string[]).includes(name)) {
                errors.push(`route "${route}": unknown tool ${JSON.stringify(name)} (known: ${KNOWN_TOOLS.join(', ')})`);
                continue;
            }
            if (seen.has(name)) {
                errors.push(`route "${route}": duplicate tool "${name}"`);
                continue;
            }
            seen.add(name);
        }
    }
    return { errors };
}

// --- Preview -----------------------------------------------------------

export type PreviewRow = {
    /** i18n key naming the scenario, e.g. "policyPreviewLookup". */
    label: string;
    ruleIndex: number | null;
    orchestrator: string | null;
    mode: string | null;
};

type PreviewSignals = {
    queryType: string;
    globalSynthesis: boolean;
    enumeration: boolean;
    recencyListing: boolean;
    hasFileSelection: boolean;
    historyTurns: number;
    kbId: string;
};

function whenMatches(when: PolicyRule['when'], sig: PreviewSignals): boolean {
    if (when.query_type && when.query_type.length > 0 && !when.query_type.includes(sig.queryType)) return false;
    if (when.global_synthesis !== undefined && when.global_synthesis !== sig.globalSynthesis) return false;
    if (when.enumeration !== undefined && when.enumeration !== sig.enumeration) return false;
    if (when.recency_listing !== undefined && when.recency_listing !== sig.recencyListing) return false;
    if (when.has_file_selection !== undefined && when.has_file_selection !== sig.hasFileSelection) return false;
    if (when.history_turns_gte !== undefined && sig.historyTurns < when.history_turns_gte) return false;
    if (when.kb_ids && when.kb_ids.length > 0 && !when.kb_ids.includes(sig.kbId)) return false;
    return true;
}

// The four canonical turns the preview evaluates the policy against.
// enumeration=true only on the enumeration row; every other signal is
// false/0/'' everywhere, mirroring the brief exactly.
const PREVIEW_SCENARIOS: { label: string; sig: PreviewSignals }[] = [
    {
        label: 'policyPreviewLookup',
        sig: { queryType: 'lookup', globalSynthesis: false, enumeration: false, recencyListing: false, hasFileSelection: false, historyTurns: 0, kbId: '' },
    },
    {
        label: 'policyPreviewEnumeration',
        sig: { queryType: 'enumeration', globalSynthesis: false, enumeration: true, recencyListing: false, hasFileSelection: false, historyTurns: 0, kbId: '' },
    },
    {
        label: 'policyPreviewComplex',
        sig: { queryType: 'complex_reasoning', globalSynthesis: false, enumeration: false, recencyListing: false, hasFileSelection: false, historyTurns: 0, kbId: '' },
    },
    {
        label: 'policyPreviewGlobalSynthesis',
        sig: { queryType: 'complex_reasoning', globalSynthesis: true, enumeration: false, recencyListing: false, hasFileSelection: false, historyTurns: 0, kbId: '' },
    },
];

/**
 * Runs the four canonical turns against a parsed policy and reports which
 * rule (if any) each one hits — first match wins, mirroring
 * chatpolicy.OrchestratorPolicy.Match.
 */
export function previewPolicy(rules: PolicyRule[]): PreviewRow[] {
    return PREVIEW_SCENARIOS.map(({ label, sig }) => {
        const idx = rules.findIndex(r => whenMatches(r.when, sig));
        if (idx === -1) return { label, ruleIndex: null, orchestrator: null, mode: null };
        const rule = rules[idx];
        return { label, ruleIndex: idx, orchestrator: rule.orchestrator, mode: rule.mode };
    });
}
