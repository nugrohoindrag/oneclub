import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';

// Member & Guest Portal (PWA). Read-only cache of the shell; API data is
// always fetched fresh (Technical Doc §6.1: read-only offline cache in P1).
export default defineConfig({
  plugins: [
    react(),
    VitePWA({
      registerType: 'autoUpdate',
      manifest: {
        name: 'Member Portal',
        short_name: 'Members',
        start_url: '/',
        display: 'standalone',
        background_color: '#F1F3F5',
        theme_color: '#254E09',
        icons: [{ src: '/favicon.png', sizes: '120x108', type: 'image/png', purpose: 'any' }],
      },
      workbox: {
        navigateFallback: '/index.html',
        navigateFallbackDenylist: [/^\/api\//],
        // woff2: the self-hosted Material Symbols subset (@oneclub/shell, decision 4g).
        globPatterns: ['**/*.{js,css,html,woff2}'],
        // Push notifications (PRD P5 FR-INT-P5-05): public/push-sw.js shows journey offers, tier changes and bookings.
        importScripts: ['/push-sw.js'],
        runtimeCaching: [{ urlPattern: /^\/api\/v1\/public\/bootstrap/, handler: 'StaleWhileRevalidate', options: { cacheName: 'bootstrap' } }],
      },
    }),
  ],
  server: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080' } } },
  preview: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080' } } },
  build: { sourcemap: process.env.SOURCEMAPS !== 'false', chunkSizeWarningLimit: 1500 },
});
