import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'

// Handle stale chunk errors after new deployments.
// When a lazy-loaded module fails to load (e.g. old hash no longer exists),
// reload the page once to fetch the new assets.
window.addEventListener('vite:preloadError', (event) => {
  const reloadedKey = 'chunk-reload';
  if (!sessionStorage.getItem(reloadedKey)) {
    sessionStorage.setItem(reloadedKey, '1');
    event.preventDefault();
    window.location.reload();
  }
});
// Clear the reload guard on successful page load so future deploys can trigger it again
window.addEventListener('load', () => {
  sessionStorage.removeItem('chunk-reload');
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
