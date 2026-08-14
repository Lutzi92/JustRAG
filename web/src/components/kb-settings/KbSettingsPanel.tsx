import { useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react';
import { motion } from 'framer-motion';
import { SlidersHorizontal, FlaskConical, Workflow, ChevronDown, ChevronRight, Search, Save, RotateCcw, Copy, Check } from 'lucide-react';
import type { KbConfigField, KbSettingsResponse } from '../../types';
import { fetchKbSettings, saveKbSettings, resetKbSetting, reembedKb } from './api';
import { useReducedMotion } from '../../hooks/useReducedMotion';
import { useModalContext } from '../../contexts/ModalContext';
import { copyToClipboard } from '../../utils/clipboard';
import AdminEvalTab from '../admin/AdminEvalTab';
import { WorkflowCanvas } from './workflow/WorkflowCanvas';

interface Props {
  kbId: string;
}

const TABS = [
  { id: 'settings', label: 'RAG Settings', Icon: SlidersHorizontal },
  { id: 'evals', label: 'Evals', Icon: FlaskConical },
  { id: 'workflow', label: 'Workflow', Icon: Workflow },
] as const;

const STORAGE_KEY = 'kb-settings-sections-open-v1';

// draft maps key -> string value being edited. Only keys whose draft differs
// from the effective value are sent on Save.
export function KbSettingsPanel({ kbId }: Props) {
  const reducedMotion = useReducedMotion();
  const { showConfirm, showAlert } = useModalContext();
  const [tab, setTab] = useState<'settings' | 'evals' | 'workflow'>('settings');
  const [data, setData] = useState<KbSettingsResponse | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [filter, setFilter] = useState('');

  // A section is open unless explicitly set to false → default-open.
  const [openMap, setOpenMap] = useState<Record<string, boolean>>(() => {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (raw) return JSON.parse(raw);
    } catch { /* ignore corrupt/disabled storage */ }
    return {};
  });
  useEffect(() => {
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(openMap)); } catch { /* quota/disabled */ }
  }, [openMap]);

  useEffect(() => {
    fetchKbSettings(kbId).then(setData).catch((e) => setError(String(e)));
  }, [kbId]);

  const effective = (key: string): string =>
    draft[key] ?? data?.values[key]?.effective ?? '';
  const isOverridden = (key: string): boolean => data?.values[key]?.override != null;

  const groups = useMemo(() => {
    const g: Record<string, KbConfigField[]> = {};
    for (const f of data?.registry ?? []) (g[f.group] ??= []).push(f);
    return g;
  }, [data]);

  const setVal = (key: string, val: string) => setDraft((d) => ({ ...d, [key]: val }));

  const onSave = async () => {
    setSaving(true);
    setError(null);
    try {
      const changed: Record<string, string> = {};
      for (const [k, v] of Object.entries(draft)) {
        if (v !== (data?.values[k]?.effective ?? '')) changed[k] = v;
      }
      if (Object.keys(changed).length > 0) await saveKbSettings(kbId, changed);

      // Labels of changed settings that only take effect after a re-ingest.
      const reingestLabels = (data?.registry ?? [])
        .filter((f) => f.requiresReingest && changed[f.key] !== undefined)
        .map((f) => f.label);

      const fresh = await fetchKbSettings(kbId);
      setData(fresh);
      setDraft({});

      if (reingestLabels.length > 0) {
        const ok = await showConfirm(
          `These changes (${reingestLabels.join(', ')}) only take effect after re-ingesting all files in this knowledge base. Schedule the re-ingest now?`,
          'Re-ingest required',
        );
        if (ok) {
          const { queued } = await reembedKb(kbId);
          await showAlert(
            queued > 0
              ? `Queued ${queued} file(s) for re-ingest.`
              : 'No files to re-ingest in this knowledge base.',
            'Re-ingest scheduled',
          );
        }
      }
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };

  // Mirrors onSave's try/catch. resetKbSetting throws on !res.ok, and the
  // DELETE can now legitimately fail with 400: clearing an override makes the
  // key fall back to the global value, which may be the enabled half of a
  // mutually-exclusive pair (kbconfig.DeleteSetting -> ValidateConflicts).
  // Without this, the reject would surface only as an unhandled promise and
  // the admin would see the button do nothing, with no explanation.
  const onReset = async (key: string) => {
    setError(null);
    try {
      await resetKbSetting(kbId, key);
      const fresh = await fetchKbSettings(kbId);
      setData(fresh);
      setDraft((d) => {
        // eslint-disable-next-line @typescript-eslint/no-unused-vars
        const { [key]: _removed, ...rest } = d;
        return rest;
      });
    } catch (e) {
      setError(String(e));
    }
  };

  const header = (
    <>
      <KbModelNameRow kbId={kbId} />
      <div style={{ display: 'flex', gap: '1.75rem', borderBottom: '1px solid var(--border-color)', marginBottom: '1.25rem' }}>
        {TABS.map(({ id, label, Icon }) => {
          const active = tab === id;
          return (
            <button
              key={id}
              type="button"
              onClick={() => setTab(id)}
              aria-pressed={active}
              style={{
                display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.6rem 0',
                background: 'none', border: 'none', cursor: 'pointer', position: 'relative',
                color: active ? 'var(--accent-primary)' : 'var(--text-secondary)',
                fontWeight: active ? 600 : 400, fontSize: '0.9rem',
              }}
            >
              <Icon size={16} aria-hidden="true" /> {label}
              {active && (
                <motion.div
                  layoutId={reducedMotion ? undefined : 'kb-settings-tab-underline'}
                  style={{ position: 'absolute', bottom: -1, left: 0, right: 0, height: 2, background: 'var(--accent-primary)' }}
                />
              )}
            </button>
          );
        })}
      </div>
    </>
  );

  if (error) {
    return (
      <div className="kb-settings">
        {header}
        <div role="alert" style={{ color: 'var(--text-primary)' }}>{error}</div>
      </div>
    );
  }
  if (!data) {
    return (
      <div className="kb-settings">
        {header}
        <div style={{ opacity: 0.6 }}>Loading…</div>
      </div>
    );
  }

  const trimmed = filter.trim().toLowerCase();
  const filtering = trimmed.length > 0;
  const fieldMatches = (f: KbConfigField) =>
    !filtering ||
    f.label.toLowerCase().includes(trimmed) ||
    f.key.toLowerCase().includes(trimmed) ||
    (f.help?.toLowerCase().includes(trimmed) ?? false);

  const groupNames = Object.keys(groups);
  const isOpen = (g: string) => openMap[g] !== false;
  const toggle = (g: string) => setOpenMap((p) => ({ ...p, [g]: !(p[g] ?? true) }));
  const setAll = (open: boolean) => setOpenMap(Object.fromEntries(groupNames.map((g) => [g, open])));

  return (
    <div className="kb-settings">
      {header}
      {tab === 'settings' ? (
        <>
          <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap', marginBottom: '1rem' }}>
            <div style={{ position: 'relative', flex: '1 1 240px' }}>
              <Search size={16} aria-hidden="true" style={{ position: 'absolute', left: '0.7rem', top: '50%', transform: 'translateY(-50%)', opacity: 0.55, pointerEvents: 'none' }} />
              <input
                type="text"
                placeholder="Search settings…"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                aria-label="Search settings"
                style={{ width: '100%', background: 'var(--bg-primary)', border: '1px solid var(--border-color)', padding: '0.55rem 0.7rem 0.55rem 2.1rem', borderRadius: 8, color: 'var(--text-primary)' }}
              />
            </div>
            <button type="button" onClick={() => setAll(true)} style={ghostBtn}>Expand all</button>
            <button type="button" onClick={() => setAll(false)} style={ghostBtn}>Collapse all</button>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
            {groupNames.map((group) => {
              const visible = filtering ? groups[group].filter(fieldMatches) : groups[group];
              if (filtering && visible.length === 0) return null;
              const open = filtering ? true : isOpen(group);
              return (
                <Section key={group} title={group} count={visible.length} open={open} onToggle={() => toggle(group)}>
                  {visible.map((f) => {
                    const overridden = isOverridden(f.key);
                    return (
                      <div key={f.key} style={{ display: 'flex', flexDirection: 'column', gap: '0.4rem', maxWidth: 480 }}>
                        <label htmlFor={`field-${f.key}`} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', fontSize: '0.9rem' }}>
                          <span style={{ color: 'var(--text-primary)', fontWeight: 500 }}>{f.label}</span>
                          {overridden && (
                            <span data-testid={`override-badge-${f.key}`} title="Overridden for this KB" style={badgeOverride}>overridden</span>
                          )}
                          {f.requiresReingest && (
                            <span title="Takes effect after re-ingest" style={badgeReingest}>re-ingest</span>
                          )}
                        </label>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.6rem' }}>
                          <FieldInput field={f} value={effective(f.key)} onChange={(v) => setVal(f.key, v)} />
                          {overridden && (
                            <button type="button" onClick={() => onReset(f.key)} title="Reset to global default" style={resetBtn}>
                              <RotateCcw size={13} aria-hidden="true" /> Reset
                            </button>
                          )}
                        </div>
                        {f.help && <p style={{ fontSize: '0.78rem', opacity: 0.6, margin: 0 }}>{f.help}</p>}
                      </div>
                    );
                  })}
                </Section>
              );
            })}
          </div>

          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '1.25rem' }}>
            <button
              type="button"
              onClick={onSave}
              disabled={saving}
              style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', background: 'var(--accent-primary)', color: '#fff', border: 'none', padding: '0.6rem 1.2rem', borderRadius: 8, cursor: saving ? 'default' : 'pointer', opacity: saving ? 0.7 : 1 }}
            >
              <Save size={16} aria-hidden="true" /> Save
            </button>
          </div>
        </>
      ) : tab === 'evals' ? (
        <AdminEvalTab basePath={`/api/kb/${kbId}/eval`} kbId={kbId} />
      ) : (
        <WorkflowCanvas kbId={kbId} />
      )}
    </div>
  );
}

