import { Trash2, Settings, Save, X } from 'lucide-react';
import { motion, AnimatePresence } from 'framer-motion';
import { useReducedMotion, getMotionProps } from '../../hooks/useReducedMotion';
import { useTheme } from '../../contexts/ThemeContext';

interface LdapConfig {
    url: string;
    bindDN: string;
    bindCredentials: string;
    searchBase: string;
    searchFilter: string;
}

interface OidcConfig {
    issuerURL: string;
    clientID: string;
    clientSecret: string;
    scopes?: string[];
    redirectURI: string;
    successRedirect?: string;
    postLogoutRedirectURI?: string;
}

type ProviderConfig = LdapConfig | OidcConfig | Record<string, unknown>;

interface AuthProvider {
    id: string;
    type: string;
    name: string;
    config: ProviderConfig;
    isActive: boolean;
}

interface AdminAuthTabProps {
    providers: AuthProvider[];
    showForm: boolean;
    editingId: string | null;
    authFormData: Partial<AuthProvider>;
    setAuthFormData: React.Dispatch<React.SetStateAction<Partial<AuthProvider>>>;
    handleAuthSubmit: (e: React.FormEvent) => void;
    startEditAuth: (provider: AuthProvider) => void;
    handleDelete: (id: string, type: 'config' | 'auth') => void;
    resetForm: () => void;
    authValidation: {
        errors: Record<string, string>;
        clearError: (field: string) => void;
    };
    loading: boolean;
}

type AuthValidation = AdminAuthTabProps['authValidation'];
type SetForm = AdminAuthTabProps['setAuthFormData'];
type FormData = Partial<AuthProvider>;
type T = (key: string) => string;

function asLdap(c: ProviderConfig | undefined): LdapConfig {
    const o = (c as Partial<LdapConfig>) ?? {};
    return {
        url: o.url ?? '',
        bindDN: o.bindDN ?? '',
        bindCredentials: o.bindCredentials ?? '',
        searchBase: o.searchBase ?? '',
        searchFilter: o.searchFilter ?? '(uid={{username}})',
    };
}

function asOidc(c: ProviderConfig | undefined): OidcConfig {
    const o = (c as Partial<OidcConfig>) ?? {};
    return {
        issuerURL: o.issuerURL ?? '',
        clientID: o.clientID ?? '',
        clientSecret: o.clientSecret ?? '',
        scopes: o.scopes ?? [],
        redirectURI: o.redirectURI ?? '',
        successRedirect: o.successRedirect ?? '',
        postLogoutRedirectURI: o.postLogoutRedirectURI ?? '',
    };
}

function renderLdapFields(form: FormData, setForm: SetForm, v: AuthValidation, t: T) {
    const cfg = asLdap(form.config);
    return (
        <>
            <div className="input-group">
                <label htmlFor="auth-url">{t('ldapUrl')}</label>
                <input id="auth-url" value={cfg.url} onChange={e => { setForm({ ...form, config: { ...cfg, url: e.target.value } }); v.clearError('url'); }} placeholder="ldap://ldap.example.com" />
                {v.errors.url && <span className="field-error" role="alert">{v.errors.url}</span>}
            </div>
            <div className="input-group">
                <label htmlFor="auth-bind-dn">{t('bindDn')}</label>
                <input id="auth-bind-dn" value={cfg.bindDN} onChange={e => setForm({ ...form, config: { ...cfg, bindDN: e.target.value } })} placeholder="cn=admin,dc=example,dc=com" />
            </div>
            <div className="input-group">
                <label htmlFor="auth-bind-creds">{t('bindCredentials')}</label>
                <input id="auth-bind-creds" type="password" value={cfg.bindCredentials} onChange={e => setForm({ ...form, config: { ...cfg, bindCredentials: e.target.value } })} />
            </div>
            <div className="input-group">
                <label htmlFor="auth-search-base">{t('searchBaseDn')}</label>
                <input id="auth-search-base" value={cfg.searchBase} onChange={e => { setForm({ ...form, config: { ...cfg, searchBase: e.target.value } }); v.clearError('searchBase'); }} placeholder="ou=users,dc=example,dc=com" />
                {v.errors.searchBase && <span className="field-error" role="alert">{v.errors.searchBase}</span>}
            </div>
        </>
    );
}

