// web/src/components/AnswerExportMenu.tsx
import { useState, useRef } from 'react';
import { Download } from 'lucide-react';
import type { Message } from '../types';
import { HAPTIC_PATTERNS, triggerHaptic } from '../utils/haptics';
import { useToast } from '../contexts/ToastContext';
import { AnchoredPopover } from './AnchoredPopover';
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
  const triggerRef = useRef<HTMLButtonElement>(null);

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
    <div style={{ display: 'flex' }}>
      <button
        ref={triggerRef}
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
      <AnchoredPopover
        open={open}
        triggerRef={triggerRef}
        onClose={() => setOpen(false)}
        align="start"
        width={180}
        role="menu"
        ariaLabel={t('exportAnswer')}
      >
        <div style={{ padding: '4px' }}>
          <button type="button" role="menuitem" style={itemStyle} onClick={handleCopy} onMouseEnter={hoverIn} onMouseLeave={hoverOut}>{t('copyWithCitations')}</button>
          <button type="button" role="menuitem" style={itemStyle} onClick={handleMarkdown} onMouseEnter={hoverIn} onMouseLeave={hoverOut}>{t('exportMarkdown')}</button>
          {kbId && (
            <button type="button" role="menuitem" style={itemStyle} onClick={handleDocx} onMouseEnter={hoverIn} onMouseLeave={hoverOut}>{t('exportDocx')}</button>
          )}
          <button type="button" role="menuitem" style={itemStyle} onClick={handlePdf} onMouseEnter={hoverIn} onMouseLeave={hoverOut}>{t('exportPdf')}</button>
        </div>
      </AnchoredPopover>
    </div>
  );
}
