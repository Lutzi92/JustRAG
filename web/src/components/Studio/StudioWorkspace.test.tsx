import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { StudioWorkspace } from './StudioWorkspace';

// Renders `value` so the "folgt einem Wechsel der Prop" test can observe the
// editor-content effect (StudioWorkspace.tsx) rather than only the header
// title, which is driven directly by the `selectedItem` prop regardless of
// that effect.
vi.mock('./MarkdownEditor', () => ({ MarkdownEditor: ({ value }: { value: string }) => <div data-testid="md-editor">{value}</div> }));
vi.mock('./QuizView', () => ({ QuizView: () => null }));
vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'de' }) }));
vi.mock('../../contexts/ToastContext', () => ({ useToast: () => ({ error: vi.fn(), success: vi.fn() }) }));

const item = { id: 'g1', kbId: 'kb1', userId: 'u1', title: 'Analyse', createdAt: '2026-08-16T00:00:00Z', type: 'analysis', content: { text: 'Hallo' } } as never;

describe('StudioWorkspace', () => {
  it('führt keine eigene Artefaktliste mehr', () => {
    render(
      <StudioWorkspace kbId="kb1" generatedContent={[item]} onGenerate={vi.fn()}
        onDeleteContent={vi.fn()} onClose={vi.fn()} selectedItem={item} />,
    );
    expect(screen.queryByText('generatedContentHeader')).not.toBeInTheDocument();
  });

  it('zeigt das über die Prop gewählte Artefakt', () => {
    render(
      <StudioWorkspace kbId="kb1" generatedContent={[item]} onGenerate={vi.fn()}
        onDeleteContent={vi.fn()} onClose={vi.fn()} selectedItem={item} />,
    );
    expect(screen.getByTestId('md-editor')).toBeInTheDocument();
  });

  it('folgt einem Wechsel der Prop', () => {
    // content.text differs from `item` deliberately: the heading alone
    // (driven directly by the selectedItem prop) can't distinguish a real
    // prop-follow from a stale-effect bug, so the editor value is checked too.
    const other = { ...(item as object), id: 'g2', title: 'FAQ', type: 'faq', content: { text: 'Andere Notiz' } } as never;
    const { rerender } = render(
      <StudioWorkspace kbId="kb1" generatedContent={[item, other]} onGenerate={vi.fn()}
        onDeleteContent={vi.fn()} onClose={vi.fn()} selectedItem={item} />,
    );
    expect(screen.getByRole('heading', { name: 'Analyse' })).toBeInTheDocument();
    expect(screen.getByTestId('md-editor')).toHaveTextContent('Hallo');
    rerender(
      <StudioWorkspace kbId="kb1" generatedContent={[item, other]} onGenerate={vi.fn()}
        onDeleteContent={vi.fn()} onClose={vi.fn()} selectedItem={other} />,
    );
    expect(screen.getByRole('heading', { name: 'FAQ' })).toBeInTheDocument();
    expect(screen.getByTestId('md-editor')).toHaveTextContent('Andere Notiz');
  });

  it('zeigt ohne Auswahl das Kachelgrid', () => {
    render(
      <StudioWorkspace kbId="kb1" generatedContent={[]} onGenerate={vi.fn()}
        onDeleteContent={vi.fn()} onClose={vi.fn()} selectedItem={null} />,
    );
    // Two matches by design, unrelated to this task: the always-visible tile
    // in studio-workspace__tiles plus the contextual hint button rendered by
    // the empty-state body when nothing is selected (both pre-date Task 5).
    expect(screen.getAllByRole('button', { name: /newAnalysis/ })).toHaveLength(2);
  });

  it('zeigt alle Kacheln, unabhängig von jeder KB-Einstellung', () => {
    render(
      <StudioWorkspace kbId="kb1" generatedContent={[]} onGenerate={vi.fn()}
        onDeleteContent={vi.fn()} onClose={vi.fn()} selectedItem={null} />,
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
});
