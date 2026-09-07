import { describe, it, expect } from 'vitest';
import {
    ORCHESTRATORS,
    ROUTES,
    KNOWN_TOOLS,
    validatePolicyJSON,
    validateToolsByRouteJSON,
    previewPolicy,
    type PolicyRule,
} from './policyPreview';

// The two example documents from the rulings (W6-R6 / W6-R8), used verbatim
// as placeholders in the admin UI — they must parse clean.
const POLICY_EXAMPLE = '[{"when":{"query_type":["lookup"]},"orchestrator":"standard","mode":"force"},{"when":{"global_synthesis":true},"orchestrator":"longcontext","mode":"prefer"}]';
const TOOLS_EXAMPLE = '{"lookup":["kb_search","chunk_read"],"complex_reasoning":["kb_search","keyword_search","chunk_read","document_outline"]}';

describe('ORCHESTRATORS / ROUTES / KNOWN_TOOLS', () => {
    it('mirrors chatpolicy.Orchestrators (7 entries)', () => {
        expect(ORCHESTRATORS).toEqual(['drift', 'longcontext', 'supervisor', 'plan_execute', 'plan_execute_dag', 'agentic', 'standard']);
    });

    it('mirrors chatpolicy.Routes (4 entries)', () => {
        expect(ROUTES).toEqual(['lookup', 'enumeration', 'complex_reasoning', 'global_synthesis']);
    });

    it('mirrors chatpolicy.KnownAnswerTools in order (14 entries)', () => {
        expect(KNOWN_TOOLS).toEqual([
            'chunk_read', 'calculator', 'keyword_search', 'kb_search', 'web_search',
            'code_exec', 'memory_read', 'memory_write', 'recent_documents',
            'count_mentions', 'document_outline', 'sql_query', 'table_query', 'graph_search',
        ]);
        expect(KNOWN_TOOLS).toHaveLength(14);
    });
});

describe('validatePolicyJSON', () => {
    it('treats an empty string as the documented default (no rules, no errors)', () => {
        expect(validatePolicyJSON('')).toEqual({ rules: [], errors: [] });
        expect(validatePolicyJSON('   ')).toEqual({ rules: [], errors: [] });
    });

    it('accepts the documented policy example with no errors', () => {
        const { rules, errors } = validatePolicyJSON(POLICY_EXAMPLE);
        expect(errors).toEqual([]);
        expect(rules).toHaveLength(2);
        expect(rules[0].orchestrator).toBe('standard');
        expect(rules[0].mode).toBe('force');
        expect(rules[1].orchestrator).toBe('longcontext');
        expect(rules[1].mode).toBe('prefer');
    });

    it('rejects a document that is not a JSON array', () => {
        const { errors } = validatePolicyJSON('{"when":{},"orchestrator":"standard","mode":"force"}');
        expect(errors.length).toBeGreaterThan(0);
        expect(errors.some(e => /array/i.test(e))).toBe(true);
    });

    it('rejects malformed JSON', () => {
        const { errors } = validatePolicyJSON('[{"orchestrator":]');
        expect(errors.length).toBeGreaterThan(0);
    });

    it('rejects more than 32 rules', () => {
        const rules = Array.from({ length: 33 }, () => ({ when: {}, orchestrator: 'standard', mode: 'force' }));
        const { errors } = validatePolicyJSON(JSON.stringify(rules));
        expect(errors.some(e => /32/.test(e))).toBe(true);
    });

    it('accepts exactly 32 rules', () => {
        const rules = Array.from({ length: 32 }, () => ({ when: {}, orchestrator: 'standard', mode: 'force' }));
        const { errors } = validatePolicyJSON(JSON.stringify(rules));
        expect(errors).toEqual([]);
    });

    it('rejects an unknown orchestrator', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: {}, orchestrator: 'made_up', mode: 'force' }]));
        expect(errors.some(e => /orchestrator/i.test(e))).toBe(true);
    });

    it('rejects an unknown mode', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: {}, orchestrator: 'standard', mode: 'sometimes' }]));
        expect(errors.some(e => /mode/i.test(e))).toBe(true);
    });

    it('rejects an unknown query_type', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { query_type: ['bogus'] }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /query_type/i.test(e))).toBe(true);
    });

    it('rejects an unknown top-level field (typo protection)', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: {}, orchestrator: 'standard', mode: 'force', extra: 1 }]));
        expect(errors.length).toBeGreaterThan(0);
    });

    it('rejects an unknown `when` field', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { bogus_signal: true }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.length).toBeGreaterThan(0);
    });

    it('rejects an empty kb_ids entry', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { kb_ids: [''] }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.length).toBeGreaterThan(0);
    });

    // Every `when.*` field must be type-checked and rejected on the wrong
    // JSON type instead of being silently dropped (which would validate the
    // rule as if the condition were absent — matching everything on that
    // axis, a routing change the operator did not ask for). Mirrors Go's
    // DisallowUnknownFields + strict struct-field typing in policy.go.
    it('rejects query_type given as a string instead of an array', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { query_type: 'lookup' }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /query_type/i.test(e))).toBe(true);
    });

    it('rejects global_synthesis given as a string instead of a boolean', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { global_synthesis: 'true' }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /global_synthesis/i.test(e))).toBe(true);
    });

    it('rejects enumeration given as a string instead of a boolean', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { enumeration: 'true' }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /enumeration/i.test(e))).toBe(true);
    });

    it('rejects recency_listing given as a string instead of a boolean', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { recency_listing: 'true' }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /recency_listing/i.test(e))).toBe(true);
    });

    it('rejects has_file_selection given as a string instead of a boolean', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { has_file_selection: 'true' }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /has_file_selection/i.test(e))).toBe(true);
    });

    it('rejects history_turns_gte given as a non-integer number', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { history_turns_gte: 1.5 }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /history_turns_gte/i.test(e))).toBe(true);
    });

    it('rejects history_turns_gte given as a negative integer', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { history_turns_gte: -1 }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /history_turns_gte/i.test(e))).toBe(true);
    });

    it('rejects kb_ids given as a string instead of an array', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { kb_ids: 'kb-1' }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /kb_ids/i.test(e))).toBe(true);
    });
});