function renderOidcFields(form: FormData, setForm: SetForm, v: AuthValidation, t: T) {
    const cfg = asOidc(form.config);
    const scopesText = (cfg.scopes ?? []).join(', ');
    return (
        <>
            <div className="input-group">
                <label htmlFor="auth-issuer">{t('oidcIssuerUrl')}</label>
                <input id="auth-issuer" value={cfg.issuerURL} onChange={e => { setForm({ ...form, config: { ...cfg, issuerURL: e.target.value } }); v.clearError('issuerURL'); }} placeholder={t('oidcIssuerUrlPlaceholder')} />
                {v.errors.issuerURL && <span className="field-error" role="alert">{v.errors.issuerURL}</span>}
            </div>
            <div className="input-group">
                <label htmlFor="auth-client-id">{t('oidcClientId')}</label>
                <input id="auth-client-id" value={cfg.clientID} onChange={e => { setForm({ ...form, config: { ...cfg, clientID: e.target.value } }); v.clearError('clientID'); }} />
                {v.errors.clientID && <span className="field-error" role="alert">{v.errors.clientID}</span>}
            </div>
            <div className="input-group">
                <label htmlFor="auth-client-secret">{t('oidcClientSecret')}</label>
                <input id="auth-client-secret" type="password" value={cfg.clientSecret} onChange={e => setForm({ ...form, config: { ...cfg, clientSecret: e.target.value } })} placeholder="********" />
                <small style={{ color: 'var(--text-secondary)', fontSize: '0.75rem' }}>{t('oidcClientSecretHelp')}</small>
            </div>
            <div className="input-group">
                <label htmlFor="auth-scopes">{t('oidcScopes')}</label>
                <input id="auth-scopes" value={scopesText} onChange={e => setForm({ ...form, config: { ...cfg, scopes: e.target.value.split(',').map(s => s.trim()).filter(Boolean) } })} placeholder={t('oidcScopesPlaceholder')} />
            </div>
            <div className="input-group">
                <label htmlFor="auth-redirect-uri">{t('oidcRedirectUri')}</label>
                <input id="auth-redirect-uri" value={cfg.redirectURI} onChange={e => { setForm({ ...form, config: { ...cfg, redirectURI: e.target.value } }); v.clearError('redirectURI'); }} placeholder={`${window.location.origin}/api/auth/oidc/callback`} />
                <small style={{ color: 'var(--text-secondary)', fontSize: '0.75rem' }}>{t('oidcRedirectUriHelp')}</small>
                {v.errors.redirectURI && <span className="field-error" role="alert">{v.errors.redirectURI}</span>}
            </div>
            <div className="input-group">
                <label htmlFor="auth-success-redirect">{t('oidcSuccessRedirect')}</label>
                <input id="auth-success-redirect" value={cfg.successRedirect ?? ''} onChange={e => setForm({ ...form, config: { ...cfg, successRedirect: e.target.value } })} placeholder="/" />
                <small style={{ color: 'var(--text-secondary)', fontSize: '0.75rem' }}>{t('oidcSuccessRedirectHelp')}</small>
            </div>
            <div className="input-group">
                <label htmlFor="auth-post-logout-redirect">{t('oidcPostLogoutRedirect')}</label>
                <input id="auth-post-logout-redirect" value={cfg.postLogoutRedirectURI ?? ''} onChange={e => setForm({ ...form, config: { ...cfg, postLogoutRedirectURI: e.target.value } })} placeholder={`${window.location.origin}/`} />
                <small style={{ color: 'var(--text-secondary)', fontSize: '0.75rem' }}>{t('oidcPostLogoutRedirectHelp')}</small>
            </div>
        </>
    );
}

function providerSummary(provider: AuthProvider): string {
    if (provider.type === 'oidc') {
        return (provider.config as Partial<OidcConfig>).issuerURL ?? '';
    }
    return (provider.config as Partial<LdapConfig>).url ?? '';
}

