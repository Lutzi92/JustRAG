import { Plus, Trash2, CheckCircle2, Settings, Save, X, RefreshCw, AlertCircle } from 'lucide-react';
import { motion, AnimatePresence } from 'framer-motion';
import { useReducedMotion, getMotionProps } from '../../hooks/useReducedMotion';
import { useTheme } from '../../contexts/ThemeContext';
import type { ChatModelOption } from '../../AdminUI';

interface AIModel {
    id?: string;
    name: string;
    isReasoning: boolean;
    isEmbedding: boolean;
    isRerank: boolean;
    isTts: boolean;
    isStt: boolean;
    dimensions?: number;
}

interface AIConfig {
    id: string;
    name: string;
    provider: string;
    api_key: string;
    base_url?: string;
    chat_models: AIModel[];
    embedding_models: AIModel[];
    rerank_models: AIModel[];
    tts_models: AIModel[];
    stt_models: AIModel[];
    is_active: boolean;
}

interface ConnectionTest {
    configId: string;
    status: 'testing' | 'healthy' | 'unhealthy';
    latencyMs?: number;
    error?: string;
}

// Per-task model overrides surfaced in the AI Config tab. The backend
// resolution chain is: per-task key → model_tier_fast → KB chat model,
// so empty values fall through automatically.
const MODEL_JOBS: { key: string; labelKey: string; helpKey: string }[] = [
    { key: 'crag_grader_model', labelKey: 'cragGraderModel', helpKey: 'cragGraderModelHelp' },
    { key: 'kg_extraction_model', labelKey: 'kgExtractionModel', helpKey: 'kgExtractionModelHelp' },
    { key: 'contextual_enrichment_model', labelKey: 'contextualEnrichmentModel', helpKey: 'contextualEnrichmentModelHelp' },
    { key: 'chat_plan_execute_model', labelKey: 'chatPlanExecuteModel', helpKey: 'chatPlanExecuteModelHelp' },
    { key: 'chat_plan_execute_dag_iterative_model', labelKey: 'chatPlanExecuteDAGIterativeModel', helpKey: 'chatPlanExecuteDAGIterativeModelHelp' },
    { key: 'chat_longmem_extraction_model', labelKey: 'chatLongmemExtractionModel', helpKey: 'chatLongmemExtractionModelHelp' },
    { key: 'chat_self_rag_model', labelKey: 'chatSelfRAGModel', helpKey: 'chatSelfRAGModelHelp' },
    { key: 'raptor_summary_model', labelKey: 'raptorSummaryModel', helpKey: 'raptorSummaryModelHelp' },
];

interface AdminConfigsTabProps {
    configs: AIConfig[];
    showForm: boolean;
    editingId: string | null;
    configFormData: Partial<AIConfig>;
    setConfigFormData: React.Dispatch<React.SetStateAction<Partial<AIConfig>>>;
    connectionTest: ConnectionTest | null;
    setConnectionTest: React.Dispatch<React.SetStateAction<ConnectionTest | null>>;
    handleConfigSubmit: (e: React.FormEvent) => void;
    startEditConfig: (config: AIConfig) => void;
    handleDelete: (id: string, type: 'config' | 'auth') => void;
    handleActivate: (id: string) => void;
    resetForm: () => void;
    configValidation: {
        errors: Record<string, string>;
        clearError: (field: string) => void;
    };
    loading: boolean;
    siteConfigs: Record<string, string>;
    setSiteConfigs: React.Dispatch<React.SetStateAction<Record<string, string>>>;
    onSiteConfigSubmit: (e: React.FormEvent) => void;
    availableChatModels: ChatModelOption[];
}

