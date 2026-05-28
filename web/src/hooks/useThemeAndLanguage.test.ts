import { describe, it, expect, beforeEach, vi } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useThemeAndLanguage } from './useThemeAndLanguage';

// Mock localStorage
const localStorageMock = (() => {
  let store: Record<string, string> = {};
  return {
    getItem: vi.fn((key: string) => store[key] ?? null),
    setItem: vi.fn((key: string, value: string) => { store[key] = value; }),
    removeItem: vi.fn((key: string) => { delete store[key]; }),
    clear: vi.fn(() => { store = {}; }),
  };
})();

Object.defineProperty(window, 'localStorage', { value: localStorageMock });

// Mock matchMedia
const matchMediaMock = vi.fn();
Object.defineProperty(window, 'matchMedia', {
  value: matchMediaMock,
});

beforeEach(() => {
  localStorageMock.clear();
  vi.clearAllMocks();
  matchMediaMock.mockReturnValue({ matches: true }); // default: prefers dark
  document.documentElement.removeAttribute('data-theme');
});

describe('useThemeAndLanguage', () => {
  it('defaults to dark theme when matchMedia prefers dark', () => {
    matchMediaMock.mockReturnValue({ matches: true });
    const { result } = renderHook(() => useThemeAndLanguage());
    expect(result.current.theme).toBe('dark');
  });

  it('defaults to light theme when matchMedia prefers light', () => {
    matchMediaMock.mockReturnValue({ matches: false });
    const { result } = renderHook(() => useThemeAndLanguage());
    expect(result.current.theme).toBe('light');
  });

  it('defaults to "de" for language', () => {
    const { result } = renderHook(() => useThemeAndLanguage());
    expect(result.current.language).toBe('de');
  });

  it('restores theme from localStorage', () => {
    localStorageMock.setItem('theme', 'light');
    const { result } = renderHook(() => useThemeAndLanguage());
    expect(result.current.theme).toBe('light');
  });

  it('restores language from localStorage', () => {
    localStorageMock.setItem('language', 'en');
    const { result } = renderHook(() => useThemeAndLanguage());
    expect(result.current.language).toBe('en');
  });

  it('toggleTheme switches light to dark and back', () => {
    localStorageMock.setItem('theme', 'light');
    const { result } = renderHook(() => useThemeAndLanguage());
    expect(result.current.theme).toBe('light');

    act(() => { result.current.toggleTheme(); });
    expect(result.current.theme).toBe('dark');

    act(() => { result.current.toggleTheme(); });
    expect(result.current.theme).toBe('light');
  });

  it('t("cancel") returns German by default', () => {
    const { result } = renderHook(() => useThemeAndLanguage());
    expect(result.current.t('cancel')).toBe('Abbrechen');
  });

  it('t("cancel") returns English after setLanguage("en")', () => {
    const { result } = renderHook(() => useThemeAndLanguage());

    act(() => { result.current.setLanguage('en'); });
    expect(result.current.t('cancel')).toBe('Cancel');
  });

  it('t() returns the key itself for missing translation keys', () => {
    const { result } = renderHook(() => useThemeAndLanguage());
    expect(result.current.t('nonexistentKey')).toBe('nonexistentKey');
  });

  it('persists theme to localStorage on change', () => {
    const { result } = renderHook(() => useThemeAndLanguage());
    act(() => { result.current.toggleTheme(); });
    expect(localStorageMock.setItem).toHaveBeenCalledWith('theme', expect.any(String));
  });

  it('persists language to localStorage on change', () => {
    const { result } = renderHook(() => useThemeAndLanguage());
    act(() => { result.current.setLanguage('en'); });
    expect(localStorageMock.setItem).toHaveBeenCalledWith('language', 'en');
  });
});
