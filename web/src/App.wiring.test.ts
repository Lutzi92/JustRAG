import { describe, it, expect, vi, beforeEach } from 'vitest';
import { JOIN_TOKEN_KEY, peekJoinToken } from './hooks/useJoinLink';

// App.tsx calls captureJoinToken() at MODULE SCOPE — before React renders
// anything — so an invite link is picked up whether the visitor lands on the
// login screen or straight in the app.
//
// That single call is the entire wiring for the /join/<token> entry route.
// Delete it and invite links stop working end to end, while every unit test
// for the hook itself keeps passing. This test exists to make that deletion
// loud.
//
// It evaluates the module rather than rendering it: a render would mount
// ThemeProvider, AuthProvider, MobileProvider, the lazy Login and
// AuthenticatedApp trees and useVersionCheck, none of which this is about.
// Module evaluation does not execute component bodies or effects.
//
// This test was impossible before `virtual:pwa-register/react` was aliased in
// vitest.config.ts — App.tsx transitively imports it via ReloadPrompt, so the
// module graph could not load under vitest at all.

class MemoryStorage implements Storage {
  private data = new Map<string, string>();
  get length() { return this.data.size; }
  clear() { this.data.clear(); }
  getItem(k: string) { return this.data.get(k) ?? null; }
  key(i: number) { return Array.from(this.data.keys())[i] ?? null; }
  removeItem(k: string) { this.data.delete(k); }
  setItem(k: string, v: string) { this.data.set(k, v); }
}

const TOKEN = 'AbCdEf0123456789AbCdEf0123456789AbCdEf012';

beforeEach(() => {
  vi.resetModules();
  Object.defineProperty(window, 'sessionStorage', {
    value: new MemoryStorage(), configurable: true, writable: true,
  });
});

describe('App module wiring', () => {
  it('captures a /join/<token> URL when the module is evaluated', async () => {
    window.history.replaceState(null, '', `/join/${TOKEN}`);

    await import('./App');

    expect(peekJoinToken()).toBe(TOKEN);
    // The URL is rewritten so a reload cannot replay the join.
    expect(window.location.pathname).toBe('/');
  });

  it('leaves an ordinary URL alone', async () => {
    window.history.replaceState(null, '', '/admin');

    await import('./App');

    expect(window.sessionStorage.getItem(JOIN_TOKEN_KEY)).toBeNull();
    expect(window.location.pathname).toBe('/admin');
  });
});
