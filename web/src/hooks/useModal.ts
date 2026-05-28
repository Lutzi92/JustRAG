import { useState, useCallback } from 'react';
import type { ModalState } from '../components/Modal';

export function useModal() {
  const [modal, setModal] = useState<ModalState>({
    show: false,
    type: 'alert',
    title: '',
    message: '',
    value: '',
    onConfirm: () => { },
    onCancel: () => { }
  });

  const showAlert = useCallback((message: string, title = 'Hinweis') => {
    return new Promise<void>((resolve) => {
      setModal({
        show: true,
        type: 'alert',
        title,
        message,
        onConfirm: () => {
          setModal(m => ({ ...m, show: false }));
          resolve();
        },
        onCancel: () => {
          setModal(m => ({ ...m, show: false }));
          resolve();
        }
      });
    });
  }, []);

  const showConfirm = useCallback((message: string, title = 'Bestätigen') => {
    return new Promise<boolean>((resolve) => {
      setModal({
        show: true,
        type: 'confirm',
        title,
        message,
        onConfirm: () => {
          setModal(m => ({ ...m, show: false }));
          resolve(true);
        },
        onCancel: () => {
          setModal(m => ({ ...m, show: false }));
          resolve(false);
        }
      });
    });
  }, []);

  const showPrompt = useCallback((message: string, defaultValue = '', title = 'Eingabe') => {
    return new Promise<string | null>((resolve) => {
      setModal({
        show: true,
        type: 'prompt',
        title,
        message,
        value: defaultValue,
        onConfirm: (val) => {
          setModal(m => ({ ...m, show: false }));
          resolve(val || '');
        },
        onCancel: () => {
          setModal(m => ({ ...m, show: false }));
          resolve(null);
        }
      });
    });
  }, []);

  return { modal, setModal, showAlert, showConfirm, showPrompt };
}
