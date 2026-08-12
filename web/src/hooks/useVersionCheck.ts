import { useEffect, useRef } from 'react';
import { API_BASE_URL } from '../api';

const POLL_INTERVAL = 60_000; // Check every 60 seconds

/**
 * Polls the server's /version endpoint and triggers a page reload
 * when the build version changes (i.e., after a new deployment).
 * The version is `git describe --tags --always` output — `v0.1.0` on a release
 * build, `v0.1.0-12-gabc1234` on a main build, a bare short SHA before the
 * first tag. Unique per commit and stable across all replicas of the same
 * release, which is all this hook needs: it only compares for inequality.
 */
export function useVersionCheck() {
  const knownVersion = useRef<string | null>(null);

  useEffect(() => {
    // In dev the backend falls back to a per-process UUID, so every
    // nodemon restart would trigger a spurious full-page reload.
    if (import.meta.env.DEV) return;

    let timer: ReturnType<typeof setInterval>;

    const check = async () => {
      try {
        const res = await fetch(`${API_BASE_URL}/version`, { cache: 'no-store' });
        if (!res.ok) return;
        const { version } = await res.json();
        if (!version) return;

        if (knownVersion.current === null) {
          // First successful check — just record the current version
          knownVersion.current = version;
        } else if (knownVersion.current !== version) {
          // New deployment detected. When a service worker controls this page
          // the PWA update prompt (ReloadPrompt) owns the refresh — a plain
          // reload would just re-serve the stale precache and never pick up
          // the new bundle. Only hard-reload when no SW is in control (e.g.
          // SW unsupported, unregistered, or first load before activation).
          const swControlled =
            'serviceWorker' in navigator && navigator.serviceWorker.controller !== null;
          if (!swControlled) {
            window.location.reload();
          }
        }
      } catch {
        // Network error — ignore, will retry next interval
      }
    };

    // Initial check after a short delay (don't block startup)
    const initialTimeout = setTimeout(() => {
      check();
      timer = setInterval(check, POLL_INTERVAL);
    }, 5_000);

    return () => {
      clearTimeout(initialTimeout);
      clearInterval(timer);
    };
  }, []);
}
