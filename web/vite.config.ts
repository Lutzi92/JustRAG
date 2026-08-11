import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import compression from 'vite-plugin-compression'
import { VitePWA } from 'vite-plugin-pwa'
import { ViteImageOptimizer } from 'vite-plugin-image-optimizer'

/**
 * Appends a content fingerprint to the icon URLs in index.html.
 *
 * Files under public/ are copied to the dist root verbatim, so their URLs
 * never change even when their bytes do. For most assets a revalidating
 * Cache-Control is enough to fix that — but not for favicons: Chrome stores
 * them in a dedicated SQLite database outside the HTTP cache, which a hard
 * reload and a private window both leave untouched. The only thing that
 * reliably retires a favicon there is a different URL.
 *
 * Hashing the file's own content (rather than stamping a build id) means the
 * URL changes exactly when the icon changes, so unrelated deploys don't force
 * a needless refetch. Missing files are left alone — a broken build is worse
 * than a stale icon.
 */
function versionedIcons(): Plugin {
  const ICONS = ['vite.svg', 'apple-touch-icon.png']

  const fingerprint = (file: string): string | null => {
    try {
      const bytes = readFileSync(new URL(`./public/${file}`, import.meta.url))
      return createHash('sha256').update(bytes).digest('hex').slice(0, 8)
    } catch {
      return null
    }
  }

  return {
    name: 'justrag-versioned-icons',
    transformIndexHtml(html) {
      return ICONS.reduce((acc, file) => {
        const hash = fingerprint(file)
        if (!hash) return acc
        // Only rewrite root-absolute references, which is how index.html
        // addresses public/ assets. Escape the dot in the filename.
        const pattern = new RegExp(`(href=")/${file.replace(/\./g, '\\.')}(")`, 'g')
        return acc.replace(pattern, `$1/${file}?v=${hash}$2`)
      }, html)
    },
  }
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    versionedIcons(),
    // Compiles the @ki4jlu/design-system `@theme` block in src/index.css into
    // CSS variables. Preflight is deliberately not imported — see index.css.
    tailwindcss(),
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
        // No skipWaiting here: the prompt flow needs the new SW to wait until
        // the user accepts, at which point updateServiceWorker(true) triggers
        // skip-waiting. clientsClaim lets the activated SW take control of the
        // already-open page on that reload.
        clientsClaim: true,
        // Exclude API calls from service worker caching — API responses
        // change with deployments and stale cached responses cause errors
        navigateFallbackDenylist: [/^\/api\//],
        // No runtimeCaching for fonts: they are self-hosted now, so they are
        // fingerprinted into /assets and precached like any other build
        // output. The previous CacheFirst rules for fonts.googleapis.com /
        // fonts.gstatic.com would never match again. woff2 is in the glob
        // below or the app drops to system fonts offline.
        globPatterns: ['**/*.{js,css,html,ico,png,svg,woff2}']
      }
    })
  ],
  build: {
    target: 'es2022',
    // Never inline font files. Vite base64s any asset under 4 KiB into the
    // stylesheet, which would have swallowed the two smallest subsets — and an
    // inlined face is fetched by every visitor regardless of its unicode-range,
    // defeating the per-subset loading the @font-face sheet exists to get.
    assetsInlineLimit: (filePath: string) =>
      filePath.endsWith('.woff2') ? false : undefined,
    minify: 'oxc',
    sourcemap: false,
    reportCompressedSize: false,
    rollupOptions: {
      output: {
        // Vite 8 uses rolldown, whose manualChunks is function-only (the
        // Rollup object form is unsupported). Same four vendor chunks, keyed
        // on the node_modules package path.
        manualChunks: (id) => {
          if (!id.includes('node_modules')) return;
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return 'vendor-react';
          if (/[\\/]node_modules[\\/](framer-motion|motion|motion-dom|motion-utils|lucide-react)[\\/]/.test(id)) return 'vendor-ui';
          if (/[\\/]node_modules[\\/]recharts[\\/]/.test(id)) return 'vendor-charts';
          if (/[\\/]node_modules[\\/](react-markdown|remark-gfm)[\\/]/.test(id)) return 'vendor-markdown';
        },
      }
    }
  }
})
