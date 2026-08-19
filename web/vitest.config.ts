import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { fileURLToPath } from 'node:url'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      // vite-plugin-pwa generates this module at build time and is registered
      // only in vite.config.ts, so under vitest the import is unresolvable.
      // ReloadPrompt imports it, and App.tsx imports ReloadPrompt — which made
      // the application's root component impossible to import in any test.
      // The stub reports "no update waiting"; there is no service worker here.
      'virtual:pwa-register/react': fileURLToPath(
        new URL('./src/test/pwaRegisterStub.ts', import.meta.url),
      ),
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    css: false,
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