export default function AdminConfigsTab({
    configs,
    showForm,
    editingId,
    configFormData,
    setConfigFormData,
    connectionTest,
    setConnectionTest,
    handleConfigSubmit,
    startEditConfig,
    handleDelete,
    handleActivate,
    resetForm,
    configValidation,
    loading,
    siteConfigs,
    setSiteConfigs,
    onSiteConfigSubmit,
    availableChatModels,
}: AdminConfigsTabProps) {
    const reducedMotion = useReducedMotion();
    const { t } = useTheme();

    return (
        <>
            <AnimatePresence>
                {connectionTest && (
                    <motion.div
                        {...getMotionProps(reducedMotion)}
                        initial={{ opacity: 0, y: -10 }}
                        animate={{ opacity: 1, y: 0 }}
                        exit={{ opacity: 0, y: -10 }}
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '0.75rem',
                            padding: '0.75rem 1rem',
                            marginBottom: '1rem',
                            borderRadius: '8px',
                            background: connectionTest.status === 'testing'
                                ? 'var(--bg-secondary)'
                                : connectionTest.status === 'healthy'
                                    ? '#2d8f4e15'
                                    : '#c0392b15',
                            border: `1px solid ${connectionTest.status === 'testing'
                                ? 'var(--border-color)'
                                : connectionTest.status === 'healthy'
                                    ? '#2d8f4e'
                                    : '#c0392b'}`,
                        }}
                    >
                        {connectionTest.status === 'testing' && (
                            <RefreshCw size={18} className="spin" style={{ color: 'var(--text-secondary)' }} />
                        )}
                        {connectionTest.status === 'healthy' && (
                            <CheckCircle2 size={18} style={{ color: '#2d8f4e' }} />
                        )}
                        {connectionTest.status === 'unhealthy' && (
                            <AlertCircle size={18} style={{ color: '#c0392b' }} />
                        )}
                        <span style={{ flex: 1, fontSize: '0.9rem' }}>
                            {connectionTest.status === 'testing' && t('connectionTesting')}
                            {connectionTest.status === 'healthy' && `${t('connectionSuccess')} (${connectionTest.latencyMs}ms)`}
                            {connectionTest.status === 'unhealthy' && (connectionTest.error || t('connectionFailed'))}
                        </span>
                        <button
                            onClick={() => setConnectionTest(null)}
                            style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', padding: '0.25rem' }}
                            aria-label={t('closeBanner')}
                        >
                            <X size={16} />
                        </button>
                    </motion.div>
                )}
            </AnimatePresence>

            <AnimatePresence>
                {showForm && (
                    <motion.div {...getMotionProps(reducedMotion)} initial={{ opacity: 0, y: -20 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -20 }} className="config-form-overlay">
                        <form onSubmit={handleConfigSubmit} className="result-card" style={{ padding: '2rem' }}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '1.5rem', alignItems: 'center' }}>
                                <h3 style={{ color: 'var(--text-primary)', margin: 0 }}>{editingId ? t('editConfig2') : t('newConfig')}</h3>
                                <button type="button" onClick={resetForm} className="icon-button" style={{ color: 'var(--text-secondary)' }} aria-label={t('closeForm')}><X size={20} /></button>
                            </div>
                            <div className="form-grid">
                                <div className="input-group">
                                    <label htmlFor="config-name">{t('configName')}</label>
                                    <input id="config-name" value={configFormData.name} onChange={e => { setConfigFormData({ ...configFormData, name: e.target.value }); configValidation.clearError('name'); }} placeholder={t('configNamePlaceholder')} />
                                    {configValidation.errors.name && <span className="field-error" role="alert">{configValidation.errors.name}</span>}
                                </div>
                                <div className="input-group">
                                    <label htmlFor="config-provider">{t('provider')}</label>
                                    <select id="config-provider" value={configFormData.provider} onChange={e => setConfigFormData({ ...configFormData, provider: e.target.value })}>
                                        <option value="openai">OpenAI (OpenAI-compatible)</option>
                                    </select>
                                </div>
                                <div className="input-group">
                                    <label htmlFor="config-api-key">{t('apiKey')}</label>
                                    <div style={{ display: 'flex', gap: '0.5rem' }}>
                                        <input id="config-api-key" type="password" style={{ flex: 1 }} value={configFormData.api_key} onChange={e => { setConfigFormData({ ...configFormData, api_key: e.target.value }); configValidation.clearError('api_key'); }} placeholder="sk-..." />
                                    </div>
                                    {configValidation.errors.api_key && <span className="field-error" role="alert">{configValidation.errors.api_key}</span>}
                                </div>
                                <div className="input-group">
                                    <label htmlFor="config-base-url">{t('baseUrl')}</label>
                                    <input id="config-base-url" value={configFormData.base_url} onChange={e => setConfigFormData({ ...configFormData, base_url: e.target.value })} placeholder="https://api.openai.com/v1" />
                                </div>

                                {/* Chat Models */}
                                <div className="input-group" style={{ gridColumn: 'span 2' }}>
                                    <label>{t('chatModels')}</label>
                                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.8rem' }}>
                                        {(configFormData.chat_models || []).map((model, idx) => (
                                            <div key={idx} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                                                <input
                                                    aria-label={`Chat model ${idx + 1}`}
                                                    style={{ flex: 1 }}
                                                    value={model.name}
                                                    onChange={e => {
                                                        const models = [...(configFormData.chat_models || [])];
                                                        models[idx] = { ...models[idx], name: e.target.value };
                                                        setConfigFormData({ ...configFormData, chat_models: models });
                                                    }}
                                                    placeholder="e.g. gpt-4o"
                                                />
                                                <label style={{ display: 'flex', alignItems: 'center', gap: '4px', fontSize: '0.8rem', whiteSpace: 'nowrap', color: 'var(--text-secondary)' }}>
                                                    <input
                                                        type="checkbox"
                                                        checked={model.isReasoning}
                                                        onChange={e => {
                                                            const models = [...(configFormData.chat_models || [])];
                                                            models[idx] = { ...models[idx], isReasoning: e.target.checked };
                                                            setConfigFormData({ ...configFormData, chat_models: models });
                                                        }}
                                                    />
                                                    {t('reasoning')}
                                                </label>
                                                {(configFormData.chat_models || []).length > 1 && (
                                                    <button
                                                        type="button"
                                                        onClick={() => {
                                                            const models = (configFormData.chat_models || []).filter((_, i) => i !== idx);
                                                            setConfigFormData({ ...configFormData, chat_models: models });
                                                        }}
                                                        className="icon-button delete"
                                                        style={{ padding: '0.4rem' }}
                                                        aria-label={t('removeModel')}
                                                    >
                                                        <X size={16} />
                                                    </button>
                                                )}
                                            </div>
                                        ))}
                                        <button
                                            type="button"
                                            onClick={() => {
                                                const models = [...(configFormData.chat_models || []), { name: '', isReasoning: false, isEmbedding: false, isRerank: false, isTts: false, isStt: false }];
                                                setConfigFormData({ ...configFormData, chat_models: models });
                                            }}
                                            className="secondary-button"
                                            style={{ padding: '0.5rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem', width: 'fit-content' }}
                                        >
                                            <Plus size={16} /> {t('addModel')}
                                        </button>
                                    </div>
                                </div>

                                {/* Embedding Models */}
                                <div className="input-group" style={{ gridColumn: 'span 2' }}>
                                    <label>{t('embeddingModels')}</label>
                                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.8rem' }}>
                                        {(configFormData.embedding_models || []).map((model, idx) => (
                                            <div key={idx} style={{ display: 'flex', gap: '0.5rem' }}>
                                                <input
                                                    aria-label={`Embedding model ${idx + 1}`}
                                                    style={{ flex: 2 }}
                                                    value={model.name}
                                                    onChange={e => {
                                                        const models = [...(configFormData.embedding_models || [])];
                                                        models[idx] = { ...models[idx], name: e.target.value };
                                                        setConfigFormData({ ...configFormData, embedding_models: models });
                                                    }}
                                                    placeholder="e.g. text-embedding-3-small"
                                                />
                                                <input
                                                    aria-label={`Dimensions for embedding model ${idx + 1}`}
                                                    type="number"
                                                    style={{ width: '100px' }}
                                                    value={model.dimensions || 1536}
                                                    onChange={e => {
                                                        const models = [...(configFormData.embedding_models || [])];
                                                        models[idx] = { ...models[idx], dimensions: parseInt(e.target.value) || 1536 };
                                                        setConfigFormData({ ...configFormData, embedding_models: models });
                                                    }}
                                                    placeholder="Dim."
                                                    title={t('vectorDimensions')}
                                                />
                                                {(configFormData.embedding_models || []).length > 1 && (
                                                    <button
                                                        type="button"
                                                        onClick={() => {
                                                            const models = (configFormData.embedding_models || []).filter((_, i) => i !== idx);
                                                            setConfigFormData({ ...configFormData, embedding_models: models });
                                                        }}
                                                        className="icon-button delete"
                                                        style={{ padding: '0.4rem' }}
                                                        aria-label={t('removeModel')}
                                                    >
                                                        <X size={16} />
                                                    </button>
                                                )}
                                            </div>
                                        ))}
                                        <button
                                            type="button"
                                            onClick={() => {
                                                const models = [...(configFormData.embedding_models || []), { name: '', isReasoning: false, isEmbedding: true, isRerank: false, isTts: false, isStt: false, dimensions: 1536 }];
                                                setConfigFormData({ ...configFormData, embedding_models: models });
                                            }}
                                            className="secondary-button"
                                            style={{ padding: '0.5rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem', width: 'fit-content' }}
                                        >
                                            <Plus size={16} /> {t('addModel')}
                                        </button>
                                    </div>
                                </div>

                                {/* Rerank Models */}
                                <div className="input-group" style={{ gridColumn: 'span 2' }}>
                                    <label>{t('rerankModels')}</label>
                                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.8rem' }}>
                                        {(configFormData.rerank_models || []).map((model, idx) => (
                                            <div key={idx} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                                                <input
                                                    aria-label={`Rerank model ${idx + 1}`}
                                                    style={{ flex: 1 }}
                                                    value={model.name}
                                                    onChange={e => {
                                                        const models = [...(configFormData.rerank_models || [])];
                                                        models[idx] = { ...models[idx], name: e.target.value };
                                                        setConfigFormData({ ...configFormData, rerank_models: models });
                                                    }}
                                                    placeholder="e.g. jina-reranker-v2-base-multilingual"
                                                />
                                                {(configFormData.rerank_models || []).length > 1 && (
                                                    <button
                                                        type="button"
                                                        onClick={() => {
                                                            const models = (configFormData.rerank_models || []).filter((_, i) => i !== idx);
                                                            setConfigFormData({ ...configFormData, rerank_models: models });
                                                        }}
                                                        className="icon-button delete"
                                                        style={{ padding: '0.4rem' }}
                                                        aria-label={t('removeModel')}
                                                    >
                                                        <X size={16} />
                                                    </button>
                                                )}
                                            </div>
                                        ))}
                                        <button
                                            type="button"
                                            onClick={() => {
                                                const models = [...(configFormData.rerank_models || []), { name: '', isReasoning: false, isEmbedding: false, isRerank: true, isTts: false, isStt: false }];
                                                setConfigFormData({ ...configFormData, rerank_models: models });
                                            }}
                                            className="secondary-button"
                                            style={{ padding: '0.5rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem', width: 'fit-content' }}
                                        >
                                            <Plus size={16} /> {t('addModel')}
                                        </button>
                                    </div>
                                </div>

                                {/* TTS Models */}
                                <div className="input-group" style={{ gridColumn: 'span 2' }}>
                                    <label>{t('ttsModels')}</label>
                                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.8rem' }}>
                                        {(configFormData.tts_models || []).map((model, idx) => (
                                            <div key={idx} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                                                <input
                                                    aria-label={`TTS model ${idx + 1}`}
                                                    style={{ flex: 1 }}
                                                    value={model.name}
                                                    onChange={e => {
                                                        const models = [...(configFormData.tts_models || [])];
                                                        models[idx] = { ...models[idx], name: e.target.value };
                                                        setConfigFormData({ ...configFormData, tts_models: models });
                                                    }}
                                                    placeholder="e.g. tts-1"
                                                />
                                                {(configFormData.tts_models || []).length > 1 && (
                                                    <button
                                                        type="button"
                                                        onClick={() => {
                                                            const models = (configFormData.tts_models || []).filter((_, i) => i !== idx);
                                                            setConfigFormData({ ...configFormData, tts_models: models });
                                                        }}
                                                        className="icon-button delete"
                                                        style={{ padding: '0.4rem' }}
                                                        aria-label={t('removeModel')}
                                                    >
                                                        <X size={16} />
                                                    </button>
                                                )}
                                            </div>
                                        ))}
                                        <button
                                            type="button"
                                            onClick={() => {
                                                const models = [...(configFormData.tts_models || []), { name: '', isReasoning: false, isEmbedding: false, isRerank: false, isTts: true, isStt: false }];
                                                setConfigFormData({ ...configFormData, tts_models: models });
                                            }}
                                            className="secondary-button"
                                            style={{ padding: '0.5rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem', width: 'fit-content' }}
                                        >
                                            <Plus size={16} /> {t('addModel')}
                                        </button>
                                    </div>
                                </div>

                                {/* STT Models */}
                                <div className="input-group" style={{ gridColumn: 'span 2' }}>
                                    <label>{t('sttModels')}</label>
                                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.8rem' }}>
                                        {(configFormData.stt_models || []).map((model, idx) => (
                                            <div key={idx} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                                                <input
                                                    aria-label={`STT model ${idx + 1}`}
                                                    style={{ flex: 1 }}
                                                    value={model.name}
                                                    onChange={e => {
                                                        const models = [...(configFormData.stt_models || [])];
                                                        models[idx] = { ...models[idx], name: e.target.value };
                                                        setConfigFormData({ ...configFormData, stt_models: models });
                                                    }}
                                                    placeholder="e.g. whisper-1"
                                                />
                                                {(configFormData.stt_models || []).length > 1 && (
                                                    <button
                                                        type="button"
                                                        onClick={() => {
                                                            const models = (configFormData.stt_models || []).filter((_, i) => i !== idx);
                                                            setConfigFormData({ ...configFormData, stt_models: models });
                                                        }}
                                                        className="icon-button delete"
                                                        style={{ padding: '0.4rem' }}
                                                        aria-label={t('removeModel')}
                                                    >
                                                        <X size={16} />
                                                    </button>
                                                )}
                                            </div>
                                        ))}
                                        <button
                                            type="button"
                                            onClick={() => {
                                                const models = [...(configFormData.stt_models || []), { name: '', isReasoning: false, isEmbedding: false, isRerank: false, isTts: false, isStt: true }];
                                                setConfigFormData({ ...configFormData, stt_models: models });
                                            }}
                                            className="secondary-button"
                                            style={{ padding: '0.5rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem', width: 'fit-content' }}
                                        >
                                            <Plus size={16} /> {t('addModel')}
                                        </button>
                                    </div>
                                </div>
                            </div>
                            <div style={{ display: 'flex', gap: '1rem', marginTop: '2rem' }}>
                                <button type="submit" className="search-button"><Save size={18} /> {t('saveConfig')}</button>
                                <button type="button" className="secondary-button" onClick={resetForm}>{t('cancel')}</button>
                            </div>
                        </form>
                    </motion.div>
                )}
            </AnimatePresence>

            <form
                onSubmit={onSiteConfigSubmit}
                className="result-card"
                style={{ padding: '1.5rem 2rem', marginBottom: '1.5rem', display: 'flex', flexDirection: 'column', gap: '1rem' }}
            >
                <div>
                    <h3 style={{ margin: 0 }}>{t('modelsPerJob')}</h3>
                    <p style={{ opacity: 0.7, margin: '0.25rem 0 0', fontSize: '0.9rem' }}>{t('modelsPerJobDesc')}</p>
                </div>

                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))', gap: '1rem' }}>
                    {MODEL_JOBS.map(job => (
                        <div key={job.key} className="input-group">
                            <label htmlFor={`job-${job.key}`}>{t(job.labelKey)}</label>
                            <select
                                id={`job-${job.key}`}
                                value={siteConfigs[job.key] || ''}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, [job.key]: e.target.value }))}
                                style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '0.75rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                            >
                                <option value="">{t('useKbDefaultModel')}</option>
                                {/* If the saved value isn't in the catalogue (model removed or
                                    renamed), keep it as an extra option so it stays visible until
                                    the admin actively changes it. */}
                                {siteConfigs[job.key] && !availableChatModels.some(m => m.value === siteConfigs[job.key]) && (
                                    <option value={siteConfigs[job.key]}>{siteConfigs[job.key]}</option>
                                )}
                                {availableChatModels.map(m => (
                                    <option key={m.label} value={m.value}>{m.label}</option>
                                ))}
                            </select>
                            <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t(job.helpKey)}</p>
                        </div>
                    ))}

                    <div className="input-group">
                        <label htmlFor="job-model_tier_fast">{t('modelTierFast')}</label>
                        <select
                            id="job-model_tier_fast"
                            value={siteConfigs.model_tier_fast || ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, model_tier_fast: e.target.value }))}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '0.75rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        >
                            <option value="">{t('useKbDefaultModel')}</option>
                            {siteConfigs.model_tier_fast && !availableChatModels.some(m => m.value === siteConfigs.model_tier_fast) && (
                                <option value={siteConfigs.model_tier_fast}>{siteConfigs.model_tier_fast}</option>
                            )}
                            {availableChatModels.map(m => (
                                <option key={m.label} value={m.value}>{m.label}</option>
                            ))}
                        </select>
                        <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('modelTierFastHelp')}</p>
                    </div>
                </div>

                <button type="submit" className="search-button" style={{ width: 'fit-content' }}>
                    <Save size={18} /> {t('saveSettings')}
                </button>
            </form>

            <div className="configs-list">
                {loading ? (
                    <div className="loading-spinner"></div>
                ) : configs.length === 0 ? (
                    <p style={{ textAlign: 'center', opacity: 0.5, padding: '3rem', color: 'var(--text-secondary)' }}>{t('noConfigsFound')}</p>
                ) : (
                    configs.map(config => (
                        <motion.div key={config.id} layout={!reducedMotion} className={`result-card ${config.is_active ? 'active-config' : ''}`}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                                <div>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                                        <h3 style={{ margin: 0 }}>{config.name}</h3>
                                        {config.is_active && <span className="active-badge">{t('active')}</span>}
                                    </div>
                                    <p style={{ opacity: 0.7, margin: '0.2rem 0' }}>{t('provider')}: {config.provider}</p>
                                    <p style={{ opacity: 0.7, margin: '0.2rem 0' }}>{t('chatModels')}: {[...config.chat_models, ...config.embedding_models].map(m => m.name).join(', ')}</p>
                                </div>
                                <div style={{ display: 'flex', gap: '0.5rem' }}>
                                    {!config.is_active && (
                                        <button onClick={() => handleActivate(config.id)} className="icon-button activate" title={t('activateConfig')} aria-label={t('activateConfig')}><CheckCircle2 size={20} /></button>
                                    )}
                                    <button onClick={() => startEditConfig(config)} className="icon-button" title={t('editConfig')} aria-label={t('editConfig')}><Settings size={20} /></button>
                                    <button onClick={() => handleDelete(config.id, 'config')} className="icon-button delete" title={t('deleteConfig')} aria-label={t('deleteConfig')}><Trash2 size={20} /></button>
                                </div>
                            </div>
                        </motion.div>
                    ))
                )}
            </div>
        </>
    );
}
