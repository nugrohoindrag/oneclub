import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Platform Administration SPA (internal OneClub team). In development /api is proxied to
// the Go API; in production Caddy serves both on the same origin.
export default defineConfig({
  plugins: [react()],
  server: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080', changeOrigin: false } } },
  preview: { proxy: { '/api': { target: process.env.ONECLUB_API ?? 'http://localhost:8080' } } },
  build: { sourcemap: process.env.SOURCEMAPS !== 'false', chunkSizeWarningLimit: 1500 },
});
