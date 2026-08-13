import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import AdminGlobalKbsTab from './AdminGlobalKbsTab';
import { translations } from '../../translations';

vi.mock('axios');
const mockedAxios = vi.mocked(axios, true);

// Real translated text (English) so button/switch accessible names actually
// contain the words the tests match on ("make private", "overview") — a
// plain key-echo mock would not. See KbAgentsSection.test.tsx for the same
// pattern.
const tMock = (key: string) => {
    const entry = translations[key as keyof typeof translations];
    return entry ? entry.en : key;
};
const themeMock = { t: tMock };
const toastMock = { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() };
const showPrompt = vi.fn();
const showConfirm = vi.fn();
const modalMock = { showPrompt, showConfirm };
const authMock = { user: { id: 'admin-1', username: 'admin', role: 'admin' }, token: 'tok' };

// Hoisted, module-level singleton mocks — a fresh object literal per call
// would change identity on every render and destabilize effect deps.
vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => themeMock }));
vi.mock('../../contexts/ToastContext', () => ({ useToast: () => toastMock }));
vi.mock('../../contexts/ModalContext', () => ({ useModalContext: () => modalMock }));
vi.mock('../../contexts/AuthContext', () => ({ useAuth: () => authMock }));

describe('AdminGlobalKbsTab', () => {
    beforeEach(() => {
        vi.resetAllMocks();
        mockedAxios.get.mockImplementation((url: string) => {
            if (url.includes('/unpublish-impact')) {
                return Promise.resolve({
                    data: { subscribers: 4, candidates: [{ userId: 'u1', username: 'alice' }] },
                });
            }
            if (url.includes('/api/admin/kb-categories')) {
                return Promise.resolve({ data: [] });
            }
            return Promise.resolve({
                data: [{ id: 'kb-1', name: 'IT-Handbuch', createdAt: '2026-01-01T00:00:00Z', autoSubscribe: false }],
            });
        });
    });

    it('states the subscriber count before unpublishing', async () => {
        renderTab();
        await userEvent.click(await screen.findByRole('button', { name: /zurücknehmen|make private/i }));
        expect(await screen.findByText(/4/)).toBeInTheDocument();
    });

    it('requires an owner and posts it', async () => {
        mockedAxios.post.mockResolvedValue({ status: 204 });
        renderTab();

        await userEvent.click(await screen.findByRole('button', { name: /zurücknehmen|make private/i }));
        const dialog = await screen.findByRole('dialog');
        await userEvent.selectOptions(within(dialog).getByRole('combobox'), 'u1');
        await userEvent.click(within(dialog).getByRole('button', { name: /zurücknehmen|make private/i }));

        await waitFor(() => {
            expect(mockedAxios.post).toHaveBeenCalledWith(
                expect.stringContaining('/api/admin/kb/kb-1/unpublish'),
                { newOwnerId: 'u1' }
            );
        });

        // The KB is no longer public — the row (and the dialog) disappear.
        await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
        expect(screen.queryByText('IT-Handbuch')).toBeNull();
    });

    it('toggles auto_subscribe through the global-kbs PATCH', async () => {
        mockedAxios.patch.mockResolvedValue({ data: { id: 'kb-1', autoSubscribe: true } });
        renderTab();

        await userEvent.click(await screen.findByRole('switch', { name: /übersichten|overview/i }));

        await waitFor(() => {
            expect(mockedAxios.patch).toHaveBeenCalledWith(
                expect.stringContaining('/api/admin/global-kbs/kb-1'),
                expect.objectContaining({ autoSubscribe: true })
            );
        });
    });

    it('blocks confirmation until an owner is chosen when candidates exist', async () => {
        renderTab();
        await userEvent.click(await screen.findByRole('button', { name: /zurücknehmen|make private/i }));
        const dialog = await screen.findByRole('dialog');
        await screen.findByText(/4/);

        const confirmButton = within(dialog).getByRole('button', { name: /zurücknehmen|make private/i });
        expect(confirmButton).toBeDisabled();

        await userEvent.selectOptions(within(dialog).getByRole('combobox'), 'u1');
        expect(confirmButton).toBeEnabled();
    });

    it('offers the acting admin as owner and enables confirmation immediately when there are no candidates', async () => {
        mockedAxios.get.mockImplementation((url: string) => {
            if (url.includes('/unpublish-impact')) {
                return Promise.resolve({ data: { subscribers: 0, candidates: [] } });
            }
            if (url.includes('/api/admin/kb-categories')) {
                return Promise.resolve({ data: [] });
            }
            return Promise.resolve({
                data: [{ id: 'kb-1', name: 'IT-Handbuch', createdAt: '2026-01-01T00:00:00Z', autoSubscribe: false }],
            });
        });
        mockedAxios.post.mockResolvedValue({ status: 204 });
        renderTab();

        await userEvent.click(await screen.findByRole('button', { name: /zurücknehmen|make private/i }));
        const dialog = await screen.findByRole('dialog');
        await screen.findByText(translations.unpublishNoCandidates.en);
        expect(within(dialog).queryByRole('combobox')).toBeNull();

        const confirmButton = within(dialog).getByRole('button', { name: /zurücknehmen|make private/i });
        expect(confirmButton).toBeEnabled();

        await userEvent.click(confirmButton);
        await waitFor(() => {
            expect(mockedAxios.post).toHaveBeenCalledWith(
                expect.stringContaining('/api/admin/kb/kb-1/unpublish'),
                { newOwnerId: 'admin-1' }
            );
        });
        await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    });

    it('disables a row\'s category chips instead of risking a wipe when its assignment fetch fails', async () => {
        mockedAxios.get.mockImplementation((url: string) => {
            if (url.includes('/unpublish-impact')) {
                return Promise.resolve({
                    data: { subscribers: 4, candidates: [{ userId: 'u1', username: 'alice' }] },
                });
            }
            if (url.includes('/api/admin/kb-categories')) {
                return Promise.resolve({ data: [{ id: 'cat-1', name: 'Security', sortOrder: 0 }] });
            }
            if (url.includes('/api/kb/kb-1/categories')) {
                return Promise.reject(new Error('boom'));
            }
            return Promise.resolve({
                data: [{ id: 'kb-1', name: 'IT-Handbuch', createdAt: '2026-01-01T00:00:00Z', autoSubscribe: false }],
            });
        });
        renderTab();

        // Scoped to the chip group: AdminCategoriesSection below renders its
        // own "Edit: Security" / "Delete: Security" buttons for the same
        // category, so an unscoped name match is ambiguous.
        const chips = await screen.findByRole('group', { name: translations.categories.en });
        const chip = within(chips).getByRole('button', { name: /Security/i });
        await waitFor(() => expect(chip).toBeDisabled());
        expect(await screen.findByText(translations.categoriesLoadError.en)).toBeInTheDocument();

        // Even a click that reaches the chip bypassing the disabled attribute
        // (fireEvent dispatches directly on the node, unlike userEvent) must
        // not be able to issue the wiping PUT — the handler itself refuses
        // once the row is marked as failed-to-load.
        fireEvent.click(chip);
        expect(mockedAxios.put).not.toHaveBeenCalled();
    });

    // The chips replaced a <select multiple> whose assigned entries could only
    // be told apart by the browser's selection highlight and could only be
    // dropped by ctrl-clicking them. Both directions are pinned here, since
    // "assignments can be removed again" is the whole point of the change.
    it('assigns a category on click and removes it on a second click', async () => {
        mockedAxios.get.mockImplementation((url: string) => {
            if (url.includes('/api/admin/kb-categories')) {
                return Promise.resolve({ data: [{ id: 'cat-1', name: 'Security', sortOrder: 0 }] });
            }
            if (url.includes('/api/kb/kb-1/categories')) {
                return Promise.resolve({ data: [] });
            }
            return Promise.resolve({
                data: [{ id: 'kb-1', name: 'IT-Handbuch', createdAt: '2026-01-01T00:00:00Z', autoSubscribe: false }],
            });
        });
        mockedAxios.put.mockResolvedValue({ status: 204 });
        renderTab();

        const chips = await screen.findByRole('group', { name: translations.categories.en });
        const chip = within(chips).getByRole('button', { name: /Security/i });
        await waitFor(() => expect(chip).toHaveAttribute('aria-pressed', 'false'));

        await userEvent.click(chip);
        await waitFor(() => {
            expect(mockedAxios.put).toHaveBeenCalledWith(
                expect.stringContaining('/api/kb/kb-1/categories'),
                { categoryIds: ['cat-1'] }
            );
        });
        await waitFor(() => expect(chip).toHaveAttribute('aria-pressed', 'true'));

        await userEvent.click(chip);
        await waitFor(() => {
            expect(mockedAxios.put).toHaveBeenLastCalledWith(
                expect.stringContaining('/api/kb/kb-1/categories'),
                { categoryIds: [] }
            );
        });
        await waitFor(() => expect(chip).toHaveAttribute('aria-pressed', 'false'));
    });
});

// renderTab wraps the component in the same context mocks the sibling admin
// tests use (KbAgentsSection.test.tsx / KbDeleteDialog.test.tsx) — the mocks
// above stand in for ThemeProvider/ToastProvider/ModalProvider/AuthProvider.
function renderTab() {
    return render(<AdminGlobalKbsTab />);
}
