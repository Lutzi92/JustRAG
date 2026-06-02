import type { KbSettingsResponse } from '../../types';
import { API_BASE_URL, authFetch } from '../../api';

// authFetch injects the Bearer token from localStorage (the app's auth scheme)
// and fires the logout event on 401 — raw fetch() would send no Authorization
// header and 401. URLs are prefixed with API_BASE_URL because there is no dev
// proxy for /api (matches every other authFetch caller).
const url = (kbId: string, suffix = ''): string =>
  `${API_BASE_URL}/api/kb/${kbId}/settings${suffix}`;

export async function fetchKbSettings(kbId: string): Promise<KbSettingsResponse> {
  const res = await authFetch(url(kbId));
  if (!res.ok) throw new Error(`fetch settings: ${res.status}`);
  return res.json();
}

export async function saveKbSettings(kbId: string, configs: Record<string, string>): Promise<void> {
  const res = await authFetch(url(kbId), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ configs }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `save settings: ${res.status}`);
  }
}

export async function resetKbSetting(kbId: string, key: string): Promise<void> {
  const res = await authFetch(url(kbId, `/${encodeURIComponent(key)}`), { method: 'DELETE' });
  if (!res.ok) throw new Error(`reset setting: ${res.status}`);
}
