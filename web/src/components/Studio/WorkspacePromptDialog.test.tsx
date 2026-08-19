import { render, screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { WorkspacePromptDialog } from './WorkspacePromptDialog';
import { useKbAgents } from '../../hooks/useKbAgents';

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'de' }) }));
// A vi.fn() rather than an inline factory: individual tests drive
// mockReturnValueOnce/mockReturnValue to force the async-arrival orderings
// useKbAgents produces in production (empty on first render, filled in once
// the fetch inside the real hook resolves).
const listAgents = vi.fn();
vi.mock('../agents/api', async (orig) => ({
  ...(await orig() as object),
  listAgents: () => listAgents(),
}));
vi.mock('../../hooks/useKbAgents', () => ({ useKbAgents: vi.fn() }));

const mockedUseKbAgents = vi.mocked(useKbAgents);

/** Fills in the KbAgentOption fields this suite doesn't care about. */
function agentOption(over: { id: string; name: string; isDefault: boolean }) {
  return { description: '', icon: '', ...over };
}

const populatedAgents = {
  agents: [agentOption({ id: 'a1', name: 'Prüfer', isDefault: true })],
  teams: [agentOption({ id: 't1', name: 'CERT-Analyse', isDefault: true })],
};

const presets = [
  { label: 'Risiken', prompt: 'Nenne die Risiken.' },
  { label: 'Stärken', prompt: 'Nenne die Stärken.' },
];
const base = {
  open: true, title: 'newAnalysis', submitLabel: 'start',
  presets, kbId: 'kb1', onSubmit: vi.fn(), onClose: vi.fn(),
};

beforeEach(() => {
  vi.clearAllMocks();
  // Default for every test that doesn't care about the arrival ordering:
  // options are already there from the first render, same as before this
  // hook became a controllable mock.
  mockedUseKbAgents.mockReturnValue(populatedAgents);
  // Every test renders the dialog, and the dialog always asks for the user's
  // own agents. Without a default the mock yields undefined and the hook
  // throws on `.then`.
  listAgents.mockResolvedValue([]);
});

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

  it('übernimmt den Default-Team, sobald die KB-Agent-Optionen verspätet eintreffen', () => {
    // Both this dialog and the AgentPicker it renders call useKbAgents once
    // per render pass, so the empty first-render state needs two queued
    // values before the populated default takes over.
    mockedUseKbAgents
      .mockReturnValueOnce({ agents: [], teams: [] })
      .mockReturnValueOnce({ agents: [], teams: [] })
      .mockReturnValue(populatedAgents);

    const { rerender } = render(<WorkspacePromptDialog {...base} />);
    // AgentPicker renders nothing while both lists are empty.
    expect(screen.queryByLabelText('agentPicker')).toBeNull();

    rerender(<WorkspacePromptDialog {...base} />);
    expect(screen.getByLabelText('agentPicker')).toHaveValue('team:t1');
  });

  it('behält eine bereits getroffene Nutzerwahl, wenn Default-Optionen erst danach eintreffen', async () => {
    // A team already exists (so the picker is interactable) but none of it
    // is flagged isDefault yet; the default-carrying options land only after
    // the user has already picked something explicit.
    const before = { agents: [], teams: [agentOption({ id: 't0', name: 'Sonstiges Team', isDefault: false })] };
    const after = {
      agents: [agentOption({ id: 'a1', name: 'Prüfer', isDefault: true })],
      teams: [
        agentOption({ id: 't0', name: 'Sonstiges Team', isDefault: false }),
        agentOption({ id: 't1', name: 'CERT-Analyse', isDefault: true }),
      ],
    };
    mockedUseKbAgents
      .mockReturnValueOnce(before)
      .mockReturnValueOnce(before)
      .mockReturnValue(after);

    const { rerender } = render(<WorkspacePromptDialog {...base} />);
    await userEvent.selectOptions(screen.getByLabelText('agentPicker'), 'team:t0');
    expect(screen.getByLabelText('agentPicker')).toHaveValue('team:t0');

    // Options refresh (now carrying the default team t1) on a render the
    // user didn't cause — the earlier explicit choice must still win.
    rerender(<WorkspacePromptDialog {...base} />);
    expect(screen.getByLabelText('agentPicker')).toHaveValue('team:t0');
  });
});

