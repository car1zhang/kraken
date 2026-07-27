import "@testing-library/jest-dom/vitest";

// jsdom has no matchMedia, which the theme code and a couple of components
// consult. A minimal always-false stub is enough: no test asserts on system
// theme, and the real behaviour is exercised by the inline IIFE in the
// browser, not here.
if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}
