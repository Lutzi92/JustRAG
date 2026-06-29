import { useState, useEffect, useMemo, useCallback } from 'react';
import axios from 'axios';
import { Plus, ChevronLeft, ShieldCheck, Cpu, UserPlus, Search, FileText, Activity, Globe, Settings, Database } from 'lucide-react';
import { getApiErrorMessage } from './utils/apiError';
import { API_BASE_URL } from './api';
import { useToast } from './contexts/ToastContext';
import { useTheme } from './contexts/ThemeContext';
import { useModalContext } from './contexts/ModalContext';
import { useFormValidation } from './hooks/useFormValidation';
import SystemHealthDashboard from './SystemHealthDashboard';
import KBOverviewDashboard from './KBOverviewDashboard';
import AdminConfigsTab from './components/admin/AdminConfigsTab';
import AdminAuthTab from './components/admin/AdminAuthTab';
import AdminUsersTab from './components/admin/AdminUsersTab';
import AdminSiteTab from './components/admin/AdminSiteTab';
import AdminSearchTab from './components/admin/AdminSearchTab';
import AdminAgentTab from './components/admin/AdminAgentTab';
import AdminEvalTab from './components/admin/AdminEvalTab';
import AdminGlobalKbsTab from './components/admin/AdminGlobalKbsTab';
import ReembedSection from './components/admin/ReembedSection';

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

interface User {
    id: string;
    username: string;
    role: string;
    firstName?: string;
    lastName?: string;
}

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

export interface ChatModelOption {
    value: string;   // the model name (what gets saved)
    label: string;   // display text: "model-name (provider)"
}

interface AdminUIProps {
    onBack: () => void;
    user: User;
    onEditGlobalKb?: (kb: { id: string; name: string }) => void;
}

type AdminTab = 'configs' | 'auth' | 'users' | 'site' | 'search' | 'agent' | 'health' | 'kb-overview' | 'global-kbs' | 'eval';

