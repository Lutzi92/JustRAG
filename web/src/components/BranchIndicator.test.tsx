import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { BranchIndicator } from './BranchIndicator';
import type { BranchInfo } from '../types';

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const translations: Record<string, string> = {
        previousBranch: 'Previous branch',
        nextBranch: 'Next branch',
      };
      return translations[key] || key;
    }
  }),
}));

function makeBranchInfo(currentIndex: number, total: number): BranchInfo {
  const siblingIds = Array.from({ length: total }, (_, i) => `s${i}`);
  return { parentMessageId: 'parent', siblingIds, currentIndex, total };
}

describe('BranchIndicator', () => {
  it('renders current/total text', () => {
    render(<BranchIndicator branchInfo={makeBranchInfo(1, 3)} onSwitchBranch={() => {}} />);
    expect(screen.getByText('2/3')).toBeInTheDocument();
  });

  it('calls onSwitchBranch with previous sibling on prev click', async () => {
    const user = userEvent.setup();
    const onSwitch = vi.fn();
    render(<BranchIndicator branchInfo={makeBranchInfo(1, 3)} onSwitchBranch={onSwitch} />);

    await user.click(screen.getByLabelText('Previous branch'));
    expect(onSwitch).toHaveBeenCalledWith('s0');
  });

  it('calls onSwitchBranch with next sibling on next click', async () => {
    const user = userEvent.setup();
    const onSwitch = vi.fn();
    render(<BranchIndicator branchInfo={makeBranchInfo(1, 3)} onSwitchBranch={onSwitch} />);

    await user.click(screen.getByLabelText('Next branch'));
    expect(onSwitch).toHaveBeenCalledWith('s2');
  });

  it('disables prev button at index 0', () => {
    render(<BranchIndicator branchInfo={makeBranchInfo(0, 3)} onSwitchBranch={() => {}} />);
    expect(screen.getByLabelText('Previous branch')).toBeDisabled();
  });

  it('disables next button at last index', () => {
    render(<BranchIndicator branchInfo={makeBranchInfo(2, 3)} onSwitchBranch={() => {}} />);
    expect(screen.getByLabelText('Next branch')).toBeDisabled();
  });

  it('does not call onSwitchBranch when clicking disabled prev', async () => {
    const user = userEvent.setup();
    const onSwitch = vi.fn();
    render(<BranchIndicator branchInfo={makeBranchInfo(0, 2)} onSwitchBranch={onSwitch} />);

    await user.click(screen.getByLabelText('Previous branch'));
    expect(onSwitch).not.toHaveBeenCalled();
  });
});
