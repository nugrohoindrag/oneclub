import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';

// Caddy Tablet — offline-first PWA (PRD P2 EP-06 FR-CTB-01).
// The service worker precaches the app shell and keeps the last bootstrap,
// session and menu so the app opens without a connection; actions go to the
// IndexedDB sync queue (@oneclub/offline).
export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      registerType: 'autoUpdate',
      manifest: {
        name: 'OneClub Caddy',
        short_name: 'Caddy',
        start_url: '/',
        display: 'standalone',
        orientation: 'portrait',
        background_color: '#F1F3F5',
        theme_color: '#254E09',
        icons: [{ src: '/favicon.svg', sizes: 'any', type: 'image/svg+xml', purpose: 'any' }],
      },
      workbox: {
        navigateFallback: '/index.html',
        navigateFallbackDenylist: [/^\/api\//],
        runtimeCaching: [
          {
            urlPattern: ({ url }) =>
              ['/api/v1/public/bootstrap', '/api/v1/auth/me', '/api/v1/platform/navigation', '/api/v1/golf/my-assignments', '/api/v1/golf/my-earnings', '/api/v1/golf/rounds', '/api/v1/golf/course-maps'].some((p) => url.pathname.startsWith(p)),
            handler: 'NetworkFirst',
            options: { cacheName: 'caddy-api', networkTimeoutSeconds: 3, expiration: { maxAgeSeconds: 12 * 3600 } },
          },
          { urlPattern: /^https:\/\/fonts\.(googleapis|gstatic)\.com\//, handler: 'CacheFirst',
            // Google Fonts are cross-origin (opaque, status 0) — allow caching them so icons render offline.
            options: { cacheName: 'fonts', cacheableResponse: { statuses: [0, 200] }, expiration: { maxEntries: 30, maxAgeSeconds: 365 * 86400 } } },
        ],
      },
    }),
  ],
  server: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080' } } },
  preview: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080' } } },
  build: { sourcemap: process.env.SOURCEMAPS !== 'false', chunkSizeWarningLimit: 1500 },
});
