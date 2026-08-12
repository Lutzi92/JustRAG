import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import type { KnowledgeBase } from '../types';
import { HomeView } from './HomeView';
import { translations } from '../translations';

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({
    theme: 'light',
    language: 'en',
    setLanguage: vi.fn(),
    toggleTheme: vi.fn(),
    t: (key: string) => {
      const entry = translations[key as keyof typeof translations];
      return entry ? entry.en : key;
    },
  }),
}));

vi.mock('../contexts/AuthContext', () => ({
  useAuth: () => ({
    user: { id: 'user-1', username: 'grace', role: 'user' },
    siteConfigs: {},
  }),
}));

const baseKb: KnowledgeBase = {
  id: 'kb-1',
  name: 'Security Advisories',
  description: null,
  userId: 'owner-1',
  createdAt: '2026-01-01T00:00:00Z',
  isPro: false,
  aiConfigId: null,
  chatModel: null,
  embeddingModel: null,
  rerankModel: null,
  ttsModel: null,
};

const noopProps = {
  globalKbs: [],
  currentKb: null,
  availableConfigs: [],
  copySuccess: false,
  onCopyUserId: vi.fn(),
  onLogout: vi.fn(),
  onViewProfile: vi.fn(),
  onViewAdmin: vi.fn(),
  onViewAgents: vi.fn(),
  onCreateKB: vi.fn(),
  onSelectKB: vi.fn(),
  onDeleteKB: vi.fn(),
  removingKb: false,
  onCreateGlobalKB: vi.fn(),
  onDeleteGlobalKB: vi.fn(),
  onOpenGlobalKbSettings: vi.fn(),
  onOpenShare: vi.fn(),
  onUpdateKBSettings: vi.fn(),
  showShareModal: false,
  setShowShareModal: vi.fn(),
  sharingKb: null,
  shareUserId: '',
  setShareUserId: vi.fn(),
  shareTargetUser: null,
  shareLoading: false,
  sharePermission: 'view' as const,
  setSharePermission: vi.fn(),
  onLookupUser: vi.fn(),
  onConfirmShare: vi.fn(),
  notFoundUsername: null,
  onPendingInvited: vi.fn(),
  showSettings: false,
  setShowSettings: vi.fn(),
};

// The members-dialog trigger used to be gated on kb.userId === user.id
// (owner-only), which made the whole `admin` tier unreachable from the UI —
// a KB admin who isn't the owner could never open the dialog to manage
// members or hand out roles. It must key on myRole instead.
describe('HomeView members-dialog trigger', () => {
  it('shows the trigger for a caller whose myRole is admin', () => {
    const kb: KnowledgeBase = { ...baseKb, myRole: 'admin' };
    render(<HomeView kbs={[kb]} {...noopProps} />);
    expect(screen.getByRole('button', { name: translations.share.en })).toBeInTheDocument();
  });

  it('shows the trigger for a caller whose myRole is owner', () => {
    const kb: KnowledgeBase = { ...baseKb, myRole: 'owner' };
    render(<HomeView kbs={[kb]} {...noopProps} />);
    expect(screen.getByRole('button', { name: translations.share.en })).toBeInTheDocument();
  });

  it('hides the trigger for a caller whose myRole is edit', () => {
    const kb: KnowledgeBase = { ...baseKb, myRole: 'edit' };
    render(<HomeView kbs={[kb]} {...noopProps} />);
    expect(screen.queryByRole('button', { name: translations.share.en })).not.toBeInTheDocument();
  });

  it('hides the trigger for a caller whose myRole is view', () => {
    const kb: KnowledgeBase = { ...baseKb, myRole: 'view' };
    render(<HomeView kbs={[kb]} {...noopProps} />);
    expect(screen.queryByRole('button', { name: translations.share.en })).not.toBeInTheDocument();
  });
});
