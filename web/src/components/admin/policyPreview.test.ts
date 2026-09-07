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

    // S3 (final review): chatpolicy.When.HistoryTurnsGTE is an unconstrained
    // *int server-side (policy.go has no sign check) — Go accepts a negative
    // value too, it would just never match a real turn's history length. The
    // FE must not be a stricter gate than the server it mirrors.
    it('accepts history_turns_gte given as a negative integer, matching the server', () => {
        const { errors, rules } = validatePolicyJSON(JSON.stringify([{ when: { history_turns_gte: -1 }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors).toEqual([]);
        expect(rules[0].when.history_turns_gte).toBe(-1);
    });

    it('rejects kb_ids given as a string instead of an array', () => {
        const { errors } = validatePolicyJSON(JSON.stringify([{ when: { kb_ids: 'kb-1' }, orchestrator: 'standard', mode: 'force' }]));
        expect(errors.some(e => /kb_ids/i.test(e))).toBe(true);
    });

    // S3 (final review): encoding/json decodes a JSON `null` into the zero
    // value of whatever it targets. Rule.When is a non-pointer struct field,
    // so `"when": null` leaves it at its zero value server-side — exactly
    // like `when` being absent (an empty when matches every turn). Each
    // individual `when.*` field is itself a Go pointer or slice, so a null
    // THERE also decodes to that field's zero value — again identical to
    // the field being absent. The FE must accept every one of these, or it
    // rejects a document Go's own save-time validator accepts.
    it('treats "when": null the same as when omitted (matches every turn)', () => {
        const { errors, rules } = validatePolicyJSON(JSON.stringify([{ when: null, orchestrator: 'standard', mode: 'force' }]));
        expect(errors).toEqual([]);
        expect(rules[0].when).toEqual({});
    });

    it('treats each when.* field as absent when given as null', () => {
        const doc = [{
            when: {
                query_type: null,
                global_synthesis: null,
                enumeration: null,
                recency_listing: null,
                has_file_selection: null,
                history_turns_gte: null,
                kb_ids: null,
            },
            orchestrator: 'standard',
            mode: 'force',
        }];
        const { errors, rules } = validatePolicyJSON(JSON.stringify(doc));
        expect(errors).toEqual([]);
        expect(rules[0].when).toEqual({});
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

    // S13 (final review): code_exec is a recognized KNOWN_TOOLS name but is
    // excluded from the answer-time catalog by MCPDispatcher.AnswerToolCatalog,
    // so naming it would validate and then silently produce an empty catalog
    // for that route. Mirrors chatpolicy's answerToolsExcludedFromCatalog.
    it('rejects code_exec on every route with a message naming it as excluded', () => {
        for (const route of ROUTES) {
            const { errors } = validateToolsByRouteJSON(JSON.stringify({ [route]: ['code_exec'] }));
            expect(errors.length).toBeGreaterThan(0);
            expect(errors.some(e => e.includes('code_exec') && /exclude/i.test(e))).toBe(true);
        }
    });

    it('rejects code_exec even alongside an otherwise-valid tool', () => {
        const { errors } = validateToolsByRouteJSON(JSON.stringify({ lookup: ['kb_search', 'code_exec'] }));
        expect(errors.some(e => e.includes('code_exec'))).toBe(true);
    });

    // S3 (final review): AnswerToolsByRoute is a Go map[string][]string — a
    // JSON `null` route value decodes to a nil (empty) slice server-side, so
    // `{"lookup": null}` is byte-identical to `{"lookup": []}`: a real
    // restriction (no tools on that route), not an error.
    it('accepts a null route value the same as an empty array (a real restriction)', () => {
        expect(validateToolsByRouteJSON(JSON.stringify({ lookup: null }))).toEqual({ errors: [] });
        expect(validateToolsByRouteJSON(JSON.stringify({ lookup: [] }))).toEqual({ errors: [] });
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

    // S5 (final review): a matched `prefer` rule whose orchestrator's flag
    // is off does not apply — chatpolicy.Decide's Applied is false, and the
    // turn falls through to the ladder. The preview must say so rather than
    // rendering it identically to an applied rule.
    describe('appliedFallthrough (S5)', () => {
        it('a prefer rule is NOT applied when its orchestrator flag is off (default: no enabled map)', () => {
            const rules: PolicyRule[] = [{ when: {}, orchestrator: 'longcontext', mode: 'prefer' }];
            const rows = previewPolicy(rules);
            expect(rows[0]).toMatchObject({ ruleIndex: 0, orchestrator: 'longcontext', mode: 'prefer', appliedFallthrough: true });
        });

        it('a prefer rule IS applied when its orchestrator flag is on', () => {
            const rules: PolicyRule[] = [{ when: {}, orchestrator: 'longcontext', mode: 'prefer' }];
            const rows = previewPolicy(rules, { longcontext: true });
            expect(rows[0]).toMatchObject({ ruleIndex: 0, orchestrator: 'longcontext', mode: 'prefer', appliedFallthrough: false });
        });

        it('a force rule is always applied regardless of the flag', () => {
            const rules: PolicyRule[] = [{ when: {}, orchestrator: 'agentic', mode: 'force' }];
            const rows = previewPolicy(rules, { agentic: false });
            expect(rows[0]).toMatchObject({ ruleIndex: 0, orchestrator: 'agentic', mode: 'force', appliedFallthrough: false });
        });

        it('a "standard"-orchestrator prefer rule is always applied — it has no flag', () => {
            const rules: PolicyRule[] = [{ when: {}, orchestrator: 'standard', mode: 'prefer' }];
            const rows = previewPolicy(rules, {});
            expect(rows[0]).toMatchObject({ ruleIndex: 0, orchestrator: 'standard', mode: 'prefer', appliedFallthrough: false });
        });

        it('a row with no matching rule at all reports appliedFallthrough: false', () => {
            const rows = previewPolicy([]);
            for (const row of rows) {
                expect(row.appliedFallthrough).toBe(false);
            }
        });

        it('plan_execute_dag reads the same enabled-map key as documented (no separate key)', () => {
            const rules: PolicyRule[] = [{ when: {}, orchestrator: 'plan_execute_dag', mode: 'prefer' }];
            expect(previewPolicy(rules, { plan_execute_dag: false })[0].appliedFallthrough).toBe(true);
            expect(previewPolicy(rules, { plan_execute_dag: true })[0].appliedFallthrough).toBe(false);
        });
    });
});
