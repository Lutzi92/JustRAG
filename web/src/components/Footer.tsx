import React from 'react';
import { useTheme } from '../contexts/ThemeContext';

type LegalPage = 'terms' | 'privacy' | 'accessibility';

interface FooterProps {
  onNavigate: (page: LegalPage) => void;
}

export function Footer({ onNavigate }: FooterProps) {
  const { t } = useTheme();
  const linkStyle: React.CSSProperties = {
    background: 'none',
    border: 'none',
    color: 'var(--text-secondary)',
    fontSize: '0.75rem',
    cursor: 'pointer',
    padding: '8px 12px',
    minHeight: '44px',
    display: 'inline-flex',
    alignItems: 'center',
    textDecoration: 'none',
  };

  return (
    <footer style={{
      display: 'flex',
      justifyContent: 'center',
      alignItems: 'center',
      gap: '0.5rem',
      padding: '0.75rem 1rem',
      fontSize: '0.75rem',
      color: 'var(--text-secondary)',
    }}>
      <a
        href={t('imprintUrl')}
        target="_blank"
        rel="noopener noreferrer"
        style={linkStyle}
      >
        {t('imprint')}
      </a>
      <span aria-hidden>·</span>
      <button style={linkStyle} onClick={() => onNavigate('terms')}>{t('termsOfUse')}</button>
      <span aria-hidden>·</span>
      <button style={linkStyle} onClick={() => onNavigate('privacy')}>{t('privacyPolicy')}</button>
      <span aria-hidden>·</span>
      <button style={linkStyle} onClick={() => onNavigate('accessibility')}>{t('accessibility')}</button>
    </footer>
  );
}
