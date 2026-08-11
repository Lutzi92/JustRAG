import { describe, it, expect, beforeEach, vi, type Mock } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
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
    // committed render tree. (This used to be explained as React 19 discarding
    // the initial mount; the real cause was the framer-motion stub handing back
    // a new component type per render — see the regression test below.)
    await waitFor(() => expect(mockedGet).toHaveBeenCalled());
    expect(await screen.findByLabelText('username')).toBeInTheDocument();
    expect(screen.queryByText('Azure AD')).not.toBeInTheDocument();
  });

  it('/admin: keeps the password form mounted across the auth-config commit', async () => {
    // Regression test for a CI-only failure. On /admin the form renders before
    // the auth-config fetch resolves, so a query can resolve against the
    // pre-commit tree. If a re-render remounts that subtree, the element the
    // test is holding gets detached and the assertion fails with "element could
    // not be found in the document" — while an identical-looking input sits in
    // the document. Locally the commit usually landed first and hid the bug; a
    // slower runner flipped the order.
    //
    // Delaying the response makes the ordering deterministic instead of racy.
    window.history.pushState({}, '', '/admin');
    mockedGet.mockImplementation(
      () => new Promise(resolve => setTimeout(
        () => resolve({ data: { providers: [{ id: 'p1', type: 'oidc', name: 'Azure AD' }], localAuthEnabled: false } }),
        50,
      )),
    );
    renderLogin();

    const input = await screen.findByLabelText('username');
    await waitFor(() => expect(mockedGet).toHaveBeenCalled());
    // Flush the delayed response and the re-render it triggers, so the
    // assertion runs strictly after the commit that used to detach the input.
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 100)); });

    expect(input).toBeInTheDocument();
  });

  it('fetch failure: falls back to showing password form', async () => {
    mockedGet.mockRejectedValue(new Error('Network error'));
    renderLogin();

    expect(await screen.findByLabelText('username')).toBeInTheDocument();
  });
});
