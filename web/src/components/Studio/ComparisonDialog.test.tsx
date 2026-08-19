import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ComparisonDialog } from './ComparisonDialog';

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'de' }) }));
vi.mock('../../hooks/useKbAgents', () => ({
  useKbAgents: () => ({
    agents: [{ id: 'a1', name: 'Prüfer', isDefault: false }],
    teams: [{ id: 't1', name: 'CERT-Analyse', isDefault: true }],
  }),
}));

const presets = [{ label: 'Abweichungen', prompt: 'Fasse die Abweichungen zusammen.' }];
const base = { open: true, kbId: 'kb1', presets, onStart: vi.fn(), onClose: vi.fn() };
const pickFile = async () => {
  await userEvent.upload(
    screen.getByLabelText('comparisonFileLabel'),
    new File(['x'], 'entwurf.docx', { type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' }),
  );
};

beforeEach(() => vi.clearAllMocks());

describe('ComparisonDialog', () => {
  it('sperrt Start ohne Datei', async () => {
    render(<ComparisonDialog {...base} />);
    await userEvent.type(screen.getByLabelText('prompt'), 'Frage');
    expect(screen.getByRole('button', { name: 'start' })).toBeDisabled();
  });

  it('sperrt Start, wenn kein Modus gewählt ist', async () => {
    render(<ComparisonDialog {...base} />);
    await pickFile();
    await userEvent.type(screen.getByLabelText('prompt'), 'Frage');
    // 'contradiction' ist vorbelegt; abwählen leert die Menge.
    await userEvent.click(screen.getByRole('button', { name: 'comparisonModeContradiction' }));
    expect(screen.getByRole('button', { name: 'start' })).toBeDisabled();
  });

  // Fix wave M1: the old composer allowed starting a comparison with an
  // attachment + modes and an empty message box — `buildComparisonSend`'s
  // `fallbackMessage` exists for exactly this case. The dialog previously
  // inherited WorkspacePromptDialog's `!prompt.trim()` guard unconditionally,
  // making that fallback path unreachable from the UI.
  it('lässt Start ohne Prompt zu, wenn Datei und Modus gewählt sind', async () => {
    const onStart = vi.fn();
    render(<ComparisonDialog {...base} onStart={onStart} />);
    await pickFile();
    expect(screen.getByRole('button', { name: 'start' })).toBeEnabled();

    await userEvent.click(screen.getByRole('button', { name: 'start' }));
    expect(onStart).toHaveBeenCalledWith(expect.objectContaining({ instruction: '' }));
  });

  it('reicht Datei, Modi, Prompt und Agent-Auswahl durch', async () => {
    const onStart = vi.fn();
    render(<ComparisonDialog {...base} onStart={onStart} />);
    await pickFile();
    await userEvent.click(screen.getByRole('button', { name: 'comparisonModeFormal' }));
    await userEvent.selectOptions(screen.getByLabelText('promptPreset'), 'Abweichungen');
    await userEvent.click(screen.getByRole('button', { name: 'start' }));

    expect(onStart).toHaveBeenCalledWith({
      file: expect.objectContaining({ name: 'entwurf.docx' }),
      modes: ['contradiction', 'formal'],
      instruction: 'Fasse die Abweichungen zusammen.',
      agentSelection: { teamId: 't1' },
    });
  });
});
