import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import AgentsView from './AgentsView';
import { deleteAgent } from './api';
import { translations } from '../../translations';

// Mirrors IngestStageIndicator.test.tsx: map real translation keys to their
// `en` value so assertions can check actual copy, not raw keys.
vi.mock('../../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const entry = translations[key as keyof typeof translations];
      return entry ? entry.en : key;
    },
  }),
}));

vi.mock('./api', () => ({
  listAgents: vi.fn().mockResolvedValue([
    { id: 'a1', userId: 'u1', name: 'Netz', description: 'network advisories', icon: 'shield',
      systemPrompt: '', chatModel: '', toolNames: [], config: {}, isEnabled: true,
      createdAt: '', updatedAt: '' },
  ]),
  listTeams: vi.fn().mockResolvedValue([]),
  fetchAgentRegistry: vi.fn().mockResolvedValue({ fields: [], tools: ['kb_search'] }),
  deleteAgent: vi.fn(),
  deleteTeam: vi.fn(),
  createAgent: vi.fn(),
  updateAgent: vi.fn(),
  createTeam: vi.fn(),
  updateTeam: vi.fn(),
}));

describe('AgentsView', () => {
  beforeEach(() => vi.clearAllMocks());

  it('lists the user agents', async () => {
    render(<AgentsView onBack={() => {}} />);
    await waitFor(() => expect(screen.getByText('Netz')).toBeInTheDocument());
    expect(screen.getByText('network advisories')).toBeInTheDocument();
  });

  it('shows the empty state on the Teams tab', async () => {
    render(<AgentsView onBack={() => {}} />);
    const teamsTab = await screen.findByRole('button', { name: /Teams/ });
    teamsTab.click();
    await waitFor(() =>
      expect(screen.getByText(/Noch keine Teams|No teams yet/)).toBeInTheDocument());
  });

  it('opens a themed dialog instead of window.confirm on delete', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm');
    render(<AgentsView onBack={() => {}} />);
    await screen.findByText('Netz');
    await userEvent.click(screen.getByRole('button', { name: /Delete/i }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(confirmSpy).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });

  it('does not render the "used by teams" line when the agent belongs to no team', async () => {
    render(<AgentsView onBack={() => {}} />);
    await screen.findByText('Netz');
    await userEvent.click(screen.getByRole('button', { name: /Delete/i }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByText(/Used by .* team\(s\)/)).not.toBeInTheDocument();
  });

  it('cancelling the dialog deletes nothing', async () => {
    render(<AgentsView onBack={() => {}} />);
    await screen.findByText('Netz');
    await userEvent.click(screen.getByRole('button', { name: /Delete/i }));
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(deleteAgent).not.toHaveBeenCalled();
    expect(screen.getByText('Netz')).toBeInTheDocument();
  });

  it('confirming the dialog calls deleteAgent', async () => {
    vi.mocked(deleteAgent).mockResolvedValueOnce(undefined);
    render(<AgentsView onBack={() => {}} />);
    await screen.findByText('Netz');
    await userEvent.click(screen.getByRole('button', { name: /Delete/i }));
    const dialog = screen.getByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: /Delete/i }));
    await waitFor(() => expect(deleteAgent).toHaveBeenCalledWith('a1'));
  });

  it('surfaces an error when the delete request fails', async () => {
    vi.mocked(deleteAgent).mockRejectedValueOnce(new Error('boom'));
    render(<AgentsView onBack={() => {}} />);
    await screen.findByText('Netz');
    await userEvent.click(screen.getByRole('button', { name: /Delete/i }));
    const dialog = screen.getByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: /Delete/i }));
    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument());
    // The dialog stays open so the message is not lost.
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
