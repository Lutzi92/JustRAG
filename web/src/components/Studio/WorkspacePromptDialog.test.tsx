import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { WorkspacePromptDialog } from './WorkspacePromptDialog';

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'de' }) }));
vi.mock('../../hooks/useKbAgents', () => ({
  useKbAgents: () => ({
    agents: [{ id: 'a1', name: 'Prüfer', isDefault: true }],
    teams: [{ id: 't1', name: 'CERT-Analyse', isDefault: true }],
  }),
}));

const presets = [
  { label: 'Risiken', prompt: 'Nenne die Risiken.' },
  { label: 'Stärken', prompt: 'Nenne die Stärken.' },
];
const base = {
  open: true, title: 'newAnalysis', submitLabel: 'start',
  presets, kbId: 'kb1', onSubmit: vi.fn(), onClose: vi.fn(),
};

beforeEach(() => vi.clearAllMocks());

describe('WorkspacePromptDialog', () => {
  it('füllt das Prompt-Feld aus dem gewählten Preset', async () => {
    render(<WorkspacePromptDialog {...base} />);
    await userEvent.selectOptions(screen.getByLabelText('promptPreset'), 'Risiken');
    expect(screen.getByLabelText('prompt')).toHaveValue('Nenne die Risiken.');
  });

  it('lässt freies Überschreiben zu und ersetzt es bei erneuter Preset-Wahl', async () => {
    render(<WorkspacePromptDialog {...base} />);
    const field = screen.getByLabelText('prompt');
    await userEvent.selectOptions(screen.getByLabelText('promptPreset'), 'Risiken');
    await userEvent.clear(field);
    await userEvent.type(field, 'Eigener Text');
    expect(field).toHaveValue('Eigener Text');

    await userEvent.selectOptions(screen.getByLabelText('promptPreset'), 'Stärken');
    expect(field).toHaveValue('Nenne die Stärken.');
  });

  it('belegt die Agent-Auswahl mit dem Default-Team vor, nicht mit dem Default-Agenten', () => {
    render(<WorkspacePromptDialog {...base} />);
    expect(screen.getByLabelText('agentPicker')).toHaveValue('team:t1');
  });

  it('reicht Prompt und Agent-Auswahl beim Start durch', async () => {
    const onSubmit = vi.fn();
    render(<WorkspacePromptDialog {...base} onSubmit={onSubmit} />);
    await userEvent.type(screen.getByLabelText('prompt'), 'Frage');
    await userEvent.click(screen.getByRole('button', { name: 'start' }));
    expect(onSubmit).toHaveBeenCalledWith({ prompt: 'Frage', agentSelection: { teamId: 't1' } });
  });

  it('sperrt Start bei leerem Prompt', async () => {
    render(<WorkspacePromptDialog {...base} />);
    expect(screen.getByRole('button', { name: 'start' })).toBeDisabled();
  });

  it('sperrt Start, wenn der Aufrufer submitDisabled setzt', async () => {
    render(<WorkspacePromptDialog {...base} submitDisabled />);
    await userEvent.type(screen.getByLabelText('prompt'), 'Frage');
    expect(screen.getByRole('button', { name: 'start' })).toBeDisabled();
  });

  it('rendert nichts, solange open false ist', () => {
    const { container } = render(<WorkspacePromptDialog {...base} open={false} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('startet nach Schließen und erneutem Öffnen mit leerem Prompt-Feld', async () => {
    const { rerender } = render(<WorkspacePromptDialog {...base} />);
    await userEvent.type(screen.getByLabelText('prompt'), 'Alter Text');
    expect(screen.getByLabelText('prompt')).toHaveValue('Alter Text');

    rerender(<WorkspacePromptDialog {...base} open={false} />);
    rerender(<WorkspacePromptDialog {...base} open={true} />);

    expect(screen.getByLabelText('prompt')).toHaveValue('');
  });
});
