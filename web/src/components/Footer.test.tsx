import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Footer } from './Footer';

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({
    theme: 'light' as const,
    toggleTheme: () => {},
    language: 'de' as const,
    setLanguage: () => {},
    t: (key: string) => {
      const map: Record<string, string> = {
        imprintUrl: 'https://example.com/imprint',
        imprint: 'Imprint',
        privacyPolicy: 'Privacy Policy',
        accessibility: 'Accessibility',
      };
      return map[key] || key;
    },
  }),
}));

describe('Footer', () => {
  it('renders imprint as external link with target="_blank"', () => {
    render(<Footer onNavigate={() => {}} />);
    const link = screen.getByText('Imprint');
    expect(link.tagName).toBe('A');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('href', 'https://example.com/imprint');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  });

  it('renders Privacy Policy as button calling onNavigate("privacy")', async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    render(<Footer onNavigate={onNavigate} />);

    const privacyButton = screen.getByText('Privacy Policy');
    expect(privacyButton.tagName).toBe('BUTTON');
    await user.click(privacyButton);
    expect(onNavigate).toHaveBeenCalledWith('privacy');
  });

  it('renders Accessibility as button calling onNavigate("accessibility")', async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    render(<Footer onNavigate={onNavigate} />);

    const a11yButton = screen.getByText('Accessibility');
    expect(a11yButton.tagName).toBe('BUTTON');
    await user.click(a11yButton);
    expect(onNavigate).toHaveBeenCalledWith('accessibility');
  });
});
