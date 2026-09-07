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
    // W6-R9: Team + Score columns on the run table
    // -----------------------------------------------------------------

    it('renders the team name and formatted score, and reorders rows on Score header click', async () => {
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

        mockedAxios.get.mockImplementation((url: string) => {
            if (url.endsWith('/golden-sets')) {
                return Promise.resolve({ data: { golden_sets: [] } });
            }
            if (url.includes('/golden-sets/jobs')) {
                return Promise.resolve({ data: { jobs: [] } });
            }
            if (url.includes('/runs')) {
                return Promise.resolve({ data: { runs: [runOne, runTwo], total: 2 } });
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

        const rowLabelOrder = () =>
            screen.getAllByRole('row')
                .map(r => r.textContent || '')
                .filter(text => text.includes('Run One') || text.includes('Run Two'))
                .map(text => (text.includes('Run One') ? 'Run One' : 'Run Two'));

        // Fetch order (server order): Run One, then Run Two.
        expect(rowLabelOrder()).toEqual(['Run One', 'Run Two']);

        const scoreHeader = screen.getByText('Score');

        // First click: desc by mean_recall -> Run Two (0.9) before Run One (0.5).
        fireEvent.click(scoreHeader);
        expect(rowLabelOrder()).toEqual(['Run Two', 'Run One']);

        // Second click: asc -> Run One (0.5) before Run Two (0.9).
        fireEvent.click(scoreHeader);
        expect(rowLabelOrder()).toEqual(['Run One', 'Run Two']);

        // Third click: back to original fetch order.
        fireEvent.click(scoreHeader);
        expect(rowLabelOrder()).toEqual(['Run One', 'Run Two']);
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
