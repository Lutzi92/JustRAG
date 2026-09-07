import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react';
import axios from 'axios';
import AdminEvalTab from './AdminEvalTab';
import { translations } from '../../translations';

vi.mock('axios');
const mockedAxios = vi.mocked(axios, true);

// fetchKbAgents uses authFetch (native fetch), not axios — mock the module
// directly so the "last team run" hint next to the selector can be tested
// without wiring a fetch polyfill.
vi.mock('../agents/api', () => ({
    fetchKbAgents: vi.fn(),
}));

// Real translated text (English) so labels/headers actually contain the
// words the tests match on, mirroring AdminGlobalKbsTab.test.tsx.
const tMock = (key: string) => {
    const entry = translations[key as keyof typeof translations];
    return entry ? entry.en : key;
};
const themeMock = { t: tMock };
const toastMock = { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() };

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => themeMock }));
vi.mock('../../contexts/ToastContext', () => ({ useToast: () => toastMock }));
vi.mock('../../hooks/useReducedMotion', () => ({ useReducedMotion: () => true, getMotionProps: () => ({}) }));

const defaultProps = {};

describe('AdminEvalTab', () => {
    beforeEach(() => {
        vi.resetAllMocks();
        mockedAxios.get.mockImplementation((url: string) => {
            if (url.endsWith('/golden-sets')) {
                return Promise.resolve({
                    data: {
                        golden_sets: [
                            {
                                id: 'g1',
                                name: 'Set',
                                content_hash: 'abc123def456',
                                question_count: 3,
                                created_at: new Date().toISOString(),
                                schedule: 'manual',
                            },
                        ],
                    },
                });
            }
            return Promise.resolve({ data: { runs: [], total: 0, jobs: [], agents: [] } });
        });
    });

    it('PATCHes the golden-set schedule when the dropdown changes', async () => {
        mockedAxios.patch.mockResolvedValue({ data: {} });
        render(<AdminEvalTab {...defaultProps} />);

        const select = await screen.findByLabelText(/Zeitplan|Schedule/i);

        // Count the initial-mount fetch(es) of the golden-sets list so the
        // post-PATCH refetch assertion below is robust to how many times
        // the component fetches on mount.
        const goldenSetsGetCalls = () =>
            mockedAxios.get.mock.calls.filter(([url]) => typeof url === 'string' && url.endsWith('/golden-sets')).length;
        const callsBeforePatch = goldenSetsGetCalls();

        fireEvent.change(select, { target: { value: 'daily' } });

        await waitFor(() =>
            expect(mockedAxios.patch).toHaveBeenCalledWith(
                expect.stringMatching(/\/golden-sets\/g1$/),
                { schedule: 'daily' }
            )
        );

        // handleScheduleChange refetches the list after a successful PATCH
        // so the row's schedule/next_run_at reflect the server state.
        await waitFor(() => expect(goldenSetsGetCalls()).toBe(callsBeforePatch + 1));
    });

    // -----------------------------------------------------------------
    // W7-R4: Team + Score columns on the run table; server-side sort.
    // -----------------------------------------------------------------

    it('renders the team name and formatted score, and refetches with sort/order params on Score header clicks', async () => {
        const runOne = {
            id: 'r1',
            label: 'Run One',
            status: 'completed',
            created_at: '2026-09-01T10:00:00Z',
            started_at: '2026-09-01T10:00:00Z',
            finished_at: '2026-09-01T10:01:00Z',
            kb_id: 'kb-1',
            kb_name: 'Test KB',
            judge_enabled: false,
            aggregate: { count: 10, mean_recall: 0.5, mrr: 0.3 },
            team_id: 'team-1',
            team_name: 'Recherche-Team',
        };
        const runTwo = {
            id: 'r2',
            label: 'Run Two',
            status: 'completed',
            created_at: '2026-09-02T10:00:00Z',
            started_at: '2026-09-02T10:00:00Z',
            finished_at: '2026-09-02T10:01:00Z',
            kb_id: 'kb-1',
            kb_name: 'Test KB',
            judge_enabled: false,
            aggregate: { count: 5, mean_recall: 0.9, mrr: 0.8 },
        };

        // The runs list is server-sorted; the fake backend always returns
        // the same order regardless of query params — this test asserts
        // what the FE *sends*, not that it reorders the response itself
        // (that responsibility moved to the server, W7-R4).
        mockedAxios.get.mockImplementation((url: string) => {
            if (url.endsWith('/golden-sets')) {
                return Promise.resolve({ data: { golden_sets: [] } });
            }
            if (url.includes('/golden-sets/jobs')) {
                return Promise.resolve({ data: { jobs: [] } });
            }
            if (url.includes('/runs')) {
                return Promise.resolve({ data: { runs: [runTwo, runOne], total: 2 } });
            }
            return Promise.resolve({ data: {} });
        });

        render(<AdminEvalTab />);

        // Team name for the team run, and the fallback dash-free rendering
        // for the standard run, both surface.
        await screen.findByText('Recherche-Team');

        // Formatted scores: mean_recall/mrr as percentages with one decimal.
        expect(screen.getByText('50.0 / 30.0')).toBeInTheDocument();
        expect(screen.getByText('90.0 / 80.0')).toBeInTheDocument();

        const runsUrls = () =>
            mockedAxios.get.mock.calls
                .map(([url]) => url as string)
                .filter(url => typeof url === 'string' && url.includes('/runs'));

        // Initial fetch carries no sort/order — the backend's own
        // created_at DESC default applies.
        await waitFor(() => expect(runsUrls().length).toBeGreaterThanOrEqual(1));
        expect(runsUrls()[0]).not.toMatch(/[?&]sort=/);
        expect(runsUrls()[0]).not.toMatch(/[?&]order=/);

        // Re-query the header fresh before each click: a fetch flips
        // listLoading true->false, which unmounts and remounts the whole
        // <table> (including the header cell), so a DOM reference cached
        // across clicks goes stale after the first one resolves. The
        // header's accessible name also grows a sort-direction arrow after
        // the first click, so match by regex rather than the exact string.
        const getScoreHeader = () => screen.getByRole('columnheader', { name: /Score/ });

        // First click: desc.
        fireEvent.click(getScoreHeader());
        await waitFor(() => expect(runsUrls().length).toBeGreaterThanOrEqual(2));
        expect(runsUrls().at(-1)).toMatch(/[?&]sort=recall&order=desc(&|$)/);

        // Second click: asc.
        fireEvent.click(getScoreHeader());
        await waitFor(() => expect(runsUrls().length).toBeGreaterThanOrEqual(3));
        expect(runsUrls().at(-1)).toMatch(/[?&]sort=recall&order=asc(&|$)/);

        // Third click: back to 'none' — no sort/order param at all.
        fireEvent.click(getScoreHeader());
        await waitFor(() => expect(runsUrls().length).toBeGreaterThanOrEqual(4));
        const lastUrl = runsUrls().at(-1) as string;
        expect(lastUrl).not.toMatch(/[?&]sort=/);
        expect(lastUrl).not.toMatch(/[?&]order=/);
    });

    it('shows the last team run recall/MRR next to the team selector', async () => {
        const { fetchKbAgents } = await import('../agents/api');
        vi.mocked(fetchKbAgents).mockResolvedValue({
            agents: [],
            teams: [{ id: 'team-1', name: 'Recherche-Team', description: '', icon: '', isDefault: false }],
        });

        const teamRun = {
            id: 'r1',
            label: 'Team run',
            status: 'completed',
            created_at: '2026-09-01T10:00:00Z',
            started_at: '2026-09-01T10:00:00Z',
            finished_at: '2026-09-01T10:01:00Z',
            kb_id: 'kb-1',
            kb_name: 'Test KB',
            judge_enabled: false,
            aggregate: { count: 8, mean_recall: 0.6, mrr: 0.45 },
            team_id: 'team-1',
            team_name: 'Recherche-Team',
        };

        mockedAxios.get.mockImplementation((url: string) => {
            if (url.endsWith('/golden-sets')) {
                return Promise.resolve({ data: { golden_sets: [] } });
            }
            if (url.includes('/golden-sets/jobs')) {
                return Promise.resolve({ data: { jobs: [] } });
            }
            if (url.includes('/runs')) {
                return Promise.resolve({ data: { runs: [teamRun], total: 1 } });
            }
            return Promise.resolve({ data: {} });
        });

        render(<AdminEvalTab kbId="kb-1" />);

        const select = await screen.findByLabelText(/Agent team/i);
        await waitFor(() => {
            const option = within(select).getByText(/Recherche-Team/);
            expect(option.textContent).toContain('last run');
            expect(option.textContent).toContain('60.0');
            expect(option.textContent).toContain('45.0');
        });
    });
});
