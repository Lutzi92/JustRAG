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

// RAGAS 24h sample stats (Wave-5 Task 2): kb-1 has a sample block, kb-2 has
// none — the FE must render the formatted "n · F/AR/CP" string for the
// former and a plain dash for the latter, never a zeroed block.
const ragasOverview = {
    rows: [
        {
            id: 'kb-1', name: 'Alpha KB', isGlobal: false, isPublished: true, fileCount: 10, totalSizeBytes: 1024,
            failedFileCount: 0, processingFileCount: 0, webTurns: 3, apiTurns: 1, chatCount: 1, createdAt: '2026-01-01T00:00:00Z',
            ragas: { n24h: 5, faithfulness: 0.61, answerRelevance: 0.98, contextPrecision: 0.47 },
        },
        {
            id: 'kb-2', name: 'Beta KB', isGlobal: false, isPublished: true, fileCount: 5, totalSizeBytes: 512,
            failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0, chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
        },
    ],
    queueSummary: {},
    timestamp: '2026-09-06T12:00:00Z',
    staleDays: 180,
};

describe('KBOverviewDashboard RAGAS column (Wave-5 Task 2)', () => {
    beforeEach(() => {
        installMemoryStorage();
        mockedAxios.get = vi.fn().mockResolvedValue({ data: ragasOverview });
        mockedAxios.delete = vi.fn().mockResolvedValue({});
        mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
        mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
    });

    it('renders "n · F/AR/CP" for a KB with samples, and a dash for one without', async () => {
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getByText('Alpha KB')).toBeTruthy());

        fireEvent.click(screen.getByRole('button', { name: 'columnsToggle' }));
        fireEvent.click(screen.getByLabelText('colRagas'));

        expect(screen.getByRole('columnheader', { name: /colRagas/ })).toBeInTheDocument();
        expect(screen.getByText('5 · F 0.61 / AR 0.98 / CP 0.47')).toBeInTheDocument();

        // Both rows' other columns can legitimately render an em dash
        // (missing owner, no activity yet), so assert the RAGAS CELL
        // specifically — found by its own tooltip, not the row's full text.
        const rows = screen.getAllByRole('row').slice(1); // drop the header row
        const betaRow = rows.find((r) => r.textContent?.includes('Beta KB'))!;
        const betaRagasCell = Array.from(betaRow.querySelectorAll('td'))
            .find((td) => td.getAttribute('title') === 'colRagasTooltip')!;
        expect(betaRagasCell.textContent).toBe('—');
    });

    it('carries the three metric names in the column tooltip', async () => {
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getByText('Alpha KB')).toBeTruthy());

        fireEvent.click(screen.getByRole('button', { name: 'columnsToggle' }));
        fireEvent.click(screen.getByLabelText('colRagas'));

        const cell = screen.getByText('5 · F 0.61 / AR 0.98 / CP 0.47');
        expect(cell.closest('td')).toHaveAttribute('title', 'colRagasTooltip');
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

// Fix round 1: the "Last sync" column must sort by the same worst-kind
// severity the cell displays, not the aggregate MAX(success) timestamp —
// otherwise a KB with a healthy RSS feed and a never-succeeded git source
// shows the alarming badge but still sorts as if it were freshly synced.
// All three rows below share nearby timestamps precisely so a
// timestamp-only sort would NOT reproduce the expected order — only a
// severity-first sort does.
const urgencyOverview = {
    rows: [
        {
            id: 'kb-x1', name: 'KB One', isGlobal: false, isPublished: false, fileCount: 1, totalSizeBytes: 10,
            failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0, chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
            // Healthy (ok tier) — but has the OLDEST timestamp of the three,
            // so a timestamp-only sort would (wrongly) put it in the middle
            // when ascending, not last.
            lastSyncAt: '2026-08-01T00:00:00Z', syncSucceeded: true, syncFailing: false, syncKinds: ['confluence'],
            syncByKind: [{ kind: 'confluence', lastSyncAt: '2026-08-01T00:00:00Z', syncSucceeded: true, syncFailing: false, sourceCount: 1 }],
        },
        {
            id: 'kb-x2', name: 'KB Two', isGlobal: false, isPublished: false, fileCount: 1, totalSizeBytes: 10,
            failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0, chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
            // Currently failing (has succeeded before) — middle timestamp.
            lastSyncAt: '2026-09-01T00:00:00Z', syncSucceeded: true, syncFailing: true, syncKinds: ['rss'],
            syncByKind: [{ kind: 'rss', lastSyncAt: '2026-09-01T00:00:00Z', syncSucceeded: true, syncFailing: true, sourceCount: 1 }],
        },
        {
            id: 'kb-x3', name: 'KB Three', isGlobal: false, isPublished: false, fileCount: 1, totalSizeBytes: 10,
            failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0, chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
            // Never succeeded — but has the NEWEST timestamp (an attempt
            // fallback) of the three, so an aggregate-timestamp sort would
            // (wrongly) treat it as the most recently synced, not the most
            // urgent.
            lastSyncAt: '2026-09-06T02:00:00Z', syncSucceeded: false, syncFailing: false, syncKinds: ['git'],
            syncByKind: [{ kind: 'git', lastSyncAt: '2026-09-06T02:00:00Z', syncSucceeded: false, syncFailing: false, sourceCount: 1 }],
        },
    ],
    queueSummary: {},
    timestamp: '2026-09-06T12:00:00Z',
    staleDays: 180,
};

