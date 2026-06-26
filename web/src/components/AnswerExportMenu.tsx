// web/src/components/AnswerExportMenu.tsx
import { useState, useEffect, useRef } from 'react';
import { Download } from 'lucide-react';
import type { Message } from '../types';
import { HAPTIC_PATTERNS, triggerHaptic } from '../utils/haptics';
import { useToast } from '../contexts/ToastContext';
import {
  copyAnswerWithCitations,
  downloadAnswerMarkdown,
  exportAnswerDocx,
  printAnswerPdf,
} from '../utils/exportAnswer';

interface AnswerExportMenuProps {
  message: Message;
  kbId?: string;
  questionText?: string;
  contentRef: React.RefObject<HTMLDivElement | null>;
  buttonStyle: React.CSSProperties;
  iconSize: number;
  t: (key: string) => string;
}

export function AnswerExportMenu({ message, kbId, questionText, contentRef, buttonStyle, iconSize, t }: AnswerExportMenuProps) {
  const [open, setOpen] = useState(false);
  const toast = useToast();
  const wrapRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDocClick = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onDocClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDocClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const heading = t('sourcesHeading');

  const handleCopy = async () => {
    setOpen(false);
    const ok = await copyAnswerWithCitations(message, heading);
    if (ok) toast.success(t('answerCopied'));
    else toast.error(t('exportFailed'));
  };
  const handleMarkdown = () => {
    setOpen(false);
    downloadAnswerMarkdown(message, heading);
  };
  const handleDocx = async () => {
    setOpen(false);
    if (!kbId) return;
    try {
      await exportAnswerDocx(kbId, message, questionText || '', heading);
    } catch {
      toast.error(t('exportFailed'));
    }
  };
  const handlePdf = () => {
    setOpen(false);
    printAnswerPdf(contentRef.current, questionText || t('exportAnswer'));
  };

  const itemStyle: React.CSSProperties = {
    display: 'block',
    width: '100%',
    textAlign: 'left',
    background: 'none',
    border: 'none',
    padding: '6px 10px',
    cursor: 'pointer',
    color: 'var(--text-primary)',
    fontSize: '0.85rem',
    borderRadius: '4px',
  };
  const hoverIn = (e: React.MouseEvent<HTMLButtonElement>) => { e.currentTarget.style.background = 'var(--tag-bg)'; };
  const hoverOut = (e: React.MouseEvent<HTMLButtonElement>) => { e.currentTarget.style.background = 'none'; };

  return (
    <div ref={wrapRef} style={{ position: 'relative', display: 'flex' }}>
      <button
        type="button"
        onClick={(e) => { e.stopPropagation(); triggerHaptic(HAPTIC_PATTERNS.action); setOpen((o) => !o); }}
        style={buttonStyle}
        title={t('exportAnswer')}
        aria-label={t('exportAnswer')}
        aria-haspopup="menu"
        aria-expanded={open}
        onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--accent-primary)'; e.currentTarget.style.background = 'var(--tag-bg)'; }}
        onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-secondary)'; e.currentTarget.style.background = 'none'; }}
      >
        <Download size={iconSize} />
      </button>
      {open && (
        <div
          role="menu"
          tabIndex={-1}
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => { if (e.key === 'Escape') setOpen(false); }}
          style={{
            position: 'absolute',
            top: '32px',
            right: 0,
            background: 'var(--bg-primary)',
            border: '1px solid var(--border-color)',
            borderRadius: '6px',
            padding: '4px',
            boxShadow: 'var(--shadow-md)',
            zIndex: 20,
            minWidth: '180px',
          }}
        >
          <button type="button" role="menuitem" style={itemStyle} onClick={handleCopy} onMouseEnter={hoverIn} onMouseLeave={hoverOut}>{t('copyWithCitations')}</button>
          <button type="button" role="menuitem" style={itemStyle} onClick={handleMarkdown} onMouseEnter={hoverIn} onMouseLeave={hoverOut}>{t('exportMarkdown')}</button>
          {kbId && (
            <button type="button" role="menuitem" style={itemStyle} onClick={handleDocx} onMouseEnter={hoverIn} onMouseLeave={hoverOut}>{t('exportDocx')}</button>
          )}
          <button type="button" role="menuitem" style={itemStyle} onClick={handlePdf} onMouseEnter={hoverIn} onMouseLeave={hoverOut}>{t('exportPdf')}</button>
        </div>
      )}
    </div>
  );
}
