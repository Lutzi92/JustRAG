import { useState, useEffect, useCallback, lazy, Suspense } from 'react';
import axios from 'axios';
import { Loader2 } from 'lucide-react';
import type { User as UserType } from './types';
import { API_BASE_URL } from './api';
import { ThemeProvider } from './contexts/ThemeContext';
import { AuthProvider } from './contexts/AuthContext';
import { MobileProvider } from './contexts/MobileContext';
import { viewportHeight } from './utils/viewport';
import { ErrorBoundary } from './components/ErrorBoundary';
import { ReloadPrompt } from './components/ReloadPrompt';
import { useVersionCheck } from './hooks/useVersionCheck';
import { captureJoinToken } from './hooks/useJoinLink';

// Runs once at module load, before React renders anything — the token has to
// be captured whether the user lands on the login screen or straight in the
// app, and before the URL is used for anything else.
captureJoinToken();

// Lazy load
const Login = lazy(() => import('./Login'));
const AuthenticatedApp = lazy(() => import('./AuthenticatedApp'));

const LoadingFallback = () => (
  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: viewportHeight('100dvh', '100vh'), width: '100%' }}>
    <Loader2 className="animate-spin" size={32} color="var(--accent-primary)" />
  </div>
);

function App() {
  // Detect server restarts and reload to pick up new assets
  useVersionCheck();

  // Auth state
  const [user, setUser] = useState<UserType | null>(() => {
    const saved = localStorage.getItem('user');
    if (!saved) return null;
    try {
      return JSON.parse(saved);
    } catch {
      localStorage.removeItem('user');
      localStorage.removeItem('token');
      return null;
    }
  });
  const [token, setToken] = useState<string | null>(() => {
    const saved = localStorage.getItem('token');
    // Set axios auth header immediately so it's available before any child effects fire
    if (saved) {
      axios.defaults.headers.common['Authorization'] = `Bearer ${saved}`;
    }
    return saved;
  });

  // Config state (needed for Login too)
  const [siteConfigs, setSiteConfigs] = useState<Record<string, string>>({});

  // Auth handlers
  // SECURITY (tracked follow-up): the JWT lives in localStorage, which is
  // readable by any script on the page. The hardened design is an
  // HttpOnly;Secure;SameSite cookie set by the backend, which would also
  // require CSRF tokens and reworking the Bearer-header axios/SSE/OIDC flows.
  // Deferred as a deliberate decision; CSP + a short token TTL are the interim
  // mitigations. See the security review (2026-05-26).
  const handleLogout = useCallback(async () => {
    const currentToken = token;
    const wasOidc = user?.authMethod === 'oidc';

    // Best-effort server-side JWT blacklist. Uses fetch (not axios) so the
    // global 401-auto-logout interceptor can't recurse if the token is
    // already expired. Browsers also don't send Authorization on top-level
    // navigation, so the blacklist has to happen here before we hand off
    // to /api/auth/oidc/logout.
    if (currentToken) {
      try {
        await fetch(`${API_BASE_URL}/api/auth/logout`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${currentToken}` },
        });
      } catch {
        // ignore — local state still gets cleared
      }
    }

    localStorage.removeItem('token');
    localStorage.removeItem('user');

    if (wasOidc) {
      // Top-level nav to the OIDC logout endpoint — it discovers the IdP's
      // end_session_endpoint and redirects through the IdP so the SSO session
      // is killed too. Don't touch React state; the page is going away.
      window.location.href = `${API_BASE_URL}/api/auth/oidc/logout`;
      return;
    }

    setToken(null);
    setUser(null);
  }, [token, user]);

  // Auth/config setup + auto-logout on 401
  useEffect(() => {
    // Fetch site configs (react.dev fetch-then-set pattern: the state write
    // happens in the async callback, not synchronously in the effect body)
    axios.get(`${API_BASE_URL}/api/site-config`)
      .then(res => setSiteConfigs(res.data))
      .catch((err: unknown) => console.error('Failed to fetch site configs:', err));

    if (token) {
      axios.defaults.headers.common['Authorization'] = `Bearer ${token}`;
    } else {
      delete axios.defaults.headers.common['Authorization'];
    }

    // Intercept 401 responses to auto-logout when token is expired/invalidated
    const interceptor = axios.interceptors.response.use(
      response => response,
      error => {
        if (error.response?.status === 401 && token) {
          handleLogout();
        }
        return Promise.reject(error);
      }
    );

    // Also handle 401 from direct fetch() calls via authFetch
    const onUnauthorized = () => handleLogout();
    window.addEventListener('auth:unauthorized', onUnauthorized);

    return () => {
      axios.interceptors.response.eject(interceptor);
      window.removeEventListener('auth:unauthorized', onUnauthorized);
    };
  }, [token, handleLogout]);

  const handleLogin = (newToken: string, newUser: UserType) => {
    // Set axios auth header synchronously BEFORE state updates trigger re-render.
    // React runs child effects before parent effects, so AuthenticatedApp's
    // fetchKBs() would fire before App's useEffect sets the header.
    axios.defaults.headers.common['Authorization'] = `Bearer ${newToken}`;
    setToken(newToken);
    setUser(newUser);
    localStorage.setItem('token', newToken);
    localStorage.setItem('user', JSON.stringify(newUser));
  };

  const handleUpdateUser = (updatedUser: UserType) => {
    setUser(updatedUser);
    localStorage.setItem('user', JSON.stringify(updatedUser));
  };

  return (
    <ErrorBoundary>
      <ThemeProvider>
        <ReloadPrompt />
        <MobileProvider>
          <Suspense fallback={<LoadingFallback />}>
            {(!token || !user) ? (
              <Login
                onLogin={handleLogin}
                siteConfigs={siteConfigs}
              />
            ) : (
              <AuthProvider
                user={user}
                token={token}
                siteConfigs={siteConfigs}
                logout={handleLogout}
                updateUser={handleUpdateUser}
              >
                <ErrorBoundary>
                  <AuthenticatedApp />
                </ErrorBoundary>
              </AuthProvider>
            )}
          </Suspense>
        </MobileProvider>
      </ThemeProvider>
    </ErrorBoundary>
  );
}

export default App;
