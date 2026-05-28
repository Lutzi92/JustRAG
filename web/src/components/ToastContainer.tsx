import { useEffect, useRef } from 'react';
import { AnimatePresence, motion } from 'framer-motion';
import { CheckCircle, XCircle, Info, AlertTriangle, X } from 'lucide-react';
import { useToastContext, type Toast } from '../contexts/ToastContext';
import { useReducedMotion, getMotionProps } from '../hooks/useReducedMotion';
import './Toast.css';

const ICONS = {
  success: CheckCircle,
  error: XCircle,
  info: Info,
  warning: AlertTriangle,
} as const;

function ToastItem({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  const reducedMotion = useReducedMotion();
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  useEffect(() => {
    timerRef.current = setTimeout(onDismiss, toast.duration);
    return () => clearTimeout(timerRef.current);
  }, [toast.duration, onDismiss]);

  const Icon = ICONS[toast.type];

  return (
    <motion.div
      layout
      {...getMotionProps(reducedMotion)}
      initial={{ opacity: 0, x: 80 }}
      animate={{ opacity: 1, x: 0 }}
      exit={{ opacity: 0, x: 80 }}
      transition={{ type: 'spring', stiffness: 500, damping: 30 }}
      className={`toast toast--${toast.type}`}
      role="status"
      aria-live="polite"
    >
      <Icon size={18} className={`toast__icon toast__icon--${toast.type}`} aria-hidden="true" />
      <span className="toast__message">{toast.message}</span>
      <button className="toast__close" onClick={onDismiss} aria-label="Dismiss">
        <X size={14} />
      </button>
    </motion.div>
  );
}

export function ToastContainer() {
  const { toasts, removeToast } = useToastContext();

  return (
    <div className="toast-container" aria-label="Notifications">
      <AnimatePresence mode="popLayout">
        {toasts.map(t => (
          <ToastItem key={t.id} toast={t} onDismiss={() => removeToast(t.id)} />
        ))}
      </AnimatePresence>
    </div>
  );
}
