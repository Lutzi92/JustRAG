import { createContext, useContext, useMemo, type ReactNode } from 'react';
import { useThemeAndLanguage } from '../hooks/useThemeAndLanguage';
import type { Language } from '../translations';

interface ThemeContextValue {
  theme: 'light' | 'dark';
  toggleTheme: () => void;
  language: Language;
  setLanguage: (lang: Language) => void;
  t: (key: string) => string;
}

const ThemeContext = createContext<ThemeContextValue | null>(null);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const { theme, language, setLanguage, toggleTheme, t } = useThemeAndLanguage();

  const value = useMemo(
    () => ({ theme, toggleTheme, language, setLanguage, t }),
    [theme, toggleTheme, language, setLanguage, t]
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) throw new Error('useTheme must be used within ThemeProvider');
  return ctx;
}
