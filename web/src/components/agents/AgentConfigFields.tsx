import { useTheme } from '../../contexts/ThemeContext';
import type { AgentConfigField } from './api';

interface Props {
  fields: AgentConfigField[];
  values: Record<string, string>;
  onChange: (key: string, value: string | null) => void; // null = remove override
}

// Renders the per-agent config registry as grouped inputs. Only keys present
// in `values` are overridden; empty input removes the override (inherit KB).
export default function AgentConfigFields({ fields, values, onChange }: Props) {
  const { t } = useTheme();
  const groups = [...new Set(fields.map(f => f.group))];
  return (
    <div>
      {groups.map(group => (
        <fieldset key={group} style={{ border: '1px solid var(--border-color)', borderRadius: 8, marginBottom: '0.75rem', padding: '0.75rem' }}>
          <legend style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', padding: '0 0.4rem' }}>{group}</legend>
          {fields.filter(f => f.group === group).map(f => (
            <label key={f.key} title={f.help} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', marginBottom: '0.4rem', fontSize: '0.85rem' }}>
              <span style={{ flex: 1 }}>{f.label}</span>
              {f.type === 'bool' ? (
                <select
                  value={values[f.key] ?? ''}
                  onChange={e => onChange(f.key, e.target.value || null)}
                >
                  <option value="">{t('agentConfigInherit')}</option>
                  <option value="true">{t('agentConfigOn')}</option>
                  <option value="false">{t('agentConfigOff')}</option>
                </select>
              ) : f.type === 'enum' ? (
                <select value={values[f.key] ?? ''} onChange={e => onChange(f.key, e.target.value || null)}>
                  <option value="">{t('agentConfigInherit')}</option>
                  {(f.enum ?? []).map(v => <option key={v} value={v}>{v}</option>)}
                </select>
              ) : (
                <input
                  type={f.type === 'string' ? 'text' : 'number'}
                  min={f.min}
                  max={f.max}
                  step={f.type === 'float' ? 0.05 : 1}
                  value={values[f.key] ?? ''}
                  onChange={e => onChange(f.key, e.target.value || null)}
                  style={{ width: 110 }}
                />
              )}
            </label>
          ))}
        </fieldset>
      ))}
    </div>
  );
}