// The OpenAI-compatible endpoint takes "kb-{uuid}" in the `model` field, NOT the
// raw KB uuid — see extractKBID in internal/openaicompat/handler.go. Surfaced
// here because this panel is the only in-KB surface api-users can reach.
function KbModelNameRow({ kbId }: { kbId: string }) {
  const modelName = `kb-${kbId}`;
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(timer);
  }, [copied]);

  const onCopy = async () => {
    if (await copyToClipboard(modelName)) setCopied(true);
  };

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: '0.6rem', flexWrap: 'wrap', marginBottom: '1.1rem' }}>
      <span style={{ fontSize: '0.8rem', color: 'var(--text-secondary)' }}>API model name</span>
      <code
        data-testid="kb-model-value"
        style={{
          fontFamily: 'var(--font-mono, monospace)', fontSize: '0.8rem', color: 'var(--text-primary)',
          background: 'var(--bg-secondary)', border: '1px solid var(--border-color)',
          borderRadius: 6, padding: '0.25rem 0.5rem', userSelect: 'all', wordBreak: 'break-all',
        }}
      >
        {modelName}
      </code>
      <button
        type="button"
        onClick={onCopy}
        title="Copy the model name for the OpenAI-compatible API"
        aria-label="Copy API model name"
        style={{ ...resetBtn, color: copied ? 'var(--accent-primary)' : 'var(--text-secondary)' }}
      >
        {copied ? <Check size={13} aria-hidden="true" /> : <Copy size={13} aria-hidden="true" />}
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  );
}

