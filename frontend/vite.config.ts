/**
 * Vite's intentionally minimal React integration.
 *
 * The React plugin supplies the JSX transform and state-preserving Fast
 * Refresh while leaving routing, data sources, and backend proxying undecided.
 */
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  // Keep the scaffold small; add proxy or environment configuration only when
  // a real backend adapter requires it.
  plugins: [react()],
});
