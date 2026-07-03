import { useCallback, useEffect, useState } from 'react';
import { Bot, Users } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import {
  attachAgentToKb, attachTeamToKb, detachAgentFromKb, detachTeamFromKb,
  fetchKbAgents, listAgents, listTeams,
  type AgentRecord, type KbAgents, type TeamRecord,
} from './api';

interface Props {
  kbId: string;
}

// Per-KB attach/detach UI for the owner's agents and teams, with a single
// "default" radio across both kinds (backend enforces one default per KB).
export default function KbAgentsSection({ kbId }: Props) {
  const { t } = useTheme();
  const toast = useToast();
  const [attached, setAttached] = useState<KbAgents>({ agents: [], teams: [] });
  const [myAgents, setMyAgents] = useState<AgentRecord[]>([]);
  const [myTeams, setMyTeams] = useState<TeamRecord[]>([]);

  const reload = useCallback(() => {
    fetchKbAgents(kbId).then(setAttached).catch(() => {});
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
    <div key={`${kind}-${id}`} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.35rem', fontSize: '0.9rem' }}>
      {kind === 'agent' ? <Bot size={15} /> : <Users size={15} />}
      <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>{name}</span>
      {isAttached && (
        <label style={{ fontSize: '0.8rem', display: 'flex', gap: '0.25rem', alignItems: 'center' }}>
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
    </div>
  );

  if (myAgents.length === 0 && myTeams.length === 0) return null;

  return (
    <section style={{ marginTop: '1rem' }}>
      <h3 style={{ fontSize: '0.9rem', margin: '0 0 0.25rem' }}>{t('kbAgentsSection')}</h3>
      <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', margin: '0 0 0.5rem' }}>{t('kbAgentsSectionHelp')}</p>
      {myTeams.map(tm => row('team', tm.id, tm.name,
        attachedTeamIds.has(tm.id),
        attached.teams.find(x => x.id === tm.id)?.isDefault ?? false))}
      {myAgents.map(a => row('agent', a.id, a.name,
        attachedAgentIds.has(a.id),
        attached.agents.find(x => x.id === a.id)?.isDefault ?? false))}
    </section>
  );
}