describe('KBOverviewDashboard last-sync sort (Wave-4 Task 7 fix round 1)', () => {
    beforeEach(() => {
        installMemoryStorage();
        mockedAxios.get = vi.fn().mockResolvedValue({ data: urgencyOverview });
        mockedAxios.delete = vi.fn().mockResolvedValue({});
        mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
        mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
    });

    it('sorts by worst-kind severity, not the aggregate lastSyncAt', async () => {
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getByText('KB One')).toBeTruthy());

        fireEvent.click(screen.getByRole('button', { name: 'columnsToggle' }));
        fireEvent.click(screen.getByLabelText('colLastSync'));

        // First click sorts ascending, which — by the ascending-rank /
        // descending-urgency convention compareSyncUrgency documents —
        // puts the worst kind first: never-succeeded, then failing, then ok.
        fireEvent.click(screen.getByRole('columnheader', { name: /colLastSync/ }));
        const namesAsc = screen.getAllByRole('row').slice(1).map((r) => r.querySelector('td')?.textContent);
        expect(namesAsc).toEqual(['KB Three', 'KB Two', 'KB One']);

        // A second click flips the direction: healthy first, never-succeeded last.
        fireEvent.click(screen.getByRole('columnheader', { name: /colLastSync/ }));
        const namesDesc = screen.getAllByRole('row').slice(1).map((r) => r.querySelector('td')?.textContent);
        expect(namesDesc).toEqual(['KB One', 'KB Two', 'KB Three']);
    });
});

// Fix wave, finding F5: an unknown sync kind has no syncKindLabel_* entry,
// and t() returns the key itself for a missing translation — so the cell and
// the tooltip used to read "syncKindLabel_svn" at the operator.
//
// Mutation: change syncKindLabel back to t(`syncKindLabel_${kind}`) → the
// raw key is rendered and both assertions below fail.
describe('KBOverviewDashboard unknown sync kind (F5)', () => {
    const unknownKindOverview = {
        rows: [
            {
                id: 'kb-9', name: 'Epsilon KB', ownerName: 'Eve', ownerId: 'user-9', ownerUsername: 'eve',
                isGlobal: false, isPublished: true, fileCount: 1, totalSizeBytes: 100,
                failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0, chatCount: 0,
                createdAt: '2026-01-01T00:00:00Z',
                lastSyncAt: '2026-09-01T00:00:00Z', syncSucceeded: false, syncFailing: false, syncKinds: ['svn'],
                syncByKind: [
                    { kind: 'svn', lastSyncAt: '2026-09-01T00:00:00Z', syncSucceeded: false, syncFailing: false, sourceCount: 1 },
                ],
            },
        ],
        queueSummary: {},
        timestamp: '2026-09-06T12:00:00Z',
        staleDays: 180,
    };

    beforeEach(() => {
        installMemoryStorage();
        mockedAxios.get = vi.fn().mockResolvedValue({ data: unknownKindOverview });
        mockedAxios.delete = vi.fn().mockResolvedValue({});
        mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
        mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
    });

    it('falls back to the raw kind in the cell and the tooltip', async () => {
        await openColumnsMenuAndEnableAll('Epsilon KB');

        const row = screen.getByRole('row', { name: /Epsilon KB/ });
        expect(row.textContent).not.toContain('syncKindLabel_svn');
        expect(row.textContent).toContain('svn');

        const cells = Array.from(row.querySelectorAll('td'));
        const syncCell = cells.find((c) => c.getAttribute('title')?.includes('svn'));
        expect(syncCell).toBeTruthy();
        expect(syncCell!.getAttribute('title')).not.toContain('syncKindLabel_svn');
    });

    it('still translates a known kind', async () => {
        // The known-kind path must keep going through t(): the identity-t stub
        // in this file renders the key, which is how the tests above assert it.
        mockedAxios.get = vi.fn().mockResolvedValue({ data: perKindOverview });
        await openColumnsMenuAndEnableAll('Delta KB');

        const row = screen.getByRole('row', { name: /Delta KB/ });
        expect(row.textContent).toContain('syncKindLabel_git');
    });
});
