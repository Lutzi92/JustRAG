import React, { useState } from 'react';
import { X } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { useKbAgents } from '../../hooks/useKbAgents';
import { AgentPicker } from '../agents/AgentPicker';
import type { AgentSelection } from '../../hooks/useKbSettings';
import './WorkspacePromptDialog.css';

export interface Preset {
  label: string;
  prompt: string;
}

export interface WorkspacePromptDialogProps {
  open: boolean;
  title: string;
  submitLabel: string;
  presets: Preset[];
  kbId: string;
  /** Zusätzliche Felder über dem Prompt-Feld (Datei, Modi). */
  extraFields?: React.ReactNode;
  /** Sperrt „Start“, z. B. solange keine Datei gewählt ist. */
  submitDisabled?: boolean;
  /**
   * Ob ein nicht-leeres Prompt-Feld Voraussetzung für „Start“ ist. Default
   * `true` (Verhalten von „Neue Analyse“). Der Vergleichsdialog setzt dies auf
   * `false`: Datei + Modi sind dort ausreichende Absicht, das Prompt-Feld ist
   * nur eine optionale zusätzliche Anweisung (siehe `buildComparisonSend`s
   * `fallbackMessage`, die genau diesen leeren Fall trägt).
   */
  requirePrompt?: boolean;
  busy?: boolean;
  onSubmit: (value: { prompt: string; agentSelection: AgentSelection }) => void;
  onClose: () => void;
}

const NO_SELECTION: AgentSelection = {};

/**
 * Gemeinsame Basis der beiden Workspace-Dialoge („Neue Analyse“,
 * „Dokumentenvergleich“): Preset-Auswahl, freies Prompt-Feld und
 * Agent-/Team-Auswahl. Der Vergleich reicht Dateiauswahl und Modus-Chips über
 * `extraFields` ein — die sind nur dort sinnvoll und gehören nicht in die
 * gemeinsame Hülle.
 *
 * Die Agent-Auswahl wird über die bestehende `AgentPicker`-Komponente
 * gerendert (gleiche Options-Gruppierung, gleiche `team:`/`agent:`-Kodierung
 * wie ChatView) statt sie hier zu duplizieren; dieser Dialog steuert nur,
 * *welcher* Wert vorbelegt ist. Die Auswahl ist wie ein neuer Chat vorbelegt:
 * Default-Team vor Default-Agent.
 *
 * Der Dialog bleibt bei `open=false` gemountet (Hooks stehen vor dem frühen
 * Return), damit die Hook-Reihenfolge stabil bleibt. Damit ein erneutes
 * Öffnen nicht den zuletzt eingegebenen Text zeigt, wird der Übergang
 * geschlossen→offen während des Renderns erkannt und der lokale Zustand dort
 * zurückgesetzt (React-Pattern „Zustand beim Prop-Wechsel anpassen“, ohne den
 * sichtbaren Zwischenzustand eines Effekts nach dem ersten Paint).
 */
export function WorkspacePromptDialog({
  open, title, submitLabel, presets, kbId, extraFields,
  submitDisabled, requirePrompt = true, busy, onSubmit, onClose,
}: WorkspacePromptDialogProps) {
  const { t } = useTheme();
  const options = useKbAgents(kbId);
  const [prompt, setPrompt] = useState('');
  const [presetLabel, setPresetLabel] = useState('');
  const [touchedSelection, setTouchedSelection] = useState<AgentSelection | null>(null);

  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setPrompt('');
      setPresetLabel('');
      setTouchedSelection(null);
    }
  }

  // Nicht-gesetzter Zustand bedeutet „noch nie angefasst" und folgt damit dem
  // KB-Default, auch wenn der erst nach dem ersten Rendern eintrifft.
  const defaultTeam = options.teams.find(x => x.isDefault);
  const defaultAgent = options.agents.find(x => x.isDefault);
  const defaultSelection: AgentSelection = defaultTeam
    ? { teamId: defaultTeam.id }
    : defaultAgent ? { agentId: defaultAgent.id } : NO_SELECTION;

  const selection = touchedSelection ?? defaultSelection;

  if (!open) return null;

  return (
    <div className="modal-overlay workspace-dialog__overlay" role="presentation" onClick={onClose}>
      {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions */}
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="modal-content workspace-dialog"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => { if (e.key === 'Escape') onClose(); }}
      >
        <div className="workspace-dialog__header">
          <h3>{title}</h3>
          <button type="button" onClick={onClose} aria-label={t('close')} className="workspace-dialog__close">
            <X size={20} />
          </button>
        </div>

        {extraFields}

        <label htmlFor="workspace-preset" className="form-hint workspace-dialog__label">{t('promptPreset')}</label>
        <select
          id="workspace-preset"
          className="workspace-dialog__input"
          value={presetLabel}
          onChange={(e) => {
            const label = e.target.value;
            setPresetLabel(label);
            const p = presets.find(x => x.label === label);
            if (p) setPrompt(p.prompt);
          }}
        >
          <option value="">{t('promptPresetOwn')}</option>
          {presets.map(p => <option key={p.label} value={p.label}>{p.label}</option>)}
        </select>

        <label htmlFor="workspace-prompt" className="form-hint workspace-dialog__label">{t('prompt')}</label>
        <textarea
          id="workspace-prompt"
          rows={5}
          className="workspace-dialog__input workspace-dialog__textarea"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
        />

        <AgentPicker
          kbId={kbId}
          selection={selection}
          onSelect={setTouchedSelection}
        />

        <div className="workspace-dialog__actions">
          <button type="button" className="btn btn--secondary" onClick={onClose}>{t('cancel')}</button>
          <button
            type="button"
            className="btn btn--primary"
            disabled={busy || submitDisabled || (requirePrompt && !prompt.trim())}
            onClick={() => onSubmit({ prompt: prompt.trim(), agentSelection: selection })}
          >
            {submitLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
