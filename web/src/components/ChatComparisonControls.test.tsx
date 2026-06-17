import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { ChatComparisonControls } from './ChatComparisonControls';

describe('ChatComparisonControls', () => {
  const att = { attachmentId: 'att1', filename: 'm.md', sectionCount: 3, charCount: 100 };

  it('renders nothing without an attachment', () => {
    const { container } = render(
      <ChatComparisonControls attachment={null} selectedModes={[]} onModesChange={() => {}} onClear={() => {}} />
    );
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the filename and toggles a mode', () => {
    const onModesChange = vi.fn();
    render(
      <ChatComparisonControls attachment={att} selectedModes={[]} onModesChange={onModesChange} onClear={() => {}} />
    );
    expect(screen.getByText(/m\.md/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /contradiction/i }));
    expect(onModesChange).toHaveBeenCalledWith(['contradiction']);
  });

  it('deselects an already-selected mode', () => {
    const onModesChange = vi.fn();
    render(
      <ChatComparisonControls attachment={att} selectedModes={['formal']} onModesChange={onModesChange} onClear={() => {}} />
    );
    fireEvent.click(screen.getByRole('button', { name: /formal/i }));
    expect(onModesChange).toHaveBeenCalledWith([]);
  });
});
