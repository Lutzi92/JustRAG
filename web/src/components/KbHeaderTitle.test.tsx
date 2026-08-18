import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { KnowledgeBase } from '../types';
import { KbHeaderTitle } from './KbHeaderTitle';
import { translations } from '../translations';

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const entry = translations[key as keyof typeof translations];
      return entry ? entry.en : key;
    },
  }),
}));

const baseKb: KnowledgeBase = {
  id: 'kb-1',
  name: 'Security Advisories',
  description: null,
  userId: 'owner-1',
  createdAt: '2026-01-01T00:00:00Z',
  isPro: false,
  aiConfigId: null,
  chatModel: null,
  embeddingModel: null,
  rerankModel: null,
  ttsModel: null,
};

const label = translations.renameKb.en;

describe('KbHeaderTitle', () => {
  it('renders the KB name', () => {
    render(<KbHeaderTitle kb={baseKb} systemRole="user" onRename={vi.fn()} />);
    expect(screen.getByText('Security Advisories')).toBeInTheDocument();
  });

  it('offers the owner a rename button that hands over the KB', async () => {
    const onRename = vi.fn();
    const kb = { ...baseKb, myRole: 'owner' as const };
    render(<KbHeaderTitle kb={kb} systemRole="user" onRename={onRename} />);
    await userEvent.click(screen.getByRole('button', { name: label }));
    expect(onRename).toHaveBeenCalledWith(kb, expect.anything());
  });

  it('shows no rename button to an admin member of a private KB', () => {
    render(<KbHeaderTitle kb={{ ...baseKb, myRole: 'admin' }} systemRole="user" onRename={vi.fn()} />);
    expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
  });

  it('shows the rename button to a system admin on a public KB', () => {
    const kb: KnowledgeBase = { ...baseKb, userId: null, isGlobal: true, visibility: 'public', isPublished: true };
    render(<KbHeaderTitle kb={kb} systemRole="admin" onRename={vi.fn()} />);
    expect(screen.getByRole('button', { name: label })).toBeInTheDocument();
  });

  it('renders nothing but an empty title when there is no KB', () => {
    render(<KbHeaderTitle kb={null} systemRole="superadmin" onRename={vi.fn()} />);
    expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
  });
});
