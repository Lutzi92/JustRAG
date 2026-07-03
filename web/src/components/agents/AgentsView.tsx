import { useCallback, useEffect, useState } from 'react';
import { ArrowLeft, Bot, Plus, Trash2, Users } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import {
  listAgents, listTeams, fetchAgentRegistry,
  createAgent, updateAgent, deleteAgent,
  createTeam, updateTeam, deleteTeam,
  type AgentRecord, type TeamRecord,
  type AgentRegistry, type AgentUpsert, type TeamUpsert,
} from './api';
import AgentForm from './AgentForm';
import TeamForm from './TeamForm';

interface Props {
  onBack: () => void;
  availableModels?: string[];
}

export default function AgentsView({ onBack, availableModels = [] }: Props) {
  const { t } = useTheme();
  const [tab, setTab] = useState<'agents' | 'teams'>('agents');
  const [agents, setAgents] = useState<AgentRecord[]>([]);
  const [teams, setTeams] = useState<TeamRecord[]>([]);
  const [registry, setRegistry] = useState<AgentRegistry>({ fields: [], tools: [] });
  const [editingAgent, setEditingAgent] = useState<AgentRecord | 'new' | null>(null);
  const [editingTeam, setEditingTeam] = useState<TeamRecord | 'new' | null>(null);

  const reload = useCallback(() => {
    listAgents().then(setAgents).catch(() => setAgents([]));
    listTeams().then(setTeams).catch(() => setTeams([]));
  }, []);

  useEffect(() => {
    reload();
    fetchAgentRegistry().then(setRegistry).catch(() => setRegistry({ fields: [], tools: [] }));
  }, [reload]);

  const saveAgent = async (a: AgentUpsert) => {
    if (editingAgent && editingAgent !== 'new') await updateAgent(editingAgent.id, a);
    else await createAgent(a);
    setEditingAgent(null);
    reload();
  };

  const saveTeam = async (tm: TeamUpsert) => {
    if (editingTeam && editingTeam !== 'new') await updateTeam(editingTeam.id, tm);
    else await createTeam(tm);
    setEditingTeam(null);
    reload();
  };

  const removeAgent = async (id: string) => {
    if (!window.confirm(t('deleteAgentConfirm'))) return;
    await deleteAgent(id);
    reload();
  };

  const removeTeam = async (id: string) => {
    if (!window.confirm(t('deleteTeamConfirm'))) return;
    await deleteTeam(id);
    reload();
  };

  return (
    <div style={{ maxWidth: 860, margin: '0 auto', padding: '1.5rem' }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1rem' }}>
        <button type="button" onClick={onBack} aria-label={t('back')}
          style={{ background: 'none', border: 'none', cursor: 'pointer' }}>
          <ArrowLeft size={20} aria-hidden="true" />
        </button>
        <h1 style={{ fontSize: '1.25rem', margin: 0 }}>{t('myAgents')}</h1>
      </header>

      <nav style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem' }}>
        <button type="button" onClick={() => setTab('agents')}
          aria-pressed={tab === 'agents'}>
          <Bot size={16} aria-hidden="true" /> {t('agentsTabAgents')}
        </button>
        <button type="button" onClick={() => setTab('teams')}
          aria-pressed={tab === 'teams'}>
          <Users size={16} aria-hidden="true" /> {t('agentsTabTeams')}
        </button>
      </nav>

      {tab === 'agents' && (
        <section>
          {editingAgent ? (
            <AgentForm
              initial={editingAgent === 'new' ? undefined : editingAgent}
              registry={registry.fields}
              availableTools={registry.tools}
              availableModels={availableModels}
              onSave={saveAgent}
              onCancel={() => setEditingAgent(null)}
            />
          ) : (
            <>
              <button type="button" onClick={() => setEditingAgent('new')}>
                <Plus size={16} aria-hidden="true" /> {t('newAgent')}
              </button>
              {agents.length === 0 && <p>{t('noAgentsYet')}</p>}
              {agents.map(a => (
                <div key={a.id} style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', padding: '0.75rem', border: '1px solid var(--border-color)', borderRadius: 8, marginTop: '0.5rem' }}>
                  <Bot size={18} aria-hidden="true" />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <strong>{a.name}</strong>
                    <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{a.description}</div>
                  </div>
                  <button type="button" onClick={() => setEditingAgent(a)}>{t('editAgent')}</button>
                  <button type="button" onClick={() => removeAgent(a.id)} aria-label={t('delete')}>
                    <Trash2 size={16} aria-hidden="true" />
                  </button>
                </div>
              ))}
            </>
          )}
        </section>
      )}

      {tab === 'teams' && (
        <section>
          {editingTeam ? (
            <TeamForm
              initial={editingTeam === 'new' ? undefined : editingTeam}
              agents={agents}
              onSave={saveTeam}
              onCancel={() => setEditingTeam(null)}
            />
          ) : (
            <>
              <button type="button" onClick={() => setEditingTeam('new')} disabled={agents.length === 0}>
                <Plus size={16} aria-hidden="true" /> {t('newTeam')}
              </button>
              {teams.length === 0 && <p>{t('noTeamsYet')}</p>}
              {teams.map(tm => (
                <div key={tm.id} style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', padding: '0.75rem', border: '1px solid var(--border-color)', borderRadius: 8, marginTop: '0.5rem' }}>
                  <Users size={18} aria-hidden="true" />
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <strong>{tm.name}</strong>
                    <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                      {tm.description} · {tm.memberIds.length} {t('membersCount')}
                    </div>
                  </div>
                  <button type="button" onClick={() => setEditingTeam(tm)}>{t('editTeam')}</button>
                  <button type="button" onClick={() => removeTeam(tm.id)} aria-label={t('delete')}>
                    <Trash2 size={16} aria-hidden="true" />
                  </button>
                </div>
              ))}
            </>
          )}
        </section>
      )}
    </div>
  );
}
