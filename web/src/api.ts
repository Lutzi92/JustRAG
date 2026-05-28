const getBaseUrl = () => {
    // If a VITE_API_URL is provided, use it.
    if (import.meta.env.VITE_API_URL) {
        return import.meta.env.VITE_API_URL;
    }

    // In development, Vite usually runs on a different port than the backend.
    if (import.meta.env.DEV) {
        return 'http://localhost:3001';
    }

    // In production, we assume the frontend is served by the backend or proxied to the same origin.
    // Using an empty string or '/' makes requests relative to the current host.
    return '';
};

export const API_BASE_URL = getBaseUrl();
export const API_URL = `${API_BASE_URL}/api`;

/**
 * Wrapper around fetch that auto-injects the Authorization header
 * from localStorage and triggers logout on 401 responses.
 * Use this instead of raw fetch() for SSE streaming and binary downloads.
 */
export const authFetch: typeof fetch = async (input, init) => {
    const token = localStorage.getItem('token');
    const headers = new Headers(init?.headers);
    if (token && !headers.has('Authorization')) {
        headers.set('Authorization', `Bearer ${token}`);
    }
    const response = await fetch(input, { ...init, headers });
    if (response.status === 401) {
        window.dispatchEvent(new Event('auth:unauthorized'));
    }
    return response;
};
