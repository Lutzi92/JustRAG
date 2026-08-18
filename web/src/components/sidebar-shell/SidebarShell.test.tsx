import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi } from 'vitest';
import { SidebarShell } from './SidebarShell';

vi.mock('../../contexts/MobileContext', () => ({ useIsMobileContext: () => false }));

const base = {
  isOpen: true, width: 320,
  onExpand: vi.fn(), onCollapse: vi.fn(),
  expandLabel: 'aufklappen', collapseLabel: 'zuklappen',
};

describe('SidebarShell', () => {
  it('setzt die Seiten-Modifikatorklasse', () => {
    const { container, rerender } = render(
      <SidebarShell {...base} side="left"><p>Inhalt</p></SidebarShell>,
    );
    expect(container.querySelector('aside')).toHaveClass('sidebar-shell--left');

    rerender(<SidebarShell {...base} side="right"><p>Inhalt</p></SidebarShell>);
    expect(container.querySelector('aside')).toHaveClass('sidebar-shell--right');
  });

  it('zeigt im zugeklappten Zustand nur den Aufklapp-Button', async () => {
    render(
      <SidebarShell {...base} side="left" isOpen={false}>
        <p>Inhalt</p>
      </SidebarShell>,
    );
    expect(screen.queryByText('Inhalt')).not.toBeVisible();
    await userEvent.click(screen.getByRole('button', { name: 'aufklappen' }));
    expect(base.onExpand).toHaveBeenCalled();
  });

  it('nutzt die übergebene Breite im offenen und 60px im zugeklappten Zustand', () => {
    const { container, rerender } = render(
      <SidebarShell {...base} side="left" width={420}><p>Inhalt</p></SidebarShell>,
    );
    expect(container.querySelector('aside')).toHaveStyle({ width: '420px' });

    rerender(<SidebarShell {...base} side="left" width={420} isOpen={false}><p>Inhalt</p></SidebarShell>);
    expect(container.querySelector('aside')).toHaveStyle({ width: '60px' });
  });

  it('zeigt den Inhalt im offenen Zustand', () => {
    render(
      <SidebarShell {...base} side="left"><p>Inhalt</p></SidebarShell>,
    );
    expect(screen.getByText('Inhalt')).toBeVisible();
  });
});
