import { describe, it, expect } from 'vitest';
import { AxiosError } from 'axios';
import { getApiErrorMessage } from './apiError';

function makeAxiosError(data: unknown, status = 400): AxiosError {
    const err = new AxiosError('Request failed');
    err.response = {
        data,
        status,
        statusText: 'Bad Request',
        headers: {},
        config: { headers: {} } as any,
    };
    return err;
}

describe('getApiErrorMessage', () => {
    it('extracts error field from response data', () => {
        const err = makeAxiosError({ error: 'File too large' });
        expect(getApiErrorMessage(err, 'fallback')).toBe('File too large');
    });

    it('extracts message field when error is absent', () => {
        const err = makeAxiosError({ message: 'Invalid input' });
        expect(getApiErrorMessage(err, 'fallback')).toBe('Invalid input');
    });

    it('prefers details[0].message over error (Zod validation)', () => {
        const err = makeAxiosError({
            error: 'Validation failed',
            details: [{ field: 'api_key', message: 'API key is required' }],
        });
        expect(getApiErrorMessage(err, 'fallback')).toBe('API key is required');
    });

    it('falls back to error when details is empty', () => {
        const err = makeAxiosError({ error: 'Validation failed', details: [] });
        expect(getApiErrorMessage(err, 'fallback')).toBe('Validation failed');
    });

    it('prefers error over message', () => {
        const err = makeAxiosError({ error: 'Specific', message: 'Generic' });
        expect(getApiErrorMessage(err, 'fallback')).toBe('Specific');
    });

    it('uses string response data as message', () => {
        const err = makeAxiosError('Plain text error');
        expect(getApiErrorMessage(err, 'fallback')).toBe('Plain text error');
    });

    it('returns fallback for non-axios errors', () => {
        expect(getApiErrorMessage(new Error('boom'), 'fallback')).toBe('fallback');
    });

    it('returns fallback when response has no useful data', () => {
        const err = makeAxiosError({});
        expect(getApiErrorMessage(err, 'fallback')).toBe('fallback');
    });

    it('returns fallback for null/undefined data', () => {
        const err = makeAxiosError(null);
        expect(getApiErrorMessage(err, 'fallback')).toBe('fallback');
    });

    it('returns fallback for empty string data', () => {
        const err = makeAxiosError('');
        expect(getApiErrorMessage(err, 'fallback')).toBe('fallback');
    });
});
