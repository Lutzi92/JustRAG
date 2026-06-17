import { useState, useEffect, useMemo } from 'react';
import { Save, ChevronDown, ChevronRight, Search } from 'lucide-react';
import { motion } from 'framer-motion';
import { useReducedMotion, getMotionProps } from '../../hooks/useReducedMotion';
import { useTheme } from '../../contexts/ThemeContext';
import AdminAgentMetricsCard from './AdminAgentMetricsCard';
import AdminMCPSection from './AdminMCPSection';

interface AdminAgentTabProps {
    siteConfigs: Record<string, string>;
    setSiteConfigs: React.Dispatch<React.SetStateAction<Record<string, string>>>;
    onSubmit: (e: React.FormEvent) => void;
}

const STORAGE_KEY = 'admin-agent-sections-open-v1';

const SECTION_CONFIGS = [
    { id: 'general', titleKey: 'agentSectionGeneral', i18nKeys: ['defaultTopK', 'scoreDropThreshold', 'contextWindowSize'], settingKeys: ['default_top_k', 'score_drop_threshold', 'context_window_size'] },
    { id: 'hybrid', titleKey: 'agentSectionHybridSearch', i18nKeys: ['minSimilarityThreshold', 'mmrLambda', 'rrfWeightVector', 'rrfWeightBM25', 'bm25SimpleArmEnabled', 'bm25TieredBoostEnabled', 'hnswEfSearch', 'mrlTwoPassEnabled', 'queryInstruction', 'hyPESearchEnabled'], settingKeys: ['min_similarity_threshold', 'mmr_lambda', 'rrf_weight_vector', 'rrf_weight_bm25', 'bm25_simple_arm_enabled', 'bm25_tiered_boost_enabled', 'hnsw_ef_search', 'mrl_two_pass_enabled', 'query_instruction', 'hype_search_enabled'] },
    { id: 'reranker', titleKey: 'agentSectionReranker', i18nKeys: ['rerankBlendAlpha', 'rerankBlendAlphaLookup', 'rerankBlendAlphaEnumeration', 'rerankBlendAlphaComplexReasoning', 'rerankBlendAlphaEntity', 'hybridDynamicAlphaEnabled', 'hybridDynamicAlphaSensitivity', 'rerankUseChatTemplate', 'rerankInstruction'], settingKeys: ['rerank_blend_alpha', 'rerank_blend_alpha_lookup', 'rerank_blend_alpha_enumeration', 'rerank_blend_alpha_complex_reasoning', 'rerank_blend_alpha_entity', 'hybrid_dynamic_alpha_enabled', 'hybrid_dynamic_alpha_sensitivity', 'rerank_use_chat_template', 'rerank_instruction'] },
    { id: 'topn', titleKey: 'agentSectionPerRouteTopN', i18nKeys: ['topNLookup', 'topNEnumeration', 'topNComplexReasoning'], settingKeys: ['top_n_lookup', 'top_n_enumeration', 'top_n_complex_reasoning'] },
    { id: 'compression', titleKey: 'agentSectionCompression', i18nKeys: ['chatContextCompressionEnabled', 'chatContextCompressionMinChunks', 'chatContextCompressionThreshold', 'chatContextCompressionModel'], settingKeys: ['chat_context_compression_enabled', 'chat_context_compression_min_chunks', 'chat_context_compression_threshold', 'chat_context_compression_model'] },
    { id: 'queryEnh', titleKey: 'agentSectionQueryEnhancement', i18nKeys: ['autoSpellCorrect', 'stepBackEnabled', 'queryDecomposeEnabled', 'queryDecomposeModel', 'chatLongcontextEnabled', 'chatLongcontextMaxTokens', 'queryCacheEnabled', 'queryCacheSimilarityThreshold', 'queryCacheSimilarityThresholdLookup', 'queryCacheSimilarityThresholdEnumeration', 'queryCacheSimilarityThresholdComplexReasoning', 'queryCacheTtlHours'], settingKeys: ['auto_spell_correct', 'step_back_enabled', 'query_decompose_enabled', 'query_decompose_model', 'chat_longcontext_enabled', 'chat_longcontext_max_tokens', 'query_cache_enabled', 'query_cache_similarity_threshold', 'query_cache_similarity_threshold_lookup', 'query_cache_similarity_threshold_enumeration', 'query_cache_similarity_threshold_complex_reasoning', 'query_cache_ttl_hours'] },
    { id: 'crag', titleKey: 'agentSectionCragAdaptive', i18nKeys: ['cragEnabled', 'cragMinRelevantChunks', 'adaptiveRoutingEnabled'], settingKeys: ['crag_enabled', 'crag_min_relevant_chunks', 'adaptive_routing_enabled'] },
    { id: 'graph', titleKey: 'agentSectionGraph', i18nKeys: ['kgExtractionEnabled', 'chatGraphRoutingEnabled', 'chatGraphRoutingInjectChunks', 'chatGraphRoutingMaxChunks', 'chatGraphRoutingPathMode', 'chatGraphRoutingPPRDamping', 'chatGraphRoutingPPRMaxIter', 'chatGraphRoutingPPRTopEntities', 'chatGraphRoutingPathsMaxLen', 'chatGraphRoutingPathsMaxPaths'], settingKeys: ['kg_extraction_enabled', 'chat_graph_routing_enabled', 'chat_graph_routing_inject_chunks', 'chat_graph_routing_max_chunks', 'chat_graph_routing_path_mode', 'chat_graph_routing_ppr_damping', 'chat_graph_routing_ppr_max_iter', 'chat_graph_routing_ppr_top_entities', 'chat_graph_routing_paths_max_len', 'chat_graph_routing_paths_max_paths'] },
    { id: 'multistep', titleKey: 'agentSectionMultiStep', i18nKeys: ['chatKBRouterEnabled', 'chatKBRouterMinConfidence', 'chatTurnBudgetSeconds', 'chatTurnBudgetTokens', 'chatTurnBudgetToolCalls', 'chatAgenticEnabled', 'chatAgenticMaxHops', 'chatPlanExecuteEnabled', 'chatPlanExecuteMaxSubQueries', 'chatPlanExecuteMaxIterations', 'chatPlanExecuteTokenBudget', 'chatPlanExecuteToolAware', 'chatPlanExecuteDAGIterative', 'chatAnswerToolsEnabled', 'chatAnswerToolsMaxRounds', 'chatSupervisorEnabled', 'chatSupervisorMultiSpecialist'], settingKeys: ['chat_kb_router_enabled', 'chat_kb_router_min_confidence', 'chat_turn_budget_seconds', 'chat_turn_budget_tokens', 'chat_turn_budget_tool_calls', 'chat_agentic_enabled', 'chat_agentic_max_hops', 'chat_plan_execute_enabled', 'chat_plan_execute_max_sub_queries', 'chat_plan_execute_max_iterations', 'chat_plan_execute_token_budget', 'chat_plan_execute_tool_aware', 'chat_plan_execute_dag_iterative', 'chat_answer_tools_enabled', 'chat_answer_tools_max_rounds', 'chat_supervisor_enabled', 'chat_supervisor_multi_specialist'] },
    { id: 'conversation', titleKey: 'agentSectionConversation', i18nKeys: ['chatAnswerHistoryEnabled', 'chatAnswerHistoryMessages', 'chatAnswerHistoryMaxChars', 'chatTransformFollowupEnabled'], settingKeys: ['chat_answer_history_enabled', 'chat_answer_history_messages', 'chat_answer_history_max_chars', 'chat_transform_followup_enabled'] },
    { id: 'corpusTable', titleKey: 'agentSectionCorpusTable', i18nKeys: ['chatCorpusTableEnabled', 'chatCorpusTableModel', 'chatCorpusTableMaxFiles', 'chatCorpusTableConcurrency', 'chatCorpusTableRouterLlmEnabled'], settingKeys: ['chat_corpus_table_enabled', 'chat_corpus_table_model', 'chat_corpus_table_max_files', 'chat_corpus_table_concurrency', 'chat_corpus_table_router_llm_enabled'] },
    { id: 'compare', titleKey: 'agentSectionCompare', i18nKeys: ['chatCompareEnabled', 'chatCompareModel', 'chatCompareMaxSections', 'chatCompareConcurrency', 'chatComparePeersPerSection', 'chatCompareAttachmentTtlHours', 'chatCompareMaxFileBytes'], settingKeys: ['chat_compare_enabled', 'chat_compare_model', 'chat_compare_max_sections', 'chat_compare_concurrency', 'chat_compare_peers_per_section', 'chat_compare_attachment_ttl_hours', 'chat_compare_max_file_bytes'] },
    { id: 'longmem', titleKey: 'agentSectionLongmem', i18nKeys: ['chatLongmemEnabled', 'chatLongmemMinSalience', 'chatLongmemRecallTopK', 'chatLongmemDecayDays', 'chatLongmemRecallSemantic', 'chatLongmemConflictResolution', 'chatLongmemConflictModel', 'chatLongmemConflictCandidates'], settingKeys: ['chat_longmem_enabled', 'chat_longmem_min_salience', 'chat_longmem_recall_top_k', 'chat_longmem_decay_days', 'chat_longmem_recall_semantic', 'chat_longmem_conflict_resolution', 'chat_longmem_conflict_model', 'chat_longmem_conflict_candidates'] },
    { id: 'validation', titleKey: 'agentSectionValidation', i18nKeys: ['factcheckInChat', 'citationValidationEnabled', 'citationValidationSemanticThreshold', 'chatFactualityGateEnabled', 'chatFactualityGateMaxRefines', 'chatSelfRAGEnabled', 'ragasSamplingEnabled', 'ragasSamplingRate'], settingKeys: ['factcheck_in_chat', 'citation_validation_enabled', 'citation_validation_semantic_threshold', 'chat_factuality_gate_enabled', 'chat_factuality_gate_max_refines', 'chat_self_rag_enabled', 'ragas_sampling_enabled', 'ragas_sampling_rate'] },
    { id: 'ingestion', titleKey: 'agentSectionIngestion', i18nKeys: ['doclingEnabled', 'doclingBaseUrl', 'describeImageEnabled', 'describeImageEnabledHelp', 'describeImageModel', 'describeImageModelHelp', 'contextualEnrichment', 'embeddingBatchSize', 'lateChunkingEnabled', 'lateChunkingMaxInputTokens', 'parentChildEnabled', 'parentChunkSize', 'childChunkSize', 'raptorEnabled', 'raptorMinChunks', 'raptorMaxLevels', 'raptorBranchingFactor', 'raptorClusteringAlgorithm', 'raptorLeidenResolution', 'hyPEEnabled', 'hyPEQuestionsPerChunk', 'hyPEModel'], settingKeys: ['docling_enabled', 'docling_base_url', 'describe_image_enabled', 'describe_image_model', 'contextual_enrichment', 'embedding_batch_size', 'late_chunking_enabled', 'late_chunking_max_input_tokens', 'parent_child_enabled', 'parent_chunk_size', 'child_chunk_size', 'raptor_enabled', 'raptor_min_chunks', 'raptor_max_levels', 'raptor_branching_factor', 'raptor_clustering_algorithm', 'raptor_leiden_resolution', 'hype_enabled', 'hype_questions_per_chunk', 'hype_model'] },
    { id: 'observability', titleKey: 'agentSectionObservability', i18nKeys: ['langfuseBaseUrl'], settingKeys: ['langfuse_base_url'] },
    { id: 'tools', titleKey: 'agentSectionTools', i18nKeys: ['chatCodeExecEnabled'], settingKeys: ['mcp_servers', 'chat_use_mcp_tools', 'chat_code_exec_enabled'] },
    { id: 'tabular', titleKey: 'agentSectionTabular', i18nKeys: ['chatTabularQueryEnabled', 'chatTabularSemanticColumnsEnabled', 'tabularSemanticMinAvgLen', 'tabularSemanticMinDistinctRatio', 'chatTabularChartsEnabled'], settingKeys: ['chat_tabular_query_enabled', 'chat_tabular_semantic_columns_enabled', 'tabular_semantic_min_avg_len', 'tabular_semantic_min_distinct_ratio', 'chat_tabular_charts_enabled'] },
] as const;
type SectionId = typeof SECTION_CONFIGS[number]['id'];

