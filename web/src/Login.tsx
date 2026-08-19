import React, { useEffect, useState } from 'react';
import axios from 'axios';
import { BookOpen, User, Lock, Loader2, Sparkles, KeyRound } from 'lucide-react';
import { motion } from 'framer-motion';
import { API_BASE_URL } from './api';

import { useTheme } from './contexts/ThemeContext';
import { useFormValidation } from './hooks/useFormValidation';
import { useReducedMotion, getMotionProps } from './hooks/useReducedMotion';
import { Footer } from './components/Footer';
import { LegalPage } from './components/LegalPage';
import { viewportHeight } from './utils/viewport';
import { peekJoinToken } from './hooks/useJoinLink';

interface LoginProps {
    onLogin: (token: string, user: { id: string; username: string; role: string }) => void;
    siteConfigs: Record<string, string>;
}

interface PublicAuthProvider {
    id: string;
    type: string;
    name: string;
}

interface PublicAuthConfig {
    providers: PublicAuthProvider[];
    localAuthEnabled: boolean;
}

const Login: React.FC<LoginProps> = ({ onLogin, siteConfigs }) => {
    const { language, setLanguage, t } = useTheme();
    const { errors: fieldErrors, validate, clearError } = useFormValidation({
        username: (v) => !v.trim() && t('fieldRequired'),
        password: (v) => !v.trim() && t('fieldRequired'),
    });
    const reducedMotion = useReducedMotion();
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState('');
    const [legalPage, setLegalPage] = useState<'terms' | 'privacy' | 'accessibility' | null>(null);
    const [providers, setProviders] = useState<PublicAuthProvider[]>([]);
    const [localAuthEnabled, setLocalAuthEnabled] = useState(false);
    const [authConfigLoaded, setAuthConfigLoaded] = useState(false);
    // Deliberately without the KB's name: naming it would need an
    // unauthenticated preview endpoint that hands KB names to anyone holding
    // a token.
    const hasPendingJoin = peekJoinToken() !== null;

    // The /admin path is the superadmin breakglass: it always shows the local
    // username/password form and never the SSO button.
    const isAdminRoute = window.location.pathname === '/admin';

    // Discover the public auth config so the page renders only the methods that
    // are actually enabled.
    useEffect(() => {
        let cancelled = false;
        axios.get<PublicAuthConfig>(`${API_BASE_URL}/api/auth/providers`)
            .then(res => {
                if (cancelled) return;
                setProviders(res.data.providers ?? []);
                setLocalAuthEnabled(!!res.data.localAuthEnabled);
                setAuthConfigLoaded(true);
            })
            .catch(() => {
                // On failure, fall back to showing the local form so a backend
                // hiccup never strands users at a button-less screen.
                if (cancelled) return;
                setLocalAuthEnabled(true);
                setAuthConfigLoaded(true);
            });
        return () => { cancelled = true; };
    }, []);

    const oidcProvider = providers.find(p => p.type === 'oidc') ?? null;
    const ldapActive = providers.some(p => p.type === 'ldap');
    const showPasswordForm = isAdminRoute || (authConfigLoaded && (localAuthEnabled || ldapActive));
    const showOidcButton = !isAdminRoute && authConfigLoaded && oidcProvider !== null;

    // OIDC callback drops the JWT + user JSON into the URL fragment so they
    // never reach server logs. Read it on mount, hand off to onLogin, and
    // strip the fragment from the URL.
    useEffect(() => {
        const hash = window.location.hash;
        if (!hash.startsWith('#oidc=')) {
            const params = new URLSearchParams(window.location.search);
            const oidcErr = params.get('oidc_error');
            if (oidcErr) {
                setError(`${t('oidcLoginFailed')} (${oidcErr})`);
                params.delete('oidc_error');
                const newSearch = params.toString();
                window.history.replaceState(null, '', window.location.pathname + (newSearch ? '?' + newSearch : ''));
            }
            return;
        }
        try {
            const encoded = hash.slice('#oidc='.length);
            const json = atob(encoded.replace(/-/g, '+').replace(/_/g, '/'));
            const payload = JSON.parse(json) as { token: string; user: { id: string; username: string; role: string } };
            window.history.replaceState(null, '', window.location.pathname);
            onLogin(payload.token, payload.user);
        } catch (decodeErr) {
            console.error('OIDC fragment decode failed:', decodeErr);
            setError(t('oidcLoginFailed'));
        }
    }, [onLogin, t]);

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        if (!validate({ username, password })) return;
        setLoading(true);
        setError('');

        try {
            const response = await axios.post(`${API_BASE_URL}/api/auth/login`, {
                username,
                password,
            });

            const { token, user } = response.data;
            if (isAdminRoute) {
                window.history.replaceState(null, '', '/');
            }
            onLogin(token, user);
        } catch (err: unknown) {
            console.error('Login failed:', err);
            const axiosErr = err as { response?: { data?: { error?: string } } };
            setError(axiosErr.response?.data?.error || t('invalidCredentials'));
        } finally {
            setLoading(false);
        }
    };

    if (legalPage) {
        return <LegalPage page={legalPage} onBack={() => setLegalPage(null)} />;
    }

    return (
        <main className="login-container" style={{
            minHeight: viewportHeight('100dvh', '100vh'),
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            justifyContent: 'center',
            background: 'var(--bg-primary)',
            padding: '1rem'
        }}>
            <motion.div
                {...getMotionProps(reducedMotion)}
                initial={{ opacity: 0, y: 20 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: 0.5 }}
                style={{
                    width: '100%',
                    maxWidth: '400px',
                    background: 'var(--bg-secondary)',
                    padding: '2.5rem',
                    borderRadius: 'var(--shape-xl)',
                    boxShadow: 'var(--shadow-lg)',
                    border: '1px solid var(--border-color)',
                    position: 'relative'
                }}
            >
                <div style={{ position: 'absolute', top: '1rem', right: '1rem' }}>
                    <button
                        onClick={() => setLanguage(language === 'de' ? 'en' : 'de')}
                        style={{
                            background: 'none',
                            border: '1px solid var(--border-color)',
                            borderRadius: '50%',
                            width: '44px',
                            height: '44px',
                            cursor: 'pointer',
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            color: 'var(--text-secondary)',
                            fontSize: '0.8rem',
                            fontWeight: 600
                        }}
                        title={language === 'de' ? "Switch to English" : "Zu Deutsch wechseln"}
                        aria-label={language === 'de' ? "Switch to English" : "Zu Deutsch wechseln"}
                    >
                        {language.toUpperCase()}
                    </button>
                </div>
                <div style={{ textAlign: 'center', marginBottom: '2.5rem' }}>
                    <div style={{
                        display: 'inline-flex',
                        padding: '1rem',
                        background: 'var(--header-icon-bg)',
                        borderRadius: 'var(--shape-lg)',
                        color: 'var(--accent-primary)',
                        marginBottom: '1rem',
                        alignItems: 'center',
                        justifyContent: 'center',
                        width: '100%',
                        height: '180px'
                    }}>
                        {siteConfigs.logo_path ? (
                            <img
                                src={`${API_BASE_URL}${siteConfigs.logo_path}`}
                                alt="Site Logo"
                                fetchPriority="high"
                                loading="eager"
                                style={{ width: '100%', height: '100%', objectFit: 'contain', maxWidth: '100%' }}
                            />
                        ) : (
                            <BookOpen size={100} />
                        )}
                    </div>
                    <h1 style={{ font: 'var(--type-h1)', color: 'var(--text-heading)', marginBottom: '0.5rem' }}>{t('welcomeBack')}</h1>
                    <p style={{ color: 'var(--text-secondary)', fontSize: '0.875rem' }}>{t('loginSubtitle')}</p>
                </div>

                {error && (
                    <div
                        role="alert"
                        id="login-error"
                        style={{
                            padding: '0.75rem 1rem',
                            background: 'var(--error-bg, #fee2e2)',
                            color: 'var(--error-text)',
                            borderRadius: 'var(--shape-md)',
                            fontSize: '0.875rem',
                            marginBottom: '1.5rem',
                            textAlign: 'center',
                            border: '1px solid var(--error-text)'
                        }}
                    >
                        {error}
                    </div>
                )}

                {hasPendingJoin && (
                    <p style={{ fontSize: '0.9rem', color: 'var(--text-secondary)', marginBottom: '1rem' }}>
                        {t('joinLinkLoginHint')}
                    </p>
                )}

                {showPasswordForm && (
                <form onSubmit={handleSubmit}>
                    <div style={{ marginBottom: '1.25rem' }}>
                        <label htmlFor="username" style={{ display: 'block', marginBottom: '0.5rem', fontSize: '0.875rem', fontWeight: 500, color: 'var(--text-primary)' }}>{t('username')}</label>
                        <div style={{ position: 'relative' }}>
                            <span style={{ position: 'absolute', left: '1rem', top: '50%', transform: 'translateY(-50%)', color: 'var(--text-secondary)' }}>
                                <User size={18} />
                            </span>
                            <input
                                id="username"
                                type="text"
                                value={username}
                                onChange={(e) => { setUsername(e.target.value); clearError('username'); }}
                                aria-invalid={!!error || !!fieldErrors.username}
                                aria-describedby={fieldErrors.username ? "username-error" : error ? "login-error" : undefined}
                                className="login-input"
                                style={{
                                    width: '100%',
                                    padding: '0.75rem 1rem 0.75rem 2.75rem',
                                    background: 'var(--bg-primary)',
                                    border: '1px solid var(--border-color)',
                                    borderRadius: 'var(--shape-md)',
                                    color: 'var(--text-primary)',
                                    fontSize: '1rem',
                                    transition: 'border-color 0.2s',
                                    boxSizing: 'border-box'
                                }}
                                placeholder={t('enterUsername')}
                            />
                        </div>
                        {fieldErrors.username && <span id="username-error" className="field-error" role="alert">{fieldErrors.username}</span>}
                    </div>

                    <div style={{ marginBottom: '2rem' }}>
                        <label htmlFor="password" style={{ display: 'block', marginBottom: '0.5rem', fontSize: '0.875rem', fontWeight: 500, color: 'var(--text-primary)' }}>{t('password')}</label>
                        <div style={{ position: 'relative' }}>
                            <span style={{ position: 'absolute', left: '1rem', top: '50%', transform: 'translateY(-50%)', color: 'var(--text-secondary)' }}>
                                <Lock size={18} />
                            </span>
                            <input
                                id="password"
                                type="password"
                                value={password}
                                onChange={(e) => { setPassword(e.target.value); clearError('password'); }}
                                aria-invalid={!!error || !!fieldErrors.password}
                                aria-describedby={fieldErrors.password ? "password-error" : error ? "login-error" : undefined}
                                className="login-input"
                                style={{
                                    width: '100%',
                                    padding: '0.75rem 1rem 0.75rem 2.75rem',
                                    background: 'var(--bg-primary)',
                                    border: '1px solid var(--border-color)',
                                    borderRadius: 'var(--shape-md)',
                                    color: 'var(--text-primary)',
                                    fontSize: '1rem',
                                    transition: 'border-color 0.2s',
                                    boxSizing: 'border-box'
                                }}
                                placeholder="••••••••"
                            />
                        </div>
                        {fieldErrors.password && <span id="password-error" className="field-error" role="alert">{fieldErrors.password}</span>}
                    </div>

                    <button
                        type="submit"
                        disabled={loading}
                        style={{
                            width: '100%',
                            padding: '0.875rem',
                            background: 'var(--accent-primary)',
                            color: 'white',
                            border: 'none',
                            borderRadius: 'var(--shape-md)',
                            fontSize: '1rem',
                            fontWeight: 600,
                            cursor: loading ? 'not-allowed' : 'pointer',
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            gap: '0.5rem',
                            boxShadow: 'var(--shadow-md)',
                        }}
                    >
                        <span className="icon-swap" key={loading ? 'loading' : 'login'}>{loading ? (
                            <Loader2 className="animate-spin" size={20} />
                        ) : (
                            <>
                                {t('login')} <Sparkles size={18} />
                            </>
                        )}</span>
                    </button>
                </form>
                )}

                {showOidcButton && (
                    <a
                        href={`${API_BASE_URL}/api/auth/oidc/login`}
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            gap: '0.5rem',
                            width: '100%',
                            marginTop: '1rem',
                            padding: '0.875rem',
                            background: 'var(--bg-primary)',
                            color: 'var(--text-primary)',
                            border: '1px solid var(--border-color)',
                            borderRadius: 'var(--shape-md)',
                            fontSize: '1rem',
                            fontWeight: 600,
                            textDecoration: 'none',
                            boxSizing: 'border-box',
                        }}
                    >
                        <KeyRound size={18} /> {oidcProvider.name || t('loginWithSso')}
                    </a>
                )}

                <div style={{ marginTop: '2rem', textAlign: 'center', color: 'var(--text-secondary)', fontSize: '0.75rem' }}>

                </div>
            </motion.div>
            <Footer onNavigate={setLegalPage} />
        </main>
    );
};

export default Login;
