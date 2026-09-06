import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import axios from 'axios';
import KBOverviewDashboard from './KBOverviewDashboard';

vi.mock('axios');
const mockedAxios = axios as unknown as {
    get: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
    patch: ReturnType<typeof vi.fn>;
    post: ReturnType<typeof vi.fn>;
};

vi.mock('./contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'en' }) }));
vi.mock('./contexts/AuthContext', () => ({ useAuth: () => ({ user: { id: 'op-1', role: 'superadmin' } }) }));

// Stub Storage: the dashboard persists optional-column visibility, and jsdom
// gives a bare {} locally vs a working localStorage in CI (see the actions
// test's installMemoryStorage — same asymmetry applies here).
function installMemoryStorage() {
    const map = new Map<string, string>();
    const storage: Storage = {
        get length() { return map.size; },
        clear: () => map.clear(),
        getItem: (k) => (map.has(k) ? map.get(k)! : null),
        key: (i) => Array.from(map.keys())[i] ?? null,
        removeItem: (k) => { map.delete(k); },
        setItem: (k, v) => { map.set(k, v); },
    };
    Object.defineProperty(window, 'localStorage', { value: storage, configurable: true });
}

// Two rows: one with a healthy last sync, one whose last sync is only an
// attempt (syncSucceeded=false) — this is the case that must render the
// failing badge per W3-R10/handoff semantics, distinct from a row with no
// sync source at all (kb-3, below, in a separate fixture).
const overview = {
    rows: [
        {
            id: 'kb-1', name: 'Alpha KB', ownerName: 'Ada Lovelace', ownerId: 'user-1', ownerUsername: 'ada',
            isGlobal: false, isPublished: true, fileCount: 10, totalSizeBytes: 1024, failedFileCount: 0,
            processingFileCount: 0, webTurns: 3, apiTurns: 1, chatCount: 1, createdAt: '2026-01-01T00:00:00Z',
            oldestFileAt: '2025-01-01T00:00:00Z', staleFileCount: 4, staleShare: 0.4,
            lastSyncAt: '2026-09-01T00:00:00Z', syncSucceeded: true, syncFailing: false, syncKinds: ['rss'],
        },
        {
            id: 'kb-2', name: 'Beta KB', ownerName: 'Bob', ownerId: 'user-2', ownerUsername: 'bob',
            isGlobal: false, isPublished: true, fileCount: 5, totalSizeBytes: 512,
            failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0, chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
            oldestFileAt: '2026-08-01T00:00:00Z', staleFileCount: 0, staleShare: 0,
            lastSyncAt: '2026-09-05T00:00:00Z', syncSucceeded: false, syncFailing: true, syncKinds: ['confluence'],
        },
    ],
    queueSummary: {},
    timestamp: '2026-09-06T12:00:00Z',
    staleDays: 180,
};

async function openColumnsMenuAndEnableAll(firstRowName = 'Alpha KB') {
    render(<KBOverviewDashboard />);
    await waitFor(() => expect(screen.getByText(firstRowName)).toBeTruthy());

    fireEvent.click(screen.getByRole('button', { name: 'columnsToggle' }));
    fireEvent.click(screen.getByLabelText('colOldestContent'));
    fireEvent.click(screen.getByLabelText('colStaleShare'));
    fireEvent.click(screen.getByLabelText('colLastSync'));
}

describe('KBOverviewDashboard freshness columns', () => {
    beforeEach(() => {
        installMemoryStorage();
        mockedAxios.get = vi.fn().mockResolvedValue({ data: overview });
        mockedAxios.delete = vi.fn().mockResolvedValue({});
        mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
        mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
    });

    it('renders the three optional freshness columns once toggled on', async () => {
        await openColumnsMenuAndEnableAll();

        expect(screen.getByRole('columnheader', { name: /colOldestContent/ })).toBeInTheDocument();
        expect(screen.getByRole('columnheader', { name: /colStaleShare/ })).toBeInTheDocument();
        expect(screen.getByRole('columnheader', { name: /colLastSync/ })).toBeInTheDocument();
    });

    it('renders staleShare as a percent with the threshold in the tooltip', async () => {
        await openColumnsMenuAndEnableAll();

        const cell = await screen.findByText('40%');
        expect(cell.closest('td')).toHaveAttribute('title', '4/10 > 180d');

        const zeroCell = screen.getByText('0%');
        expect(zeroCell.closest('td')).toHaveAttribute('title', '0/5 > 180d');
    });

    it('renders the failing badge for a row whose last sync is only an attempt (syncSucceeded=false)', async () => {
        await openColumnsMenuAndEnableAll();

        // kb-1 succeeded: no badge.
        const rows = screen.getAllByRole('row').slice(1); // drop the header row
        const alphaRow = rows.find((r) => r.textContent?.includes('Alpha KB'))!;
        const betaRow = rows.find((r) => r.textContent?.includes('Beta KB'))!;

        expect(alphaRow.querySelector('[data-testid="kb-sync-failing-badge"]')).toBeNull();
        expect(betaRow.querySelector('[data-testid="kb-sync-failing-badge"]')).not.toBeNull();
    });

    it('surfaces syncKinds in the last-sync tooltip', async () => {
        await openColumnsMenuAndEnableAll();

        // Which source kinds a KB actually syncs is the missing half of
        // "last sync 5 days ago" — without it an operator cannot tell whether
        // that number describes an RSS feed, a Confluence space or a repo.
        const alphaRow = screen.getAllByRole('row').find((r) => r.textContent?.includes('Alpha KB'))!;
        const cells = Array.from(alphaRow.querySelectorAll('td'));
        const syncCell = cells.find((c) => c.getAttribute('title')?.includes('2026-09-01'));
        expect(syncCell).toBeTruthy();
        expect(syncCell!.getAttribute('title')).toContain('rss');
    });

    it('renders oldestFileAt as a relative time', async () => {
        await openColumnsMenuAndEnableAll();

        // 2025-01-01 relative to the fixed system time 2026-09-06 is far enough
        // in the past that formatRelative falls into its day-unit branch.
        const cells = screen.getAllByText(/days ago/);
        expect(cells.length).toBeGreaterThan(0);
    });

    it('degrades to the placeholder when a row has no sync source at all', async () => {
        mockedAxios.get = vi.fn().mockResolvedValue({
            data: {
                rows: [{
                    id: 'kb-3', name: 'Gamma KB', isGlobal: false, isPublished: true, fileCount: 1,
                    totalSizeBytes: 100, failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0,
                    chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
                }],
                queueSummary: {},
                timestamp: '2026-09-06T12:00:00Z',
                staleDays: 180,
            },
        });

        await openColumnsMenuAndEnableAll('Gamma KB');

        const row = screen.getByRole('row', { name: /Gamma KB/ });
        expect(row.querySelector('[data-testid="kb-sync-failing-badge"]')).toBeNull();
        expect(row.textContent).toContain('—');
    });
});

// Per-kind sync status (Wave-4 Task 7 / W4-R9): a KB whose RSS feed has
// succeeded but whose git source has NEVER succeeded must show the git
// status — not the healthy RSS one — so a single good source can no longer
// mask a source that has never synced at all.
const perKindOverview = {
    rows: [
        {
            id: 'kb-4', name: 'Delta KB', ownerName: 'Dana', ownerId: 'user-4', ownerUsername: 'dana',
            isGlobal: false, isPublished: true, fileCount: 3, totalSizeBytes: 300,
            failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0, chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
            lastSyncAt: '2026-09-01T00:00:00Z', syncSucceeded: false, syncFailing: false, syncKinds: ['rss', 'git'],
            syncByKind: [
                { kind: 'rss', lastSyncAt: '2026-09-01T00:00:00Z', syncSucceeded: true, syncFailing: false, sourceCount: 1 },
                { kind: 'git', lastSyncAt: '2026-09-06T02:00:00Z', syncSucceeded: false, syncFailing: false, sourceCount: 1 },
            ],
        },
    ],
    queueSummary: {},
    timestamp: '2026-09-06T12:00:00Z',
    staleDays: 180,
};

describe('KBOverviewDashboard per-kind sync status (Wave-4 Task 7)', () => {
    beforeEach(() => {
        installMemoryStorage();
        mockedAxios.get = vi.fn().mockResolvedValue({ data: perKindOverview });
        mockedAxios.delete = vi.fn().mockResolvedValue({});
        mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
        mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
    });

    it('shows the worst kind (never-succeeded beats a healthy kind) in the cell', async () => {
        await openColumnsMenuAndEnableAll('Delta KB');

        const row = screen.getByRole('row', { name: /Delta KB/ });
        // rss succeeded; git never has. The cell must surface git — the
        // worse of the two — not the healthy rss kind.
        expect(row.textContent).toContain('syncKindLabel_git');
        expect(row.textContent).not.toContain('syncKindLabel_rss');

        const badge = row.querySelector('[data-testid="kb-sync-failing-badge"]');
        expect(badge).not.toBeNull();
        // The never-succeeded badge is distinct from the generic "currently
        // failing" one — an operator must be able to tell "has not run yet /
        // has never worked" apart from "was fine, is failing now".
        expect(badge?.getAttribute('title')).toBe('syncNeverSucceeded');
    });

    it('lists every kind with its own last-sync time in the tooltip', async () => {
        await openColumnsMenuAndEnableAll('Delta KB');

        const row = screen.getByRole('row', { name: /Delta KB/ });
        const cells = Array.from(row.querySelectorAll('td'));
        const syncCell = cells.find((c) => c.getAttribute('title')?.includes('syncKindLabel_rss'));
        expect(syncCell).toBeTruthy();

        const title = syncCell!.getAttribute('title')!;
        expect(title).toContain('2026-09-01');
        expect(title).toContain('syncKindLabel_git');
        expect(title).toContain('2026-09-06');
        expect(title).toContain('syncNeverSucceeded');
    });
});