function Section({ title, open, onToggle, hidden, children }: { title: string; open: boolean; onToggle: () => void; hidden: boolean; children: React.ReactNode }) {
    if (hidden) return null;
    return (
        <div style={{ border: '1px solid var(--border-color)', borderRadius: '12px', overflow: 'hidden', background: 'var(--bg-secondary)' }}>
            <button
                type="button"
                onClick={onToggle}
                aria-expanded={open}
                style={{
                    width: '100%',
                    padding: '1rem 1.25rem',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    background: 'transparent',
                    border: 'none',
                    cursor: 'pointer',
                    color: 'var(--text-primary)',
                    fontSize: '1rem',
                    fontWeight: 600,
                    textAlign: 'left',
                }}
            >
                <span>{title}</span>
                {open ? <ChevronDown size={18} /> : <ChevronRight size={18} />}
            </button>
            {open && (
                <div style={{
                    padding: '1.25rem 1.25rem 1.5rem',
                    display: 'flex',
                    flexDirection: 'column',
                    gap: '1.5rem',
                    background: 'var(--bg-primary)',
                    borderTop: '1px solid var(--border-color)',
                }}>
                    {children}
                </div>
            )}
        </div>
    );
}

export default function AdminAgentTab({ siteConfigs, setSiteConfigs, onSubmit }: AdminAgentTabProps) {
    const reducedMotion = useReducedMotion();
    const { t } = useTheme();

    const isCragEnabled = siteConfigs.crag_enabled === 'true' || siteConfigs.crag_enabled === '1';
    const isDoclingEnabled = siteConfigs.docling_enabled === 'true' || siteConfigs.docling_enabled === '1';
    const isDescribeImageEnabled = siteConfigs.describe_image_enabled === 'true' || siteConfigs.describe_image_enabled === '1';
    const isContextualEnrichmentEnabled = siteConfigs.contextual_enrichment !== 'false' && siteConfigs.contextual_enrichment !== '0';
    const isQueryCacheEnabled = siteConfigs.query_cache_enabled === 'true' || siteConfigs.query_cache_enabled === '1';
    const isTabularSemanticEnabled = siteConfigs.chat_tabular_semantic_columns_enabled === 'true' || siteConfigs.chat_tabular_semantic_columns_enabled === '1';
    const isCorpusTableEnabled = siteConfigs.chat_corpus_table_enabled === 'true' || siteConfigs.chat_corpus_table_enabled === '1';

    const [openMap, setOpenMap] = useState<Record<string, boolean>>(() => {
        try {
            const raw = localStorage.getItem(STORAGE_KEY);
            if (raw) return JSON.parse(raw);
        } catch { /* ignore corrupt entry */ }
        return { general: true };
    });
    useEffect(() => {
        try { localStorage.setItem(STORAGE_KEY, JSON.stringify(openMap)); } catch { /* quota or disabled storage */ }
    }, [openMap]);

    const [filter, setFilter] = useState('');
    const trimmedFilter = filter.trim().toLowerCase();

    const sectionSearch = useMemo(() => {
        const r: Record<string, string> = {};
        for (const s of SECTION_CONFIGS) {
            const parts: string[] = [t(s.titleKey)];
            for (const k of s.i18nKeys) parts.push(t(k));
            for (const k of s.settingKeys) parts.push(k);
            r[s.id] = parts.join(' ').toLowerCase();
        }
        return r;
    }, [t]);

    const sectionState = (id: SectionId) => {
        const isFiltering = trimmedFilter.length > 0;
        const matches = isFiltering ? sectionSearch[id]?.includes(trimmedFilter) : true;
        return {
            hidden: isFiltering && !matches,
            open: isFiltering ? !!matches : !!openMap[id],
            onToggle: () => setOpenMap(prev => ({ ...prev, [id]: !prev[id] })),
        };
    };

    const expandAll = () => setOpenMap(Object.fromEntries(SECTION_CONFIGS.map(s => [s.id, true])));
    const collapseAll = () => setOpenMap({});

    return (
        <motion.div {...getMotionProps(reducedMotion)} initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="result-card" style={{ padding: '2rem' }}>
            <h3 style={{ marginTop: 0 }}>{t('agentConfig')}</h3>
            <p style={{ opacity: 0.7 }}>{t('agentConfigDesc')}</p>

            <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginTop: '1.5rem', flexWrap: 'wrap' }}>
                <div style={{ position: 'relative', flex: '1 1 280px', maxWidth: '480px' }}>
                    <Search size={16} style={{ position: 'absolute', left: '0.75rem', top: '50%', transform: 'translateY(-50%)', opacity: 0.55, pointerEvents: 'none' }} />
                    <input
                        type="text"
                        placeholder={t('agentFilterPlaceholder')}
                        value={filter}
                        onChange={e => setFilter(e.target.value)}
                        style={{
                            width: '100%',
                            background: 'var(--bg-primary)',
                            border: '1px solid var(--border-color)',
                            padding: '0.65rem 0.75rem 0.65rem 2.25rem',
                            borderRadius: '8px',
                            color: 'var(--text-primary)',
                        }}
                    />
                </div>
                <button type="button" onClick={expandAll} style={{ background: 'transparent', border: '1px solid var(--border-color)', color: 'var(--text-primary)', padding: '0.5rem 0.85rem', borderRadius: '8px', cursor: 'pointer', fontSize: '0.85rem' }}>
                    {t('agentExpandAll')}
                </button>
                <button type="button" onClick={collapseAll} style={{ background: 'transparent', border: '1px solid var(--border-color)', color: 'var(--text-primary)', padding: '0.5rem 0.85rem', borderRadius: '8px', cursor: 'pointer', fontSize: '0.85rem' }}>
                    {t('agentCollapseAll')}
                </button>
            </div>

            <form onSubmit={onSubmit} className="form-grid" style={{ display: 'flex', flexDirection: 'column', gap: '1rem', marginTop: '1.5rem' }}>

                <Section title={t('agentSectionGeneral')} {...sectionState('general')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="default-top-k">{t('defaultTopK')}</label>
                        <input
                            id="default-top-k"
                            type="number"
                            min="1"
                            max="50"
                            value={siteConfigs.default_top_k || '5'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, default_top_k: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('defaultTopKHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="score-drop-threshold">{t('scoreDropThreshold')}</label>
                        <input
                            id="score-drop-threshold"
                            type="number"
                            min="0"
                            max="1"
                            step="0.05"
                            value={siteConfigs.score_drop_threshold || '0.15'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, score_drop_threshold: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('scoreDropThresholdHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="context-window-size">{t('contextWindowSize')}</label>
                        <input
                            id="context-window-size"
                            type="number"
                            min="0"
                            max="5"
                            value={siteConfigs.context_window_size || '1'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, context_window_size: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('contextWindowSizeHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionHybridSearch')} {...sectionState('hybrid')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="min-similarity-threshold">{t('minSimilarityThreshold')}</label>
                        <input
                            id="min-similarity-threshold"
                            type="number"
                            min="0"
                            max="1"
                            step="0.05"
                            value={siteConfigs.min_similarity_threshold || '0.3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, min_similarity_threshold: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('minSimilarityThresholdHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="mmr-lambda">{t('mmrLambda')}</label>
                        <input
                            id="mmr-lambda"
                            type="number"
                            min="0"
                            max="1"
                            step="0.05"
                            value={siteConfigs.mmr_lambda || '0.7'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, mmr_lambda: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('mmrLambdaHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="rrf-weight-vector">{t('rrfWeightVector')}</label>
                        <input
                            id="rrf-weight-vector"
                            type="number"
                            min="0"
                            max="10"
                            step="0.1"
                            value={siteConfigs.rrf_weight_vector || '1.0'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, rrf_weight_vector: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rrfWeightVectorHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="rrf-weight-bm25">{t('rrfWeightBM25')}</label>
                        <input
                            id="rrf-weight-bm25"
                            type="number"
                            min="0"
                            max="10"
                            step="0.1"
                            value={siteConfigs.rrf_weight_bm25 || '1.0'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, rrf_weight_bm25: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rrfWeightBM25Help')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="bm25-simple-arm-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="bm25-simple-arm-enabled"
                                type="checkbox"
                                checked={siteConfigs.bm25_simple_arm_enabled === 'true' || siteConfigs.bm25_simple_arm_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, bm25_simple_arm_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('bm25SimpleArmEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('bm25SimpleArmEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="bm25-tiered-boost-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="bm25-tiered-boost-enabled"
                                type="checkbox"
                                checked={siteConfigs.bm25_tiered_boost_enabled === 'true' || siteConfigs.bm25_tiered_boost_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, bm25_tiered_boost_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('bm25TieredBoostEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('bm25TieredBoostEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="hnsw-ef-search">{t('hnswEfSearch')}</label>
                        <input
                            id="hnsw-ef-search"
                            type="number"
                            min="1"
                            max="1000"
                            step="10"
                            value={siteConfigs.hnsw_ef_search || '150'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, hnsw_ef_search: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('hnswEfSearchHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="mrl-two-pass-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="mrl-two-pass-enabled"
                                type="checkbox"
                                checked={siteConfigs.mrl_two_pass_enabled === 'true' || siteConfigs.mrl_two_pass_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, mrl_two_pass_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('mrlTwoPassEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('mrlTwoPassEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '600px' }}>
                        <label htmlFor="query-instruction">{t('queryInstruction')}</label>
                        <input
                            id="query-instruction"
                            type="text"
                            placeholder="Given a question, retrieve all relevant passages that provide information to answer it"
                            value={siteConfigs.query_instruction || ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, query_instruction: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryInstructionHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="hype-search-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="hype-search-enabled"
                                type="checkbox"
                                checked={siteConfigs.hype_search_enabled === 'true'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, hype_search_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('hyPESearchEnabled')}
                        </label>
                    </div>
                </Section>

                <Section title={t('agentSectionReranker')} {...sectionState('reranker')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="rerank-blend-alpha">{t('rerankBlendAlpha')}</label>
                        <input
                            id="rerank-blend-alpha"
                            type="number"
                            min="0"
                            max="1"
                            step="0.05"
                            value={siteConfigs.rerank_blend_alpha || '0.5'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, rerank_blend_alpha: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rerankBlendAlphaHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="rerank-blend-alpha-lookup">{t('rerankBlendAlphaLookup')}</label>
                        <input
                            id="rerank-blend-alpha-lookup"
                            type="number"
                            min="-1"
                            max="1"
                            step="0.05"
                            placeholder="-1 (inherit)"
                            value={siteConfigs.rerank_blend_alpha_lookup ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, rerank_blend_alpha_lookup: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rerankBlendAlphaLookupHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="rerank-blend-alpha-enumeration">{t('rerankBlendAlphaEnumeration')}</label>
                        <input
                            id="rerank-blend-alpha-enumeration"
                            type="number"
                            min="-1"
                            max="1"
                            step="0.05"
                            placeholder="-1 (inherit)"
                            value={siteConfigs.rerank_blend_alpha_enumeration ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, rerank_blend_alpha_enumeration: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rerankBlendAlphaEnumerationHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="rerank-blend-alpha-complex-reasoning">{t('rerankBlendAlphaComplexReasoning')}</label>
                        <input
                            id="rerank-blend-alpha-complex-reasoning"
                            type="number"
                            min="-1"
                            max="1"
                            step="0.05"
                            placeholder="-1 (inherit)"
                            value={siteConfigs.rerank_blend_alpha_complex_reasoning ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, rerank_blend_alpha_complex_reasoning: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rerankBlendAlphaComplexReasoningHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="rerank-blend-alpha-entity">{t('rerankBlendAlphaEntity')}</label>
                        <input
                            id="rerank-blend-alpha-entity"
                            type="number"
                            min="-1"
                            max="1"
                            step="0.05"
                            placeholder="-1 (inherit)"
                            value={siteConfigs.rerank_blend_alpha_entity ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, rerank_blend_alpha_entity: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rerankBlendAlphaEntityHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="hybrid-dynamic-alpha-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="hybrid-dynamic-alpha-enabled"
                                type="checkbox"
                                checked={siteConfigs.hybrid_dynamic_alpha_enabled === 'true' || siteConfigs.hybrid_dynamic_alpha_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, hybrid_dynamic_alpha_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('hybridDynamicAlphaEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('hybridDynamicAlphaEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="hybrid-dynamic-alpha-sensitivity">{t('hybridDynamicAlphaSensitivity')}</label>
                        <input
                            id="hybrid-dynamic-alpha-sensitivity"
                            type="number"
                            min="0"
                            max="1"
                            step="0.05"
                            value={siteConfigs.hybrid_dynamic_alpha_sensitivity || '0.3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, hybrid_dynamic_alpha_sensitivity: e.target.value }))}
                            disabled={!(siteConfigs.hybrid_dynamic_alpha_enabled === 'true' || siteConfigs.hybrid_dynamic_alpha_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('hybridDynamicAlphaSensitivityHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="rerank-use-chat-template" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="rerank-use-chat-template"
                                type="checkbox"
                                checked={siteConfigs.rerank_use_chat_template === 'true'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, rerank_use_chat_template: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('rerankUseChatTemplate')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rerankUseChatTemplateHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '600px' }}>
                        <label htmlFor="rerank-instruction">{t('rerankInstruction')}</label>
                        <input
                            id="rerank-instruction"
                            type="text"
                            placeholder="Given a web search query, retrieve relevant passages that answer the query"
                            value={siteConfigs.rerank_instruction || ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, rerank_instruction: e.target.value }))}
                            disabled={siteConfigs.rerank_use_chat_template !== 'true'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('rerankInstructionHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionPerRouteTopN')} {...sectionState('topn')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="top-n-lookup">{t('topNLookup')}</label>
                        <input
                            id="top-n-lookup"
                            type="number"
                            min="0"
                            max="50"
                            step="1"
                            placeholder="0 (inherit default_top_k)"
                            value={siteConfigs.top_n_lookup ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, top_n_lookup: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('topNLookupHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="top-n-enumeration">{t('topNEnumeration')}</label>
                        <input
                            id="top-n-enumeration"
                            type="number"
                            min="0"
                            max="50"
                            step="1"
                            placeholder="0 (inherit default_top_k)"
                            value={siteConfigs.top_n_enumeration ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, top_n_enumeration: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('topNEnumerationHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="top-n-complex-reasoning">{t('topNComplexReasoning')}</label>
                        <input
                            id="top-n-complex-reasoning"
                            type="number"
                            min="0"
                            max="50"
                            step="1"
                            placeholder="0 (inherit default_top_k)"
                            value={siteConfigs.top_n_complex_reasoning ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, top_n_complex_reasoning: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('topNComplexReasoningHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionCompression')} {...sectionState('compression')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-context-compression-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-context-compression-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_context_compression_enabled === 'true' || siteConfigs.chat_context_compression_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_context_compression_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatContextCompressionEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatContextCompressionEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-context-compression-min-chunks">{t('chatContextCompressionMinChunks')}</label>
                        <input
                            id="chat-context-compression-min-chunks"
                            type="number"
                            min={1}
                            max={50}
                            step={1}
                            value={siteConfigs.chat_context_compression_min_chunks || '15'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_context_compression_min_chunks: e.target.value }))}
                            disabled={!(siteConfigs.chat_context_compression_enabled === 'true' || siteConfigs.chat_context_compression_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatContextCompressionMinChunksHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-context-compression-threshold">{t('chatContextCompressionThreshold')}</label>
                        <input
                            id="chat-context-compression-threshold"
                            type="number"
                            min={0}
                            max={1}
                            step={0.05}
                            value={siteConfigs.chat_context_compression_threshold || '0.3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_context_compression_threshold: e.target.value }))}
                            disabled={!(siteConfigs.chat_context_compression_enabled === 'true' || siteConfigs.chat_context_compression_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatContextCompressionThresholdHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-context-compression-model">{t('chatContextCompressionModel')}</label>
                        <input
                            id="chat-context-compression-model"
                            type="text"
                            placeholder="(fast-tier default)"
                            value={siteConfigs.chat_context_compression_model ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_context_compression_model: e.target.value }))}
                            disabled={!(siteConfigs.chat_context_compression_enabled === 'true' || siteConfigs.chat_context_compression_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatContextCompressionModelHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionQueryEnhancement')} {...sectionState('queryEnh')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="auto-spell-correct" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="auto-spell-correct"
                                type="checkbox"
                                checked={siteConfigs.auto_spell_correct !== 'false'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, auto_spell_correct: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('autoSpellCorrect')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('autoSpellCorrectHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="step-back-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="step-back-enabled"
                                type="checkbox"
                                checked={siteConfigs.step_back_enabled === 'true' || siteConfigs.step_back_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, step_back_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('stepBackEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('stepBackEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="query-decompose-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="query-decompose-enabled"
                                type="checkbox"
                                checked={siteConfigs.query_decompose_enabled === 'true' || siteConfigs.query_decompose_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, query_decompose_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('queryDecomposeEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryDecomposeEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="query-decompose-model">{t('queryDecomposeModel')}</label>
                        <input
                            id="query-decompose-model"
                            type="text"
                            placeholder="(fast-tier default)"
                            value={siteConfigs.query_decompose_model ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, query_decompose_model: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryDecomposeModelHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longcontext-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-longcontext-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_longcontext_enabled === 'true' || siteConfigs.chat_longcontext_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longcontext_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatLongcontextEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongcontextEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longcontext-max-tokens">{t('chatLongcontextMaxTokens')}</label>
                        <input
                            id="chat-longcontext-max-tokens"
                            type="number"
                            min={10000}
                            max={500000}
                            step={5000}
                            value={siteConfigs.chat_longcontext_max_tokens || '100000'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longcontext_max_tokens: e.target.value }))}
                            disabled={!(siteConfigs.chat_longcontext_enabled === 'true' || siteConfigs.chat_longcontext_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongcontextMaxTokensHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="query-cache-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="query-cache-enabled"
                                type="checkbox"
                                checked={isQueryCacheEnabled}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, query_cache_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('queryCacheEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryCacheEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="query-cache-similarity-threshold">{t('queryCacheSimilarityThreshold')}</label>
                        <input
                            id="query-cache-similarity-threshold"
                            type="number"
                            min="0"
                            max="1"
                            step="0.01"
                            placeholder="0.96"
                            value={siteConfigs.query_cache_similarity_threshold ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, query_cache_similarity_threshold: e.target.value }))}
                            disabled={!isQueryCacheEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryCacheSimilarityThresholdHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="query-cache-similarity-threshold-lookup">{t('queryCacheSimilarityThresholdLookup')}</label>
                        <input
                            id="query-cache-similarity-threshold-lookup"
                            type="number"
                            min="0"
                            max="1"
                            step="0.01"
                            placeholder="(inherit)"
                            value={siteConfigs.query_cache_similarity_threshold_lookup ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, query_cache_similarity_threshold_lookup: e.target.value }))}
                            disabled={!isQueryCacheEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryCacheSimilarityThresholdLookupHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="query-cache-similarity-threshold-enumeration">{t('queryCacheSimilarityThresholdEnumeration')}</label>
                        <input
                            id="query-cache-similarity-threshold-enumeration"
                            type="number"
                            min="0"
                            max="1"
                            step="0.01"
                            placeholder="(inherit)"
                            value={siteConfigs.query_cache_similarity_threshold_enumeration ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, query_cache_similarity_threshold_enumeration: e.target.value }))}
                            disabled={!isQueryCacheEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryCacheSimilarityThresholdEnumerationHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="query-cache-similarity-threshold-complex-reasoning">{t('queryCacheSimilarityThresholdComplexReasoning')}</label>
                        <input
                            id="query-cache-similarity-threshold-complex-reasoning"
                            type="number"
                            min="0"
                            max="1"
                            step="0.01"
                            placeholder="(inherit)"
                            value={siteConfigs.query_cache_similarity_threshold_complex_reasoning ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, query_cache_similarity_threshold_complex_reasoning: e.target.value }))}
                            disabled={!isQueryCacheEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryCacheSimilarityThresholdComplexReasoningHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="query-cache-ttl-hours">{t('queryCacheTtlHours')}</label>
                        <input
                            id="query-cache-ttl-hours"
                            type="number"
                            min="1"
                            max="720"
                            step="1"
                            placeholder="24"
                            value={siteConfigs.query_cache_ttl_hours ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, query_cache_ttl_hours: e.target.value }))}
                            disabled={!isQueryCacheEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('queryCacheTtlHoursHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionCragAdaptive')} {...sectionState('crag')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="crag-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="crag-enabled"
                                type="checkbox"
                                checked={isCragEnabled}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, crag_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('cragEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('cragEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="crag-min-relevant-chunks">{t('cragMinRelevantChunks')}</label>
                        <input
                            id="crag-min-relevant-chunks"
                            type="number"
                            min="1"
                            max="10"
                            value={siteConfigs.crag_min_relevant_chunks || '3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, crag_min_relevant_chunks: e.target.value }))}
                            disabled={!isCragEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('cragMinRelevantChunksHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="adaptive-routing-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="adaptive-routing-enabled"
                                type="checkbox"
                                checked={siteConfigs.adaptive_routing_enabled === 'true' || siteConfigs.adaptive_routing_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, adaptive_routing_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('adaptiveRoutingEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('adaptiveRoutingEnabledHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionGraph')} {...sectionState('graph')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="kg-extraction-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="kg-extraction-enabled"
                                type="checkbox"
                                checked={siteConfigs.kg_extraction_enabled === 'true' || siteConfigs.kg_extraction_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, kg_extraction_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('kgExtractionEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('kgExtractionEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-graph-routing-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_graph_routing_enabled === 'true' || siteConfigs.chat_graph_routing_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatGraphRoutingEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-inject-chunks" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-graph-routing-inject-chunks"
                                type="checkbox"
                                checked={siteConfigs.chat_graph_routing_inject_chunks === 'true' || siteConfigs.chat_graph_routing_inject_chunks === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_inject_chunks: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatGraphRoutingInjectChunks')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingInjectChunksHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-max-chunks">{t('chatGraphRoutingMaxChunks')}</label>
                        <input
                            id="chat-graph-routing-max-chunks"
                            type="number"
                            min="1"
                            max="50"
                            step="1"
                            value={siteConfigs.chat_graph_routing_max_chunks || '15'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_max_chunks: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingMaxChunksHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-path-mode">{t('chatGraphRoutingPathMode')}</label>
                        <select
                            id="chat-graph-routing-path-mode"
                            value={siteConfigs.chat_graph_routing_path_mode || 'neighbors'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_path_mode: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        >
                            <option value="neighbors">{t('chatGraphRoutingPathModeNeighbors')}</option>
                            <option value="ppr">{t('chatGraphRoutingPathModePPR')}</option>
                            <option value="paths">{t('chatGraphRoutingPathModePaths')}</option>
                        </select>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingPathModeHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-ppr-damping">{t('chatGraphRoutingPPRDamping')}</label>
                        <input
                            id="chat-graph-routing-ppr-damping"
                            type="number"
                            min="0.01"
                            max="0.99"
                            step="0.05"
                            value={siteConfigs.chat_graph_routing_ppr_damping || '0.85'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_ppr_damping: e.target.value }))}
                            disabled={(siteConfigs.chat_graph_routing_path_mode || 'neighbors') !== 'ppr'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingPPRDampingHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-ppr-max-iter">{t('chatGraphRoutingPPRMaxIter')}</label>
                        <input
                            id="chat-graph-routing-ppr-max-iter"
                            type="number"
                            min="1"
                            max="100"
                            step="1"
                            value={siteConfigs.chat_graph_routing_ppr_max_iter || '20'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_ppr_max_iter: e.target.value }))}
                            disabled={(siteConfigs.chat_graph_routing_path_mode || 'neighbors') !== 'ppr'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingPPRMaxIterHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-ppr-top-entities">{t('chatGraphRoutingPPRTopEntities')}</label>
                        <input
                            id="chat-graph-routing-ppr-top-entities"
                            type="number"
                            min="1"
                            max="50"
                            step="1"
                            value={siteConfigs.chat_graph_routing_ppr_top_entities || '10'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_ppr_top_entities: e.target.value }))}
                            disabled={(siteConfigs.chat_graph_routing_path_mode || 'neighbors') !== 'ppr'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingPPRTopEntitiesHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-paths-max-len">{t('chatGraphRoutingPathsMaxLen')}</label>
                        <input
                            id="chat-graph-routing-paths-max-len"
                            type="number"
                            min="1"
                            max="6"
                            step="1"
                            value={siteConfigs.chat_graph_routing_paths_max_len || '3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_paths_max_len: e.target.value }))}
                            disabled={(siteConfigs.chat_graph_routing_path_mode || 'neighbors') !== 'paths'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingPathsMaxLenHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-graph-routing-paths-max-paths">{t('chatGraphRoutingPathsMaxPaths')}</label>
                        <input
                            id="chat-graph-routing-paths-max-paths"
                            type="number"
                            min="1"
                            max="50"
                            step="1"
                            value={siteConfigs.chat_graph_routing_paths_max_paths || '5'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_graph_routing_paths_max_paths: e.target.value }))}
                            disabled={(siteConfigs.chat_graph_routing_path_mode || 'neighbors') !== 'paths'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatGraphRoutingPathsMaxPathsHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionMultiStep')} {...sectionState('multistep')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-kb-router-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-kb-router-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_kb_router_enabled === 'true' || siteConfigs.chat_kb_router_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_kb_router_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatKBRouterEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatKBRouterEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-kb-router-min-confidence">{t('chatKBRouterMinConfidence')}</label>
                        <input
                            id="chat-kb-router-min-confidence"
                            type="number"
                            min="0"
                            max="1"
                            step="0.05"
                            value={siteConfigs.chat_kb_router_min_confidence || '0.6'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_kb_router_min_confidence: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatKBRouterMinConfidenceHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-turn-budget-seconds">{t('chatTurnBudgetSeconds')}</label>
                        <input
                            id="chat-turn-budget-seconds"
                            type="number"
                            min="0"
                            max="600"
                            value={siteConfigs.chat_turn_budget_seconds || '0'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_turn_budget_seconds: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatTurnBudgetSecondsHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-turn-budget-tokens">{t('chatTurnBudgetTokens')}</label>
                        <input
                            id="chat-turn-budget-tokens"
                            type="number"
                            min="0"
                            max="2000000"
                            value={siteConfigs.chat_turn_budget_tokens || '0'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_turn_budget_tokens: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatTurnBudgetTokensHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-turn-budget-tool-calls">{t('chatTurnBudgetToolCalls')}</label>
                        <input
                            id="chat-turn-budget-tool-calls"
                            type="number"
                            min="0"
                            max="100"
                            value={siteConfigs.chat_turn_budget_tool_calls || '0'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_turn_budget_tool_calls: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatTurnBudgetToolCallsHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-agentic-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-agentic-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_agentic_enabled === 'true' || siteConfigs.chat_agentic_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_agentic_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatAgenticEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatAgenticEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-agentic-max-hops">{t('chatAgenticMaxHops')}</label>
                        <input
                            id="chat-agentic-max-hops"
                            type="number"
                            min="1"
                            max="5"
                            step="1"
                            value={siteConfigs.chat_agentic_max_hops || '3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_agentic_max_hops: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatAgenticMaxHopsHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-plan-execute-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-plan-execute-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_plan_execute_enabled === 'true' || siteConfigs.chat_plan_execute_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_plan_execute_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatPlanExecuteEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatPlanExecuteEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-plan-execute-max-sub-queries">{t('chatPlanExecuteMaxSubQueries')}</label>
                        <input
                            id="chat-plan-execute-max-sub-queries"
                            type="number"
                            min="1"
                            max="5"
                            step="1"
                            value={siteConfigs.chat_plan_execute_max_sub_queries || '3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_plan_execute_max_sub_queries: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatPlanExecuteMaxSubQueriesHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-plan-execute-max-iterations">{t('chatPlanExecuteMaxIterations')}</label>
                        <input
                            id="chat-plan-execute-max-iterations"
                            type="number"
                            min="1"
                            max="5"
                            step="1"
                            value={siteConfigs.chat_plan_execute_max_iterations || '3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_plan_execute_max_iterations: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatPlanExecuteMaxIterationsHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-plan-execute-token-budget">{t('chatPlanExecuteTokenBudget')}</label>
                        <input
                            id="chat-plan-execute-token-budget"
                            type="number"
                            min="2000"
                            max="32000"
                            step="500"
                            value={siteConfigs.chat_plan_execute_token_budget || '8000'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_plan_execute_token_budget: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatPlanExecuteTokenBudgetHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-plan-execute-tool-aware" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-plan-execute-tool-aware"
                                type="checkbox"
                                checked={siteConfigs.chat_plan_execute_tool_aware === 'true' || siteConfigs.chat_plan_execute_tool_aware === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_plan_execute_tool_aware: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatPlanExecuteToolAware')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatPlanExecuteToolAwareHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-plan-execute-dag-iterative" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-plan-execute-dag-iterative"
                                type="checkbox"
                                checked={siteConfigs.chat_plan_execute_dag_iterative === 'true' || siteConfigs.chat_plan_execute_dag_iterative === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_plan_execute_dag_iterative: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatPlanExecuteDAGIterative')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatPlanExecuteDAGIterativeHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-answer-tools-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-answer-tools-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_answer_tools_enabled === 'true' || siteConfigs.chat_answer_tools_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_answer_tools_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatAnswerToolsEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatAnswerToolsEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-answer-tools-max-rounds">{t('chatAnswerToolsMaxRounds')}</label>
                        <input
                            id="chat-answer-tools-max-rounds"
                            type="number"
                            min="1"
                            max="10"
                            step="1"
                            value={siteConfigs.chat_answer_tools_max_rounds || '5'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_answer_tools_max_rounds: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatAnswerToolsMaxRoundsHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-supervisor-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-supervisor-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_supervisor_enabled === 'true' || siteConfigs.chat_supervisor_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_supervisor_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatSupervisorEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatSupervisorEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-supervisor-multi-specialist" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-supervisor-multi-specialist"
                                type="checkbox"
                                checked={siteConfigs.chat_supervisor_multi_specialist === 'true' || siteConfigs.chat_supervisor_multi_specialist === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_supervisor_multi_specialist: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatSupervisorMultiSpecialist')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatSupervisorMultiSpecialistHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionConversation')} {...sectionState('conversation')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-answer-history-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-answer-history-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_answer_history_enabled !== 'false' && siteConfigs.chat_answer_history_enabled !== '0'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_answer_history_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatAnswerHistoryEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatAnswerHistoryEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-answer-history-messages">{t('chatAnswerHistoryMessages')}</label>
                        <input
                            id="chat-answer-history-messages"
                            type="number"
                            min="1"
                            max="50"
                            step="1"
                            value={siteConfigs.chat_answer_history_messages || '6'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_answer_history_messages: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatAnswerHistoryMessagesHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-answer-history-max-chars">{t('chatAnswerHistoryMaxChars')}</label>
                        <input
                            id="chat-answer-history-max-chars"
                            type="number"
                            min="200"
                            max="32000"
                            step="100"
                            value={siteConfigs.chat_answer_history_max_chars || '4000'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_answer_history_max_chars: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatAnswerHistoryMaxCharsHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-transform-followup-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-transform-followup-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_transform_followup_enabled !== 'false' && siteConfigs.chat_transform_followup_enabled !== '0'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_transform_followup_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatTransformFollowupEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatTransformFollowupEnabledHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionCorpusTable')} {...sectionState('corpusTable')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-corpus-table-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-corpus-table-enabled"
                                type="checkbox"
                                checked={isCorpusTableEnabled}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_corpus_table_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatCorpusTableEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCorpusTableEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-corpus-table-model">{t('chatCorpusTableModel')}</label>
                        <input
                            id="chat-corpus-table-model"
                            type="text"
                            placeholder="(fast-tier default)"
                            value={siteConfigs.chat_corpus_table_model ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_corpus_table_model: e.target.value }))}
                            disabled={!isCorpusTableEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCorpusTableModelHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-corpus-table-max-files">{t('chatCorpusTableMaxFiles')}</label>
                        <input
                            id="chat-corpus-table-max-files"
                            type="number"
                            min={1}
                            max={500}
                            step={1}
                            value={siteConfigs.chat_corpus_table_max_files || '50'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_corpus_table_max_files: e.target.value }))}
                            disabled={!isCorpusTableEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCorpusTableMaxFilesHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-corpus-table-concurrency">{t('chatCorpusTableConcurrency')}</label>
                        <input
                            id="chat-corpus-table-concurrency"
                            type="number"
                            min={1}
                            max={50}
                            step={1}
                            value={siteConfigs.chat_corpus_table_concurrency || '6'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_corpus_table_concurrency: e.target.value }))}
                            disabled={!isCorpusTableEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCorpusTableConcurrencyHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-corpus-table-router-llm-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-corpus-table-router-llm-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_corpus_table_router_llm_enabled !== 'false' && siteConfigs.chat_corpus_table_router_llm_enabled !== '0'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_corpus_table_router_llm_enabled: e.target.checked ? 'true' : 'false' }))}
                                disabled={!isCorpusTableEnabled}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatCorpusTableRouterLlmEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCorpusTableRouterLlmEnabledHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionCompare')} {...sectionState('compare')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-compare-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-compare-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_compare_enabled === 'true' || siteConfigs.chat_compare_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_compare_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatCompareEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCompareEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-compare-model">{t('chatCompareModel')}</label>
                        <input
                            id="chat-compare-model"
                            type="text"
                            placeholder="(fast-tier default)"
                            value={siteConfigs.chat_compare_model ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_compare_model: e.target.value }))}
                            disabled={!(siteConfigs.chat_compare_enabled === 'true' || siteConfigs.chat_compare_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCompareModelHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-compare-max-sections">{t('chatCompareMaxSections')}</label>
                        <input
                            id="chat-compare-max-sections"
                            type="number"
                            min={1}
                            max={500}
                            step={1}
                            value={siteConfigs.chat_compare_max_sections || '60'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_compare_max_sections: e.target.value }))}
                            disabled={!(siteConfigs.chat_compare_enabled === 'true' || siteConfigs.chat_compare_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCompareMaxSectionsHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-compare-concurrency">{t('chatCompareConcurrency')}</label>
                        <input
                            id="chat-compare-concurrency"
                            type="number"
                            min={1}
                            max={32}
                            step={1}
                            value={siteConfigs.chat_compare_concurrency || '6'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_compare_concurrency: e.target.value }))}
                            disabled={!(siteConfigs.chat_compare_enabled === 'true' || siteConfigs.chat_compare_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCompareConcurrencyHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-compare-peers-per-section">{t('chatComparePeersPerSection')}</label>
                        <input
                            id="chat-compare-peers-per-section"
                            type="number"
                            min={1}
                            max={20}
                            step={1}
                            value={siteConfigs.chat_compare_peers_per_section || '5'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_compare_peers_per_section: e.target.value }))}
                            disabled={!(siteConfigs.chat_compare_enabled === 'true' || siteConfigs.chat_compare_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatComparePeersPerSectionHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-compare-attachment-ttl-hours">{t('chatCompareAttachmentTtlHours')}</label>
                        <input
                            id="chat-compare-attachment-ttl-hours"
                            type="number"
                            min={1}
                            max={720}
                            step={1}
                            value={siteConfigs.chat_compare_attachment_ttl_hours || '24'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_compare_attachment_ttl_hours: e.target.value }))}
                            disabled={!(siteConfigs.chat_compare_enabled === 'true' || siteConfigs.chat_compare_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCompareAttachmentTtlHoursHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-compare-max-file-bytes">{t('chatCompareMaxFileBytes')}</label>
                        <input
                            id="chat-compare-max-file-bytes"
                            type="number"
                            min={1024}
                            max={104857600}
                            step={1024}
                            value={siteConfigs.chat_compare_max_file_bytes || '10485760'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_compare_max_file_bytes: e.target.value }))}
                            disabled={!(siteConfigs.chat_compare_enabled === 'true' || siteConfigs.chat_compare_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCompareMaxFileBytesHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionLongmem')} {...sectionState('longmem')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longmem-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-longmem-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_longmem_enabled === 'true' || siteConfigs.chat_longmem_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longmem_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatLongmemEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongmemEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longmem-min-salience">{t('chatLongmemMinSalience')}</label>
                        <input
                            id="chat-longmem-min-salience"
                            type="number"
                            min="0"
                            max="1"
                            step="0.05"
                            value={siteConfigs.chat_longmem_min_salience || '0.5'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longmem_min_salience: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongmemMinSalienceHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longmem-recall-top-k">{t('chatLongmemRecallTopK')}</label>
                        <input
                            id="chat-longmem-recall-top-k"
                            type="number"
                            min="1"
                            max="20"
                            value={siteConfigs.chat_longmem_recall_top_k || '5'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longmem_recall_top_k: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongmemRecallTopKHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longmem-decay-days">{t('chatLongmemDecayDays')}</label>
                        <input
                            id="chat-longmem-decay-days"
                            type="number"
                            min="1"
                            max="365"
                            value={siteConfigs.chat_longmem_decay_days || '30'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longmem_decay_days: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongmemDecayDaysHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longmem-recall-semantic" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-longmem-recall-semantic"
                                type="checkbox"
                                checked={siteConfigs.chat_longmem_recall_semantic === 'true' || siteConfigs.chat_longmem_recall_semantic === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longmem_recall_semantic: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatLongmemRecallSemantic')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongmemRecallSemanticHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longmem-conflict-resolution" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-longmem-conflict-resolution"
                                type="checkbox"
                                checked={siteConfigs.chat_longmem_conflict_resolution === 'true' || siteConfigs.chat_longmem_conflict_resolution === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longmem_conflict_resolution: e.target.checked ? 'true' : 'false' }))}
                                disabled={!(siteConfigs.chat_longmem_recall_semantic === 'true' || siteConfigs.chat_longmem_recall_semantic === '1')}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatLongmemConflictResolution')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongmemConflictResolutionHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longmem-conflict-model">{t('chatLongmemConflictModel')}</label>
                        <input
                            id="chat-longmem-conflict-model"
                            type="text"
                            placeholder="(fast-tier default)"
                            value={siteConfigs.chat_longmem_conflict_model ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longmem_conflict_model: e.target.value }))}
                            disabled={!(siteConfigs.chat_longmem_conflict_resolution === 'true' || siteConfigs.chat_longmem_conflict_resolution === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongmemConflictModelHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-longmem-conflict-candidates">{t('chatLongmemConflictCandidates')}</label>
                        <input
                            id="chat-longmem-conflict-candidates"
                            type="number"
                            min="1"
                            max="10"
                            step="1"
                            value={siteConfigs.chat_longmem_conflict_candidates || '3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_longmem_conflict_candidates: e.target.value }))}
                            disabled={!(siteConfigs.chat_longmem_conflict_resolution === 'true' || siteConfigs.chat_longmem_conflict_resolution === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatLongmemConflictCandidatesHelp')}</p>
                    </div>

                </Section>

                <Section title={t('agentSectionValidation')} {...sectionState('validation')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="factcheck-in-chat" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="factcheck-in-chat"
                                type="checkbox"
                                checked={siteConfigs.factcheck_in_chat !== 'false' && siteConfigs.factcheck_in_chat !== '0'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, factcheck_in_chat: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('factcheckInChat')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('factcheckInChatHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="citation-validation-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="citation-validation-enabled"
                                type="checkbox"
                                checked={siteConfigs.citation_validation_enabled !== 'false' && siteConfigs.citation_validation_enabled !== '0'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, citation_validation_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('citationValidationEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('citationValidationEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="citation-validation-semantic-threshold">{t('citationValidationSemanticThreshold')}</label>
                        <input
                            id="citation-validation-semantic-threshold"
                            type="number"
                            min="0"
                            max="1"
                            step="0.05"
                            value={siteConfigs.citation_validation_semantic_threshold || '0.85'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, citation_validation_semantic_threshold: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('citationValidationSemanticThresholdHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-factuality-gate-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-factuality-gate-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_factuality_gate_enabled === 'true' || siteConfigs.chat_factuality_gate_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_factuality_gate_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatFactualityGateEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatFactualityGateEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-factuality-gate-max-refines">{t('chatFactualityGateMaxRefines')}</label>
                        <input
                            id="chat-factuality-gate-max-refines"
                            type="number"
                            min="0"
                            max="2"
                            step="1"
                            value={siteConfigs.chat_factuality_gate_max_refines || '1'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, chat_factuality_gate_max_refines: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatFactualityGateMaxRefinesHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="chat-self-rag-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-self-rag-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_self_rag_enabled === 'true' || siteConfigs.chat_self_rag_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_self_rag_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatSelfRAGEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatSelfRAGEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="ragas-sampling-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="ragas-sampling-enabled"
                                type="checkbox"
                                checked={siteConfigs.ragas_sampling_enabled === 'true' || siteConfigs.ragas_sampling_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, ragas_sampling_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('ragasSamplingEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('ragasSamplingEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="ragas-sampling-rate">{t('ragasSamplingRate')}</label>
                        <input
                            id="ragas-sampling-rate"
                            type="number"
                            min="0"
                            max="1"
                            step="0.01"
                            value={siteConfigs.ragas_sampling_rate || '0.0'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, ragas_sampling_rate: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('ragasSamplingRateHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionIngestion')} {...sectionState('ingestion')}>
                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="docling-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="docling-enabled"
                                type="checkbox"
                                checked={isDoclingEnabled}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, docling_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('doclingEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('doclingEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '600px' }}>
                        <label htmlFor="docling-base-url">{t('doclingBaseUrl')}</label>
                        <input
                            id="docling-base-url"
                            type="text"
                            placeholder="http://docling:5001"
                            value={siteConfigs.docling_base_url || ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, docling_base_url: e.target.value }))}
                            disabled={!isDoclingEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('doclingBaseUrlHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="describe-image-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="describe-image-enabled"
                                type="checkbox"
                                checked={isDescribeImageEnabled}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, describe_image_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('describeImageEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('describeImageEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="describe-image-model">{t('describeImageModel')}</label>
                        <input
                            id="describe-image-model"
                            type="text"
                            placeholder="(fast-tier default)"
                            value={siteConfigs.describe_image_model ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, describe_image_model: e.target.value }))}
                            disabled={!isDescribeImageEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('describeImageModelHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="contextual-enrichment" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="contextual-enrichment"
                                type="checkbox"
                                checked={isContextualEnrichmentEnabled}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, contextual_enrichment: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('contextualEnrichment')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('contextualEnrichmentHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="embedding-batch-size">{t('embeddingBatchSize')}</label>
                        <input
                            id="embedding-batch-size"
                            type="number"
                            min="1"
                            max="500"
                            step="1"
                            value={siteConfigs.embedding_batch_size || '20'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, embedding_batch_size: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('embeddingBatchSizeHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="late-chunking-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="late-chunking-enabled"
                                type="checkbox"
                                checked={siteConfigs.late_chunking_enabled === 'true' || siteConfigs.late_chunking_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, late_chunking_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('lateChunkingEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('lateChunkingEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="late-chunking-max-input-tokens">{t('lateChunkingMaxInputTokens')}</label>
                        <input
                            id="late-chunking-max-input-tokens"
                            type="number"
                            min="512"
                            max="32768"
                            step="512"
                            value={siteConfigs.late_chunking_max_input_tokens || '8192'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, late_chunking_max_input_tokens: e.target.value }))}
                            disabled={!(siteConfigs.late_chunking_enabled === 'true' || siteConfigs.late_chunking_enabled === '1')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('lateChunkingMaxInputTokensHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="parent-child-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="parent-child-enabled"
                                type="checkbox"
                                checked={siteConfigs.parent_child_enabled === 'true' || siteConfigs.parent_child_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, parent_child_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('parentChildEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('parentChildEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="parent-chunk-size">{t('parentChunkSize')}</label>
                        <input
                            id="parent-chunk-size"
                            type="number"
                            min="128"
                            max="4096"
                            step="64"
                            value={siteConfigs.parent_chunk_size || '512'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, parent_chunk_size: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('parentChunkSizeHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="child-chunk-size">{t('childChunkSize')}</label>
                        <input
                            id="child-chunk-size"
                            type="number"
                            min="32"
                            max="512"
                            step="16"
                            value={siteConfigs.child_chunk_size || '128'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, child_chunk_size: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('childChunkSizeHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="raptor-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="raptor-enabled"
                                type="checkbox"
                                checked={siteConfigs.raptor_enabled === 'true' || siteConfigs.raptor_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, raptor_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('raptorEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('raptorEnabledHelp')}</p>
                        {(siteConfigs.raptor_enabled === 'true' || siteConfigs.raptor_enabled === '1') &&
                         (siteConfigs.parent_child_enabled === 'true' || siteConfigs.parent_child_enabled === '1') && (
                            <p style={{ fontSize: '0.8rem', color: 'var(--warning, #d97706)', marginTop: '0.5rem' }}>
                                {t('raptorParentChildConflict')}
                            </p>
                        )}
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="raptor-min-chunks">{t('raptorMinChunks')}</label>
                        <input
                            id="raptor-min-chunks"
                            type="number"
                            min={5}
                            max={200}
                            step={5}
                            value={siteConfigs.raptor_min_chunks || '25'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, raptor_min_chunks: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('raptorMinChunksHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="raptor-max-levels">{t('raptorMaxLevels')}</label>
                        <input
                            id="raptor-max-levels"
                            type="number"
                            min={2}
                            max={8}
                            step={1}
                            value={siteConfigs.raptor_max_levels || '4'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, raptor_max_levels: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('raptorMaxLevelsHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="raptor-branching-factor">{t('raptorBranchingFactor')}</label>
                        <input
                            id="raptor-branching-factor"
                            type="number"
                            min={2}
                            max={20}
                            step={1}
                            value={siteConfigs.raptor_branching_factor || '5'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, raptor_branching_factor: e.target.value }))}
                            disabled={(siteConfigs.raptor_clustering_algorithm || 'kmeans') === 'leiden'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('raptorBranchingFactorHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="raptor-clustering-algorithm">{t('raptorClusteringAlgorithm')}</label>
                        <select
                            id="raptor-clustering-algorithm"
                            value={siteConfigs.raptor_clustering_algorithm || 'kmeans'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, raptor_clustering_algorithm: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        >
                            <option value="kmeans">{t('raptorClusteringAlgorithmKMeans')}</option>
                            <option value="leiden">{t('raptorClusteringAlgorithmLeiden')}</option>
                        </select>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('raptorClusteringAlgorithmHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="raptor-leiden-resolution">{t('raptorLeidenResolution')}</label>
                        <input
                            id="raptor-leiden-resolution"
                            type="number"
                            min={0.01}
                            max={10}
                            step={0.05}
                            value={siteConfigs.raptor_leiden_resolution || '1.0'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, raptor_leiden_resolution: e.target.value }))}
                            disabled={(siteConfigs.raptor_clustering_algorithm || 'kmeans') !== 'leiden'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('raptorLeidenResolutionHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="hype-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="hype-enabled"
                                type="checkbox"
                                checked={siteConfigs.hype_enabled === 'true'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, hype_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('hyPEEnabled')}
                        </label>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="hype-questions-per-chunk">{t('hyPEQuestionsPerChunk')}</label>
                        <input
                            id="hype-questions-per-chunk"
                            type="number"
                            min={1}
                            max={20}
                            step={1}
                            value={siteConfigs.hype_questions_per_chunk ?? '3'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, hype_questions_per_chunk: e.target.value }))}
                            disabled={siteConfigs.hype_enabled !== 'true'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="hype-model">{t('hyPEModel')}</label>
                        <input
                            id="hype-model"
                            type="text"
                            placeholder="(fast-tier default)"
                            value={siteConfigs.hype_model ?? ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, hype_model: e.target.value }))}
                            disabled={siteConfigs.hype_enabled !== 'true'}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                    </div>

                </Section>

                <Section title={t('agentSectionObservability')} {...sectionState('observability')}>
                    <div className="input-group" style={{ maxWidth: '600px' }}>
                        <label htmlFor="langfuse-base-url">{t('langfuseBaseUrl')}</label>
                        <input
                            id="langfuse-base-url"
                            type="text"
                            placeholder="https://langfuse.example.local"
                            value={siteConfigs.langfuse_base_url || ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, langfuse_base_url: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('langfuseBaseUrlHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionTools')} {...sectionState('tools')}>
                    <AdminMCPSection siteConfigs={siteConfigs} setSiteConfigs={setSiteConfigs} />

                    <div className="input-group" style={{ maxWidth: '600px' }}>
                        <label htmlFor="chat-code-exec-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-code-exec-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_code_exec_enabled === 'true' || siteConfigs.chat_code_exec_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_code_exec_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatCodeExecEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatCodeExecEnabledHelp')}</p>
                    </div>
                </Section>

                <Section title={t('agentSectionTabular')} {...sectionState('tabular')}>
                    <div className="input-group" style={{ maxWidth: '600px' }}>
                        <label htmlFor="chat-tabular-query-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-tabular-query-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_tabular_query_enabled === 'true' || siteConfigs.chat_tabular_query_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_tabular_query_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatTabularQueryEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatTabularQueryEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '600px' }}>
                        <label htmlFor="chat-tabular-semantic-columns-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-tabular-semantic-columns-enabled"
                                type="checkbox"
                                checked={isTabularSemanticEnabled}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_tabular_semantic_columns_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatTabularSemanticColumnsEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatTabularSemanticColumnsEnabledHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="tabular-semantic-min-avg-len">{t('tabularSemanticMinAvgLen')}</label>
                        <input
                            id="tabular-semantic-min-avg-len"
                            type="number"
                            min={0}
                            step={1}
                            value={siteConfigs.tabular_semantic_min_avg_len || '32'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, tabular_semantic_min_avg_len: e.target.value }))}
                            disabled={!isTabularSemanticEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('tabularSemanticMinAvgLenHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '400px' }}>
                        <label htmlFor="tabular-semantic-min-distinct-ratio">{t('tabularSemanticMinDistinctRatio')}</label>
                        <input
                            id="tabular-semantic-min-distinct-ratio"
                            type="number"
                            min={0}
                            max={1}
                            step={0.05}
                            value={siteConfigs.tabular_semantic_min_distinct_ratio || '0.6'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, tabular_semantic_min_distinct_ratio: e.target.value }))}
                            disabled={!isTabularSemanticEnabled}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('tabularSemanticMinDistinctRatioHelp')}</p>
                    </div>

                    <div className="input-group" style={{ maxWidth: '600px' }}>
                        <label htmlFor="chat-tabular-charts-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                            <input
                                id="chat-tabular-charts-enabled"
                                type="checkbox"
                                checked={siteConfigs.chat_tabular_charts_enabled === 'true' || siteConfigs.chat_tabular_charts_enabled === '1'}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, chat_tabular_charts_enabled: e.target.checked ? 'true' : 'false' }))}
                                style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                            />
                            {t('chatTabularChartsEnabled')}
                        </label>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('chatTabularChartsEnabledHelp')}</p>
                    </div>
                </Section>

                <button type="submit" className="search-button" style={{ width: 'fit-content' }}>
                    <Save size={18} /> {t('saveSettings')}
                </button>
            </form>

            <AdminAgentMetricsCard />
        </motion.div>
    );
}
