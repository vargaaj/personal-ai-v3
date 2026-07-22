/**
 * Browser entry point for the React frontend.
 *
 * This file intentionally contains only application bootstrapping and global
 * asset imports. Product behavior belongs in App.tsx, while backend-facing
 * data access will live behind the frontend data layer in a later increment.
 */
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

// Self-host the design-system typefaces so the interface does not depend on a
// third-party font CDN at runtime.
import "@fontsource-variable/literata/wght.css";
import "@fontsource-variable/public-sans/wght.css";
import "@fontsource/ibm-plex-mono/400.css";
import "@fontsource/ibm-plex-mono/500.css";

import ReviewApp from "./App";
import "./styles.css";

// index.html owns this mount point. Failing loudly is preferable to silently
// rendering nothing when the entry document and application drift apart.
const root = document.getElementById("root");

if (!root) {
  throw new Error("The application root element is missing.");
}

// This is the standard React 18+ browser startup pattern. `createRoot` connects
// React to `<div id="root">` in index.html, and `render` places ReviewApp inside
// that element. ReviewApp creates the visible page; createRoot only establishes
// the connection between React and the existing HTML document.
createRoot(root).render(
  // StrictMode is a standard development helper. It performs extra checks that
  // can reveal unsafe effects or accidental mutations while developing the app;
  // it does not add a visible wrapper or duplicate the production interface.
  <StrictMode>
    <ReviewApp />
  </StrictMode>,
);
