// window.krakenTheme is defined by the pre-paint theme IIFE inlined in
// index.html's head (ported verbatim from web/static/index.html). It is the
// theme toggle's entire API: `get` returns the stored preference, `set`
// writes it and re-stamps <html data-theme>. Declared rather than
// reimplemented because the IIFE has to run before the stylesheet applies,
// which no React code can do.
declare global {
  interface Window {
    krakenTheme?: {
      get(): "light" | "dark" | "system";
      set(pref: "light" | "dark" | "system"): void;
    };
  }
}

export {};
