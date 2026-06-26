import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NodeSourcesPanel } from './NodeSourcesPanel';

vi.mock('../../contexts/ThemeContext', () => ({
  useTheme: () => ({ t: (k: string) => k }),
}));

describe('NodeSourcesPanel', () => {
  it('lists deduped sources and fires onAsk', async () => {
    const onAsk = vi.fn();
    render(<NodeSourcesPanel entityName="Berlin"
      sources={[
        { fileId: 'f1', fileName: 'report.pdf', chunkId: 'c1' },
        { fileId: 'f1', fileName: 'report.pdf', chunkId: 'c2' },
        { fileId: 'f2', fileName: 'notes.md', chunkId: 'c3' },
      ]}
      onAsk={onAsk} onClose={vi.fn()} />);
    expect(screen.getAllByText('report.pdf')).toHaveLength(1); // deduped
    expect(screen.getByText('notes.md')).toBeInTheDocument();
    await userEvent.click(screen.getByText(/askAbout/));
    expect(onAsk).toHaveBeenCalled();
  });
});
