import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi } from 'vitest';
import { MobileTabBar } from './MobileTabBar';

vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));

describe('MobileTabBar', () => {
  it('zeigt vier Reiter in der Reihenfolge Verlauf, Chat, Workspace, Quellen', () => {
    render(<MobileTabBar activeTab="chat" onTabChange={vi.fn()} />);
    expect(screen.getAllByRole('button').map(b => b.textContent))
      .toEqual(['tabHistory', 'tabChat', 'tabWorkspace', 'tabFiles']);
  });

  it('markiert den aktiven Reiter', () => {
    render(<MobileTabBar activeTab="workspace" onTabChange={vi.fn()} />);
    expect(screen.getByRole('button', { name: 'tabWorkspace' })).toHaveAttribute('aria-current', 'page');
  });

  it('meldet einen Reiterwechsel', async () => {
    const onTabChange = vi.fn();
    render(<MobileTabBar activeTab="chat" onTabChange={onTabChange} />);
    await userEvent.click(screen.getByRole('button', { name: 'tabHistory' }));
    expect(onTabChange).toHaveBeenCalledWith('history');
  });
});