export default function AdminUI({ onBack, user, onEditGlobalKb }: AdminUIProps) {
    const toast = useToast();
    const { t } = useTheme();
    const { showConfirm } = useModalContext();
    const [activeTab, setActiveTab] = useState<AdminTab>('configs');
    const [configs, setConfigs] = useState<AIConfig[]>([]);
    const [providers, setProviders] = useState<AuthProvider[]>([]);
    const [usersList, setUsersList] = useState<User[]>([]);
    const [siteConfigs, setSiteConfigs] = useState<Record<string, string>>({});
    const [loading, setLoading] = useState(true);
    const [showForm, setShowForm] = useState(false);
    const [editingId, setEditingId] = useState<string | null>(null);
    const [uploading, setUploading] = useState(false);
    const [connectionTest, setConnectionTest] = useState<{
        configId: string;
        status: 'testing' | 'healthy' | 'unhealthy';
        latencyMs?: number;
        error?: string;
    } | null>(null);

    const availableChatModels = useMemo<ChatModelOption[]>(() => {
        const seen = new Set<string>();
        const out: ChatModelOption[] = [];
        for (const cfg of configs) {
            if (!cfg.chat_models) continue;
            for (const m of cfg.chat_models) {
                // Include every (name, provider) pair. If the SAME name appears
                // under multiple providers, show both so admins can see it exists
                // in more than one config.
                const key = `${m.name}__${cfg.name}`;
                if (seen.has(key)) continue;
                seen.add(key);
                out.push({ value: m.name, label: `${m.name} (${cfg.name})` });
            }
        }
        // Deterministic order: by label.
        out.sort((a, b) => a.label.localeCompare(b.label));
        return out;
    }, [configs]);

    // Config Form Data
    const [configFormData, setConfigFormData] = useState<Partial<AIConfig>>({
        name: '',
        provider: 'openai',
        api_key: '',
        base_url: 'https://api.openai.com/v1/',
        chat_models: [{ name: 'gpt-4o-mini', isReasoning: false, isEmbedding: false, isRerank: false, isTts: false, isStt: false }],
        embedding_models: [{ name: 'text-embedding-3-small', isReasoning: false, isEmbedding: true, isRerank: false, isTts: false, isStt: false, dimensions: 1536 }],
        rerank_models: [],
        tts_models: [],
        stt_models: []
    });

    // Auth Provider Form Data
    const [authFormData, setAuthFormData] = useState<Partial<AuthProvider>>({
        name: '',
        type: 'ldap',
        config: {
            url: '',
            bindDN: '',
            bindCredentials: '',
            searchBase: '',
            searchFilter: '(uid={{username}})'
        },
        isActive: true
    });

    const configValidation = useFormValidation({
        name: (v) => !v.trim() && t('configNameRequired'),
        api_key: (v) => !v.trim() && t('apiKeyRequired'),
    });

    const authValidation = useFormValidation(
        authFormData.type === 'oidc'
            ? {
                name: (v) => !v.trim() && t('providerNameRequired'),
                issuerURL: (v) => !v.trim() && t('oidcIssuerUrlRequired'),
                clientID: (v) => !v.trim() && t('oidcClientIdRequired'),
                redirectURI: (v) => !v.trim() && t('oidcRedirectUriRequired'),
            }
            : {
                name: (v) => !v.trim() && t('providerNameRequired'),
                url: (v) => !v.trim() && t('ldapUrlRequired'),
                searchBase: (v) => !v.trim() && t('searchBaseRequired'),
            }
    );

    const fetchSiteConfigs = useCallback(async () => {
        setLoading(true);
        try {
            const res = await axios.get(`${API_BASE_URL}/api/site-config`);
            setSiteConfigs(res.data);
        } catch (error: unknown) {
            console.error('Failed to fetch site configs:', error);
            toast.error(t('siteConfigFetchError'));
        } finally {
            setLoading(false);
        }
    }, [toast, t]);

    const handleSiteConfigSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        const prev = { ...siteConfigs };
        try {
            await axios.post(`${API_BASE_URL}/api/site-config`, { configs: siteConfigs });
            toast.success(t('settingsSaved'));
        } catch {
            setSiteConfigs(prev);
            toast.error(t('settingsSaveError'));
        }
    };

    const handleLogoUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
        if (!e.target.files?.[0]) return;
        setUploading(true);
        const formData = new FormData();
        formData.append('logo', e.target.files[0]);

        try {
            const res = await axios.post(`${API_BASE_URL}/api/site-config/logo`, formData, {
                headers: { 'Content-Type': 'multipart/form-data' }
            });
            setSiteConfigs(prev => ({ ...prev, logo_path: res.data.logoPath }));
            toast.success(t('logoUploaded'));
        } catch (error: unknown) {
            console.error('Logo upload failed:', error);
            toast.error(t('logoUploadError'));
        } finally {
            setUploading(false);
        }
    };

    const fetchConfigs = useCallback(async () => {
        setLoading(true);
        try {
            const res = await axios.get(`${API_BASE_URL}/api/admin/configs`);
            setConfigs(res.data);
        } catch (error: unknown) {
            console.error('Failed to fetch configs:', error);
            toast.error(t('configsFetchError'));
        } finally {
            setLoading(false);
        }
    }, [toast, t]);

    const fetchAuthProviders = useCallback(async () => {
        setLoading(true);
        try {
            const res = await axios.get(`${API_BASE_URL}/api/admin/auth-providers`);
            setProviders(res.data);
        } catch (error: unknown) {
            console.error('Failed to fetch providers:', error);
            toast.error(t('providersFetchError'));
        } finally {
            setLoading(false);
        }
    }, [toast, t]);

    const fetchUsers = useCallback(async () => {
        setLoading(true);
        try {
            const res = await axios.get(`${API_BASE_URL}/api/admin/users`);
            setUsersList(res.data);
        } catch (error: unknown) {
            console.error('Failed to fetch users:', error);
            toast.error(t('usersFetchError'));
        } finally {
            setLoading(false);
        }
    }, [toast, t]);

    useEffect(() => {
        if (activeTab === 'configs') {
            fetchConfigs();
            fetchSiteConfigs();
        } else if (activeTab === 'auth') fetchAuthProviders();
        else if (activeTab === 'users') fetchUsers();
        else if (activeTab === 'site' || activeTab === 'search' || activeTab === 'agent') fetchSiteConfigs();
    }, [activeTab, fetchConfigs, fetchSiteConfigs, fetchAuthProviders, fetchUsers]);

    const handleRoleChange = async (id: string, newRole: string) => {
        if (!await showConfirm(t('confirmPromoteUser'))) return;
        try {
            await axios.patch(`${API_BASE_URL}/api/admin/users/${id}/role`, { role: newRole });
            fetchUsers();
        } catch (error: unknown) {
            console.error('Role change failed:', error);
            toast.error(t('roleChangeError'));
        }
    };

    const handleDeleteUser = async (id: string, username: string) => {
        if (!await showConfirm(t('confirmDeleteUser').replace('{username}', username))) return;
        try {
            await axios.delete(`${API_BASE_URL}/api/admin/users/${id}`);
            fetchUsers();
        } catch (error: unknown) {
            console.error('User deletion failed:', error);
            toast.error(t('userDeleteError'));
        }
    };

    const handleConfigSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!configValidation.validate({ name: configFormData.name ?? '', api_key: configFormData.api_key ?? '' })) return;
        try {
            // reasoning_models is derived server-side from chat_models[*].isReasoning.
            // startEditConfig spreads the full API response (which includes it) into
            // form state, so without this drop the PATCH would echo a stale list and
            // the backend's legacy reasoning_models write path would clobber chat_models.
            const { reasoning_models: _legacyReasoning, ...rest } = configFormData as Partial<AIConfig> & { reasoning_models?: unknown };
            void _legacyReasoning;
            const dataToSend = {
                ...rest,
                chat_models: (configFormData.chat_models || []).filter(m => m.name.trim()),
                embedding_models: (configFormData.embedding_models || []).filter(m => m.name.trim()),
                rerank_models: (configFormData.rerank_models || []).filter(m => m.name.trim()),
                tts_models: (configFormData.tts_models || []).filter(m => m.name.trim()),
                stt_models: (configFormData.stt_models || []).filter(m => m.name.trim()),
            };

            let savedId: string;
            if (editingId) {
                await axios.patch(`${API_BASE_URL}/api/admin/configs/${editingId}`, dataToSend);
                savedId = editingId;
            } else {
                const res = await axios.post(`${API_BASE_URL}/api/admin/configs`, dataToSend);
                savedId = res.data.id;
            }
            resetForm();
            fetchConfigs();

            // Test connectivity after save
            setConnectionTest({ configId: savedId, status: 'testing' });
            try {
                const testRes = await axios.post(`${API_BASE_URL}/api/admin/configs/${savedId}/test`);
                setConnectionTest({
                    configId: savedId,
                    status: testRes.data.status === 'healthy' ? 'healthy' : 'unhealthy',
                    latencyMs: testRes.data.latencyMs,
                    error: testRes.data.error,
                });
            } catch {
                setConnectionTest({
                    configId: savedId,
                    status: 'unhealthy',
                    error: t('connectionTestFailed'),
                });
            }
            // Auto-dismiss after 15s
            setTimeout(() => setConnectionTest(prev => prev?.configId === savedId ? null : prev), 15_000);
        } catch (error: unknown) {
            console.error('Save failed:', error);
            toast.error(getApiErrorMessage(error, t('saveFailed')));
        }
    };

    const handleAuthSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        const cfg = (authFormData.config ?? {}) as Partial<LdapConfig & OidcConfig>;
        const inputs: Record<string, string> = authFormData.type === 'oidc'
            ? {
                name: authFormData.name ?? '',
                issuerURL: cfg.issuerURL ?? '',
                clientID: cfg.clientID ?? '',
                redirectURI: cfg.redirectURI ?? '',
            }
            : {
                name: authFormData.name ?? '',
                url: cfg.url ?? '',
                searchBase: cfg.searchBase ?? '',
            };
        if (!authValidation.validate(inputs)) return;
        try {
            if (editingId) {
                await axios.patch(`${API_BASE_URL}/api/admin/auth-providers/${editingId}`, authFormData);
            } else {
                await axios.post(`${API_BASE_URL}/api/admin/auth-providers`, authFormData);
            }
            resetForm();
            fetchAuthProviders();
        } catch (error: unknown) {
            console.error('Save failed:', error);
            toast.error(getApiErrorMessage(error, t('saveFailed')));
        }
    };

    // clearAll is a stable useCallback inside useFormValidation; depending on
    // the destructured functions (not the hook objects) keeps resetForm stable.
    const { clearAll: clearConfigValidation } = configValidation;
    const { clearAll: clearAuthValidation } = authValidation;

    const resetForm = useCallback(() => {
        setShowForm(false);
        setEditingId(null);
        setConfigFormData({
            name: '',
            provider: 'openai',
            api_key: '',
            base_url: 'https://api.openai.com/v1/',
            chat_models: [{ name: 'gpt-4o-mini', isReasoning: false, isEmbedding: false, isRerank: false, isTts: false, isStt: false }],
            embedding_models: [{ name: 'text-embedding-3-small', isReasoning: false, isEmbedding: true, isRerank: false, isTts: false, isStt: false, dimensions: 1536 }],
            rerank_models: [],
            tts_models: [],
            stt_models: []
        });
        setAuthFormData({
            name: '',
            type: 'ldap',
            config: {
                url: '',
                bindDN: '',
                bindCredentials: '',
                searchBase: '',
                searchFilter: '(uid={{username}})'
            },
            isActive: true
        });
        clearConfigValidation();
        clearAuthValidation();
    }, [clearConfigValidation, clearAuthValidation]);

    useEffect(() => {
        const handleEsc = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                if (showForm) {
                    resetForm();
                }
            }
        };
        window.addEventListener('keydown', handleEsc);
        return () => window.removeEventListener('keydown', handleEsc);
    }, [showForm, resetForm]);

    const handleDelete = async (id: string, type: 'config' | 'auth') => {
        if (!await showConfirm(type === 'config' ? t('confirmDeleteConfig') : t('confirmDeleteProvider'))) return;
        try {
            const endpoint = type === 'config' ? 'configs' : 'auth-providers';
            await axios.delete(`${API_BASE_URL}/api/admin/${endpoint}/${id}`);
            if (type === 'config') fetchConfigs();
            else fetchAuthProviders();
        } catch (error: unknown) {
            console.error('Delete failed:', error);
            toast.error(t('deleteError'));
        }
    };

    const handleActivate = async (id: string) => {
        try {
            await axios.post(`${API_BASE_URL}/api/admin/configs/${id}/activate`);
            fetchConfigs();
        } catch (error: unknown) {
            console.error('Activation failed:', error);
            toast.error(t('activationError'));
        }
    };

    const startEditConfig = (config: AIConfig) => {
        setConfigFormData(config);
        setEditingId(config.id);
        setShowForm(true);
    };

    const startEditAuth = (provider: AuthProvider) => {
        setAuthFormData(provider);
        setEditingId(provider.id);
        setShowForm(true);
    };

    const sectionTitle = activeTab === 'configs' ? t('aiConfigurations')
        : activeTab === 'auth' ? t('authProviders')
        : activeTab === 'site' ? t('siteSettings')
        : activeTab === 'search' ? t('searchProviderConfig')
        : activeTab === 'agent' ? t('agentConfig')
        : activeTab === 'eval' ? t('evalRunner')
        : t('userManagement');

    const isDataTab = activeTab === 'health' || activeTab === 'kb-overview' || activeTab === 'users';

    const navItem = (tab: AdminTab, Icon: typeof Cpu, label: string) => {
        const active = activeTab === tab;
        return (
            <button
                type="button"
                onClick={() => setActiveTab(tab)}
                className={`admin-nav-item ${active ? 'active' : ''}`}
                aria-current={active ? 'page' : undefined}
            >
                <Icon size={18} />
                <span>{label}</span>
            </button>
        );
    };

    return (
        <div className="admin-container">
            <header className="header" style={{ display: 'flex', alignItems: 'center', gap: '1rem', borderBottom: '1px solid var(--border-color)', paddingBottom: '1rem', marginBottom: '2rem' }}>
                <button onClick={onBack} className="back-button" aria-label={t('back')}>
                    <ChevronLeft size={24} />
                </button>
                <div style={{ textAlign: 'left' }}>
                    <h1 style={{ color: 'var(--text-primary)', margin: 0 }}>{t('adminDashboard')}</h1>
                    <p style={{ color: 'var(--text-secondary)', margin: 0 }}>{t('adminSubtitle')}</p>
                </div>
            </header>

            <div className="admin-layout">
                <nav className="admin-nav-rail" aria-label={t('adminDashboard')}>
                    <div className="admin-nav-group">
                        <div className="admin-nav-group-heading">{t('adminNavGroupConfig')}</div>
                        {navItem('configs', Cpu, t('adminTabAiModels'))}
                        {navItem('auth', ShieldCheck, t('adminTabAuth'))}
                        {user.role === 'superadmin' && navItem('search', Search, t('adminTabSearch'))}
                        {user.role === 'superadmin' && navItem('agent', FileText, t('adminTabAgents'))}
                        {navItem('global-kbs', Globe, t('adminTabGlobalKbs'))}
                        {user.role === 'superadmin' && navItem('site', Settings, t('adminTabSettings'))}
                    </div>
                    {(user.role === 'superadmin' || user.role === 'admin') && (
                        <div className="admin-nav-group">
                            <div className="admin-nav-group-heading">{t('adminNavGroupUsers')}</div>
                            {navItem('users', UserPlus, t('adminTabUsers'))}
                        </div>
                    )}
                    {(user.role === 'superadmin' || user.role === 'admin') && (
                        <div className="admin-nav-group">
                            <div className="admin-nav-group-heading">{t('adminNavGroupMonitoring')}</div>
                            {navItem('kb-overview', Database, t('adminTabKbOverview'))}
                            {navItem('health', Activity, t('adminTabHealth'))}
                            {user.role === 'superadmin' && navItem('eval', Activity, t('evalRunner'))}
                        </div>
                    )}
                </nav>

                <div className={`admin-content-col ${isDataTab ? 'admin-content-col--fluid' : 'admin-content-col--form'}`}>
            {activeTab === 'health' ? (
                <>
                    <SystemHealthDashboard />
                    {user.role === 'superadmin' && <ReembedSection />}
                </>
            ) : activeTab === 'kb-overview' ? (
                <KBOverviewDashboard />
            ) : activeTab === 'global-kbs' ? (
                <AdminGlobalKbsTab onEditGlobalKb={onEditGlobalKb} />
            ) : (
                <section className="admin-content">
                    <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '2rem', alignItems: 'center' }}>
                        <h2 style={{ color: 'var(--text-primary)', margin: 0 }}>
                            {sectionTitle}
                        </h2>
                        {activeTab !== 'users' && activeTab !== 'site' && activeTab !== 'search' && activeTab !== 'agent' && (
                            <button className="search-button" onClick={() => { setShowForm(true); setEditingId(null); }} style={{ background: 'var(--accent-primary)', color: 'white', border: 'none', padding: '0.6rem 1.2rem', borderRadius: '8px', display: 'flex', alignItems: 'center', gap: '0.5rem', cursor: 'pointer' }}>
                                <Plus size={18} />
                                {activeTab === 'configs' ? t('addConfig') : t('addProvider')}
                            </button>
                        )}
                    </div>

                    {activeTab === 'configs' && (
                        <AdminConfigsTab
                            configs={configs}
                            showForm={showForm}
                            editingId={editingId}
                            configFormData={configFormData}
                            setConfigFormData={setConfigFormData}
                            connectionTest={connectionTest}
                            setConnectionTest={setConnectionTest}
                            handleConfigSubmit={handleConfigSubmit}
                            startEditConfig={startEditConfig}
                            handleDelete={handleDelete}
                            handleActivate={handleActivate}
                            resetForm={resetForm}
                            configValidation={configValidation}
                            loading={loading}
                            siteConfigs={siteConfigs}
                            setSiteConfigs={setSiteConfigs}
                            onSiteConfigSubmit={handleSiteConfigSubmit}
                            availableChatModels={availableChatModels}
                        />
                    )}

                    {activeTab === 'auth' && (
                        <AdminAuthTab
                            providers={providers}
                            showForm={showForm}
                            editingId={editingId}
                            authFormData={authFormData}
                            setAuthFormData={setAuthFormData}
                            handleAuthSubmit={handleAuthSubmit}
                            startEditAuth={startEditAuth}
                            handleDelete={handleDelete}
                            resetForm={resetForm}
                            authValidation={authValidation}
                            loading={loading}
                        />
                    )}

                    {activeTab === 'users' && (
                        <AdminUsersTab
                            usersList={usersList}
                            currentUser={user}
                            handleRoleChange={handleRoleChange}
                            handleDeleteUser={handleDeleteUser}
                            loading={loading}
                        />
                    )}

                    {activeTab === 'site' && (
                        <AdminSiteTab
                            siteConfigs={siteConfigs}
                            setSiteConfigs={setSiteConfigs}
                            onSubmit={handleSiteConfigSubmit}
                            onLogoUpload={handleLogoUpload}
                            uploading={uploading}
                        />
                    )}

                    {activeTab === 'search' && (
                        <AdminSearchTab
                            siteConfigs={siteConfigs}
                            setSiteConfigs={setSiteConfigs}
                            onSubmit={handleSiteConfigSubmit}
                        />
                    )}

                    {activeTab === 'agent' && (
                        <AdminAgentTab
                            siteConfigs={siteConfigs}
                            setSiteConfigs={setSiteConfigs}
                            onSubmit={handleSiteConfigSubmit}
                        />
                    )}

                    {activeTab === 'eval' && <AdminEvalTab />}
                </section>
            )}
                </div>
            </div>

            <style>{`
                .admin-container {
                    margin: 0 auto;
                    padding: 2rem clamp(1rem, 4vw, 3rem);
                }
                .admin-layout {
                    display: flex;
                    gap: 2rem;
                    align-items: flex-start;
                }
                .admin-nav-rail {
                    flex: 0 0 220px;
                    width: 220px;
                    display: flex;
                    flex-direction: column;
                    gap: 1.5rem;
                    position: sticky;
                    top: 1rem;
                    max-height: calc(100vh - 2rem);
                    overflow-y: auto;
                }
                .admin-content-col {
                    flex: 1 1 auto;
                    min-width: 0;
                }
                .admin-content-col--form {
                    max-width: 760px;
                    margin: 0 auto;
                }
                .admin-nav-group {
                    display: flex;
                    flex-direction: column;
                    gap: 0.25rem;
                }
                .admin-nav-group-heading {
                    font-size: 0.7rem;
                    font-weight: 600;
                    letter-spacing: 0.05em;
                    text-transform: uppercase;
                    color: var(--text-secondary);
                    padding: 0 0.75rem;
                    margin-bottom: 0.25rem;
                }
                .admin-nav-item {
                    display: flex;
                    align-items: center;
                    gap: 0.6rem;
                    width: 100%;
                    padding: 0.55rem 0.75rem;
                    background: none;
                    border: none;
                    border-left: 3px solid transparent;
                    border-radius: 0 var(--radius-md) var(--radius-md) 0;
                    cursor: pointer;
                    color: var(--text-secondary);
                    font-size: 0.9rem;
                    font-family: inherit;
                    text-align: left;
                    transition: background 0.15s, color 0.15s, border-color 0.15s;
                }
                .admin-nav-item:hover {
                    background: var(--tag-bg);
                    color: var(--accent-primary);
                }
                .admin-nav-item.active {
                    color: var(--accent-primary);
                    font-weight: 600;
                    border-left-color: var(--accent-primary);
                    background: rgba(var(--accent-primary-rgb), 0.08);
                }
                @media (max-width: 768px) {
                    .admin-layout {
                        flex-direction: column;
                    }
                    .admin-nav-rail {
                        flex: 1 1 auto;
                        width: 100%;
                        position: static;
                        max-height: none;
                        flex-direction: row;
                        flex-wrap: wrap;
                    }
                    .admin-content-col--form {
                        max-width: none;
                    }
                }
                .back-button {
                    background: none;
                    border: none;
                    color: var(--text-primary);
                    cursor: pointer;
                    padding: 0.5rem;
                    border-radius: var(--radius-full);
                    display: flex;
                    align-items: center;
                    justify-content: center;
                    transition: background 0.2s;
                }
                .back-button:hover {
                    background: var(--tag-bg);
                }
                .form-grid {
                    display: grid;
                    grid-template-columns: 1fr 1fr;
                    gap: 1.5rem;
                }
                .input-group {
                    display: flex;
                    flex-direction: column;
                    gap: 0.5rem;
                }
                .input-group label {
                    font-size: 0.9rem;
                    color: var(--text-secondary);
                }
                .input-group input, .input-group select {
                    background: var(--bg-primary);
                    border: 1px solid var(--border-color);
                    padding: 0.8rem;
                    border-radius: var(--radius-md);
                    color: var(--text-primary);
                }
                .result-card {
                    background: var(--bg-secondary);
                    border: 1px solid var(--border-color);
                    border-radius: var(--radius-lg);
                    padding: 1.5rem;
                    margin-bottom: 1rem;
                }
                .active-config {
                    border-left: 4px solid var(--accent-primary);
                }
                .active-badge {
                    background: var(--accent-primary);
                    color: white;
                    font-size: 0.75rem;
                    font-weight: bold;
                    padding: 0.1rem 0.5rem;
                    border-radius: var(--radius-full);
                    text-transform: uppercase;
                }
                .icon-button {
                    background: var(--bg-primary);
                    border: 1px solid var(--border-color);
                    color: var(--text-primary);
                    padding: 0.5rem;
                    border-radius: var(--radius-md);
                    cursor: pointer;
                    transition: border-color 0.2s, color 0.2s, background 0.2s;
                }
                .icon-button:hover {
                    background: var(--tag-bg);
                }
                .icon-button.delete:hover {
                    background: var(--error-bg);
                    color: var(--error-text);
                    border-color: var(--error-text);
                }
                .icon-button.activate:hover {
                    background: var(--success-bg);
                    color: var(--success-text);
                    border-color: var(--success-text);
                }
                .secondary-button {
                    background: none;
                    border: 1px solid var(--border-color);
                    color: var(--text-primary);
                    padding: 0.8rem 1.5rem;
                    border-radius: var(--radius-md);
                    cursor: pointer;
                    font-family: inherit;
                    transition: border-color 0.2s, color 0.2s, background 0.2s;
                }
                .secondary-button:hover {
                    background: var(--tag-bg);
                }
                .config-form-overlay {
                    margin-bottom: 2rem;
                }
                .configs-list {
                    margin-top: 1rem;
                }
                h3 {
                    color: var(--text-primary);
                }
                p {
                    color: var(--text-secondary);
                }
                .spin {
                    animation: spin 1s linear infinite;
                }
                @keyframes spin {
                    from { transform: rotate(0deg); }
                    to { transform: rotate(360deg); }
                }
            `}</style>
        </div >
    );
}
