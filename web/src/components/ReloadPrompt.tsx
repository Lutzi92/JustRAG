import { useRegisterSW } from 'virtual:pwa-register/react';
import { RefreshCw, X } from 'lucide-react';
import { useTheme } from '../contexts/ThemeContext';

// Re-check for a waiting service worker on this cadence. Matches the
// useVersionCheck poll interval so a freshly deployed bundle surfaces a
// prompt within ~a minute even on a long-lived tab.
const UPDATE_CHECK_INTERVAL = 60_000;

/**
 * ReloadPrompt renders a non-blocking banner when a new build's service
 * worker is installed and waiting. Accepting it posts SKIP_WAITING and
 * reloads with the fresh, content-hashed assets — the only reliable way to
 * move a PWA client off a precached app shell. A plain page reload would
 * just re-serve the stale precache.
 *
 * Mounted once near the app root (see App.tsx) so it covers both the login
 * screen and the authenticated app. Renders nothing until an update waits.
 */
export function ReloadPrompt() {
    const { language } = useTheme();
    const {
        needRefresh: [needRefresh, setNeedRefresh],
        updateServiceWorker,
    } = useRegisterSW({
        onRegisteredSW(_swUrl, registration) {
            if (!registration) return;
            setInterval(() => {
                // Offline / transient failures just retry on the next tick.
                registration.update().catch(() => { /* no-op */ });
            }, UPDATE_CHECK_INTERVAL);
        },
    });

    if (!needRefresh) return null;

    const t = language === 'en'
        ? { msg: 'A new version is available.', reload: 'Reload', dismiss: 'Dismiss' }
        : { msg: 'Eine neue Version ist verfügbar.', reload: 'Neu laden', dismiss: 'Später' };

    return (
        <div
            role="alert"
            aria-live="polite"
            style={{
                position: 'fixed',
                bottom: '1rem',
                left: '50%',
                transform: 'translateX(-50%)',
                zIndex: 9999,
                display: 'flex',
                alignItems: 'center',
                gap: '0.75rem',
                maxWidth: 'calc(100vw - 2rem)',
                padding: '0.625rem 0.75rem 0.625rem 1rem',
                background: 'var(--bg-secondary)',
                color: 'var(--text-primary)',
                border: '1px solid var(--border-color)',
                borderRadius: '10px',
                boxShadow: '0 6px 24px rgba(0, 0, 0, 0.18)',
                fontSize: '0.875rem',
            }}
        >
            <span>{t.msg}</span>
            <button
                onClick={() => updateServiceWorker(true)}
                style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: '0.375rem',
                    padding: '0.375rem 0.75rem',
                    background: 'var(--accent-primary)',
                    color: '#fff',
                    border: 'none',
                    borderRadius: '7px',
                    cursor: 'pointer',
                    fontSize: '0.8125rem',
                    fontWeight: 600,
                }}
            >
                <RefreshCw size={14} aria-hidden="true" />
                {t.reload}
            </button>
            <button
                onClick={() => setNeedRefresh(false)}
                aria-label={t.dismiss}
                title={t.dismiss}
                style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    padding: '0.25rem',
                    background: 'transparent',
                    color: 'var(--text-secondary)',
                    border: 'none',
                    borderRadius: '6px',
                    cursor: 'pointer',
                }}
            >
                <X size={16} />
            </button>
        </div>
    );
}
