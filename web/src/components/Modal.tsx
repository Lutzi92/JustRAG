import React, { useEffect, useRef } from 'react';
import { motion } from 'framer-motion';
import { AlertCircle, HelpCircle } from 'lucide-react';
import { useTheme } from '../contexts/ThemeContext';
import { useReducedMotion, getMotionProps } from '../hooks/useReducedMotion';

export interface ModalState {
    show: boolean;
    type: 'alert' | 'confirm' | 'prompt';
    title: string;
    message: string;
    value?: string;
    onConfirm: (value?: string) => void;
    onCancel: () => void;
}

export interface CustomModalProps {
    modal: ModalState;
    setModal: React.Dispatch<React.SetStateAction<ModalState>>;
}

export const Modal: React.FC<CustomModalProps> = ({ modal, setModal }) => {
    const { t } = useTheme();
    const reducedMotion = useReducedMotion();
    const modalRef = useRef<HTMLDivElement>(null);
    const inputRef = useRef<HTMLInputElement>(null);
    const confirmButtonRef = useRef<HTMLButtonElement>(null);
    const previousFocusRef = useRef<HTMLElement | null>(null);

    // Save previous focus on mount
    useEffect(() => {
        if (modal.show) {
            previousFocusRef.current = document.activeElement as HTMLElement;
            // Focus internal element after a short delay to allow animation/mount
            setTimeout(() => {
                if (modal.type === 'prompt' && inputRef.current) {
                    inputRef.current.focus();
                } else if (confirmButtonRef.current) {
                    confirmButtonRef.current.focus();
                }
            }, 50);
        } else if (previousFocusRef.current) {
            // Restore focus on close
            previousFocusRef.current.focus();
        }
    }, [modal.show, modal.type]);

    // Focus trap and Escape key
    useEffect(() => {
        if (!modal.show) return;

        const onCancel = modal.onCancel;
        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                onCancel();
                return;
            }

            if (e.key === 'Tab' && modalRef.current) {
                const focusableElements = modalRef.current.querySelectorAll(
                    'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
                );
                const firstElement = focusableElements[0] as HTMLElement;
                const lastElement = focusableElements[focusableElements.length - 1] as HTMLElement;

                if (e.shiftKey) {
                    if (document.activeElement === firstElement) {
                        lastElement.focus();
                        e.preventDefault();
                    }
                } else {
                    if (document.activeElement === lastElement) {
                        firstElement.focus();
                        e.preventDefault();
                    }
                }
            }
        };

        document.addEventListener('keydown', handleKeyDown);
        return () => document.removeEventListener('keydown', handleKeyDown);
    }, [modal.show, modal.onCancel]);

    if (!modal.show) return null;

    return (
        <div
            className="modal-overlay"
            style={{ zIndex: 3000 }}
            role="presentation"
            onClick={() => modal.onCancel()}
        >
            <motion.div
                role="dialog"
                aria-modal="true"
                aria-labelledby="modal-title"
                aria-describedby="modal-desc"
                ref={modalRef}
                {...getMotionProps(reducedMotion)}
                initial={{ opacity: 0, scale: 0.95, y: 20 }}
                animate={{ opacity: 1, scale: 1, y: 0 }}
                exit={{ opacity: 0, scale: 0.95, y: 20 }}
                className="modal-content"
                style={{ maxWidth: '400px' }}
                onClick={e => e.stopPropagation()}
            >
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '1.5rem' }}>
                    <div style={{
                        padding: '10px',
                        background: 'var(--tag-bg)',
                        borderRadius: '12px',
                        color: 'var(--accent-primary)',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center'
                    }}>
                        {modal.type === 'alert' ? <AlertCircle size={24} aria-hidden="true" /> : <HelpCircle size={24} aria-hidden="true" />}
                    </div>
                    <h3 id="modal-title" style={{ margin: 0, fontSize: '1.25rem' }}>{modal.title}</h3>
                </div>

                <div id="modal-desc" style={{ marginBottom: '2rem', color: 'var(--text-secondary)', lineHeight: '1.6', fontSize: '0.95rem' }}>
                    {modal.message}
                </div>

                {modal.type === 'prompt' && (
                    <div style={{ marginBottom: '2rem' }}>
                        <label htmlFor="modal-input" className="sr-only" style={{ position: 'absolute', width: '1px', height: '1px', padding: 0, margin: '-1px', overflow: 'hidden', clip: 'rect(0,0,0,0)', border: 0 }}>
                            {modal.message}
                        </label>
                        <input
                            ref={inputRef}
                            id="modal-input"
                            // eslint-disable-next-line jsx-a11y/no-autofocus
                            autoFocus
                            className="chat-input"
                            style={{
                                width: '100%',
                                border: '1px solid var(--border-color)',
                                borderRadius: '12px',
                                padding: '0.75rem 1rem',
                                background: 'var(--bg-primary)',
                                color: 'var(--text-primary)',
                                display: 'block'
                            }}
                            value={modal.value}
                            onChange={e => setModal(prev => ({ ...prev, value: e.target.value }))}
                            onKeyDown={e => {
                                if (e.key === 'Enter') modal.onConfirm(modal.value);
                            }}
                            placeholder={t('inputPlaceholder')}
                        />
                    </div>
                )}

                <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                    {(modal.type === 'confirm' || modal.type === 'prompt') && (
                        <button
                            onClick={() => modal.onCancel()}
                            className="secondary-button"
                            style={{
                                flex: 1,
                                padding: '0.75rem',
                                borderRadius: '12px',
                                border: '1px solid var(--border-color)',
                                background: 'var(--bg-primary)',
                                color: 'var(--text-primary)',
                                cursor: 'pointer',
                                fontWeight: 500
                            }}
                        >
                            {t('cancel')}
                        </button>
                    )}
                    <button
                        ref={confirmButtonRef}
                        onClick={() => modal.onConfirm(modal.value)}
                        className="search-button"
                        style={{
                            flex: 1,
                            padding: '0.75rem',
                            borderRadius: '12px',
                            border: 'none',
                            background: 'var(--accent-primary)',
                            color: 'white',
                            cursor: 'pointer',
                            fontWeight: 600
                        }}
                    >
                        {modal.type === 'alert' ? t('ok') : t('confirm')}
                    </button>
                </div>
            </motion.div>
        </div>
    );
};
