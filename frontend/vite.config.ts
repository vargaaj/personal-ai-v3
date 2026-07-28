/**
 * Local development configuration for the React frontend.
 *
 * The React plugin supplies the JSX transform and state-preserving Fast
 * Refresh. The development proxy connects browser API requests to the separate
 * Go process without changing the same-origin URLs used by application code.
 */
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [react()],

  // The browser always requests the relative URL `/api/areas`. While Vite owns
  // localhost:5173 in development, this proxy forwards that path to the Go
  // server on localhost:8081 and returns its response to the browser. This also
  // avoids development-only CORS settings. The production bundle does not use
  // Vite's dev server; Go will serve `/api` directly from the same Cloud Run
  // origin, so App.tsx does not need a different production URL.
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8081',
        changeOrigin: true,
      },
    },
  },
});
