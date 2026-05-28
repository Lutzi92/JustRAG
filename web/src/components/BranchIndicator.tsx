import { ChevronLeft, ChevronRight } from 'lucide-react';
import type { BranchInfo } from '../types';
import { useTheme } from '../contexts/ThemeContext';

interface BranchIndicatorProps {
    branchInfo: BranchInfo;
    onSwitchBranch: (siblingId: string) => void;
}

export function BranchIndicator({ branchInfo, onSwitchBranch }: BranchIndicatorProps) {
    const { t } = useTheme();
    const { siblingIds, currentIndex, total } = branchInfo;
    const canGoPrev = currentIndex > 0;
    const canGoNext = currentIndex < total - 1;

    return (
        <div style={{
            display: 'flex',
            alignItems: 'center',
            gap: '2px',
            fontSize: '0.75rem',
            color: 'var(--text-secondary)',
            userSelect: 'none',
            marginBottom: '4px',
        }}>
            <button
                onClick={(e) => {
                    e.stopPropagation();
                    if (canGoPrev) onSwitchBranch(siblingIds[currentIndex - 1]);
                }}
                disabled={!canGoPrev}
                style={{
                    background: 'none',
                    border: 'none',
                    padding: '0 2px',
                    cursor: canGoPrev ? 'pointer' : 'default',
                    color: canGoPrev ? 'var(--accent-primary)' : 'var(--text-secondary)',
                    opacity: canGoPrev ? 1 : 0.3,
                    display: 'flex',
                    alignItems: 'center',
                }}
                aria-label={t('previousBranch')}
            >
                <ChevronLeft size={14} />
            </button>
            <span style={{ fontVariantNumeric: 'tabular-nums' }}>
                {currentIndex + 1}/{total}
            </span>
            <button
                onClick={(e) => {
                    e.stopPropagation();
                    if (canGoNext) onSwitchBranch(siblingIds[currentIndex + 1]);
                }}
                disabled={!canGoNext}
                style={{
                    background: 'none',
                    border: 'none',
                    padding: '0 2px',
                    cursor: canGoNext ? 'pointer' : 'default',
                    color: canGoNext ? 'var(--accent-primary)' : 'var(--text-secondary)',
                    opacity: canGoNext ? 1 : 0.3,
                    display: 'flex',
                    alignItems: 'center',
                }}
                aria-label={t('nextBranch')}
            >
                <ChevronRight size={14} />
            </button>
        </div>
    );
}
