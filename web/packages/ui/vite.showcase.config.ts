import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Morphic showcase (PRD M0: "showcase Morphic dapat diakses").
export default defineConfig({
  root: 'showcase',
  plugins: [react()],
  build: { outDir: '../dist-showcase', emptyOutDir: true, sourcemap: process.env.SOURCEMAPS !== 'false' },
  server: { port: 5199 },
});
