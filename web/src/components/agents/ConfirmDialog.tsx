import { useEffect, useId, useRef } from 'react';
import type { ReactNode } from 'react';
import { Loader2 } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';

export interface ConfirmDialogProps {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  tone?: 'default' | 'destructive';
  busy: boolean;
  error: string | null;
  onCancel: () => void;
  onConfirm: () => void;
}

/**
 * Themed confirm modal on the app's .modal-overlay pattern.
 *
 * Presentational only: the parent owns the request, so a failure keeps the
 * dialog open with `error` set rather than closing and losing the message.
 * Modelled on admin/KbDeleteDialog.tsx, minus the type-to-confirm ceremony —
 * agent deletion is consequential but not KB-cascade destructive.
 */
export function ConfirmDialog({
  title, body, confirmLabel, tone = 'default', busy, error, onCancel, onConfirm,
}: ConfirmDialogProps) {
  const { t } = useTheme();
  const titleId = useId();
  const confirmRef = useRef<HTMLButtonElement>(null);

  // Focus the confirm button on open; restore focus to the trigger on close.
  // Mount-only on purpose — re-running on `busy` would steal focus mid-request.
  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    confirmRef.current?.focus();
    return () => previouslyFocused?.focus();
  }, []);

  // Escape cancels, but never while a request is in flight.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) onCancel();
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [busy, onCancel]);

  return (
    <div
      className="modal-overlay"
      role="presentation"
      onClick={(e) => { if (e.target === e.currentTarget && !busy) onCancel(); }}
    >
      <div
        className="modal-content"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        style={{ maxWidth: '440px' }}
      >
        <h3 id={titleId} style={{ margin: '0 0 0.75rem' }}>{title}</h3>
        <div style={{ marginBottom: '1rem' }}>{body}</div>

        {error && (
          <p role="alert" style={{ margin: '0 0 1rem', color: 'var(--error-text)', fontSize: '0.9rem' }}>
            {error}
          </p>
        )}

        <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end' }}>
          <button type="button" className="btn btn--tertiary" onClick={onCancel} disabled={busy}>
            {t('cancel')}
          </button>
          <button
            ref={confirmRef}
            type="button"
            className={`btn ${tone === 'destructive' ? 'btn--destructive' : 'btn--primary'}`}
            onClick={onConfirm}
            disabled={busy}
          >
            {busy && <Loader2 className="animate-spin" size={15} aria-hidden="true" />}
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

export default ConfirmDialog;
