import React, { useState } from 'react';
import { ChevronLeft, ChevronRight, Download } from 'lucide-react';
import type { FlashcardItem } from '../../types';
import { useTheme } from '../../contexts/ThemeContext';

const toCsv = (cards: FlashcardItem[]) =>
    'data:text/csv;charset=utf-8,' +
    cards.map(c => `"${c.front.replace(/"/g, '""')}","${c.back.replace(/"/g, '""')}"`).join('\n');

/**
 * Flashcards artifact view: flip-card UI + CSV export. Ported from the
 * retired `ContentModal` (fix wave item 1) — `StudioWorkspace` previously
 * fell back to a flat "F:/A:" list here.
 *
 * `id` resets the local card index/flip state on artifact switch (the
 * "adjust state during render" pattern also used for `promptResetKey` in
 * ChatView and `degradedForId` in StudioWorkspace) instead of relying on a
 * shared, never-reset index from a parent hook — that was the latent bug in
 * the retired `useGeneratedContent.currentCardIndex`.
 */
export const FlashcardsArtifact: React.FC<{ id: string; content: FlashcardItem[] }> = ({ id, content }) => {
    const { t } = useTheme();
    const [index, setIndex] = useState(0);
    const [showAnswer, setShowAnswer] = useState(false);
    const [resetForId, setResetForId] = useState(id);
    if (id !== resetForId) {
        setResetForId(id);
        setIndex(0);
        setShowAnswer(false);
    }

    const handleDownload = () => {
        const link = document.createElement('a');
        link.setAttribute('href', encodeURI(toCsv(content)));
        link.setAttribute('download', 'flashcards.csv');
        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);
    };

    if (content.length === 0) return null;
    const card = content[Math.min(index, content.length - 1)];

    return (
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: '1.5rem', padding: '1rem' }}>
            <div>
                <button className="secondary-button" onClick={handleDownload}>
                    <Download size={16} aria-hidden="true" /> {t('exportCsv')}
                </button>
            </div>

            <button
                className={`flashcard ${showAnswer ? 'is-flipped' : ''}`}
                onClick={() => setShowAnswer(!showAnswer)}
                onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault();
                        setShowAnswer(!showAnswer);
                    }
                }}
                style={{ border: 'none', padding: 0 }}
                aria-label={`Flashcard ${index + 1}. ${t('flashcardFlip')}`}
            >
                <div className="flashcard-inner">
                    <div className="flashcard-front">
                        <div style={{ padding: '2rem', textAlign: 'center' }}>
                            <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '1rem', textTransform: 'uppercase' }}>Frage</div>
                            <div style={{ fontSize: '1.25rem', fontWeight: 600 }}>{card.front}</div>
                        </div>
                    </div>
                    <div className="flashcard-back">
                        <div style={{ padding: '2rem', textAlign: 'center' }}>
                            <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '1rem', textTransform: 'uppercase' }}>Antwort</div>
                            <div style={{ fontSize: '1.25rem' }}>{card.back}</div>
                        </div>
                    </div>
                </div>
            </button>

            <div style={{ display: 'flex', alignItems: 'center', gap: '2rem' }}>
                <button
                    className="icon-button"
                    disabled={index === 0}
                    onClick={() => {
                        setIndex(prev => prev - 1);
                        setShowAnswer(false);
                    }}
                    style={{
                        padding: '12px',
                        background: 'var(--bg-secondary)',
                        border: '1px solid var(--border-color)',
                        borderRadius: '50%',
                        opacity: index === 0 ? 0.3 : 1,
                    }}
                    aria-label={t('previousCard')}
                >
                    <ChevronLeft size={24} aria-hidden="true" />
                </button>

                <div style={{ fontSize: '0.9rem', color: 'var(--text-secondary)', fontWeight: 500 }}>
                    {index + 1} / {content.length}
                </div>

                <button
                    className="icon-button"
                    disabled={index === content.length - 1}
                    onClick={() => {
                        setIndex(prev => prev + 1);
                        setShowAnswer(false);
                    }}
                    style={{
                        padding: '12px',
                        background: 'var(--bg-secondary)',
                        border: '1px solid var(--border-color)',
                        borderRadius: '50%',
                        opacity: index === content.length - 1 ? 0.3 : 1,
                    }}
                    aria-label={t('nextCard')}
                >
                    <ChevronRight size={24} aria-hidden="true" />
                </button>
            </div>
        </div>
    );
};