describe('validateToolsByRouteJSON', () => {
    it('treats an empty string as the documented default (no errors)', () => {
        expect(validateToolsByRouteJSON('')).toEqual({ errors: [] });
        expect(validateToolsByRouteJSON('  ')).toEqual({ errors: [] });
    });

    it('accepts the documented tools example with no errors', () => {
        expect(validateToolsByRouteJSON(TOOLS_EXAMPLE)).toEqual({ errors: [] });
    });

    it('rejects a document that is not a JSON object', () => {
        const { errors } = validateToolsByRouteJSON('["kb_search"]');
        expect(errors.length).toBeGreaterThan(0);
        expect(errors.some(e => /object/i.test(e))).toBe(true);
    });

    it('rejects malformed JSON', () => {
        const { errors } = validateToolsByRouteJSON('{"lookup":]');
        expect(errors.length).toBeGreaterThan(0);
    });

    it('rejects an unknown route', () => {
        const { errors } = validateToolsByRouteJSON(JSON.stringify({ made_up_route: ['kb_search'] }));
        expect(errors.some(e => /route/i.test(e))).toBe(true);
    });

    it('rejects an unknown tool', () => {
        const { errors } = validateToolsByRouteJSON(JSON.stringify({ lookup: ['not_a_real_tool'] }));
        expect(errors.some(e => /tool/i.test(e))).toBe(true);
    });

    it('rejects a duplicate tool within one route', () => {
        const { errors } = validateToolsByRouteJSON(JSON.stringify({ lookup: ['kb_search', 'kb_search'] }));
        expect(errors.some(e => /duplicate/i.test(e))).toBe(true);
    });
});

describe('previewPolicy', () => {
    it('returns four null rows for an empty rule set', () => {
        const rows = previewPolicy([]);
        expect(rows).toHaveLength(4);
        for (const row of rows) {
            expect(row.ruleIndex).toBeNull();
            expect(row.orchestrator).toBeNull();
            expect(row.mode).toBeNull();
        }
        expect(rows.map(r => r.label)).toEqual([
            'policyPreviewLookup',
            'policyPreviewEnumeration',
            'policyPreviewComplex',
            'policyPreviewGlobalSynthesis',
        ]);
    });

    it('a query_type rule hits only its own route', () => {
        const rules: PolicyRule[] = [{ when: { query_type: ['lookup'] }, orchestrator: 'standard', mode: 'force' }];
        const rows = previewPolicy(rules);
        expect(rows[0]).toMatchObject({ ruleIndex: 0, orchestrator: 'standard', mode: 'force' });
        expect(rows[1].ruleIndex).toBeNull();
        expect(rows[2].ruleIndex).toBeNull();
        expect(rows[3].ruleIndex).toBeNull();
    });

    it('a global_synthesis:true rule hits only the 4th row', () => {
        const rules: PolicyRule[] = [{ when: { global_synthesis: true }, orchestrator: 'longcontext', mode: 'prefer' }];
        const rows = previewPolicy(rules);
        expect(rows[0].ruleIndex).toBeNull();
        expect(rows[1].ruleIndex).toBeNull();
        expect(rows[2].ruleIndex).toBeNull();
        expect(rows[3]).toMatchObject({ ruleIndex: 0, orchestrator: 'longcontext', mode: 'prefer' });
    });

    it('enumeration=true in `when` matches only the enumeration row', () => {
        const rules: PolicyRule[] = [{ when: { enumeration: true }, orchestrator: 'supervisor', mode: 'force' }];
        const rows = previewPolicy(rules);
        expect(rows[0].ruleIndex).toBeNull();
        expect(rows[1]).toMatchObject({ ruleIndex: 0, orchestrator: 'supervisor' });
        expect(rows[2].ruleIndex).toBeNull();
        expect(rows[3].ruleIndex).toBeNull();
    });

    it('first match wins', () => {
        const rules: PolicyRule[] = [
            { when: {}, orchestrator: 'standard', mode: 'force' },
            { when: { query_type: ['lookup'] }, orchestrator: 'supervisor', mode: 'force' },
        ];
        const rows = previewPolicy(rules);
        expect(rows[0]).toMatchObject({ ruleIndex: 0, orchestrator: 'standard' });
    });

    it('previews the documented policy example', () => {
        const { rules, errors } = validatePolicyJSON(POLICY_EXAMPLE);
        expect(errors).toEqual([]);
        const rows = previewPolicy(rules);
        // lookup -> rule 0 (standard/force)
        expect(rows[0]).toMatchObject({ ruleIndex: 0, orchestrator: 'standard', mode: 'force' });
        // enumeration -> no rule matches (rule 0 requires query_type lookup, rule 1 requires global_synthesis)
        expect(rows[1].ruleIndex).toBeNull();
        // complex_reasoning without global_synthesis -> no match
        expect(rows[2].ruleIndex).toBeNull();
        // complex_reasoning + global_synthesis -> rule 1 (longcontext/prefer)
        expect(rows[3]).toMatchObject({ ruleIndex: 1, orchestrator: 'longcontext', mode: 'prefer' });
    });
});
