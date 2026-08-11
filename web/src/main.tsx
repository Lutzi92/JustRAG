import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import {
  shouldReloadForChunkError,
  readLastAttempt,
  RELOAD_GUARD_KEY,
} from './utils/chunkReload'

// Handle stale chunk errors after new deployments.
// When a lazy-loaded module fails to load (its hash no longer exists on the
// server), reload once to pick up the current app shell. The cooldown guard
// survives that reload, so a chunk that stays missing surfaces the error
// instead of looping — see utils/chunkReload.ts.
window.addEventListener('vite:preloadError', (event) => {
  const now = Date.now();
  if (!shouldReloadForChunkError(readLastAttempt(sessionStorage), now)) return;
  sessionStorage.setItem(RELOAD_GUARD_KEY, String(now));
  event.preventDefault();
  window.location.reload();
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
