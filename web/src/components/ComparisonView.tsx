import { useMemo } from 'react';
import { X } from 'lucide-react';
import type { Message } from '../types';
import MessageBubble from '../MessageBubble';
import { getPathToRoot, findCommonAncestor } from '../utils/messageTree';
import { useTheme } from '../contexts/ThemeContext';
import { useMessageSections } from '../hooks/useMessageSections';

interface ComparisonViewProps {
    messageTree: Map<string, Message>;
    leafIdA: string;
    leafIdB: string;
    onUseBranch: (leafId: string) => void;
    onClose: () => void;
    onPdfOpen?: (fileId: string, fileName: string, page: number) => void;
}

export function ComparisonView({ messageTree, leafIdA, leafIdB, onUseBranch, onClose, onPdfOpen }: ComparisonViewProps) {
    // This view isn't virtualized, but MessageBubble's expand state is owned by
    // the caller now, so it needs its own store or the toggles would be dead.
    const messageSections = useMessageSections();
    const { t } = useTheme();
    const pathA = useMemo(() => getPathToRoot(messageTree, leafIdA), [messageTree, leafIdA]);
    const pathB = useMemo(() => getPathToRoot(messageTree, leafIdB), [messageTree, leafIdB]);

    const commonAncestorId = useMemo(
        () => findCommonAncestor(messageTree, leafIdA, leafIdB),
        [messageTree, leafIdA, leafIdB]
    );

    // Split into shared prefix and divergent parts
    const { shared, branchA, branchB } = useMemo(() => {
        if (!commonAncestorId) {
            return { shared: [], branchA: pathA, branchB: pathB };
        }

        const ancestorIdxA = pathA.findIndex(m => m.id === commonAncestorId);
        const ancestorIdxB = pathB.findIndex(m => m.id === commonAncestorId);

        return {
            shared: pathA.slice(0, ancestorIdxA + 1),
            branchA: pathA.slice(ancestorIdxA + 1),
            branchB: pathB.slice(ancestorIdxB + 1),
        };
    }, [pathA, pathB, commonAncestorId]);

    const labelA = t('branchACurrent');
    const labelB = t('branchB');
    const useLabel = t('useThisBranch');

    return (
        <div style={{
            display: 'flex',
            flexDirection: 'column',
            height: '100%',
            overflow: 'hidden',
        }}>
            {/* Header */}
            <div style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '0.75rem 1rem',
                borderBottom: '1px solid var(--border-color)',
                background: 'var(--bg-secondary)',
            }}>
                <span style={{ fontWeight: 600, fontSize: '0.95rem' }}>
                    {t('branchComparison')}
                </span>
                <button
                    onClick={onClose}
                    aria-label={t('close')}
                    style={{
                        background: 'none',
                        border: 'none',
                        cursor: 'pointer',
                        color: 'var(--text-secondary)',
                        padding: '4px',
                        borderRadius: '4px',
                        display: 'flex',
                    }}
                >
                    <X size={18} />
                </button>
            </div>

            <div style={{ flex: 1, overflow: 'auto', padding: '1rem' }}>
                {/* Shared history */}
                {shared.length > 0 && (
                    <div style={{ marginBottom: '1rem' }}>
                        {shared.map((msg, i) => (
                            <MessageBubble
                                key={msg.id || i}
                                message={msg}
                                onPdfOpen={onPdfOpen}
                                reasoningOpen={messageSections.isOpen(msg.id, 'reasoning')}
                                sourcesOpen={messageSections.isOpen(msg.id, 'sources')}
                                confidenceOpen={messageSections.isOpen(msg.id, 'confidence')}
                                onToggleSection={messageSections.toggle}
                            />
                        ))}
                        <div style={{
                            borderTop: '2px dashed var(--border-color)',
                            margin: '1rem 0',
                            position: 'relative',
                        }}>
                            <span style={{
                                position: 'absolute',
                                top: '-10px',
                                left: '50%',
                                transform: 'translateX(-50%)',
                                background: 'var(--bg-primary)',
                                padding: '0 8px',
                                fontSize: '0.75rem',
                                color: 'var(--text-secondary)',
                            }}>
                                {t('forkPoint')}
                            </span>
                        </div>
                    </div>
                )}

                {/* Side-by-side branches */}
                <div style={{
                    display: 'grid',
                    gridTemplateColumns: '1fr 1fr',
                    gap: '1rem',
                }}>
                    {/* Branch A */}
                    <div style={{
                        borderRight: '1px solid var(--border-color)',
                        paddingRight: '0.5rem',
                    }}>
                        <div style={{
                            fontSize: '0.8rem',
                            fontWeight: 600,
                            color: 'var(--accent-primary)',
                            marginBottom: '0.5rem',
                            textAlign: 'center',
                        }}>
                            {labelA}
                        </div>
                        {branchA.map((msg, i) => (
                            <MessageBubble
                                key={msg.id || `a-${i}`}
                                message={msg}
                                onPdfOpen={onPdfOpen}
                                reasoningOpen={messageSections.isOpen(msg.id, 'reasoning')}
                                sourcesOpen={messageSections.isOpen(msg.id, 'sources')}
                                confidenceOpen={messageSections.isOpen(msg.id, 'confidence')}
                                onToggleSection={messageSections.toggle}
                            />
                        ))}
                        <div style={{ textAlign: 'center', marginTop: '0.75rem' }}>
                            <button
                                onClick={() => onUseBranch(leafIdA)}
                                style={{
                                    background: 'var(--accent-primary)',
                                    color: 'white',
                                    border: 'none',
                                    borderRadius: '8px',
                                    padding: '6px 16px',
                                    cursor: 'pointer',
                                    fontSize: '0.85rem',
                                }}
                            >
                                {useLabel}
                            </button>
                        </div>
                    </div>

                    {/* Branch B */}
                    <div style={{ paddingLeft: '0.5rem' }}>
                        <div style={{
                            fontSize: '0.8rem',
                            fontWeight: 600,
                            color: 'var(--text-secondary)',
                            marginBottom: '0.5rem',
                            textAlign: 'center',
                        }}>
                            {labelB}
                        </div>
                        {branchB.map((msg, i) => (
                            <MessageBubble
                                key={msg.id || `b-${i}`}
                                message={msg}
                                onPdfOpen={onPdfOpen}
                                reasoningOpen={messageSections.isOpen(msg.id, 'reasoning')}
                                sourcesOpen={messageSections.isOpen(msg.id, 'sources')}
                                confidenceOpen={messageSections.isOpen(msg.id, 'confidence')}
                                onToggleSection={messageSections.toggle}
                            />
                        ))}
                        <div style={{ textAlign: 'center', marginTop: '0.75rem' }}>
                            <button
                                onClick={() => onUseBranch(leafIdB)}
                                style={{
                                    background: 'var(--tag-bg)',
                                    color: 'var(--accent-primary)',
                                    border: '1px solid var(--accent-primary)',
                                    borderRadius: '8px',
                                    padding: '6px 16px',
                                    cursor: 'pointer',
                                    fontSize: '0.85rem',
                                }}
                            >
                                {useLabel}
                            </button>
                        </div>
                    </div>
                </div>
            </div>
        </div>
    );
}
