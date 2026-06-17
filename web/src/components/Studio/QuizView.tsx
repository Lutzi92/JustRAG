import React, { useState } from 'react';
import { Check, X } from 'lucide-react';
import type { QuizItem } from '../../types';
import { useTheme } from '../../contexts/ThemeContext';

interface QuizViewProps {
    items: QuizItem[];
}

// QuizView renders a multiple-choice quiz interactively: clicking an option
// records the answer and reveals which option was correct plus the explanation.
// Each question is answered independently; a running score is shown at the top.
export const QuizView: React.FC<QuizViewProps> = ({ items }) => {
    const { t } = useTheme();
    // questionIndex -> chosen optionIndex
    const [answers, setAnswers] = useState<Record<number, number>>({});

    if (!Array.isArray(items) || items.length === 0) {
        return <div style={{ color: 'var(--text-secondary)' }}>{t('quizEmpty')}</div>;
    }

    const answeredCount = Object.keys(answers).length;
    const correctCount = items.reduce((n, q, i) => n + (answers[i] === q.answerIndex ? 1 : 0), 0);

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '1.25rem' }}>
            <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', fontWeight: 500 }}>
                {t('quizScore')}: {correctCount} / {items.length}
                {answeredCount < items.length && ` (${answeredCount}/${items.length} ${t('quizAnswered')})`}
            </div>

            {items.map((q, qi) => {
                const chosen = answers[qi];
                const isAnswered = chosen !== undefined;
                return (
                    <div
                        key={qi}
                        style={{
                            background: 'var(--bg-primary)',
                            border: '1px solid var(--border-color)',
                            borderRadius: '8px',
                            padding: '1rem',
                        }}
                    >
                        <div style={{ fontWeight: 600, marginBottom: '0.75rem' }}>
                            {qi + 1}. {q.question}
                        </div>
                        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                            {q.options.map((opt, oi) => {
                                const isCorrect = oi === q.answerIndex;
                                const isChosen = oi === chosen;
                                // Colour only after the question is answered.
                                let borderColor = 'var(--border-color)';
                                let bg = 'var(--bg-secondary)';
                                if (isAnswered && isCorrect) {
                                    borderColor = 'var(--success-text, green)';
                                    bg = 'var(--success-bg, rgba(45,143,78,0.12))';
                                } else if (isAnswered && isChosen && !isCorrect) {
                                    borderColor = 'var(--error-text, #c0392b)';
                                    bg = 'var(--error-bg, rgba(192,57,43,0.12))';
                                }
                                return (
                                    <button
                                        key={oi}
                                        type="button"
                                        disabled={isAnswered}
                                        onClick={() => setAnswers(prev => ({ ...prev, [qi]: oi }))}
                                        style={{
                                            display: 'flex', alignItems: 'center', gap: '0.5rem',
                                            textAlign: 'left', padding: '0.6rem 0.75rem',
                                            borderRadius: '6px', border: `1px solid ${borderColor}`,
                                            background: bg, color: 'var(--text-primary)',
                                            cursor: isAnswered ? 'default' : 'pointer',
                                            width: '100%',
                                        }}
                                    >
                                        {isAnswered && isCorrect && <Check size={16} color="var(--success-text, green)" aria-hidden="true" />}
                                        {isAnswered && isChosen && !isCorrect && <X size={16} color="var(--error-text, #c0392b)" aria-hidden="true" />}
                                        <span>{opt}</span>
                                    </button>
                                );
                            })}
                        </div>
                        {isAnswered && q.explanation && (
                            <div style={{
                                marginTop: '0.75rem', fontSize: '0.85rem', color: 'var(--text-secondary)',
                                borderLeft: '3px solid var(--border-color)', paddingLeft: '0.75rem',
                            }}>
                                {q.explanation}
                            </div>
                        )}
                    </div>
                );
            })}
        </div>
    );
};
