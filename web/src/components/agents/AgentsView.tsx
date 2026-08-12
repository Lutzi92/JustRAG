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
import AgentEntityRow from './AgentEntityRow';
import ConfirmDialog from './ConfirmDialog';

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

  // Which entity is queued for deletion, plus the in-flight request state.
  // Held here (not in ConfirmDialog) so a failed request keeps the dialog open
  // with its message — the dialog itself stays presentational.
  const [pendingDelete, setPendingDelete] = useState<
    { kind: 'agent' | 'team'; id: string; name: string } | null
  >(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  const askDelete = (kind: 'agent' | 'team', id: string, name: string) => {
    setDeleteError(null);
    setPendingDelete({ kind, id, name });
  };

  const confirmDelete = async () => {
    if (!pendingDelete) return;
    setDeleteBusy(true);
    setDeleteError(null);
    try {
      if (pendingDelete.kind === 'agent') await deleteAgent(pendingDelete.id);
      else await deleteTeam(pendingDelete.id);
      setPendingDelete(null);
      reload();
    } catch (e) {
      setDeleteError(e instanceof Error ? e.message : t('deleteFailed'));
    } finally {
      setDeleteBusy(false);
    }
  };

  return (
    <div className="admin-container">
      <header style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '1rem' }}>
        <button type="button" className="btn btn--icon" onClick={onBack} aria-label={t('back')}>
          <ArrowLeft size={20} aria-hidden="true" />
        </button>
        <h1 style={{ fontSize: '1.25rem', margin: 0 }}>{t('myAgents')}</h1>
      </header>

      <nav className="segmented-control" style={{ marginBottom: '1rem' }}>
        <button
          type="button"
          className="segmented-control__item"
          onClick={() => setTab('agents')}
          aria-pressed={tab === 'agents'}
        >
          <Bot size={16} aria-hidden="true" /> {t('agentsTabAgents')}
        </button>
        <button
          type="button"
          className="segmented-control__item"
          onClick={() => setTab('teams')}
          aria-pressed={tab === 'teams'}
        >
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
              <button type="button" className="btn btn--primary" onClick={() => setEditingAgent('new')}>
                <Plus size={16} aria-hidden="true" /> {t('newAgent')}
              </button>
              {agents.length === 0 && <p className="form-hint">{t('noAgentsYet')}</p>}
              {agents.map(a => (
                <AgentEntityRow
                  key={a.id}
                  icon={<Bot size={18} aria-hidden="true" />}
                  name={a.name}
                  secondary={a.description}
                  actions={
                    <>
                      <button type="button" className="btn btn--secondary" onClick={() => setEditingAgent(a)}>
                        {t('editAgent')}
                      </button>
                      <button
                        type="button"
                        className="btn btn--icon btn--destructive"
                        onClick={() => askDelete('agent', a.id, a.name)}
                        aria-label={t('delete')}
                      >
                        <Trash2 size={16} aria-hidden="true" />
                      </button>
                    </>
                  }
                />
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
              <button
                type="button"
                className="btn btn--primary"
                onClick={() => setEditingTeam('new')}
                disabled={agents.length === 0}
              >
                <Plus size={16} aria-hidden="true" /> {t('newTeam')}
              </button>
              {teams.length === 0 && <p className="form-hint">{t('noTeamsYet')}</p>}
              {teams.map(tm => (
                <AgentEntityRow
                  key={tm.id}
                  icon={<Users size={18} aria-hidden="true" />}
                  name={tm.name}
                  secondary={`${tm.description} · ${tm.memberIds.length} ${t('membersCount')}`}
                  actions={
                    <>
                      <button type="button" className="btn btn--secondary" onClick={() => setEditingTeam(tm)}>
                        {t('editTeam')}
                      </button>
                      <button
                        type="button"
                        className="btn btn--icon btn--destructive"
                        onClick={() => askDelete('team', tm.id, tm.name)}
                        aria-label={t('delete')}
                      >
                        <Trash2 size={16} aria-hidden="true" />
                      </button>
                    </>
                  }
                />
              ))}
            </>
          )}
        </section>
      )}

      {pendingDelete && (
        <ConfirmDialog
          title={pendingDelete.kind === 'agent' ? t('deleteAgentTitle') : t('deleteTeamTitle')}
          body={
            <>
              <strong>{pendingDelete.name}</strong>
              <br />
              {pendingDelete.kind === 'agent' ? t('deleteAgentConfirm') : t('deleteTeamConfirm')}
              {pendingDelete.kind === 'agent' && (() => {
                const usedByCount = teams.filter(tm => tm.memberIds.includes(pendingDelete.id)).length;
                return usedByCount > 0 && (
                  <>
                    <br />
                    {t('deleteAgentUsedByTeams').replace('{count}', String(usedByCount))}
                  </>
                );
              })()}
              <br />
              {t('deleteAttributionNote')}
            </>
          }
          confirmLabel={t('delete')}
          tone="destructive"
          busy={deleteBusy}
          error={deleteError}
          onCancel={() => { setPendingDelete(null); setDeleteError(null); }}
          onConfirm={confirmDelete}
        />
      )}
    </div>
  );
}
