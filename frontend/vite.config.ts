import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// Vite no longer fronts the user-facing port. Go owns :10000 and reverse-proxies
// non-/api, non-/ws requests here. We bind to an internal port; HMR is told to
// connect back through Go's port so the browser only ever talks to :10000.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 10001,
    strictPort: true,
    hmr: { clientPort: 10000 },
  },
  preview: {
    port: 10000,
    strictPort: true,
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    css: true,
  },
});