function Section({ title, count, open, onToggle, children }: { title: string; count: number; open: boolean; onToggle: () => void; children: ReactNode }) {
  return (
    <div style={{ border: '1px solid var(--border-color)', borderRadius: 12, overflow: 'hidden', background: 'var(--bg-secondary)' }}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        style={{ width: '100%', padding: '0.85rem 1.1rem', display: 'flex', alignItems: 'center', justifyContent: 'space-between', background: 'transparent', border: 'none', cursor: 'pointer', color: 'var(--text-primary)', fontSize: '0.95rem', fontWeight: 600, textAlign: 'left' }}
      >
        <span>{title}{' '}<span style={{ opacity: 0.5, fontWeight: 400, fontSize: '0.85rem' }}>({count})</span></span>
        {open ? <ChevronDown size={18} aria-hidden="true" /> : <ChevronRight size={18} aria-hidden="true" />}
      </button>
      {open && (
        <div style={{ padding: '1rem 1.1rem 1.25rem', display: 'flex', flexDirection: 'column', gap: '1.25rem', background: 'var(--bg-primary)', borderTop: '1px solid var(--border-color)' }}>
          {children}
        </div>
      )}
    </div>
  );
}

function FieldInput({ field, value, onChange }: { field: KbConfigField; value: string; onChange: (v: string) => void }) {
  const id = `field-${field.key}`;
  if (field.type === 'bool') {
    return (
      <input
        id={id}
        data-testid={id}
        type="checkbox"
        checked={value === 'true' || value === '1'}
        onChange={(e) => onChange(e.target.checked ? 'true' : 'false')}
        style={{ width: 18, height: 18, cursor: 'pointer' }}
      />
    );
  }
  if (field.type === 'enum') {
    return (
      <select id={id} data-testid={id} value={value} onChange={(e) => onChange(e.target.value)} style={fieldInputStyle}>
        {(field.enum ?? []).map((o) => (
          <option key={o} value={o}>{o}</option>
        ))}
      </select>
    );
  }
  const numeric = field.type === 'int' || field.type === 'float';
  return (
    <input
      id={id}
      data-testid={id}
      type={numeric ? 'number' : 'text'}
      step={field.type === 'float' ? '0.01' : '1'}
      min={field.min}
      max={field.max}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      style={fieldInputStyle}
    />
  );
}

const fieldInputStyle: CSSProperties = {
  background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
  padding: '0.55rem 0.7rem', borderRadius: 8, color: 'var(--text-primary)', minWidth: 160,
};
const ghostBtn: CSSProperties = {
  background: 'transparent', border: '1px solid var(--border-color)', color: 'var(--text-primary)',
  padding: '0.45rem 0.75rem', borderRadius: 8, cursor: 'pointer', fontSize: '0.8rem',
};
const badgeOverride: CSSProperties = {
  fontSize: '0.68rem', fontWeight: 600, color: 'var(--accent-primary)', background: 'var(--tag-bg)',
  border: '1px solid var(--accent-primary)', borderRadius: 6, padding: '0.05rem 0.4rem',
};
const badgeReingest: CSSProperties = {
  fontSize: '0.68rem', color: 'var(--text-secondary)', background: 'var(--bg-secondary)',
  border: '1px solid var(--border-color)', borderRadius: 6, padding: '0.05rem 0.4rem',
};
const resetBtn: CSSProperties = {
  display: 'flex', alignItems: 'center', gap: '0.3rem', fontSize: '0.78rem', color: 'var(--text-secondary)',
  background: 'transparent', border: '1px solid var(--border-color)', borderRadius: 6, padding: '0.3rem 0.55rem', cursor: 'pointer',
};