export default function AdminAuthTab({
    providers,
    showForm,
    editingId,
    authFormData,
    setAuthFormData,
    handleAuthSubmit,
    startEditAuth,
    handleDelete,
    resetForm,
    authValidation,
    loading,
}: AdminAuthTabProps) {
    const reducedMotion = useReducedMotion();
    const { t } = useTheme();

    return (
        <>
            <AnimatePresence>
                {showForm && (
                    <motion.div {...getMotionProps(reducedMotion)} initial={{ opacity: 0, y: -20 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -20 }} className="config-form-overlay">
                        <form onSubmit={handleAuthSubmit} className="result-card" style={{ padding: '2rem' }}>
                            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '1.5rem', alignItems: 'center' }}>
                                <h3 style={{ color: 'var(--text-primary)', margin: 0 }}>{editingId ? t('editAuthProvider') : t('newAuthProvider')}</h3>
                                <button type="button" onClick={resetForm} className="icon-button" style={{ color: 'var(--text-secondary)' }} aria-label={t('closeForm')}><X size={20} /></button>
                            </div>
                            <div className="form-grid">
                                <div className="input-group">
                                    <label htmlFor="auth-name">{t('providerName')}</label>
                                    <input id="auth-name" value={authFormData.name} onChange={e => { setAuthFormData({ ...authFormData, name: e.target.value }); authValidation.clearError('name'); }} placeholder={t('providerNamePlaceholder')} />
                                    {authValidation.errors.name && <span className="field-error" role="alert">{authValidation.errors.name}</span>}
                                </div>
                                <div className="input-group">
                                    <label htmlFor="auth-type">{t('providerType')}</label>
                                    <select id="auth-type" value={authFormData.type} onChange={e => setAuthFormData({ ...authFormData, type: e.target.value })}>
                                        <option value="ldap">LDAP</option>
                                        <option value="oidc">OIDC</option>
                                    </select>
                                </div>
                                {authFormData.type === 'oidc' ? renderOidcFields(authFormData, setAuthFormData, authValidation, t) : renderLdapFields(authFormData, setAuthFormData, authValidation, t)}
                            </div>
                            <div style={{ display: 'flex', gap: '1rem', marginTop: '2rem' }}>
                                <button type="submit" className="search-button"><Save size={18} /> {t('saveProvider')}</button>
                                <button type="button" className="secondary-button" onClick={resetForm}>{t('cancel')}</button>
                            </div>
                        </form>
                    </motion.div>
                )}
            </AnimatePresence>

            <div className="configs-list">
                {loading ? (
                    <div className="loading-spinner"></div>
                ) : providers.length === 0 ? (
                    <p style={{ textAlign: 'center', opacity: 0.5, padding: '3rem', color: 'var(--text-secondary)' }}>{t('noProvidersFound')}</p>
                ) : (
                    providers.map(provider => (
                        <motion.div key={provider.id} layout={!reducedMotion} className="result-card">
                            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                                <div>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.5rem' }}>
                                        <h3 style={{ margin: 0 }}>{provider.name}</h3>
                                        {provider.isActive && <span className="active-badge" style={{ background: 'var(--success-text)' }}>{t('enabled')}</span>}
                                    </div>
                                    <p style={{ opacity: 0.7, margin: '0.2rem 0' }}>{t('providerType')}: {provider.type.toUpperCase()}</p>
                                    <p style={{ opacity: 0.7, margin: '0.2rem 0' }}>URL: {providerSummary(provider)}</p>
                                </div>
                                <div style={{ display: 'flex', gap: '0.5rem' }}>
                                    <button onClick={() => startEditAuth(provider)} className="icon-button" title={t('editProvider')} aria-label={t('editProvider')}><Settings size={20} /></button>
                                    <button onClick={() => handleDelete(provider.id, 'auth')} className="icon-button delete" title={t('deleteProvider')} aria-label={t('deleteProvider')}><Trash2 size={20} /></button>
                                </div>
                            </div>
                        </motion.div>
                    ))
                )}
            </div>
        </>
    );
}
