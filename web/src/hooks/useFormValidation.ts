import { useState, useCallback, useRef, useEffect } from 'react';

type ValidationRule = (value: string) => string | false;
type ValidationRules = Record<string, ValidationRule>;
type Errors = Record<string, string>;

export function useFormValidation(rules: ValidationRules) {
  const [errors, setErrors] = useState<Errors>({});
  // Keep the latest rules in a ref without writing it during render.
  // `validate` is only invoked from event handlers (after the effect has
  // run), so syncing in an effect preserves the "always latest" semantics.
  const rulesRef = useRef(rules);
  useEffect(() => {
    rulesRef.current = rules;
  });

  const validate = useCallback((values: Record<string, string>): boolean => {
    const newErrors: Errors = {};
    for (const [field, rule] of Object.entries(rulesRef.current)) {
      const error = rule(values[field] ?? '');
      if (error) newErrors[field] = error;
    }
    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  }, []);

  const clearError = useCallback((field: string) => {
    setErrors(prev => {
      if (!prev[field]) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  }, []);

  const clearAll = useCallback(() => setErrors({}), []);

  return { errors, validate, clearError, clearAll };
}
