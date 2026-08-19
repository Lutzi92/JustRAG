import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { StudioWorkspace } from './StudioWorkspace';
import { useWorkspacePresets } from '../../hooks/useWorkspacePresets';

// Renders `value` so the "folgt einem Wechsel der Prop" test can observe the
// editor-content effect (StudioWorkspace.tsx) rather than only the header
// title, which is driven directly by the `selectedItem` prop regardless of
// that effect.
vi.mock('./MarkdownEditor', () => ({ MarkdownEditor: ({ value }: { value: string }) => <div data-testid="md-editor">{value}</div> }));
vi.mock('./QuizView', () => ({ QuizView: () => null }));
vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'de' }) }));
vi.mock('../../contexts/ToastContext', () => ({ useToast: () => ({ error: vi.fn(), success: vi.fn() }) }));
vi.mock('axios');
// vi.fn() (not a static factory) so the two new "Dokumentenvergleich"
// dialog tests below can turn compareEnabled on per-test without disturbing
// every other test in this file, which relies on the false default (no
// comparison tile rendered).
vi.mock('../../hooks/useWorkspacePresets', () => ({ useWorkspacePresets: vi.fn() }));
const mockedUseWorkspacePresets = vi.mocked(useWorkspacePresets);
// Both WorkspacePromptDialog and the AgentPicker it renders call
// useKbAgents(kbId) directly, so mocking the hook (rather than mocking
// AgentPicker away) is what actually produces the `teamId: 't1'` default —
// see the identical pattern/comment in WorkspacePromptDialog.test.tsx.
vi.mock('../../hooks/useKbAgents', () => ({
  useKbAgents: () => ({
    agents: [],
    teams: [{ id: 't1', name: 'CERT-Analyse', description: '', icon: '', isDefault: true }],
  }),
}));

const item = { id: 'g1', kbId: 'kb1', userId: 'u1', title: 'Analyse', createdAt: '2026-08-16T00:00:00Z', type: 'analysis', content: { text: 'Hallo' } } as never;

beforeEach(() => {
  mockedUseWorkspacePresets.mockReturnValue({
    analysis: [{ label: 'Risiken', prompt: 'Nenne die Risiken.' }],
    comparison: [],
    compareEnabled: false,
  });
});

