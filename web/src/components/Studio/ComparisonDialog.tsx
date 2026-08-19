import { useState } from 'react';
import { useTheme } from '../../contexts/ThemeContext';
import { WorkspacePromptDialog, type Preset } from './WorkspacePromptDialog';
import type { AgentSelection } from '../../hooks/useKbSettings';

export type ComparisonMode = 'contradiction' | 'formal' | 'completeness';

const MODES: { id: ComparisonMode; key: string }[] = [
  { id: 'contradiction', key: 'comparisonModeContradiction' },
  { id: 'formal', key: 'comparisonModeFormal' },
  { id: 'completeness', key: 'comparisonModeCompleteness' },
];

/**
 * Workspace tile dialog for in-chat document comparison. Built on the same
 * `WorkspacePromptDialog` shell as "New analysis" (Task 13/14), adding a file
 * picker and mode chips via `extraFields`. Submitting starts a real chat turn
 * via `StudioWorkspace`'s `onStartComparison` — see `useChat.startComparison`
 * for why (backend `buildFollowUpContext` needs the resulting chat).
 */
export function ComparisonDialog({ open, kbId, presets, busy, onStart, onClose }: {
  open: boolean;
  kbId: string;
  presets: Preset[];
  busy?: boolean;
  onStart: (v: { file: File; modes: ComparisonMode[]; instruction: string; agentSelection: AgentSelection }) => void;
  onClose: () => void;
}) {
  const { t } = useTheme();
  const [file, setFile] = useState<File | null>(null);
  const [modes, setModes] = useState<ComparisonMode[]>(['contradiction']);

  const toggle = (m: ComparisonMode) =>
    setModes(prev => prev.includes(m) ? prev.filter(x => x !== m) : [...prev, m]);

  return (
    <WorkspacePromptDialog
      open={open}
      title={t('documentComparison')}
      submitLabel={t('start')}
      presets={presets}
      kbId={kbId}
      busy={busy}
      submitDisabled={!file || modes.length === 0}
      requirePrompt={false}
      onClose={onClose}
      onSubmit={({ prompt, agentSelection }) => {
        if (!file) return;
        onStart({ file, modes, instruction: prompt, agentSelection });
      }}
      extraFields={
        <>
          <label htmlFor="comparison-file" className="form-hint workspace-dialog__label">{t('comparisonFileLabel')}</label>
          <input
            id="comparison-file"
            type="file"
            className="workspace-dialog__input"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
          <fieldset className="workspace-dialog__modes">
            <legend className="form-hint">{t('comparisonModesLabel')}</legend>
            {MODES.map(m => (
              <button
                key={m.id}
                type="button"
                aria-pressed={modes.includes(m.id)}
                className={modes.includes(m.id) ? 'mode active' : 'mode'}
                onClick={() => toggle(m.id)}
              >
                {t(m.key)}
              </button>
            ))}
          </fieldset>
        </>
      }
    />
  );
}
