import { useCallback, useEffect, useState } from 'react';
import { Bot, Plus, Users } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';
import AgentEntityRow from './AgentEntityRow';
import {
  attachAgentToKb, attachTeamToKb, detachAgentFromKb, detachTeamFromKb,
  fetchKbAgents, listAgents, listTeams,
  type AgentRecord, type KbAgentOption, type KbAgents, type TeamRecord,
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

/**
 * One row in the section: an agent or team that is either the caller's own, or
 * attached to this KB, or both.
 *
 * `owned` is what separates the two sources. It is not a permission — attach,
 * detach and "make default" all work on a foreign entry, because the backend
 * gates those on the KB role plus the link, not on who created the agent — it
 * is what the row SAYS, so an admin is not left wondering why an entry has no
 * edit affordance.
 */
interface Entry {
  id: string;
  name: string;
  owned: boolean;
  attached: boolean;
  isDefault: boolean;
}

/**
 * Merge "my agents/teams" with "what is attached to this KB" into one list.
 *
 * The section used to render `myAgents`/`myTeams` alone, which meant an agent
 * attached to this KB but created by a co-admin had NO ROW: the tab showed no
 * default selected and offered no detach, while the Workflow tab — one click
 * away in the same panel — named it. Foreign-owned attachments are the expected
 * case now that binding is a KB-admin decision rather than an owner one, so the
 * attached set has to be a source here, not just a lookup.
 *
 * Attached wins on name/default (it is the KB's own view of the entry); `owned`
 * survives from the caller's list. Re-sorted by name because both inputs are
 * sorted individually and a concatenation of two sorted lists is not sorted.
 *
 * Note the attached list is the chat picker's read, which filters disabled
 * agents away — so a bound-but-disabled entry still has no row here. That state
 * is surfaced on the Workflow tab, which reads a query that keeps them.
 */
function mergeEntries(
  mine: { id: string; name: string }[],
  attached: KbAgentOption[],
): Entry[] {
  const byId = new Map<string, Entry>();
  for (const m of mine) {
    byId.set(m.id, { id: m.id, name: m.name, owned: true, attached: false, isDefault: false });
  }
  for (const a of attached) {
    const prev = byId.get(a.id);
    byId.set(a.id, {
      id: a.id,
      name: a.name,
      owned: prev?.owned ?? false,
      attached: true,
      isDefault: a.isDefault,
    });
  }
  return [...byId.values()].sort((x, y) => x.name.localeCompare(y.name, 'de'));
}

// Per-KB attach/detach UI for every agent and team the caller can act on here —
// their own, plus everything already attached to this KB — with a single
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

  const agentEntries = mergeEntries(myAgents, attached.agents);
  const teamEntries = mergeEntries(myTeams, attached.teams);

  const row = (kind: 'agent' | 'team', e: Entry) => (
    <AgentEntityRow
      key={`${kind}-${e.id}`}
      icon={kind === 'agent' ? <Bot size={15} aria-hidden="true" /> : <Users size={15} aria-hidden="true" />}
      name={e.name}
      // Said once, quietly, on the row it applies to. A foreign entry is fully
      // operable here; what it is not is editable, and the muted secondary line
      // is where that belongs — not in a badge competing with the actions.
      secondary={e.owned ? undefined : t('kbAgentsForeign')}
      actions={!kbId ? null : (
        <>
          {e.attached && (
            <label className="form-hint" style={{ display: 'flex', gap: '0.25rem', alignItems: 'center', margin: 0 }}>
              <input
                type="radio"
                name={`kb-default-${kbId}`}
                checked={e.isDefault}
                onChange={async () => {
                  try {
                    if (kind === 'agent') await attachAgentToKb(kbId, e.id, true);
                    else await attachTeamToKb(kbId, e.id, true);
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
                  if (e.attached) await detachAgentFromKb(kbId, e.id);
                  else await attachAgentToKb(kbId, e.id);
                } else {
                  if (e.attached) await detachTeamFromKb(kbId, e.id);
                  else await attachTeamToKb(kbId, e.id);
                }
                reload();
              } catch {
                toast.error(t('settingsUpdateError'));
              }
            }}
          >
            {e.attached ? t('kbAgentsDetach') : t('kbAgentsAttach')}
          </button>
        </>
      )}
    />
  );

  // Always rendered. This section used to `return null` when the user owned no
  // agents, which meant someone who had never created one saw no trace of the
  // feature in KB settings — the exact place they'd be deciding how the KB
  // should behave. The empty state is the entry point instead.
  //
  // Counted over the MERGED lists: an admin who owns nothing but whose KB has a
  // co-admin's agent attached used to fall into the empty state and see no
  // rows at all — the same defect one level up from the missing row itself.
  const isEmpty = agentEntries.length === 0 && teamEntries.length === 0;

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

          {tab === 'agents' && (agentEntries.length === 0
            ? <p className="form-hint">{t('noAgentsYet')}</p>
            : agentEntries.map(e => row('agent', e)))}

          {tab === 'teams' && (teamEntries.length === 0
            ? <p className="form-hint">{t('noTeamsYet')}</p>
            : teamEntries.map(e => row('team', e)))}

          {!kbId && <p className="form-hint">{t('kbAgentsNoKbNote')}</p>}
        </>
      )}
    </section>
  );
}
