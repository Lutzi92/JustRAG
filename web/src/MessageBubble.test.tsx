import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import MessageBubble from './MessageBubble';
import { useMessageSections } from './hooks/useMessageSections';
import type { Message, MessageConflict } from './types';

// MessageBubble pulls in ThemeContext (t/language), AuthContext + ToastContext
// (via the always-mounted §8 footer <MessageActions variant="inline">) and
// useReducedMotion (window.matchMedia, not available in jsdom by default —
// same gap documented in Login.test.tsx). `t` is the identity function so
// assertions can match on the raw translation keys instead of duplicating
// the German/English copy here.
vi.mock('./contexts/ThemeContext', () => ({
  useTheme: () => ({ t: (key: string) => key, language: 'en' as const }),
}));
vi.mock('./contexts/ToastContext', () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}));
vi.mock('./contexts/AuthContext', () => ({
  useAuth: () => ({ user: null, siteConfigs: {} }),
}));
vi.mock('./hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

// MessageBubble's expand/collapse state is deliberately NOT internal — the
// real chat view owns it (useMessageSections) so it survives virtualized
// remounts (see that hook's doc comment and ComparisonView, its only current
// caller). A bare `<MessageBubble conflicts={...} />` with no `onToggleSection`
// would render a badge whose click is a no-op forever. This harness wires the
// same real hook the production callers use, so clicking the badge behaves
// exactly as it does in the app.
function Harness({ message }: { message: Message }) {
  const sections = useMessageSections();
  return (
    <MessageBubble
      message={message}
      conflictsOpen={sections.isOpen(message.id, 'conflicts')}
      onToggleSection={sections.toggle}
    />
  );
}

const baseMessage: Message = {
  id: 'ai1',
  role: 'ai',
  content: 'Die Beitragshöhe beträgt 50 EUR.',
};

const conflict: MessageConflict = {
  claim: 'Beitragshöhe',
  sourceA: 1,
  sourceB: 2,
  kind: 'contradiction',
  newer: 'unknown',
  fileA: 'alt.md',
  fileB: 'neu.md',
};

describe('MessageBubble — conflicting-sources badge', () => {
  it('shows no badge when the message has no conflicts', () => {
    render(<Harness message={baseMessage} />);
    expect(screen.queryByLabelText('conflictsBadge')).not.toBeInTheDocument();
  });

  it('shows no badge when conflicts is an empty array', () => {
    render(<Harness message={{ ...baseMessage, conflicts: [] }} />);
    expect(screen.queryByLabelText('conflictsBadge')).not.toBeInTheDocument();
  });

  it('shows the badge with the conflict count when conflicts are present', () => {
    render(<Harness message={{ ...baseMessage, conflicts: [conflict] }} />);
    const badge = screen.getByLabelText('conflictsBadge');
    expect(badge).toBeInTheDocument();
    expect(badge).toHaveTextContent('1');
  });

  it('does not show the details panel before the badge is clicked', () => {
    render(<Harness message={{ ...baseMessage, conflicts: [conflict] }} />);
    expect(screen.queryByText('Beitragshöhe')).not.toBeInTheDocument();
  });

  it('lists the claim, both file names and the kind label on click', async () => {
    const user = userEvent.setup();
    render(<Harness message={{ ...baseMessage, conflicts: [conflict] }} />);

    await user.click(screen.getByLabelText('conflictsBadge'));

    expect(screen.getByText('Beitragshöhe')).toBeInTheDocument();
    expect(screen.getByText('alt.md vs. neu.md')).toBeInTheDocument();
    expect(screen.getByText('conflictKindContradiction')).toBeInTheDocument();
  });

  it('toggles the details panel closed on a second click (aria-expanded flips)', async () => {
    const user = userEvent.setup();
    render(<Harness message={{ ...baseMessage, conflicts: [conflict] }} />);
    const badge = screen.getByLabelText('conflictsBadge');

    await user.click(badge);
    expect(badge).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('Beitragshöhe')).toBeInTheDocument();

    await user.click(badge);
    expect(badge).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('Beitragshöhe')).not.toBeInTheDocument();
  });

  it('shows the superseded kind label for a superseded conflict', async () => {
    const user = userEvent.setup();
    const superseded: MessageConflict = { ...conflict, kind: 'superseded' };
    render(<Harness message={{ ...baseMessage, conflicts: [superseded] }} />);

    await user.click(screen.getByLabelText('conflictsBadge'));

    expect(screen.getByText('conflictKindSuperseded')).toBeInTheDocument();
    expect(screen.queryByText('conflictKindContradiction')).not.toBeInTheDocument();
  });

  it('resolves newer "a" to fileA in the "neuer: <file>" line', async () => {
    const user = userEvent.setup();
    const newerA: MessageConflict = { ...conflict, newer: 'a' };
    render(<Harness message={{ ...baseMessage, conflicts: [newerA] }} />);

    await user.click(screen.getByLabelText('conflictsBadge'));

    expect(screen.getByText('conflictNewerPrefix: alt.md')).toBeInTheDocument();
  });

  it('resolves newer "b" to fileB in the "neuer: <file>" line', async () => {
    const user = userEvent.setup();
    const newerB: MessageConflict = { ...conflict, newer: 'b' };
    render(<Harness message={{ ...baseMessage, conflicts: [newerB] }} />);

    await user.click(screen.getByLabelText('conflictsBadge'));

    expect(screen.getByText('conflictNewerPrefix: neu.md')).toBeInTheDocument();
  });

  it('renders no "neuer" line when newer is "unknown"', async () => {
    const user = userEvent.setup();
    render(<Harness message={{ ...baseMessage, conflicts: [conflict] }} />);

    await user.click(screen.getByLabelText('conflictsBadge'));

    expect(screen.queryByText(/conflictNewerPrefix/)).not.toBeInTheDocument();
  });
});
