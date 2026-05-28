export const HAPTIC_PATTERNS = {
  send: 16,
  toggle: 10,
  reveal: 8,
  action: 8,
} as const;

export function triggerHaptic(pattern: number | number[]): boolean {
  if (typeof navigator === 'undefined' || typeof navigator.vibrate !== 'function') {
    return false;
  }

  try {
    return navigator.vibrate(pattern);
  } catch {
    return false;
  }
}
