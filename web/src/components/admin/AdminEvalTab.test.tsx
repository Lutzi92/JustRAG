import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import axios from 'axios';
import AdminEvalTab from './AdminEvalTab';
import { translations } from '../../translations';

vi.mock('axios');
const mockedAxios = vi.mocked(axios, true);

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
        fireEvent.change(select, { target: { value: 'daily' } });

        await waitFor(() =>
            expect(mockedAxios.patch).toHaveBeenCalledWith(
                expect.stringMatching(/\/golden-sets\/g1$/),
                { schedule: 'daily' }
            )
        );
    });
});
