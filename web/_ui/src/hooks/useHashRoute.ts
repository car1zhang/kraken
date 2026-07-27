import { useCallback, useEffect, useState } from "react";

import { readLS, writeLS } from "./useLocalStorage.ts";

export const VIEWS = ["board", "dag", "log"] as const;
export type View = (typeof VIEWS)[number];

const LEGACY_KEY = "kraken-view";

function parseHash(): View | null {
  const raw = location.hash.replace(/^#\/?/, "");
  return (VIEWS as readonly string[]).includes(raw) ? (raw as View) : null;
}

/**
 * The three views as real URLs (#/board, #/dag, #/log) — Q6.
 *
 * The vanilla board had no router at all: the active view was a localStorage
 * string toggling `hidden` attributes, so there was no back button and no
 * deep link, and a reload always landed on whatever was open last.
 *
 * Hash rather than pushState is a deliberate constraint, not a shortcut. A
 * hash never reaches the server, so web/serve.go keeps serving embedded files
 * with http.FileServer and needs no index.html fallback for unknown paths —
 * pushState routing would have required one. Nothing about serving changes.
 *
 * The legacy localStorage value seeds the FIRST load only, so an existing tab
 * reopens where it left off; after that the hash is the single source of
 * truth and the key is just kept in step for a downgrade.
 */
export function useHashRoute(): [View, (v: View) => void] {
  const [view, setView] = useState<View>(() => {
    const fromHash = parseHash();
    if (fromHash) return fromHash;
    const legacy = readLS(LEGACY_KEY);
    return (VIEWS as readonly string[]).includes(legacy ?? "")
      ? (legacy as View)
      : "board";
  });

  // Stamp the hash on first mount when the URL had none, so the address bar
  // agrees with what is rendered and a reload is stable.
  useEffect(() => {
    if (!parseHash()) location.replace(`#/${view}`);
    // deliberately mount-only: later changes go through navigate()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const onHash = () => {
      const v = parseHash();
      if (v) setView(v);
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  const navigate = useCallback((v: View) => {
    setView(v);
    writeLS(LEGACY_KEY, v);
    if (parseHash() !== v) location.hash = `#/${v}`;
  }, []);

  return [view, navigate];
}