// The Start button used the composer's `send-button` class — a 40x40 circle
// (border-radius: 50%) built for an icon — while its Cancel sibling used a
// normal text button. Rendered side by side that put a round button next to a
// rectangular one. The repo's convention is `btn btn--primary` /
// `btn btn--secondary`, so this pins the classes rather than the pixels
// (jsdom does not resolve stylesheets).
describe('WorkspacePromptDialog: Button-Form', () => {
  it('nutzt die Haus-Button-Klassen statt des runden Composer-Knopfs', () => {
    render(<WorkspacePromptDialog {...base} />);
    const start = screen.getByRole('button', { name: 'start' });
    const cancel = screen.getByRole('button', { name: 'cancel' });

    expect(start.className).toContain('btn');
    expect(start.className).toContain('btn--primary');
    expect(start.className).not.toContain('send-button');

    expect(cancel.className).toContain('btn');
    expect(cancel.className).toContain('btn--secondary');
  });
});

// Users create agents under /agents; those agents carry a persona prompt that
// is often exactly what they want to run an analysis or comparison with. The
// preset dropdown therefore offers them alongside the built-in templates.
// Note this is the PROMPT only — running as an agent pipeline stays the job of
// the separate agent picker below the prompt field.
describe('WorkspacePromptDialog: Vorlagen aus eigenen Agenten', () => {
  const agents = [
    { id: 'a1', name: 'Prüfer', systemPrompt: 'Prüfe streng auf Widersprüche.', isEnabled: true },
    { id: 'a2', name: 'Stillgelegt', systemPrompt: 'egal', isEnabled: false },
    { id: 'a3', name: 'Ohne Prompt', systemPrompt: '   ', isEnabled: true },
  ];

  beforeEach(() => { listAgents.mockResolvedValue(agents); });

  it('bietet die Persona-Prompts der eigenen Agenten als Vorlage an', async () => {
    render(<WorkspacePromptDialog {...base} />);
    const select = await screen.findByLabelText('promptPreset');
    await userEvent.selectOptions(select, 'Prüfer');
    expect(screen.getByLabelText('prompt')).toHaveValue('Prüfe streng auf Widersprüche.');
  });

  // Scope every option lookup to the PRESET select: the agent picker below it
  // renders <option> elements too, and the useKbAgents mock happens to contain
  // an agent called "Prüfer" — an unscoped query matches that one and passes
  // whether or not this feature exists at all.
  const presetOptions = async () => {
    const select = await screen.findByLabelText('promptPreset');
    return within(select as HTMLElement);
  };

  it('lässt deaktivierte Agenten und solche ohne Persona-Prompt weg', async () => {
    render(<WorkspacePromptDialog {...base} />);
    const opts = await presetOptions();
    await waitFor(() => expect(opts.getByRole('option', { name: 'Prüfer' })).toBeInTheDocument());
    expect(opts.queryByRole('option', { name: 'Stillgelegt' })).not.toBeInTheDocument();
    expect(opts.queryByRole('option', { name: 'Ohne Prompt' })).not.toBeInTheDocument();
  });

  it('zeigt weiterhin die eingebauten Vorlagen', async () => {
    render(<WorkspacePromptDialog {...base} />);
    const opts = await presetOptions();
    await waitFor(() => expect(opts.getByRole('option', { name: 'Prüfer' })).toBeInTheDocument());
    expect(opts.getByRole('option', { name: 'Risiken' })).toBeInTheDocument();
  });
});
