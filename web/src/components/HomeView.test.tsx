import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import type { KnowledgeBase } from '../types';
import { HomeView, type HomeViewProps } from './HomeView';
import { translations } from '../translations';
import { ModalProvider } from '../contexts/ModalContext';
import { useKbRemoval } from '../hooks/useKbRemoval';

vi.mock('axios');
const mockedAxios = vi.mocked(axios, true);

// Modal (behind ModalProvider) calls useReducedMotion, which reads
// window.matchMedia — jsdom doesn't implement it, so tests that mount a real
// ModalProvider need it stubbed (see KbSettingsPanel.test.tsx for the same
// pattern). Harmless for the other describe blocks in this file, which never
// mount ModalProvider.
beforeEach(() => {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }));
});

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
  onOpenCatalog: vi.fn(),
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

// RealRemovalHomeView wires onDeleteKB to the real useKbRemoval hook (rather
// than noopProps' vi.fn() stub) so tests can exercise the actual delete /
// leave / unsubscribe decision, including the real confirmation dialog —
// which needs a real ModalProvider underneath, not a mocked showConfirm.
function RealRemovalHomeView(props: { kbs: KnowledgeBase[]; globalKbs: KnowledgeBase[] } & Partial<HomeViewProps>) {
  const { removeKb, removing } = useKbRemoval();
  const onDeleteKB = async (kb: KnowledgeBase, e: React.MouseEvent) => {
    e.stopPropagation();
    await removeKb(kb);
  };
  const merged = { ...noopProps, ...props, onDeleteKB, removingKb: removing } as HomeViewProps;
  return <HomeView {...merged} />;
}

// The single render helper for this file (Step 2 of the brief): plain
// prop-override tests (badge) and real-removal-flow tests (subscriber
// unsubscribe) both go through it, wrapped in a real ModalProvider so
// showConfirm's dialog actually renders.
function renderHomeView(overrides: Partial<HomeViewProps> & { kbs?: KnowledgeBase[]; globalKbs?: KnowledgeBase[] } = {}) {
  const { kbs = [], globalKbs = [], ...rest } = overrides;
  return render(
    <ModalProvider>
      <RealRemovalHomeView kbs={kbs} globalKbs={globalKbs} {...rest} />
    </ModalProvider>
  );
}

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

// auto_subscribe defaults to false (Phase 2): a newly published global KB
// shows up in the catalog but not in anyone's overview until they subscribe,
// so "globalKbs: []" is the default state for every new user, not an edge
// case. The Discover trigger must therefore be reachable from the
// globalKbs.length === 0 branch too, for every authenticated user — not just
// admins (the mocked useAuth user above has role: 'user').
describe('HomeView catalog-discovery trigger', () => {
  it('shows the trigger for a non-admin user with no global KBs and opens the catalog on click', async () => {
    const onOpenCatalog = vi.fn();
    render(<HomeView kbs={[]} {...noopProps} globalKbs={[]} onOpenCatalog={onOpenCatalog} />);

    const trigger = screen.getByRole('button', { name: translations.discoverKbs.en });
    expect(trigger).toBeInTheDocument();

    await userEvent.click(trigger);
    expect(onOpenCatalog).toHaveBeenCalledTimes(1);
  });

  it('does not gate the empty-state Discover trigger behind admin/superadmin', () => {
    // Same as above but spelled out against the admin-gated create-KB button
    // in the same branch, to pin the distinction: create stays admin-only,
    // discover does not.
    render(<HomeView kbs={[]} {...noopProps} globalKbs={[]} />);
    expect(screen.getByRole('button', { name: translations.discoverKbs.en })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: translations.createGlobalKB.en })).not.toBeInTheDocument();
  });

  it('shows exactly one trigger when the caller already has global KBs', () => {
    const globalKb: KnowledgeBase = { ...baseKb, id: 'gkb-1', isGlobal: true };
    render(<HomeView kbs={[]} {...noopProps} globalKbs={[globalKb]} />);
    expect(screen.getAllByRole('button', { name: translations.discoverKbs.en })).toHaveLength(1);
  });
});

// Three displayed states from two stored ones (visibility + memberCount).
// memberCount counts the owner too, hence <= 1 rather than === 0.
describe('KB visibility badge', () => {
  it('shows "personal" for a private KB with only the owner', async () => {
    renderHomeView({ kbs: [{ ...baseKb, id: 'kb-1', name: 'Meine KB', visibility: 'private', memberCount: 1 }] });
    expect(await screen.findByText(/persönlich|personal/i)).toBeInTheDocument();
  });

  it('shows the member count for a shared private KB', async () => {
    renderHomeView({ kbs: [{ ...baseKb, id: 'kb-1', name: 'Team-KB', visibility: 'private', memberCount: 4 }] });
    expect(await screen.findByText(/geteilt \(4\)|shared \(4\)/i)).toBeInTheDocument();
  });

  it('shows "public" regardless of member count', async () => {
    renderHomeView({ kbs: [{ ...baseKb, id: 'kb-1', name: 'Katalog-KB', visibility: 'public', memberCount: 1 }] });
    expect(await screen.findByText(/öffentlich|public/i)).toBeInTheDocument();
  });
});

// Phase 1 built the contextual remove button for members and explicitly
// deferred the subscriber case: a 404 on /membership/impact means there is
// no kb_members row at all, i.e. the caller reaches this KB through a
// subscription (or auto_subscribe), not membership. Leaving a membership
// deletes the caller's chats in that KB; unsubscribing from a public KB does
// not — access survives via rule 4 of EffectiveRole — so the dialog and the
// request must differ.
describe('remove action for a subscriber', () => {
  beforeEach(() => {
    mockedAxios.get.mockReset();
    mockedAxios.delete.mockReset();
  });

  // Kein kb_members-Eintrag (myRole undefined) auf einer oeffentlichen KB:
  // der Nutzer ist Abonnent, nicht Mitglied.
  it('unsubscribes instead of leaving, and does not warn about chats', async () => {
    mockedAxios.get.mockRejectedValueOnce({ response: { status: 404 } }); // /membership/impact
    mockedAxios.delete.mockResolvedValue({ status: 204 });

    renderHomeView({
      globalKbs: [{ ...baseKb, id: 'kb-1', name: 'Katalog-KB', visibility: 'public', isPublished: true }],
    });

    await userEvent.click(await screen.findByRole('button', { name: /entfernen|remove/i }));

    // Wait for the confirmation dialog to actually mount before asserting on
    // its content.
    await screen.findByRole('dialog');

    // Kein Chat-Verlust in dieser Variante — die Warnung darf nicht erscheinen.
    expect(screen.queryByText(/chat/i)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /bestätigen|confirm/i }));

    await waitFor(() => {
      expect(mockedAxios.delete).toHaveBeenCalledWith(
        expect.stringContaining('/api/kb/kb-1/subscription')
      );
    });
    expect(mockedAxios.delete).not.toHaveBeenCalledWith(
      expect.stringContaining('/membership')
    );
  });
});
