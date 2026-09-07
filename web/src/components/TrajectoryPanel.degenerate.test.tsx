import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { TrajectoryPanel } from './TrajectoryPanel';
import type { TrajectoryEvent } from '../types';

// The degenerate-answer guard (W5-R4) emits one trajectory event when it cuts
// a runaway repetition off. Without a label the row rendered the raw stage
// key; without the numbers it said an answer was truncated but not how far the
// run had grown or which limit it crossed.
const guarded: TrajectoryEvent[] = [
  { stage: 'answer_degenerate_guard', decision: 'truncated', limit: 400, run_length: 1234 },
];

describe('TrajectoryPanel answer_degenerate_guard', () => {
  it('renders the German label and the run length + limit', () => {
    render(<TrajectoryPanel trajectory={guarded} language="de" defaultExpanded />);
    expect(screen.getByText('Antwort gekürzt')).toBeInTheDocument();
    const detail = screen.getByTestId('degenerate-guard-detail');
    expect(detail.textContent).toContain('1234');
    expect(detail.textContent).toContain('400');
    expect(detail.textContent).toContain('Wiederholung');
    expect(detail.textContent).toContain('Grenze');
  });

  it('renders the English label and the run length + limit', () => {
    render(<TrajectoryPanel trajectory={guarded} language="en" defaultExpanded />);
    expect(screen.getByText('Answer truncated')).toBeInTheDocument();
    const detail = screen.getByTestId('degenerate-guard-detail');
    expect(detail.textContent).toContain('repetition: 1234');
    expect(detail.textContent).toContain('limit: 400');
  });

  it('never falls back to the raw stage key', () => {
    render(<TrajectoryPanel trajectory={guarded} language="de" defaultExpanded />);
    expect(screen.queryByText('answer_degenerate_guard')).not.toBeInTheDocument();
  });

  it('omits the detail line when the event carries neither number', () => {
    render(<TrajectoryPanel trajectory={[{ stage: 'answer_degenerate_guard' }]} language="en" defaultExpanded />);
    expect(screen.getByText('Answer truncated')).toBeInTheDocument();
    expect(screen.queryByTestId('degenerate-guard-detail')).not.toBeInTheDocument();
  });
});
