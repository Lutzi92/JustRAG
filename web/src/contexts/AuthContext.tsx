import { createContext, useContext, useMemo, type ReactNode } from 'react';
import type { User as UserType } from '../types';

interface AuthContextValue {
  user: UserType;
  token: string;
  logout: () => void;
  updateUser: (user: UserType) => void;
  siteConfigs: Record<string, string>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

interface AuthProviderProps {
  user: UserType;
  token: string;
  logout: () => void;
  updateUser: (user: UserType) => void;
  siteConfigs: Record<string, string>;
  children: ReactNode;
}

export function AuthProvider({ user, token, logout, updateUser, siteConfigs, children }: AuthProviderProps) {
  const value = useMemo(
    () => ({ user, token, logout, updateUser, siteConfigs }),
    [user, token, logout, updateUser, siteConfigs]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used within AuthProvider');
  return ctx;
}
