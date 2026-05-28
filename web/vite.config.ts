import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import compression from 'vite-plugin-compression'
import { VitePWA } from 'vite-plugin-pwa'
import { ViteImageOptimizer } from 'vite-plugin-image-optimizer'

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    ViteImageOptimizer(),
    compression({
      algorithm: 'gzip',
      ext: '.gz',
    }),
    compression({
      algorithm: 'brotliCompress',
      ext: '.br',
    }),
    VitePWA({
      // 'prompt' (not 'autoUpdate'): a freshly deployed service worker stays
      // in the waiting state and surfaces a user-driven reload prompt
      // (see components/ReloadPrompt.tsx) instead of silently swapping the
      // app shell mid-session. updateServiceWorker(true) posts SKIP_WAITING.
      registerType: 'prompt',
      includeAssets: ['vite.svg', 'apple-touch-icon.png'],
      manifest: {
        name: 'JustRAG',
        short_name: 'JustRAG',
        description: 'Advanced Knowledge Retrieval System',
        theme_color: '#ffffff',
        icons: [
          {
            src: 'apple-touch-icon.png',
            sizes: '180x180',
            type: 'image/png'
          },
          {
            src: 'vite.svg',
            sizes: 'any',
            type: 'image/svg+xml'
          }
        ]
      },
      workbox: {
        globPatterns: ['**/*.{js,css,html,ico,png,svg}'],
        // No skipWaiting here: the prompt flow needs the new SW to wait until
        // the user accepts, at which point updateServiceWorker(true) triggers
        // skip-waiting. clientsClaim lets the activated SW take control of the
        // already-open page on that reload.
        clientsClaim: true,
        // Exclude API calls from service worker caching — API responses
        // change with deployments and stale cached responses cause errors
        navigateFallbackDenylist: [/^\/api\//],
        runtimeCaching: [
          {
            urlPattern: /^https:\/\/fonts\.googleapis\.com\/.*/i,
            handler: 'CacheFirst',
            options: {
              cacheName: 'google-fonts-cache',
              expiration: {
                maxEntries: 10,
                maxAgeSeconds: 60 * 60 * 24 * 365 // <== 365 days
              },
              cacheableResponse: {
                statuses: [0, 200]
              }
            }
          },
          {
            urlPattern: /^https:\/\/fonts\.gstatic\.com\/.*/i,
            handler: 'CacheFirst',
            options: {
              cacheName: 'gstatic-fonts-cache',
              expiration: {
                maxEntries: 10,
                maxAgeSeconds: 60 * 60 * 24 * 365 // <== 365 days
              },
              cacheableResponse: {
                statuses: [0, 200]
              },
            }
          }
        ]
      }
    })
  ],
  build: {
    target: 'es2022',
    minify: 'esbuild',
    sourcemap: false,
    reportCompressedSize: false,
    rollupOptions: {
      output: {
        manualChunks: {
          'vendor-react': ['react', 'react-dom'],
          'vendor-ui': ['framer-motion', 'lucide-react'],
          'vendor-charts': ['recharts'],
          'vendor-markdown': ['react-markdown', 'remark-gfm'],
        }
      }
    }
  }
})
