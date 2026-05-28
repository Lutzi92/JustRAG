import { createContext, useContext, type ReactNode } from 'react';
import type { useSidebarResize } from '../hooks/useSidebarResize';

export interface KbLayoutContextValue {
  sidebar: ReturnType<typeof useSidebarResize>;
}

const KbLayoutContext = createContext<KbLayoutContextValue | null>(null);

export function KbLayoutProvider({ value, children }: { value: KbLayoutContextValue; children: ReactNode }) {
  return <KbLayoutContext.Provider value={value}>{children}</KbLayoutContext.Provider>;
}

export function useKbLayout(): KbLayoutContextValue {
  const ctx = useContext(KbLayoutContext);
  if (!ctx) throw new Error('useKbLayout must be used within KbLayoutProvider');
  return ctx;
}
