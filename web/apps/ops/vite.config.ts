import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';

// Operational Staff app — offline-first PWA (Technical Doc §6.4, PRD FR-SH-05).
// The service worker precaches the app shell and keeps the last bootstrap,
// session and menu so the app opens without a connection; actions go to the
// IndexedDB sync queue (@oneclub/offline).
export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      registerType: 'autoUpdate',
      manifest: {
        name: 'OneClub Ops',
        short_name: 'Ops',
        start_url: '/',
        display: 'standalone',
        orientation: 'any',
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
              ['/api/v1/public/bootstrap', '/api/v1/auth/me', '/api/v1/platform/navigation', '/api/v1/commercial/outlets'].some((p) => url.pathname.startsWith(p)),
            handler: 'NetworkFirst',
            options: { cacheName: 'ops-api', networkTimeoutSeconds: 3, expiration: { maxAgeSeconds: 12 * 3600 } },
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
