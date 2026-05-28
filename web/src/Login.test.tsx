import { describe, it, expect, beforeEach, vi, type Mock } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import axios from 'axios';
import Login from './Login';

vi.mock('axios', () => ({
  default: { get: vi.fn(), post: vi.fn() },
}));

// Login pulls in useReducedMotion which calls window.matchMedia,
// not available in jsdom by default.
vi.mock('./hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

vi.mock('./contexts/ThemeContext', () => ({
  useTheme: () => ({
    theme: 'light' as const,
    toggleTheme: () => {},
    language: 'en' as const,
    setLanguage: () => {},
    t: (key: string) => key,
  }),
}));

const mockedGet = axios.get as unknown as Mock;

function mockProviders(data: { providers: Array<{ id: string; type: string; name: string }>; localAuthEnabled: boolean }) {
  mockedGet.mockResolvedValue({ data });
}

function renderLogin() {
  return render(<Login onLogin={vi.fn()} siteConfigs={{}} />);
}

describe('Login visibility', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.history.pushState({}, '', '/');
  });

  it('OIDC-only on /: hides password form, shows SSO button', async () => {
    mockProviders({ providers: [{ id: 'p1', type: 'oidc', name: 'Azure AD' }], localAuthEnabled: false });
    renderLogin();

    expect(await screen.findByText('Azure AD')).toBeInTheDocument();
    expect(screen.queryByLabelText('username')).not.toBeInTheDocument();
  });

  it('local enabled on /: shows password form, no SSO', async () => {
    mockProviders({ providers: [], localAuthEnabled: true });
    renderLogin();

    expect(await screen.findByLabelText('username')).toBeInTheDocument();
    expect(screen.queryByText('loginWithSso')).not.toBeInTheDocument();
  });

  it('LDAP active on /: shows password form', async () => {
    mockProviders({ providers: [{ id: 'l1', type: 'ldap', name: 'Corp LDAP' }], localAuthEnabled: false });
    renderLogin();

    expect(await screen.findByLabelText('username')).toBeInTheDocument();
  });

  it('/admin: always shows password form, never SSO', async () => {
    window.history.pushState({}, '', '/admin');
    mockProviders({ providers: [{ id: 'p1', type: 'oidc', name: 'Azure AD' }], localAuthEnabled: false });
    renderLogin();

    // Wait for the auth-config fetch to settle so the assertion runs against the
    // committed render tree (React 19 discards the initial synchronous mount once
    // the discovery effect's setState commits).
    await waitFor(() => expect(mockedGet).toHaveBeenCalled());
    expect(await screen.findByLabelText('username')).toBeInTheDocument();
    expect(screen.queryByText('Azure AD')).not.toBeInTheDocument();
  });

  it('fetch failure: falls back to showing password form', async () => {
    mockedGet.mockRejectedValue(new Error('Network error'));
    renderLogin();

    expect(await screen.findByLabelText('username')).toBeInTheDocument();
  });
});
