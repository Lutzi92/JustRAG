import { createContext, useCallback, useContext, useReducer, type ReactNode } from 'react';

export interface Toast {
  id: string;
  type: 'success' | 'error' | 'info' | 'warning';
  message: string;
  duration: number;
}

interface ToastOptions {
  duration?: number;
}

interface ToastApi {
  success: (message: string, options?: ToastOptions) => void;
  error: (message: string, options?: ToastOptions) => void;
  info: (message: string, options?: ToastOptions) => void;
  warning: (message: string, options?: ToastOptions) => void;
}

interface ToastContextValue {
  toasts: Toast[];
  toast: ToastApi;
  removeToast: (id: string) => void;
}

type Action =
  | { type: 'ADD'; toast: Toast }
  | { type: 'REMOVE'; id: string };

const MAX_TOASTS = 5;

function toastReducer(state: Toast[], action: Action): Toast[] {
  switch (action.type) {
    case 'ADD': {
      const next = [...state, action.toast];
      return next.length > MAX_TOASTS ? next.slice(next.length - MAX_TOASTS) : next;
    }
    case 'REMOVE':
      return state.filter(t => t.id !== action.id);
    default:
      return state;
  }
}

const DEFAULT_DURATIONS: Record<Toast['type'], number> = {
  success: 4000,
  error: 6000,
  info: 4000,
  warning: 5000,
};

let toastCounter = 0;

const ToastContext = createContext<ToastContextValue | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, dispatch] = useReducer(toastReducer, []);

  const removeToast = useCallback((id: string) => {
    dispatch({ type: 'REMOVE', id });
  }, []);

  const addToast = useCallback((type: Toast['type'], message: string, options?: ToastOptions) => {
    const id = `toast-${++toastCounter}`;
    const duration = options?.duration ?? DEFAULT_DURATIONS[type];
    dispatch({ type: 'ADD', toast: { id, type, message, duration } });
  }, []);

  const toast: ToastApi = {
    success: useCallback((msg: string, opts?: ToastOptions) => addToast('success', msg, opts), [addToast]),
    error: useCallback((msg: string, opts?: ToastOptions) => addToast('error', msg, opts), [addToast]),
    info: useCallback((msg: string, opts?: ToastOptions) => addToast('info', msg, opts), [addToast]),
    warning: useCallback((msg: string, opts?: ToastOptions) => addToast('warning', msg, opts), [addToast]),
  };

  return (
    <ToastContext.Provider value={{ toasts, toast, removeToast }}>
      {children}
    </ToastContext.Provider>
  );
}

export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used within ToastProvider');
  return ctx.toast;
}

export function useToastContext(): ToastContextValue {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToastContext must be used within ToastProvider');
  return ctx;
}
