import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { TrajectoryPanel } from './TrajectoryPanel';
import type { TrajectoryEvent } from '../types';

const teamTrajectory: TrajectoryEvent[] = [
  { stage: 'plan', decision: 'team_route', reason: 'both relevant', queries: ['Netz', 'Recht'], findings: 2 },
  { stage: 'hop', step: 1, query: 'Netz', findings: 4, reason: 'specialist_complete' },
  { stage: 'hop', step: 2, query: 'Recht', findings: 2, reason: 'specialist_complete' },
  { stage: 'answer', decision: 'team_synthesis', step: 2, findings: 6 },
];

describe('TrajectoryPanel team grouping', () => {
  it('renders a router header and groups hops under agent names', () => {
    render(<TrajectoryPanel trajectory={teamTrajectory} language="en" defaultExpanded />);
    // Router header: selected agents + reasoning
    expect(screen.getByText(/Netz, Recht/)).toBeInTheDocument();
    expect(screen.getByText(/both relevant/)).toBeInTheDocument();
    // Each specialist appears as a group label
    expect(screen.getAllByText('Netz').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Recht').length).toBeGreaterThanOrEqual(1);
    // Router stage label itself is present
    expect(screen.getByText('Team-Router')).toBeInTheDocument();
    // Synthesis row uses the dedicated label
    expect(screen.getByText('Synthesis')).toBeInTheDocument();
  });

  it('renders the German router + synthesis labels', () => {
    render(<TrajectoryPanel trajectory={teamTrajectory} language="de" defaultExpanded />);
    expect(screen.getByText('Team-Router')).toBeInTheDocument();
    expect(screen.getByText('Synthese')).toBeInTheDocument();
  });

  it('renders non-team trajectories exactly as before (no router header)', () => {
    render(<TrajectoryPanel
      trajectory={[{ stage: 'plan', reason: 'supervisor.dispatch' }, { stage: 'answer' }]}
      language="en" defaultExpanded />);
    expect(screen.queryByText('Team-Router')).not.toBeInTheDocument();
    expect(screen.queryByText(/Router/)).not.toBeInTheDocument();
  });

  it('grouped view: last <li> has no bottom border, earlier rows keep the dashed separator', () => {
    const { container } = render(<TrajectoryPanel trajectory={teamTrajectory} language="en" defaultExpanded />);
    const items = Array.from(container.querySelectorAll('li')) as HTMLElement[];
    expect(items.length).toBeGreaterThan(1);
    const last = items[items.length - 1];
    expect(last.style.borderBottom).not.toContain('dashed');
    expect(items.some(li => li.style.borderBottom.includes('dashed'))).toBe(true);
  });

  it('flat rows preceding team_route (e.g. graph_traversal decision) still render', () => {
    const trajectoryWithPreDecision: TrajectoryEvent[] = [
      { stage: 'decision', decision: 'graph_traversal', reason: 'kg hop' },
      ...teamTrajectory,
    ];
    render(<TrajectoryPanel trajectory={trajectoryWithPreDecision} language="en" defaultExpanded />);
    expect(screen.getByText('graph_traversal')).toBeInTheDocument();
    expect(screen.getByText('Team-Router')).toBeInTheDocument();
  });
});
