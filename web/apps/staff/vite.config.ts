import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';
import { AREAS } from '../../packages/shell/src/area-list';

// Staff App: every staff area in one SPA (Technical Doc §6.1). In development
// /api is proxied to the Go API; in production Caddy serves both on the same
// origin.
//
// One service worker (Technical Doc §6.4, PRD FR-SH-05) precaches only what
// the offline areas need: the app shell plus the chunks of the areas with
// `offline: true` in @oneclub/shell (Operational, Caddy Tablet). It keeps the
// last bootstrap, session and the data those areas read, so they open without
// a connection; actions go to the IndexedDB sync queue (@oneclub/offline).

const OFFLINE_AREAS = new RegExp(`[\\\\/]src[\\\\/]areas[\\\\/](${AREAS.filter((a) => a.offline).map((a) => a.code).join('|')})\\.tsx$`);

/** Collects the files of the app shell and the offline areas, with their static imports and CSS. */
function offlineAreaFiles(): Plugin & { files: Set<string> } {
  const files = new Set<string>();
  return {
    name: 'oneclub-offline-areas',
    apply: 'build',
    files,
    generateBundle(_, bundle) {
      files.clear();
      const add = (name: string) => {
        const c = bundle[name];
        if (!c || files.has(name)) return;
        files.add(name);
        if (c.type !== 'chunk') return;
        c.viteMetadata?.importedCss.forEach((css) => files.add(css));
        c.imports.forEach(add);
      };
      for (const c of Object.values(bundle)) {
        if (c.type === 'chunk' && (c.isEntry || (c.facadeModuleId && OFFLINE_AREAS.test(c.facadeModuleId)))) add(c.fileName);
      }
    },
  };
}

const offline = offlineAreaFiles();

export default defineConfig({
  plugins: [
    react(),
    offline,
    VitePWA({
      registerType: 'autoUpdate',
      manifest: {
        name: 'OneClub Staff',
        short_name: 'OneClub',
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
        manifestTransforms: [
          async (entries) => ({ manifest: entries.filter((e) => !/\.(js|css)$/.test(e.url) || offline.files.has(e.url)), warnings: [] }),
        ],
        runtimeCaching: [
          {
            urlPattern: ({ url }) =>
              ['/api/v1/public/bootstrap', '/api/v1/auth/me', '/api/v1/platform/navigation', '/api/v1/commercial/outlets', '/api/v1/golf/my-assignments',
                '/api/v1/golf/my-earnings', '/api/v1/golf/rounds', '/api/v1/golf/course-maps'].some((p) => url.pathname.startsWith(p)),
            handler: 'NetworkFirst',
            options: { cacheName: 'staff-api', networkTimeoutSeconds: 3, expiration: { maxAgeSeconds: 12 * 3600 } },
          },
          { urlPattern: /^https:\/\/fonts\.(googleapis|gstatic)\.com\//, handler: 'CacheFirst',
            // Google Fonts are cross-origin (opaque, status 0) — allow caching them so icons render offline.
            options: { cacheName: 'fonts', cacheableResponse: { statuses: [0, 200] }, expiration: { maxEntries: 30, maxAgeSeconds: 365 * 86400 } } },
        ],
      },
    }),
  ],
  server: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080', changeOrigin: false } } },
  preview: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080' } } },
  build: { sourcemap: process.env.SOURCEMAPS !== 'false', chunkSizeWarningLimit: 1500 },
});
