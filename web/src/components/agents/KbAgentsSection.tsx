import { useCallback, useEffect, useState } from 'react';
import { Bot, Plus, Users } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import AgentEntityRow from './AgentEntityRow';
import {
  attachAgentToKb, attachTeamToKb, detachAgentFromKb, detachTeamFromKb,
  fetchKbAgents, listAgents, listTeams,
  type AgentRecord, type KbAgents, type TeamRecord,
} from './api';

interface Props {
  /**
   * The KB to attach to, or null when no KB is current (SettingsModal is
   * reachable from HomeView, where currentKb can be null). With null the
   * section still explains itself and links to agent creation — it just
   * cannot offer attach controls.
   */
  kbId: string | null;
  /** Navigate to the My Agents screen. */
  onCreateAgent?: () => void;
}

// Per-KB attach/detach UI for the owner's agents and teams, with a single
// "default" radio across both kinds (backend enforces one default per KB).
export default function KbAgentsSection({ kbId, onCreateAgent }: Props) {
  const { t } = useTheme();
  const toast = useToast();
  const [attached, setAttached] = useState<KbAgents>({ agents: [], teams: [] });
  const [myAgents, setMyAgents] = useState<AgentRecord[]>([]);
  const [myTeams, setMyTeams] = useState<TeamRecord[]>([]);
  const [tab, setTab] = useState<'agents' | 'teams'>('agents');

  const reload = useCallback(() => {
    if (kbId) fetchKbAgents(kbId).then(setAttached).catch(() => {});
    listAgents().then(setMyAgents).catch(() => setMyAgents([]));
    listTeams().then(setMyTeams).catch(() => setMyTeams([]));
  }, [kbId]);

  useEffect(reload, [reload]);

  const attachedAgentIds = new Set(attached.agents.map(a => a.id));
  const attachedTeamIds = new Set(attached.teams.map(x => x.id));

  const row = (
    kind: 'agent' | 'team',
    id: string, name: string, isAttached: boolean, isDefault: boolean,
  ) => (
    <AgentEntityRow
      key={`${kind}-${id}`}
      icon={kind === 'agent' ? <Bot size={15} aria-hidden="true" /> : <Users size={15} aria-hidden="true" />}
      name={name}
      actions={!kbId ? null : (
        <>
          {isAttached && (
            <label className="form-hint" style={{ display: 'flex', gap: '0.25rem', alignItems: 'center', margin: 0 }}>
              <input
                type="radio"
                name={`kb-default-${kbId}`}
                checked={isDefault}
                onChange={async () => {
                  try {
                    if (kind === 'agent') await attachAgentToKb(kbId, id, true);
                    else await attachTeamToKb(kbId, id, true);
                    reload();
                  } catch {
                    toast.error(t('settingsUpdateError'));
                  }
                }}
              />
              {t('kbAgentsDefault')}
            </label>
          )}
          <button
            type="button"
            className="btn btn--secondary"
            onClick={async () => {
              try {
                if (kind === 'agent') {
                  if (isAttached) await detachAgentFromKb(kbId, id);
                  else await attachAgentToKb(kbId, id);
                } else {
                  if (isAttached) await detachTeamFromKb(kbId, id);
                  else await attachTeamToKb(kbId, id);
                }
                reload();
              } catch {
                toast.error(t('settingsUpdateError'));
              }
            }}
          >
            {isAttached ? t('kbAgentsDetach') : t('kbAgentsAttach')}
          </button>
        </>
      )}
    />
  );

  // Always rendered. This section used to `return null` when the user owned no
  // agents, which meant someone who had never created one saw no trace of the
  // feature in KB settings — the exact place they'd be deciding how the KB
  // should behave. The empty state is the entry point instead.
  const isEmpty = myAgents.length === 0 && myTeams.length === 0;

  return (
    <section style={{ marginTop: '1rem' }}>
      {/* The create link lives in the header so it is present in every state.
          It used to sit inside the empty state only, which meant it vanished
          as soon as you owned one agent — precisely when you'd want a second. */}
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: '0.5rem' }}>
        <h3 style={{ fontSize: '0.9rem', margin: '0 0 0.25rem' }}>{t('kbAgentsSection')}</h3>
        <button type="button" className="btn btn--tertiary" onClick={onCreateAgent}>
          <Plus size={14} aria-hidden="true" /> {t('kbAgentsCreateFirst')}
        </button>
      </div>
      <p className="form-hint">{t('kbAgentsSectionHelp')}</p>

      {isEmpty ? null : (
        <>
          {/* Same segmented control as the My Agents screen: agents and teams
              are different things and a single flat list read as one pile. */}
          <nav className="segmented-control" style={{ marginBottom: '0.5rem' }}>
            <button
              type="button"
              className="segmented-control__item"
              onClick={() => setTab('agents')}
              aria-pressed={tab === 'agents'}
            >
              <Bot size={14} aria-hidden="true" /> {t('agentsTabAgents')}
            </button>
            <button
              type="button"
              className="segmented-control__item"
              onClick={() => setTab('teams')}
              aria-pressed={tab === 'teams'}
            >
              <Users size={14} aria-hidden="true" /> {t('agentsTabTeams')}
            </button>
          </nav>

          {tab === 'agents' && (myAgents.length === 0
            ? <p className="form-hint">{t('noAgentsYet')}</p>
            : myAgents.map(a => row('agent', a.id, a.name,
              attachedAgentIds.has(a.id),
              attached.agents.find(x => x.id === a.id)?.isDefault ?? false)))}

          {tab === 'teams' && (myTeams.length === 0
            ? <p className="form-hint">{t('noTeamsYet')}</p>
            : myTeams.map(tm => row('team', tm.id, tm.name,
              attachedTeamIds.has(tm.id),
              attached.teams.find(x => x.id === tm.id)?.isDefault ?? false)))}

          {!kbId && <p className="form-hint">{t('kbAgentsNoKbNote')}</p>}
        </>
      )}
    </section>
  );
}
