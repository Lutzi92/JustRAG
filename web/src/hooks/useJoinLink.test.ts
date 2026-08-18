import { describe, it, expect, beforeEach } from 'vitest';
import { captureJoinToken, peekJoinToken, takeJoinToken, JOIN_TOKEN_KEY } from './useJoinLink';

// sessionStorage is a bare {} under local vitest/jsdom but a real Storage in
// CI, so persisted state leaks between tests in CI only. Stub our own.
class MemoryStorage implements Storage {
  private data = new Map<string, string>();
  get length() { return this.data.size; }
  clear() { this.data.clear(); }
  getItem(k: string) { return this.data.get(k) ?? null; }
  key(i: number) { return Array.from(this.data.keys())[i] ?? null; }
  removeItem(k: string) { this.data.delete(k); }
  setItem(k: string, v: string) { this.data.set(k, v); }
}

const setPath = (path: string) => {
  window.history.replaceState(null, '', path);
};

beforeEach(() => {
  Object.defineProperty(window, 'sessionStorage', {
    value: new MemoryStorage(), configurable: true, writable: true,
  });
  setPath('/');
});

describe('captureJoinToken', () => {
  it('parks the token and cleans the URL', () => {
    setPath('/join/AbCdEf0123456789AbCdEf0123456789AbCdEf012');
    captureJoinToken();

    expect(peekJoinToken()).toBe('AbCdEf0123456789AbCdEf0123456789AbCdEf012');
    expect(window.location.pathname).toBe('/');
  });

  it('ignores paths that are not join links', () => {
    setPath('/admin');
    captureJoinToken();

    expect(peekJoinToken()).toBeNull();
    expect(window.location.pathname).toBe('/admin');
  });

  it('ignores a token with illegal characters', () => {
    setPath('/join/not$a$valid$token$not$a$valid$token$xxx');
    captureJoinToken();

    expect(peekJoinToken()).toBeNull();
  });

  it('ignores a token that is too short', () => {
    setPath('/join/tooshort');
    captureJoinToken();

    expect(peekJoinToken()).toBeNull();
  });
});

describe('takeJoinToken', () => {
  it('returns the token once and clears it', () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'AbCdEf0123456789AbCdEf0123456789AbCdEf012');

    expect(takeJoinToken()).toBe('AbCdEf0123456789AbCdEf0123456789AbCdEf012');
    expect(takeJoinToken()).toBeNull();
  });
});
