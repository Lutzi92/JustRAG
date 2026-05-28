import { Save } from 'lucide-react';
import { motion } from 'framer-motion';
import { useReducedMotion, getMotionProps } from '../../hooks/useReducedMotion';
import { useTheme } from '../../contexts/ThemeContext';

interface AdminSearchTabProps {
    siteConfigs: Record<string, string>;
    setSiteConfigs: React.Dispatch<React.SetStateAction<Record<string, string>>>;
    onSubmit: (e: React.FormEvent) => void;
}

export default function AdminSearchTab({ siteConfigs, setSiteConfigs, onSubmit }: AdminSearchTabProps) {
    const reducedMotion = useReducedMotion();
    const { t } = useTheme();

    return (
        <motion.div {...getMotionProps(reducedMotion)} initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="result-card" style={{ padding: '2rem' }}>
            <form onSubmit={onSubmit} className="form-grid" style={{ display: 'flex', flexDirection: 'column', gap: '2rem' }}>
                <h3 style={{ margin: 0 }}>{t('webSearchSection')}</h3>

                <div className="input-group">
                    <label htmlFor="web-search-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                        <input
                            id="web-search-enabled"
                            type="checkbox"
                            checked={siteConfigs.web_search_enabled === 'true'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, web_search_enabled: e.target.checked ? 'true' : 'false' }))}
                            style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                        />
                        {t('webSearchEnabled')}
                    </label>
                </div>

                {siteConfigs.web_search_enabled === 'true' && (
                    <>
                        <div className="input-group">
                            <label htmlFor="google-search-api-key">{t('googleSearchApiKey')}</label>
                            <input
                                id="google-search-api-key"
                                type="password"
                                value={siteConfigs.google_search_api_key || ''}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, google_search_api_key: e.target.value }))}
                                placeholder="AIza..."
                                style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                            />
                            <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('googleSearchApiKeyHelp')}</p>
                        </div>

                        <div className="input-group">
                            <label htmlFor="google-search-cx">{t('googleSearchCx')}</label>
                            <input
                                id="google-search-cx"
                                type="text"
                                value={siteConfigs.google_search_cx || ''}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, google_search_cx: e.target.value }))}
                                placeholder="0123456789..."
                                style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                            />
                            <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('googleSearchCxHelp')}</p>
                        </div>
                    </>
                )}

                <hr style={{ border: 'none', borderTop: '1px solid var(--border-color)', margin: '0.5rem 0' }} />
                <h3 style={{ margin: 0 }}>{t('academicSearchSection')}</h3>

                <div className="input-group">
                    <label htmlFor="academic-search-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                        <input
                            id="academic-search-enabled"
                            type="checkbox"
                            checked={siteConfigs.academic_search_enabled === 'true'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, academic_search_enabled: e.target.checked ? 'true' : 'false' }))}
                            style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                        />
                        {t('academicSearchEnabled')}
                    </label>
                </div>

                {siteConfigs.academic_search_enabled === 'true' && (
                    <>
                        <div className="input-group">
                            <label htmlFor="academic-search-base-url">{t('academicSearchBaseUrl')}</label>
                            <input
                                id="academic-search-base-url"
                                type="text"
                                value={siteConfigs.academic_search_base_url || ''}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, academic_search_base_url: e.target.value }))}
                                placeholder={t('academicSearchBaseUrlPlaceholder')}
                                style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                            />
                            <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('academicSearchBaseUrlHelp')}</p>
                        </div>

                        <div className="input-group">
                            <label htmlFor="academic-search-name">{t('academicSearchName')}</label>
                            <input
                                id="academic-search-name"
                                type="text"
                                value={siteConfigs.academic_search_name || ''}
                                onChange={e => setSiteConfigs(prev => ({ ...prev, academic_search_name: e.target.value }))}
                                placeholder={t('academicSearchNamePlaceholder')}
                                style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                            />
                            <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('academicSearchNameHelp')}</p>
                        </div>
                    </>
                )}

                <button type="submit" className="search-button" style={{ width: 'fit-content' }}>
                    <Save size={18} /> {t('saveSearchProviders')}
                </button>
            </form>
        </motion.div>
    );
}
