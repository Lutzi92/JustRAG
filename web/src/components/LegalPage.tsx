import { useState, useEffect } from 'react';
import { ArrowLeft, Loader2 } from 'lucide-react';
import DOMPurify from 'dompurify';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';
import type { Language } from '../translations';

type LegalPageType = 'terms' | 'privacy' | 'accessibility';

interface LegalPageProps {
  page: LegalPageType;
  onBack: () => void;
}

const titleKeys: Record<LegalPageType, string> = {
  terms: 'termsOfUseTitle',
  privacy: 'privacyPolicyTitle',
  accessibility: 'accessibilityTitle',
};

const htmlFiles: Record<LegalPageType, Record<Language, string>> = {
  terms: { de: '/legal/terms-de.html', en: '/legal/terms-en.html' },
  privacy: { de: '/legal/privacy-de.html', en: '/legal/privacy-en.html' },
  accessibility: { de: '/legal/accessibility-de.html', en: '/legal/accessibility-en.html' },
};

export function LegalPage({ page, onBack }: LegalPageProps) {
  const { t, language } = useTheme();
  const toast = useToast();
  // Loaded content is keyed by page+language; `loading` is derived during
  // render (no synchronous setState in the effect — state only changes in the
  // fetch continuations).
  const [loaded, setLoaded] = useState<{ key: string; html: string } | null>(null);
  const contentKey = `${page}:${language}`;
  const loading = loaded?.key !== contentKey;
  const html = loaded?.key === contentKey ? loaded.html : '';

  useEffect(() => {
    let cancelled = false;
    const key = `${page}:${language}`;
    fetch(htmlFiles[page][language])
      .then(res => res.text())
      .then(text => { if (!cancelled) setLoaded({ key, html: text }); })
      .catch(() => {
        if (cancelled) return;
        setLoaded({ key, html: '' });
        toast.error(t('pageLoadError'));
      });
    return () => { cancelled = true; };
  }, [page, language, t, toast]);

  return (
    <div style={{
      minHeight: '100vh',
      background: 'var(--bg-primary)',
      display: 'flex',
      flexDirection: 'column',
      alignItems: 'center',
      padding: '2rem 1rem',
    }}>
      <div style={{
        width: '100%',
        maxWidth: '720px',
      }}>
        <button
          onClick={onBack}
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            gap: '0.5rem',
            background: 'none',
            border: 'none',
            color: 'var(--accent-primary)',
            cursor: 'pointer',
            fontSize: '0.875rem',
            padding: '0.5rem 0',
            marginBottom: '1.5rem',
          }}
        >
          <ArrowLeft size={16} />
          {t('backToHome')}
        </button>

        <h1 style={{
          fontSize: '1.75rem',
          color: 'var(--text-primary)',
          marginBottom: '1.5rem',
        }}>
          {t(titleKeys[page])}
        </h1>

        {loading ? (
          <div style={{ display: 'flex', justifyContent: 'center', padding: '2rem' }}>
            <Loader2 className="animate-spin" size={24} color="var(--text-secondary)" />
          </div>
        ) : (
          <div
            className="content-fade-in"
            style={{
              color: 'var(--text-secondary)',
              fontSize: '0.9375rem',
              lineHeight: '1.7',
            }}
            dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(html) }}
          />
        )}
      </div>
    </div>
  );
}
