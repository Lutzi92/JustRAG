import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import KbCatalogPanel from './KbCatalogPanel';

vi.mock('axios');
const mockedAxios = vi.mocked(axios, true);

// framer-motion is mocked globally in src/test/setup.ts — no per-file override.

// Real context hooks return referentially stable values across renders (see
// MembersModal.test.tsx for the full rationale): hoisted, module-level
// singleton objects, never a fresh object literal per call, so effects keyed
// on these values don't re-fire on every render. `t` just echoes the key
// back, so tests select by data-testid/role rather than translated text.
const tMock = (k: string) => k;
const themeMock = { t: tMock };
const toastMock = { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() };
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => themeMock }));
vi.mock('../contexts/ToastContext', () => ({ useToast: () => toastMock }));

function renderPanel(onSubscriptionChange = vi.fn()) {
    return render(<KbCatalogPanel onSubscriptionChange={onSubscriptionChange} />);
}

describe('KbCatalogPanel', () => {
    beforeEach(() => {
        vi.resetAllMocks();
        mockedAxios.get.mockImplementation((url: string) => {
            if (url.includes('/api/admin/kb-categories')) {
                return Promise.resolve({ data: [{ id: 'c1', name: 'IT', sortOrder: 1 }] });
            }
            return Promise.resolve({
                data: [
                    { id: 'kb-1', name: 'IT-Handbuch', description: null, subscribed: false, categoryIds: ['c1'] },
                    { id: 'kb-2', name: 'Rechtsfragen', description: null, subscribed: true, categoryIds: [] },
                ],
            });
        });
    });

    it('lists catalog entries', async () => {
        renderPanel();
        expect(await screen.findByText('IT-Handbuch')).toBeInTheDocument();
        expect(screen.getByText('Rechtsfragen')).toBeInTheDocument();
    });

    it('subscribes via PUT and reports the change', async () => {
        mockedAxios.put.mockResolvedValue({ status: 204 });
        const onChange = vi.fn();
        renderPanel(onChange);

        const row = (await screen.findByText('IT-Handbuch')).closest('[data-testid="catalog-entry"]');
        expect(row).not.toBeNull();
        await userEvent.click(within(row as HTMLElement).getByRole('button', { name: /abonnieren|subscribe/i }));

        await waitFor(() => {
            expect(mockedAxios.put).toHaveBeenCalledWith(
                expect.stringContaining('/api/kb/kb-1/subscription')
            );
        });
        expect(onChange).toHaveBeenCalled();
    });

    it('unsubscribes via DELETE for an already-subscribed entry', async () => {
        mockedAxios.delete.mockResolvedValue({ status: 204 });
        renderPanel();

        const row = (await screen.findByText('Rechtsfragen')).closest('[data-testid="catalog-entry"]');
        await userEvent.click(within(row as HTMLElement).getByRole('button', { name: /abbestellen|unsubscribe/i }));

        await waitFor(() => {
            expect(mockedAxios.delete).toHaveBeenCalledWith(
                expect.stringContaining('/api/kb/kb-2/subscription')
            );
        });
    });
});
