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

function renderPanel(onSubscriptionChange = vi.fn(), onOpenKb = vi.fn()) {
    return render(<KbCatalogPanel onSubscriptionChange={onSubscriptionChange} onOpenKb={onOpenKb} />);
}

describe('KbCatalogPanel', () => {
    beforeEach(() => {
        vi.resetAllMocks();
        mockedAxios.get.mockImplementation((url: string) => {
            if (url.includes('/api/kb-categories')) {
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

    // The discovery rows are cards now, matching the other overview sections.
    // Two things have to hold for that to be usable: the card opens the KB,
    // and the add-to-favorites control on it does not.
    it('opens the KB when the card is activated', async () => {
        const onOpenKb = vi.fn();
        renderPanel(vi.fn(), onOpenKb);

        const row = (await screen.findByText('IT-Handbuch')).closest('[data-testid="catalog-entry"]');
        await userEvent.click(row as HTMLElement);
        expect(onOpenKb).toHaveBeenCalledWith('kb-1');
    });

    it('opens the KB from the name button', async () => {
        const onOpenKb = vi.fn();
        renderPanel(vi.fn(), onOpenKb);

        await userEvent.click(await screen.findByRole('button', { name: 'IT-Handbuch' }));
        expect(onOpenKb).toHaveBeenCalledWith('kb-1');
    });

    it('does not open the KB when the favorites toggle is clicked', async () => {
        mockedAxios.put.mockResolvedValue({ status: 204 });
        const onOpenKb = vi.fn();
        renderPanel(vi.fn(), onOpenKb);

        const row = (await screen.findByText('IT-Handbuch')).closest('[data-testid="catalog-entry"]');
        await userEvent.click(within(row as HTMLElement).getByRole('button', { name: /abonnieren|subscribe/i }));

        await waitFor(() => {
            expect(mockedAxios.put).toHaveBeenCalledWith(expect.stringContaining('/api/kb/kb-1/subscription'));
        });
        expect(onOpenKb).not.toHaveBeenCalled();
    });

    // aria-pressed is what tells a screen-reader user whether the KB is
    // already a favorite; the filled/outline star alone would not.
    it('reflects the subscription state on the toggle', async () => {
        renderPanel();

        const unsubscribed = (await screen.findByText('IT-Handbuch')).closest('[data-testid="catalog-entry"]');
        expect(within(unsubscribed as HTMLElement).getByRole('button', { name: /abonnieren|subscribe/i }))
            .toHaveAttribute('aria-pressed', 'false');

        const subscribed = screen.getByText('Rechtsfragen').closest('[data-testid="catalog-entry"]');
        expect(within(subscribed as HTMLElement).getByRole('button', { name: /abbestellen|unsubscribe/i }))
            .toHaveAttribute('aria-pressed', 'true');
    });

    // The taxonomy used to be fetched from /api/admin/kb-categories, which
    // answers 403 for a non-admin — so the filter was invisible to exactly
    // the users the catalog exists for.
    it('reads the categories from the authentication-only endpoint', async () => {
        renderPanel();
        await waitFor(() => {
            expect(mockedAxios.get).toHaveBeenCalledWith(expect.stringContaining('/api/kb-categories'));
        });
        expect(mockedAxios.get).not.toHaveBeenCalledWith(expect.stringContaining('/api/admin/'));
    });

    // Tabs, not toggle chips: the filter is single-select and always has one
    // active value. aria-selected (not aria-pressed) is what carries that.
    it('renders the categories as a tab strip with "All" leftmost and selected', async () => {
        renderPanel();
        const tabs = await screen.findAllByRole('tab');
        expect(tabs.map(el => el.textContent)).toEqual(['catalogAllCategories', 'IT']);
        expect(tabs[0]).toHaveAttribute('aria-selected', 'true');
        expect(tabs[1]).toHaveAttribute('aria-selected', 'false');
    });

    it('filters by the selected category tab and moves the selection to it', async () => {
        renderPanel();
        await userEvent.click(await screen.findByRole('tab', { name: 'IT' }));

        await waitFor(() => {
            expect(mockedAxios.get).toHaveBeenCalledWith(expect.stringContaining('category=c1'));
        });
        expect(screen.getByRole('tab', { name: 'IT' })).toHaveAttribute('aria-selected', 'true');
        expect(screen.getByRole('tab', { name: 'catalogAllCategories' })).toHaveAttribute('aria-selected', 'false');
    });
});

// A deployment can hold far more public KBs than fit above the fold; the
// panel sits between the Favoriten section and the two below it, so an
// unbounded grid buries them.
describe('KbCatalogPanel result fold', () => {
    function entries(n: number) {
        return Array.from({ length: n }, (_, i) => ({
            id: `kb-${i}`, name: `KB ${i}`, description: null, subscribed: false, categoryIds: [],
        }));
    }

    function mockEntries(n: number) {
        mockedAxios.get.mockImplementation((url: string) =>
            Promise.resolve({ data: url.includes('/api/kb-categories') ? [] : entries(n) }));
    }

    beforeEach(() => { vi.resetAllMocks(); });

    it('shows only the first eight and offers the rest behind a button', async () => {
        mockEntries(11);
        renderPanel();

        expect(await screen.findByText('KB 0')).toBeInTheDocument();
        expect(screen.getAllByTestId('catalog-entry')).toHaveLength(8);
        expect(screen.queryByText('KB 8')).not.toBeInTheDocument();

        // The button names the number actually hidden, not a page size.
        await userEvent.click(screen.getByRole('button', { name: 'catalogShowMore' }));
        expect(screen.getAllByTestId('catalog-entry')).toHaveLength(11);
        expect(screen.getByText('KB 10')).toBeInTheDocument();
    });

    it('offers no expand button when everything already fits', async () => {
        mockEntries(8);
        renderPanel();

        expect(await screen.findByText('KB 0')).toBeInTheDocument();
        expect(screen.getAllByTestId('catalog-entry')).toHaveLength(8);
        expect(screen.queryByRole('button', { name: 'catalogShowMore' })).not.toBeInTheDocument();
    });

    // Ohne das Zuruecksetzen bliebe jede spaetere Ergebnisliste aufgeklappt,
    // weil `expanded` sonst ueber den Filterwechsel hinweg haengen bliebe.
    it('folds the list again when the search changes', async () => {
        mockEntries(11);
        renderPanel();

        await userEvent.click(await screen.findByRole('button', { name: 'catalogShowMore' }));
        expect(screen.getAllByTestId('catalog-entry')).toHaveLength(11);

        await userEvent.type(screen.getByLabelText('catalogSearchPlaceholder'), 'KB');
        await waitFor(() => expect(screen.getAllByTestId('catalog-entry')).toHaveLength(8));
    });
});
