import { createContext, useContext, type ReactNode } from 'react';
import { useModal } from '../hooks/useModal';
import { Modal } from '../components/Modal';
import type { ModalState } from '../components/Modal';

interface ModalContextValue {
  modal: ModalState;
  setModal: React.Dispatch<React.SetStateAction<ModalState>>;
  showAlert: (message: string, title?: string) => Promise<void>;
  showConfirm: (message: string, title?: string) => Promise<boolean>;
  showPrompt: (message: string, defaultValue?: string, title?: string) => Promise<string | null>;
}

const ModalContext = createContext<ModalContextValue | null>(null);

export function ModalProvider({ children }: { children: ReactNode }) {
  const modalState = useModal();

  return (
    <ModalContext.Provider value={modalState}>
      {children}
      <Modal modal={modalState.modal} setModal={modalState.setModal} />
    </ModalContext.Provider>
  );
}

export function useModalContext(): ModalContextValue {
  const ctx = useContext(ModalContext);
  if (!ctx) throw new Error('useModalContext must be used within ModalProvider');
  return ctx;
}