describe('StudioWorkspace', () => {
  it('führt keine eigene Artefaktliste mehr', () => {
    render(
      <StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={item} />,
    );
    // Die frühere Assertion suchte hier den Text 'generatedContentHeader'.
    // Dieser Übersetzungsschlüssel ist mitsamt der inneren Artefaktliste
    // gelöscht, also konnte sie nicht mehr fehlschlagen. Was tatsächlich
    // regressieren könnte, ist eine zweite Liste neben dem Verlauf — dafür
    // ist die Abwesenheit eines listbox/list-Elements der belastbare Test.
    expect(screen.queryAllByRole('list')).toHaveLength(0);
  });

  it('zeigt das über die Prop gewählte Artefakt', () => {
    render(
      <StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={item} />,
    );
    expect(screen.getByTestId('md-editor')).toBeInTheDocument();
  });

  it('folgt einem Wechsel der Prop', () => {
    // content.text differs from `item` deliberately: the heading alone
    // (driven directly by the selectedItem prop) can't distinguish a real
    // prop-follow from a stale-effect bug, so the editor value is checked too.
    const other = { ...(item as object), id: 'g2', title: 'FAQ', type: 'faq', content: { text: 'Andere Notiz' } } as never;
    const { rerender } = render(
      <StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={item} />,
    );
    expect(screen.getByRole('heading', { name: 'Analyse' })).toBeInTheDocument();
    expect(screen.getByTestId('md-editor')).toHaveTextContent('Hallo');
    rerender(
      <StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={other} />,
    );
    expect(screen.getByRole('heading', { name: 'FAQ' })).toBeInTheDocument();
    expect(screen.getByTestId('md-editor')).toHaveTextContent('Andere Notiz');
  });

  it('zeigt ohne Auswahl das Kachelgrid', () => {
    render(
      <StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={null} />,
    );
    // Two matches by design, unrelated to this task: the always-visible tile
    // in studio-workspace__tiles plus the contextual hint button rendered by
    // the empty-state body when nothing is selected (both pre-date Task 5).
    expect(screen.getAllByRole('button', { name: /newAnalysis/ })).toHaveLength(2);
  });

  it('zeigt alle Kacheln, unabhängig von jeder KB-Einstellung', () => {
    render(
      <StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={null} />,
    );
    for (const key of ['newAnalysis', 'flashcards', 'slides', 'podcast', 'abstract',
                       'chart', 'briefingDoc', 'faq', 'studyGuide', 'timeline', 'quiz']) {
      // newAnalysis renders twice (tile grid + empty-state hint button), every
      // other tile exactly once — see the length-2 assertion above.
      if (key === 'newAnalysis') {
        expect(screen.getAllByRole('button', { name: new RegExp(key) }).length).toBeGreaterThan(0);
      } else {
        expect(screen.getByRole('button', { name: new RegExp(key) })).toBeInTheDocument();
      }
    }
  });

  it('schickt Prompt und Agent-Auswahl an die Analyse', async () => {
    const post = vi.mocked(axios.post).mockResolvedValue({ data: { record: { id: 'g9', type: 'analysis', title: 'A', content: { text: 'x' }, createdAt: '2026-08-18T00:00:00Z' }, degradedReason: '' } });
    render(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={null} />);

    // Two "newAnalysis" buttons render with no selection (tile grid + the
    // empty-state hint button, see 'zeigt ohne Auswahl das Kachelgrid' above)
    // — either opens the same dialog via handleAnalyzeRequest.
    await userEvent.click(screen.getAllByRole('button', { name: /newAnalysis/ })[0]);
    await userEvent.selectOptions(screen.getByLabelText('promptPreset'), 'Risiken');
    await userEvent.click(screen.getByRole('button', { name: 'start' }));

    expect(post).toHaveBeenCalledWith(
      expect.stringContaining('/api/kb/kb1/generate/analysis'),
      expect.objectContaining({ topic: 'Nenne die Risiken.', teamId: 't1' }),
    );
  });

  it('zeigt den Degradationshinweis, wenn der Agent nicht verfügbar war', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: { record: { id: 'g9', type: 'analysis', title: 'A', content: { text: 'x' }, createdAt: '2026-08-18T00:00:00Z' }, degradedReason: 'load_failed' } });
    render(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={null} />);

    await userEvent.click(screen.getAllByRole('button', { name: /newAnalysis/ })[0]);
    await userEvent.type(screen.getByLabelText('prompt'), 'Frage');
    await userEvent.click(screen.getByRole('button', { name: 'start' }));

    expect(await screen.findByText('analysisDegradedNoAgent')).toBeInTheDocument();
  });

  // Dismissal behaviour (this task's own UX call, not specified by the brief):
  // the hint is bound to the id of the artifact it describes. It must survive
  // the very same prop update that `onAnalysisCreated` drives in production
  // (the parent selects the freshly created artifact right after receiving
  // it — see ChatView.tsx's onAnalysisCreated) but must NOT survive a later,
  // genuine switch to a different artifact. Both directions are covered
  // because a naive "clear on any selectedItem change" implementation passes
  // the second test here while silently breaking the first in production.
  const createdRecord = { id: 'g9', kbId: 'kb1', userId: 'u1', title: 'A', createdAt: '2026-08-18T00:00:00Z', type: 'analysis', content: { text: 'x' } } as never;

  it('behält den Degradationshinweis, wenn das gerade erzeugte Artefakt ausgewählt wird', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: { record: createdRecord, degradedReason: 'load_failed' } });
    const { rerender } = render(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={null} />);

    await userEvent.click(screen.getAllByRole('button', { name: /newAnalysis/ })[0]);
    await userEvent.type(screen.getByLabelText('prompt'), 'Frage');
    await userEvent.click(screen.getByRole('button', { name: 'start' }));
    expect(await screen.findByText('analysisDegradedNoAgent')).toBeInTheDocument();

    // Simulates ChatView's onAnalysisCreated selecting the new artifact.
    rerender(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={createdRecord} />);
    expect(screen.getByText('analysisDegradedNoAgent')).toBeInTheDocument();
  });

  it('verwirft den Degradationshinweis, sobald ein anderes Artefakt gewählt wird', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: { record: createdRecord, degradedReason: 'load_failed' } });
    const { rerender } = render(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={null} />);

    await userEvent.click(screen.getAllByRole('button', { name: /newAnalysis/ })[0]);
    await userEvent.type(screen.getByLabelText('prompt'), 'Frage');
    await userEvent.click(screen.getByRole('button', { name: 'start' }));
    expect(await screen.findByText('analysisDegradedNoAgent')).toBeInTheDocument();

    rerender(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={item} />);
    expect(screen.queryByText('analysisDegradedNoAgent')).not.toBeInTheDocument();
  });

  // Fix wave item 3 follow-up (coordinator re-review): the "dialog stays
  // open on a failed comparison" behaviour lives at this layer, not in
  // ChatView — StudioWorkspace is the one that awaits onStartComparison and
  // decides whether to close the dialog (`started !== false`). The
  // production caller (ChatView.tsx) is exercised separately by
  // ChatView.composer.test.ts-adjacent coverage; here we drive the contract
  // directly against onStartComparison's return value.
  describe('Dokumentenvergleich-Dialog bleibt bei Fehlschlag offen', () => {
    const openComparisonDialog = async () => {
      await userEvent.click(screen.getByRole('button', { name: /documentComparison/ }));
      await userEvent.upload(
        screen.getByLabelText('comparisonFileLabel'),
        new File(['x'], 'entwurf.docx', { type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' }),
      );
    };

    it('bleibt offen, wenn onStartComparison false zurückgibt (fehlgeschlagener Upload)', async () => {
      mockedUseWorkspacePresets.mockReturnValue({ analysis: [], comparison: [], compareEnabled: true });
      const onStartComparison = vi.fn().mockResolvedValue(false);
      render(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={null} onStartComparison={onStartComparison} />);

      await openComparisonDialog();
      await userEvent.click(screen.getByRole('button', { name: 'start' }));

      expect(onStartComparison).toHaveBeenCalled();
      // The dialog (its own "Abbrechen" button) is still mounted.
      expect(await screen.findByRole('button', { name: 'cancel' })).toBeInTheDocument();
    });

    it('schließt sich, wenn onStartComparison true zurückgibt (erfolgreich gestartet)', async () => {
      mockedUseWorkspacePresets.mockReturnValue({ analysis: [], comparison: [], compareEnabled: true });
      const onStartComparison = vi.fn().mockResolvedValue(true);
      render(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={null} onStartComparison={onStartComparison} />);

      await openComparisonDialog();
      await userEvent.click(screen.getByRole('button', { name: 'start' }));

      expect(onStartComparison).toHaveBeenCalled();
      await screen.findByRole('button', { name: /documentComparison/ });
      expect(screen.queryByRole('button', { name: 'cancel' })).not.toBeInTheDocument();
    });
  });

  // Fix wave item 2 follow-up (coordinator re-review, low-priority): a
  // malformed quiz artifact (content is not an array, so QuizView can't
  // render it) must still fall back to the JSON dump — ContentModal had
  // this fallback, and the array-exclusion-list refactor in this file
  // dropped it for 'quiz' specifically (it kept it for every other type).
  it('zeigt einen JSON-Dump für ein Quiz-Artefakt mit fehlerhaftem content (kein Array)', () => {
    const malformedQuiz = { id: 'g6', kbId: 'kb1', userId: 'u1', title: 'Kaputtes Quiz', createdAt: '2026-08-19T00:00:00Z', type: 'quiz', content: { not: 'an-array' } } as never;
    render(<StudioWorkspace kbId="kb1" onGenerate={vi.fn()} onClose={vi.fn()} selectedItem={malformedQuiz} />);
    expect(screen.getByText(/an-array/)).toBeInTheDocument();
  });
});
