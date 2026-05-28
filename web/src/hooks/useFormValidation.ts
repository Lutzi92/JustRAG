import { useState, useCallback, useRef } from 'react';

type ValidationRule = (value: string) => string | false;
type ValidationRules = Record<string, ValidationRule>;
type Errors = Record<string, string>;

export function useFormValidation(rules: ValidationRules) {
  const [errors, setErrors] = useState<Errors>({});
  const rulesRef = useRef(rules);
  rulesRef.current = rules;

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
