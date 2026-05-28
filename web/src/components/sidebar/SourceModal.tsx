import React, { memo, useEffect } from 'react';
import { motion } from 'framer-motion';
import { X } from 'lucide-react';
import { useReducedMotion, getMotionProps } from '../../hooks/useReducedMotion';
import './SourceModal.css';

interface SourceModalProps {
    title: string;
    show: boolean;
    onClose: () => void;
    children: React.ReactNode;
    className?: string;
}

const SourceModalComp: React.FC<SourceModalProps> = ({ title, show, onClose, children, className }) => {
    const reducedMotion = useReducedMotion();

    useEffect(() => {
        if (!show) return;
        const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
        document.addEventListener('keydown', handler);
        return () => document.removeEventListener('keydown', handler);
    }, [show, onClose]);

    if (!show) return null;

    return (
        <div className="modal-overlay" onClick={onClose} role="dialog" aria-modal="true" aria-label={title}>
            <motion.div
                {...getMotionProps(reducedMotion)}
                initial={{ opacity: 0, scale: 0.95, y: 20 }}
                animate={{ opacity: 1, scale: 1, y: 0 }}
                className={`modal-content source-modal${className ? ` ${className}` : ''}`}
                onClick={(e: React.MouseEvent) => e.stopPropagation()}
            >
                <div className="source-modal__header">
                    <h3 className="source-modal__title">{title}</h3>
                    <button className="source-modal__close" onClick={onClose} aria-label="Close">
                        <X size={20} />
                    </button>
                </div>
                <div className="source-modal__body">
                    {children}
                </div>
            </motion.div>
        </div>
    );
};

export const SourceModal = memo(SourceModalComp);
