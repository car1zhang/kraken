import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { App } from "./App.tsx";
import "./styles/style.css";

// style.css is imported here rather than <link>ed from index.html so vite
// hashes it and web/serve.go can serve it immutable. The pre-paint theme
// stamp still runs first: it is an inline <script> in index.html's head,
// which executes before the module script (type=module is deferred) and
// before the extracted stylesheet is applied. That ordering is the FOUC
// guard and must not be rearranged.

const el = document.getElementById("root");
if (!el) throw new Error("#root missing from index.html");

createRoot(el).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
