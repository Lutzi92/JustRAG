// web/src/hooks/useFormValidation.test.ts
import { renderHook, act } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import { useFormValidation } from './useFormValidation';

describe('useFormValidation', () => {
  const rules = {
    name: (v: string) => (!v.trim() ? 'Name is required' : false),
    email: (v: string) => (!v.trim() ? 'Email is required' : false),
  };

  it('returns no errors initially', () => {
    const { result } = renderHook(() => useFormValidation(rules));
    expect(result.current.errors).toEqual({});
  });

  it('returns errors for invalid fields', () => {
    const { result } = renderHook(() => useFormValidation(rules));
    let valid: boolean;
    act(() => { valid = result.current.validate({ name: '', email: 'a@b.com' }); });
    expect(valid!).toBe(false);
    expect(result.current.errors).toEqual({ name: 'Name is required' });
  });

  it('returns true when all fields valid', () => {
    const { result } = renderHook(() => useFormValidation(rules));
    let valid: boolean;
    act(() => { valid = result.current.validate({ name: 'Alice', email: 'a@b.com' }); });
    expect(valid!).toBe(true);
    expect(result.current.errors).toEqual({});
  });

  it('clears a single field error', () => {
    const { result } = renderHook(() => useFormValidation(rules));
    act(() => { result.current.validate({ name: '', email: '' }); });
    expect(Object.keys(result.current.errors)).toHaveLength(2);
    act(() => { result.current.clearError('name'); });
    expect(result.current.errors.name).toBeUndefined();
    expect(result.current.errors.email).toBe('Email is required');
  });

  it('clearAll removes all errors', () => {
    const { result } = renderHook(() => useFormValidation(rules));
    act(() => { result.current.validate({ name: '', email: '' }); });
    act(() => { result.current.clearAll(); });
    expect(result.current.errors).toEqual({});
  });

  it('clearError is a no-op when field has no error', () => {
    const { result } = renderHook(() => useFormValidation(rules));
    const before = result.current.errors;
    act(() => { result.current.clearError('name'); });
    expect(result.current.errors).toBe(before); // same reference
  });
});
