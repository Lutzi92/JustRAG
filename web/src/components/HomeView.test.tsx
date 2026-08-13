import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import type { KnowledgeBase } from '../types';
import { HomeView, type HomeViewProps } from './HomeView';
import { translations } from '../translations';
import { ModalProvider } from '../contexts/ModalContext';
import { ToastProvider } from '../contexts/ToastContext';
import { useKbRemoval } from '../hooks/useKbRemoval';

vi.mock('axios');
const mockedAxios = vi.mocked(axios, true);

// Modal (behind ModalProvider) calls useReducedMotion, which reads
// window.matchMedia — jsdom doesn't implement it, so the real ModalProvider
// every render mounts needs it stubbed (see KbSettingsPanel.test.tsx for the
// same pattern).

// A real in-memory Storage, installed fresh per test.
//
// KbAccordion persists each section's open state in localStorage, so without
// this a test that expands a section decides the starting state of every test
// after it. That is not hypothetical: it is exactly how this file broke in CI
// while passing locally — jsdom's localStorage differs between the two
// environments (locally it is a bare object with no getItem/setItem, so the
// accordion's try/catch silently fell back to defaultOpen every time and hid
// the leak). Owning the implementation here removes that difference: the
// persistence path is genuinely exercised on both machines, and each test
// starts from the defaults.
function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() { return map.size; },
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => { map.set(k, String(v)); },
    removeItem: (k: string) => { map.delete(k); },
    clear: () => { map.clear(); },
  } as Storage;
}

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
  vi.stubGlobal('localStorage', memoryStorage());
  // The discovery panel fetches on mount; a bare auto-mocked axios.get
  // returns undefined and its .then() would throw. Individual describes
  // override this.
  mockedAxios.get.mockResolvedValue({ data: [] });
});

// expandSection clicks an accordion header by its title. The accessible name
// also carries the item count and the sr-only expand/collapse hint, hence the
// substring match.
async function expandSection(title: string) {
  await userEvent.click(screen.getByRole('button', { name: new RegExp(title, 'i') }));
}

// Every render goes through the providers HomeView's subtree actually needs in
// production. ToastProvider is not optional even for tests that never mean to
// touch the discovery panel: whether that panel mounts depends on persisted
// accordion state, so a bare render would fail or pass depending on what ran
// before it.
function renderView(ui: React.ReactElement) {
  return render(<ToastProvider><ModalProvider>{ui}</ModalProvider></ToastProvider>);
}

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
  onSubscriptionChange: vi.fn(),
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

// Same providers as renderView, but with onDeleteKB wired to the real
// useKbRemoval hook: badge tests and real-removal-flow tests (subscriber
// unsubscribe) both go through it, and showConfirm's dialog actually renders
// because ModalProvider is real rather than mocked.
function renderHomeView(overrides: Partial<HomeViewProps> & { kbs?: KnowledgeBase[]; globalKbs?: KnowledgeBase[] } = {}) {
  const { kbs = [], globalKbs = [], ...rest } = overrides;
  return renderView(<RealRemovalHomeView kbs={kbs} globalKbs={globalKbs} {...rest} />);
}

// The members-dialog trigger used to be gated on kb.userId === user.id
// (owner-only), which made the whole `admin` tier unreachable from the UI —
// a KB admin who isn't the owner could never open the dialog to manage
// members or hand out roles. It must key on myRole instead.
describe('HomeView members-dialog trigger', () => {
  it('shows the trigger for a caller whose myRole is admin', async () => {
    const kb: KnowledgeBase = { ...baseKb, myRole: 'admin' };
    renderView(<HomeView kbs={[kb]} {...noopProps} />);
    await expandSection(translations.homeSharedWithMe.en);
    expect(screen.getByRole('button', { name: translations.share.en })).toBeInTheDocument();
  });

  it('shows the trigger for a caller whose myRole is owner', () => {
    const kb: KnowledgeBase = { ...baseKb, myRole: 'owner' };
    renderView(<HomeView kbs={[kb]} {...noopProps} />);
    expect(screen.getByRole('button', { name: translations.share.en })).toBeInTheDocument();
  });

  it('hides the trigger for a caller whose myRole is edit', async () => {
    const kb: KnowledgeBase = { ...baseKb, myRole: 'edit' };
    renderView(<HomeView kbs={[kb]} {...noopProps} />);
    await expandSection(translations.homeSharedWithMe.en);
    expect(screen.queryByRole('button', { name: translations.share.en })).not.toBeInTheDocument();
  });

  it('hides the trigger for a caller whose myRole is view', async () => {
    const kb: KnowledgeBase = { ...baseKb, myRole: 'view' };
    renderView(<HomeView kbs={[kb]} {...noopProps} />);
    await expandSection(translations.homeSharedWithMe.en);
    expect(screen.queryByRole('button', { name: translations.share.en })).not.toBeInTheDocument();
  });
});

