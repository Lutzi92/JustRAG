import { describe, it, expect } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useModal } from './useModal';

describe('useModal', () => {
  it('starts with show: false', () => {
    const { result } = renderHook(() => useModal());
    expect(result.current.modal.show).toBe(false);
  });

  describe('showAlert', () => {
    it('sets show: true and type: "alert"', () => {
      const { result } = renderHook(() => useModal());

      act(() => { result.current.showAlert('test message'); });

      expect(result.current.modal.show).toBe(true);
      expect(result.current.modal.type).toBe('alert');
      expect(result.current.modal.message).toBe('test message');
    });

    it('resolves and hides modal when onConfirm is called', async () => {
      const { result } = renderHook(() => useModal());

      let resolved = false;
      act(() => {
        result.current.showAlert('msg').then(() => { resolved = true; });
      });

      await act(async () => { result.current.modal.onConfirm(); });

      expect(resolved).toBe(true);
      expect(result.current.modal.show).toBe(false);
    });
  });

  describe('showConfirm', () => {
    it('resolves true on confirm', async () => {
      const { result } = renderHook(() => useModal());

      let value: boolean | undefined;
      act(() => {
        result.current.showConfirm('sure?').then(v => { value = v; });
      });

      await act(async () => { result.current.modal.onConfirm(); });

      expect(value).toBe(true);
      expect(result.current.modal.show).toBe(false);
    });

    it('resolves false on cancel', async () => {
      const { result } = renderHook(() => useModal());

      let value: boolean | undefined;
      act(() => {
        result.current.showConfirm('sure?').then(v => { value = v; });
      });

      await act(async () => { result.current.modal.onCancel(); });

      expect(value).toBe(false);
      expect(result.current.modal.show).toBe(false);
    });
  });

  describe('showPrompt', () => {
    it('resolves with value on confirm', async () => {
      const { result } = renderHook(() => useModal());

      let value: string | null | undefined;
      act(() => {
        result.current.showPrompt('Name?', 'default').then(v => { value = v; });
      });

      await act(async () => { result.current.modal.onConfirm('user input'); });

      expect(value).toBe('user input');
      expect(result.current.modal.show).toBe(false);
    });

    it('resolves null on cancel', async () => {
      const { result } = renderHook(() => useModal());

      let value: string | null | undefined = 'sentinel';
      act(() => {
        result.current.showPrompt('Name?').then(v => { value = v; });
      });

      await act(async () => { result.current.modal.onCancel(); });

      expect(value).toBeNull();
      expect(result.current.modal.show).toBe(false);
    });
  });
});
