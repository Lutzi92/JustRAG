import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import KbAgentsSection from './KbAgentsSection';
import { translations } from '../../translations';

vi.mock('../../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const entry = translations[key as keyof typeof translations];
      return entry ? entry.en : key;
    },
  }),
}));

vi.mock('../../contexts/ToastContext', () => ({
  useToast: () => ({ error: vi.fn(), success: vi.fn() }),
}));

const listAgents = vi.fn();
const listTeams = vi.fn();
const fetchKbAgents = vi.fn();

vi.mock('./api', () => ({
  listAgents: (...a: unknown[]) => listAgents(...a),
  listTeams: (...a: unknown[]) => listTeams(...a),
  fetchKbAgents: (...a: unknown[]) => fetchKbAgents(...a),
  attachAgentToKb: vi.fn(),
  attachTeamToKb: vi.fn(),
  detachAgentFromKb: vi.fn(),
  detachTeamFromKb: vi.fn(),
}));

const anAgent = {
  id: 'a1', userId: 'u1', name: 'Netz', description: 'network advisories', icon: 'bot',
  systemPrompt: '', chatModel: '', toolNames: [], config: {}, isEnabled: true,
  createdAt: '', updatedAt: '',
};

const aTeam = {
  id: 't1', userId: 'u1', name: 'Sicherheitsteam', description: 'security', icon: 'users',
  maxAgentsPerTurn: 3, isEnabled: true, memberIds: ['a1'],
  createdAt: '', updatedAt: '',
};

describe('KbAgentsSection', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fetchKbAgents.mockResolvedValue({ agents: [], teams: [] });
    listAgents.mockResolvedValue([]);
    listTeams.mockResolvedValue([]);
  });

  // The section used to `return null` when the user owned no agents, so a user
  // who had never created one saw no trace of the feature in KB settings —
  // exactly where they'd be thinking about how the KB should behave.
  it('renders an explainer and a create link when the user has no agents', async () => {
    render(<KbAgentsSection kbId="kb1" onCreateAgent={() => {}} />);
    await waitFor(() =>
      expect(screen.getByText(translations.kbAgentsSectionHelp.en)).toBeInTheDocument());
    expect(screen.getByRole('button', { name: translations.kbAgentsCreateFirst.en }))
      .toBeInTheDocument();
  });

  it('invokes onCreateAgent when the create link is clicked', async () => {
    const onCreateAgent = vi.fn();
    render(<KbAgentsSection kbId="kb1" onCreateAgent={onCreateAgent} />);
    const btn = await screen.findByRole('button', { name: translations.kbAgentsCreateFirst.en });
    btn.click();
    expect(onCreateAgent).toHaveBeenCalledTimes(1);
  });

  // The link is the only route from "configuring this KB" to "making an agent".
  // It used to appear only in the empty state, so it vanished the moment you
  // owned one agent — exactly when you'd want to add a second.
  it('still offers the agent-creator link when agents already exist', async () => {
    listAgents.mockResolvedValue([anAgent]);
    listTeams.mockResolvedValue([aTeam]);
    const onCreateAgent = vi.fn();
    render(<KbAgentsSection kbId="kb1" onCreateAgent={onCreateAgent} />);

    await waitFor(() => expect(screen.getByText('Netz')).toBeInTheDocument());
    const link = screen.getByRole('button', { name: translations.kbAgentsCreateFirst.en });
    link.click();
    expect(onCreateAgent).toHaveBeenCalledTimes(1);
  });

  it('lists the user agents when they have some', async () => {
    listAgents.mockResolvedValue([anAgent]);
    render(<KbAgentsSection kbId="kb1" onCreateAgent={() => {}} />);
    await waitFor(() => expect(screen.getByText('Netz')).toBeInTheDocument());
    expect(screen.getByRole('button', { name: translations.kbAgentsAttach.en }))
      .toBeInTheDocument();
  });

  // Agents and teams are different things and were previously rendered as one
  // flat list, teams silently first. Tabs separate them the same way the
  // My Agents screen does.
  it('separates agents and teams into tabs, defaulting to agents', async () => {
    listAgents.mockResolvedValue([anAgent]);
    listTeams.mockResolvedValue([aTeam]);
    render(<KbAgentsSection kbId="kb1" onCreateAgent={() => {}} />);

    await waitFor(() => expect(screen.getByText('Netz')).toBeInTheDocument());
    expect(screen.queryByText('Sicherheitsteam')).toBeNull();

    const teamsTab = screen.getByRole('button', { name: translations.agentsTabTeams.en });
    expect(teamsTab).toHaveAttribute('aria-pressed', 'false');
    teamsTab.click();

    await waitFor(() => expect(screen.getByText('Sicherheitsteam')).toBeInTheDocument());
    expect(screen.queryByText('Netz')).toBeNull();
  });

  it('shows a per-tab empty message rather than hiding the tab', async () => {
    listAgents.mockResolvedValue([anAgent]);
    listTeams.mockResolvedValue([]);
    render(<KbAgentsSection kbId="kb1" onCreateAgent={() => {}} />);

    const teamsTab = await screen.findByRole('button', { name: translations.agentsTabTeams.en });
    teamsTab.click();
    await waitFor(() =>
      expect(screen.getByText(translations.noTeamsYet.en)).toBeInTheDocument());
  });

  it('shows no tabs at all when the user owns neither agents nor teams', async () => {
    render(<KbAgentsSection kbId="kb1" onCreateAgent={() => {}} />);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: translations.kbAgentsCreateFirst.en }))
        .toBeInTheDocument());
    expect(screen.queryByRole('button', { name: translations.agentsTabTeams.en })).toBeNull();
  });

  // The bug: SettingsModal is reachable from HomeView with currentKb === null.
  // The system-prompt block renders anyway (optional chaining), so the section
  // vanishing silently looked like "the feature isn't there".
  it('still explains itself when there is no current KB, without attach controls', async () => {
    listAgents.mockResolvedValue([anAgent]);
    render(<KbAgentsSection kbId={null} onCreateAgent={() => {}} />);
    await waitFor(() =>
      expect(screen.getByText(translations.kbAgentsSectionHelp.en)).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: translations.kbAgentsAttach.en })).toBeNull();
    expect(fetchKbAgents).not.toHaveBeenCalled();
  });
});