// The overview splits GET /api/kb into "mine" and "shared with me" on myRole
// alone — the backend returns one list, and both sections render the same
// card, so the partition is the only thing keeping a KB out of the wrong
// section.
describe('HomeView owned/shared split', () => {
  const owned: KnowledgeBase = { ...baseKb, id: 'kb-own', name: 'Owned KB', myRole: 'owner' };
  const shared: KnowledgeBase = { ...baseKb, id: 'kb-shared', name: 'Shared KB', myRole: 'edit' };

  it('puts an owned KB in the always-open "my KBs" section and a shared one behind the collapsed section', async () => {
    renderView(<HomeView kbs={[owned, shared]} {...noopProps} />);

    // "Meine KBs" is open by default, "Mit mir geteilt" is not.
    expect(screen.getByText('Owned KB')).toBeInTheDocument();
    expect(screen.queryByText('Shared KB')).not.toBeInTheDocument();

    await expandSection(translations.homeSharedWithMe.en);
    expect(screen.getByText('Shared KB')).toBeInTheDocument();
  });

  // Also the guard on the storage stub above: if localStorage were inert (as
  // it is in some jsdom builds), the second render would fall back to
  // defaultOpen and this would fail — which is what stops the rest of the
  // file from silently not exercising persistence at all.
  it('remembers a section it was told to expand across a remount', async () => {
    const { unmount } = renderView(<HomeView kbs={[owned, shared]} {...noopProps} />);
    await expandSection(translations.homeSharedWithMe.en);
    expect(screen.getByText('Shared KB')).toBeInTheDocument();
    unmount();

    renderView(<HomeView kbs={[owned, shared]} {...noopProps} />);
    expect(screen.getByText('Shared KB')).toBeInTheDocument();
  });

  it('reports the per-section counts on the headers', () => {
    renderView(<HomeView kbs={[owned, shared]} {...noopProps} />);
    expect(screen.getByRole('button', { name: new RegExp(`${translations.myKBs.en}\\s*1`, 'i') })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: new RegExp(`${translations.homeSharedWithMe.en}\\s*1`, 'i') })).toBeInTheDocument();
  });
});

// auto_subscribe defaults to false (Phase 2): a newly published global KB
// shows up in the catalog but not in anyone's overview until they subscribe,
// so "globalKbs: []" is the default state for every new user, not an edge
// case. The Discover trigger must therefore be reachable from the
// globalKbs.length === 0 branch too, for every authenticated user — not just
// admins (the mocked useAuth user above has role: 'user').
describe('HomeView discovery accordion', () => {
  // The only test in this file that actually mounts KbCatalogPanel, which
  // calls useToast — hence the provider here rather than around every render.
  it('offers discovery to a non-admin with no favorites, and mounts the catalog only once expanded', async () => {
    renderView(<HomeView kbs={[]} {...noopProps} globalKbs={[]} />);

    // Collapsed: the panel is unmounted, so it has not fetched anything yet.
    expect(mockedAxios.get).not.toHaveBeenCalledWith(expect.stringContaining('/api/kb/catalog'));

    await expandSection(translations.discoverKbs.en);

    expect(await screen.findByLabelText(translations.catalogSearchPlaceholder.en)).toBeInTheDocument();
    await waitFor(() => {
      expect(mockedAxios.get).toHaveBeenCalledWith(expect.stringContaining('/api/kb/catalog'));
    });
  });

  it('does not gate discovery behind admin/superadmin', () => {
    // Spelled out against the admin-gated create-KB button in the same
    // section, to pin the distinction: create stays admin-only, discover
    // does not (the mocked useAuth user has role: 'user').
    renderView(<HomeView kbs={[]} {...noopProps} globalKbs={[]} />);
    expect(screen.getByRole('button', { name: new RegExp(translations.discoverKbs.en, 'i') })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: translations.createGlobalKB.en })).not.toBeInTheDocument();
  });

  it('shows exactly one discovery header when the caller already has favorites', () => {
    const globalKb: KnowledgeBase = { ...baseKb, id: 'gkb-1', isGlobal: true };
    renderView(<HomeView kbs={[]} {...noopProps} globalKbs={[globalKb]} />);
    expect(screen.getAllByRole('button', { name: new RegExp(translations.discoverKbs.en, 'i') })).toHaveLength(1);
  });
});

// Three displayed states from two stored ones (visibility + memberCount).
// memberCount counts the owner too, hence <= 1 rather than === 0.
//
// The fixtures have to respect where each state can actually come from, or the
// test proves nothing about production. GET /api/kb hard-filters
// `WHERE kb.visibility = 'private'`, so the `kbs` prop can never hold a public
// KB; public ones arrive through GET /api/kb/global, i.e. the `globalKbs` prop.
// The earlier version of this file passed `kbs: [{ visibility: 'public' }]` —
// a shape the backend cannot produce — and so kept a dead badge branch green.
describe('KB visibility badge', () => {
  it('shows "personal" for a private KB with only the owner', async () => {
    renderHomeView({ kbs: [{ ...baseKb, id: 'kb-1', name: 'Meine KB', visibility: 'private', memberCount: 1, myRole: 'owner' }] });
    expect(await screen.findByText(/persönlich|personal/i)).toBeInTheDocument();
  });

  it('shows the member count for a shared private KB', async () => {
    renderHomeView({ kbs: [{ ...baseKb, id: 'kb-1', name: 'Team-KB', visibility: 'private', memberCount: 4, myRole: 'owner' }] });
    expect(await screen.findByText(/geteilt \(4\)|shared \(4\)/i)).toBeInTheDocument();
  });

  it('shows "public" on a public KB card, regardless of member count', async () => {
    renderHomeView({
      globalKbs: [{ ...baseKb, id: 'gkb-1', name: 'Katalog-KB', visibility: 'public', isPublished: true, memberCount: 1 }],
    });
    expect(await screen.findByText(/öffentlich|public/i)).toBeInTheDocument();
  });

  // Vor dem Fix trug die oeffentliche Karte einen eigenen statischen
  // "Global"-Chip, waehrend der 'public'-Zweig des Badges unerreichbar war.
  it('renders the shared badge, not the old static global chip, on a public KB card', async () => {
    renderHomeView({
      globalKbs: [{ ...baseKb, id: 'gkb-1', name: 'Katalog-KB', visibility: 'public', isPublished: true }],
    });
    expect(await screen.findByText(translations.visibilityPublic.en)).toBeInTheDocument();
    expect(screen.queryByText(translations.globalBadge.en)).not.toBeInTheDocument();
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
