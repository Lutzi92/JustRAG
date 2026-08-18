import React from 'react';
import { Pencil } from 'lucide-react';
import type { KnowledgeBase } from '../types';
import { useTheme } from '../contexts/ThemeContext';
import { canRenameKb } from '../utils/kbAccess';

interface KbHeaderTitleProps {
  kb: KnowledgeBase | null;
  systemRole?: string;
  onRename: (kb: KnowledgeBase, e: React.MouseEvent) => void;
  compact?: boolean;
}

/**
 * KbHeaderTitle is the KB name in the workspace header, with a pencil beside
 * it for callers who may rename the KB. Extracted from ChatView so the gate
 * has a unit test of its own; canRenameKb is the same predicate the Home
 * card's pencil uses (owner on a private KB, system admin on a public one —
 * kbaccess.CanRename server-side).
 */
export const KbHeaderTitle: React.FC<KbHeaderTitleProps> = ({ kb, systemRole, onRename, compact = false }) => {
  const { t } = useTheme();
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px', minWidth: 0 }}>
      <span
        style={{
          fontWeight: 600,
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
          fontSize: compact ? '0.85rem' : '1.15rem',
        }}
      >
        {kb?.name}
      </span>
      {kb && canRenameKb(kb, systemRole) && (
        <button
          type="button"
          onClick={(e) => onRename(kb, e)}
          title={t('renameKb')}
          aria-label={t('renameKb')}
          style={{
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            padding: '4px',
            border: '1px solid transparent',
            borderRadius: '6px',
            background: 'transparent',
            color: 'var(--text-secondary)',
            cursor: 'pointer',
            flexShrink: 0,
          }}
        >
          <Pencil size={compact ? 14 : 16} aria-hidden="true" />
        </button>
      )}
    </span>
  );
};
