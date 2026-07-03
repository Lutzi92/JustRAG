import { useState } from 'react';
import { useTheme } from '../../contexts/ThemeContext';
import type { AgentRecord, TeamRecord, TeamUpsert } from './api';

interface Props {
  initial?: TeamRecord;
  agents: AgentRecord[]; // the user's agents (member candidates)
  onSave: (tm: TeamUpsert) => Promise<void>;
  onCancel: () => void;
}

export default function TeamForm({ initial, agents, onSave, onCancel }: Props) {
  const { t } = useTheme();
  const [name, setName] = useState(initial?.name ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [memberIds, setMemberIds] = useState<string[]>(initial?.memberIds ?? []);
  const [maxAgentsPerTurn, setMaxAgentsPerTurn] = useState(initial?.maxAgentsPerTurn ?? 3);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);

  const toggleMember = (id: string) =>
    setMemberIds(prev => prev.includes(id) ? prev.filter(x => x !== id) : [...prev, id]);

  const submit = async () => {
    setSaving(true);
    setError('');
    try {
      await onSave({
        name, description, memberIds, maxAgentsPerTurn,
        icon: initial?.icon ?? 'users',
        isEnabled: initial?.isEnabled ?? true,
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
      <label>{t('agentName')}
        <input value={name} onChange={e => setName(e.target.value)} maxLength={100} required />
      </label>
      <label>{t('agentDescription')}
        <textarea value={description} onChange={e => setDescription(e.target.value)} rows={2} maxLength={1000} />
      </label>
      <fieldset>
        <legend>{t('teamMembers')}</legend>
        <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('teamMembersHelp')}</p>
        {agents.map(a => (
          <label key={a.id} style={{ display: 'block', marginBottom: '0.3rem' }}>
            <input type="checkbox" checked={memberIds.includes(a.id)}
              disabled={!memberIds.includes(a.id) && memberIds.length >= 8}
              onChange={() => toggleMember(a.id)} />
            {' '}{a.name} <span style={{ color: 'var(--text-secondary)', fontSize: '0.8rem' }}>{a.description}</span>
          </label>
        ))}
      </fieldset>
      <label>{t('teamMaxAgents')}
        <input type="number" min={1} max={5} value={maxAgentsPerTurn}
          onChange={e => setMaxAgentsPerTurn(Number(e.target.value) || 3)} style={{ width: 80 }} />
      </label>
      {error && <div role="alert" style={{ color: 'var(--error-text)' }}>{error}</div>}
      <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
        <button type="button" onClick={onCancel}>{t('cancel')}</button>
        <button type="button" onClick={submit} disabled={saving || !name.trim() || memberIds.length === 0}>{t('save')}</button>
      </div>
    </div>
  );
}
