import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';

vi.mock('@xyflow/react', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  Panel: (props: any) => <div data-testid="panel">{props.children}</div>,
}));

import { GraphToolbar } from './GraphToolbar';

const t = (k: string) => k;

describe('GraphToolbar', () => {
  it('submits the search query on Enter', () => {
    const onSearch = vi.fn();
    render(<GraphToolbar allTypes={['org']} activeTypes={new Set(['org'])} onToggleType={() => {}} onSearch={onSearch} t={t} />);
    const input = screen.getByPlaceholderText('graphSearchPlaceholder');
    fireEvent.change(input, { target: { value: 'alice' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onSearch).toHaveBeenCalledWith('alice');
  });

  it('renders a chip per type and toggles it', () => {
    const onToggleType = vi.fn();
    render(<GraphToolbar allTypes={['org', 'person']} activeTypes={new Set(['org'])} onToggleType={onToggleType} onSearch={() => {}} t={t} />);
    const personChip = screen.getByRole('button', { name: 'person' });
    expect(personChip.getAttribute('aria-pressed')).toBe('false'); // not in activeTypes
    fireEvent.click(personChip);
    expect(onToggleType).toHaveBeenCalledWith('person');
  });
});
