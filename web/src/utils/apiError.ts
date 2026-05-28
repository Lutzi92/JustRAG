import axios from 'axios';

/**
 * Extract a human-readable error message from an axios error or unknown error.
 *
 * Checks (in order): `response.data.details[0].message` (Zod validation),
 * `response.data.error`, `response.data.message`,
 * `response.data` (if string), then falls back to the provided default.
 */
export function getApiErrorMessage(err: unknown, fallback: string): string {
    if (axios.isAxiosError(err)) {
        const data = err.response?.data;
        if (data && typeof data === 'object') {
            // Zod validation errors: { error: 'Validation failed', details: [{ message: '...' }] }
            if (Array.isArray(data.details) && data.details.length > 0) {
                const detail = data.details[0];
                if (detail && typeof detail.message === 'string' && detail.message) return detail.message;
            }
            if (typeof data.error === 'string' && data.error) return data.error;
            if (typeof data.message === 'string' && data.message) return data.message;
        }
        if (typeof data === 'string' && data) return data;
    }
    return fallback;
}
