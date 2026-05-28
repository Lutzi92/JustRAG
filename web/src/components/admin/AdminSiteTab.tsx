import { Save } from 'lucide-react';
import { motion } from 'framer-motion';
import { useReducedMotion, getMotionProps } from '../../hooks/useReducedMotion';
import { useTheme } from '../../contexts/ThemeContext';
import { API_BASE_URL } from '../../api';

interface AdminSiteTabProps {
    siteConfigs: Record<string, string>;
    setSiteConfigs: React.Dispatch<React.SetStateAction<Record<string, string>>>;
    onSubmit: (e: React.FormEvent) => void;
    onLogoUpload: (e: React.ChangeEvent<HTMLInputElement>) => void;
    uploading: boolean;
}

export default function AdminSiteTab({ siteConfigs, setSiteConfigs, onSubmit, onLogoUpload, uploading }: AdminSiteTabProps) {
    const reducedMotion = useReducedMotion();
    const { t } = useTheme();

    return (
        <motion.div {...getMotionProps(reducedMotion)} initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="result-card" style={{ padding: '2rem', maxHeight: 'calc(100vh - 350px)', overflowY: 'auto' }}>
            <form onSubmit={onSubmit} className="form-grid" style={{ display: 'flex', flexDirection: 'column', gap: '2rem' }}>
                <div className="input-group" style={{ maxWidth: '400px' }}>
                    <label htmlFor="site-logo">{t('siteLogo')}</label>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '1rem' }}>
                        {siteConfigs.logo_path && (
                            <div style={{ width: '64px', height: '64px', border: '1px solid var(--border-color)', borderRadius: '8px', overflow: 'hidden', background: 'white', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '4px' }}>
                                <img src={`${API_BASE_URL}${siteConfigs.logo_path}`} alt={t('logoPreview')} style={{ width: '100%', height: '100%', objectFit: 'contain' }} />
                            </div>
                        )}
                        <input id="site-logo" type="file" accept=".svg,.png" onChange={onLogoUpload} disabled={uploading} style={{ flex: 1 }} />
                        {uploading && <div className="loading-spinner" style={{ width: '20px', height: '20px' }}></div>}
                    </div>
                    <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('siteLogoHelp')}</p>
                </div>

                <div className="input-group">
                    <label htmlFor="chat-footer">{t('chatFooterLabel')}</label>
                    <textarea
                        id="chat-footer"
                        value={siteConfigs.chat_footer || ''}
                        onChange={e => setSiteConfigs(prev => ({ ...prev, chat_footer: e.target.value }))}
                        placeholder={t('chatFooterPlaceholder')}
                        style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)', minHeight: '80px', fontFamily: 'inherit' }}
                    />
                </div>

                <div className="input-group">
                    <label htmlFor="kb-header">{t('kbHeaderLabel')}</label>
                    <textarea
                        id="kb-header"
                        value={siteConfigs.kb_header || ''}
                        onChange={e => setSiteConfigs(prev => ({ ...prev, kb_header: e.target.value }))}
                        placeholder={t('kbHeaderPlaceholder')}
                        style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)', minHeight: '80px', fontFamily: 'inherit' }}
                    />
                </div>

                <div className="input-group">
                    <label htmlFor="imprint">{t('imprintLabel')}</label>
                    <textarea
                        id="imprint"
                        value={siteConfigs.imprint || ''}
                        onChange={e => setSiteConfigs(prev => ({ ...prev, imprint: e.target.value }))}
                        placeholder={t('imprintPlaceholder')}
                        style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)', minHeight: '120px', fontFamily: 'inherit' }}
                    />
                </div>

                <div className="input-group">
                    <label htmlFor="example-prompts">{t('examplePromptsLabel')}</label>
                    <textarea
                        id="example-prompts"
                        value={siteConfigs.example_prompts || ''}
                        onChange={e => setSiteConfigs(prev => ({ ...prev, example_prompts: e.target.value }))}
                        placeholder={t('examplePromptsPlaceholder')}
                        style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)', minHeight: '120px', fontFamily: 'inherit' }}
                    />
                    <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('examplePromptsHelp')}</p>
                </div>

                <div className="input-group">
                    <label htmlFor="web-proxy">{t('webProxy')}</label>
                    <input
                        id="web-proxy"
                        type="text"
                        value={siteConfigs.web_proxy || ''}
                        onChange={e => setSiteConfigs(prev => ({ ...prev, web_proxy: e.target.value }))}
                        placeholder="http://user:pass@host:port"
                        style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                    />
                    <p style={{ fontSize: '0.8rem', opacity: 0.6, marginTop: '0.5rem' }}>{t('webProxyHelp')}</p>
                </div>

                <hr style={{ border: 'none', borderTop: '1px solid var(--border-color)', margin: '0.5rem 0' }} />
                <h3 style={{ margin: 0 }}>{t('confluenceSettings')}</h3>

                <div className="input-group">
                    <label htmlFor="confluence-enabled" style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', cursor: 'pointer' }}>
                        <input
                            id="confluence-enabled"
                            type="checkbox"
                            checked={siteConfigs.confluence_enabled === 'true'}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, confluence_enabled: e.target.checked ? 'true' : 'false' }))}
                            style={{ width: '18px', height: '18px', cursor: 'pointer' }}
                        />
                        {t('confluenceEnabled')}
                    </label>
                </div>

                {siteConfigs.confluence_enabled === 'true' && (
                    <div className="input-group">
                        <label htmlFor="confluence-base-url">{t('confluenceBaseUrl')}</label>
                        <input
                            id="confluence-base-url"
                            type="text"
                            value={siteConfigs.confluence_base_url || ''}
                            onChange={e => setSiteConfigs(prev => ({ ...prev, confluence_base_url: e.target.value }))}
                            placeholder={t('confluenceBaseUrlPlaceholder')}
                            style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '1rem', borderRadius: '8px', color: 'var(--text-primary)' }}
                        />
                    </div>
                )}

                <button type="submit" className="search-button" style={{ width: 'fit-content' }}>
                    <Save size={18} /> {t('saveSettings')}
                </button>
            </form>
        </motion.div>
    );
}
