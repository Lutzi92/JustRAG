import { useState } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import AgentConfigFields from './AgentConfigFields';
import type { AgentRecord, AgentUpsert, AgentConfigField } from './api';

interface Props {
  initial?: AgentRecord;
  registry: AgentConfigField[];
  availableTools: string[]; // tool names the backend allows (from registry status or hardcoded builtin list; see note)
  availableModels: string[]; // model names from /api/public/configs (passed by AgentsView)
  onSave: (a: AgentUpsert) => Promise<void>;
  onCancel: () => void;
}

export default function AgentForm({ initial, registry, availableTools, availableModels, onSave, onCancel }: Props) {
  const { t } = useTheme();
  const [name, setName] = useState(initial?.name ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [systemPrompt, setSystemPrompt] = useState(initial?.systemPrompt ?? '');
  const [chatModel, setChatModel] = useState(initial?.chatModel ?? '');
  const [toolNames, setToolNames] = useState<string[]>(initial?.toolNames ?? []);
  const [config, setConfig] = useState<Record<string, string>>(initial?.config ?? {});
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);

  const toggleTool = (tool: string) =>
    setToolNames(prev => prev.includes(tool) ? prev.filter(x => x !== tool) : [...prev, tool]);

  const submit = async () => {
    setSaving(true);
    setError('');
    try {
      await onSave({
        name, description, systemPrompt, chatModel, toolNames, config,
        icon: initial?.icon ?? 'bot',
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
        <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('agentDescriptionHelp')}</p>
        <textarea value={description} onChange={e => setDescription(e.target.value)}
          placeholder={t('agentDescriptionPlaceholder')} rows={2} maxLength={1000} />
      </label>
      <label>{t('agentSystemPrompt')}
        <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('agentSystemPromptHelp')}</p>
        <textarea value={systemPrompt} onChange={e => setSystemPrompt(e.target.value)} rows={5} maxLength={8000} />
      </label>
      <label>{t('agentModel')}
        <select value={chatModel} onChange={e => setChatModel(e.target.value)}>
          <option value="">{t('agentModelDefault')}</option>
          {availableModels.map(m => <option key={m} value={m}>{m}</option>)}
        </select>
      </label>
      <fieldset>
        <legend>{t('agentTools')}</legend>
        <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('agentToolsHelp')}</p>
        {availableTools.map(tool => (
          <label key={tool} style={{ display: 'inline-flex', gap: '0.3rem', marginRight: '1rem' }}>
            <input type="checkbox" checked={toolNames.includes(tool)} onChange={() => toggleTool(tool)} />
            {tool}
          </label>
        ))}
      </fieldset>
      <button type="button" onClick={() => setShowAdvanced(v => !v)}
        style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)' }}>
        {showAdvanced ? <ChevronDown size={16} aria-hidden="true" /> : <ChevronRight size={16} aria-hidden="true" />}
        {t('agentAdvanced')}
      </button>
      {showAdvanced && (
        <AgentConfigFields fields={registry} values={config}
          onChange={(key, value) => setConfig(prev => {
            const next = { ...prev };
            if (value === null) delete next[key]; else next[key] = value;
            return next;
          })} />
      )}
      {error && <div role="alert" style={{ color: 'var(--error-text)' }}>{error}</div>}
      <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
        <button type="button" onClick={onCancel}>{t('cancel')}</button>
        <button type="button" onClick={submit} disabled={saving || !name.trim()}>{t('save')}</button>
      </div>
    </div>
  );
}
