import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import AgentsView from './AgentsView';
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
  fetchAgentRegistry: vi.fn().mockResolvedValue([]),
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
});
