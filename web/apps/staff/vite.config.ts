import { defineConfig, type Plugin, type PreviewServer, type ViteDevServer } from 'vite';
import react from '@vitejs/plugin-react';
import { VitePWA } from 'vite-plugin-pwa';
import { AREAS, SURFACES, type Surface } from '../../packages/shell/src/area-list';
import { cacheableApiPattern } from './src/sw-cache';

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

// One build on four domains (Technical Doc §6.1). Caddy serves each domain
// its `/surface.json` and rewrites `/manifest.webmanifest` to the manifest
// of the surface, so a tablet installs "Caddy", not "OneClub Staff".
const MANIFEST = {
  name: 'OneClub Staff',
  short_name: 'OneClub',
  start_url: '/',
  display: 'standalone' as const,
  orientation: 'any' as const,
  background_color: '#F1F3F5',
  theme_color: '#254E09',
  icons: [{ src: '/favicon.svg', sizes: 'any', type: 'image/svg+xml', purpose: 'any' }],
};
const SURFACE_NAMES: Record<Surface, string> = { dashboard: 'OneClub', cashier: 'Cashier', caddy: 'Caddy', kitchen: 'Kitchen' };

/**
 * Emits manifest-<surface>.webmanifest. In development and preview it also
 * plays Caddy's part for <surface>.localhost (browsers resolve *.localhost
 * to this machine), so the surfaces are tried on one port: e.g.
 * http://cashier.localhost:5173. Plain localhost has no surface.
 */
function surfaces(): Plugin {
  const serve = (server: ViteDevServer | PreviewServer) => {
    server.middlewares.use((req, res, next) => {
      const m = /^(dashboard|cashier|caddy|kitchen)\.localhost(:\d+)?$/.exec(req.headers.host ?? '');
      if (!m) return next();
      const surface = m[1] as Surface;
      if (req.url === '/surface.json') {
        const domains = Object.fromEntries(SURFACES.map((s) => [s, `http://${s}.localhost${m[2] ?? ''}`]));
        res.setHeader('Content-Type', 'application/json');
        res.setHeader('Cache-Control', 'no-cache');
        return res.end(JSON.stringify({ surface, domains }));
      }
      if (req.url === '/manifest.webmanifest') req.url = `/manifest-${surface}.webmanifest`;
      next();
    });
  };
  return {
    name: 'oneclub-surfaces',
    configureServer: serve,
    configurePreviewServer: serve,
    generateBundle() {
      for (const s of SURFACES) {
        const name = s === 'dashboard' ? MANIFEST.name : `OneClub ${SURFACE_NAMES[s]}`;
        this.emitFile({ type: 'asset', fileName: `manifest-${s}.webmanifest`, source: JSON.stringify({ ...MANIFEST, name, short_name: SURFACE_NAMES[s] }) });
      }
    },
  };
}

export default defineConfig({
  plugins: [
    react(),
    offline,
    surfaces(),
    VitePWA({
      registerType: 'autoUpdate',
      manifest: MANIFEST,
      workbox: {
        navigateFallback: '/index.html',
        navigateFallbackDenylist: [/^\/api\//],
        // woff2: the self-hosted Material Symbols subset (@oneclub/shell,
        // decision 4g), so icons render on a device that never was online
        // after install.
        globPatterns: ['**/*.{js,css,html,woff2}'],
        manifestTransforms: [
          async (entries) => ({ manifest: entries.filter((e) => !/\.(js|css)$/.test(e.url) || offline.files.has(e.url)), warnings: [] }),
        ],
        // Push notifications of Employee Self Service (PRD P5 FR-INT-P5-05): public/push-sw.js shows them and opens their link.
        importScripts: ['/push-sw.js'],
        runtimeCaching: [
          {
            // Payroll, salary and personal HR data are never cached offline (PRD P5 FR-OPS-P5-05, src/sw-cache.ts).
            urlPattern: cacheableApiPattern, // self-contained: workbox copies its source into sw.js
            handler: 'NetworkFirst',
            options: { cacheName: 'staff-api', networkTimeoutSeconds: 3, expiration: { maxAgeSeconds: 12 * 3600 } },
          },
          { urlPattern: /^https:\/\/fonts\.(googleapis|gstatic)\.com\//, handler: 'CacheFirst',
            // Text fonts (Inter, Roboto Flex) still come from Google Fonts and fall back to system
            // fonts offline; cross-origin (opaque, status 0) — allow caching them once seen.
            options: { cacheName: 'fonts', cacheableResponse: { statuses: [0, 200] }, expiration: { maxEntries: 30, maxAgeSeconds: 365 * 86400 } } },
        ],
      },
    }),
  ],
  server: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080', changeOrigin: false } } },
  preview: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080' } } },
  build: { sourcemap: process.env.SOURCEMAPS !== 'false', chunkSizeWarningLimit: 1500 },
});
