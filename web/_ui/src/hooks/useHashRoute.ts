import { useCallback, useEffect, useState } from "react";

import { readLS, writeLS } from "./useLocalStorage.ts";

export const VIEWS = ["board", "log", "dream", "memory"] as const;
export type View = (typeof VIEWS)[number];

const LEGACY_KEY = "kraken-view";

/**
 * Views that were RENAMED, mapped to what they are called now.
 *
 * `graph` → `memory` is a LABEL change only: the view's id, its classes, its
 * files and every graph route keep the name `graph`. What moved is the tab and
 * the hash, and both of those are things a browser has already written down —
 * a bookmarked `#/memory` is what carl will make from here on, but the tab he
 * left open yesterday is sitting on `#/graph`, and `kraken-view` in his
 * localStorage says `"graph"` too.
 *
 * This table is why neither of those falls back to the board. That fallback is
 * the RIGHT answer for a view that was REMOVED (`dag`, below) and the wrong one
 * for a view that was merely renamed: the reader asked for the memory view and
 * it is still here, so silently landing him on the board would read as the
 * board having eaten his tab.
 */
const RENAMED: Readonly<Record<string, View>> = { graph: "memory" };

/** The one place a stored/URL view is validated. See useHashRoute's header. */
function isView(raw: string | null | undefined): raw is View {
  return (VIEWS as readonly string[]).includes(raw ?? "");
}

/**
 * A stored/URL view, current name or renamed-from — null if it is neither.
 *
 * `Object.hasOwn`, not a bare `RENAMED[v] ?? null`: an object literal carries
 * Object.prototype, so `#/constructor`, `#/toString` and `#/valueOf` all read
 * back a FUNCTION from the chain — truthy, so the `??` never fires — which
 * normalize would then hand out typed as a View. That matches no <main> at all:
 * every view hidden, a blank page, no error, and a garbage hash stamped into
 * the address bar for good measure. Exactly the failure the `dag` fallback
 * below exists to prevent, arriving through the door added to fix it.
 */
function normalize(raw: string | null | undefined): View | null {
  const v = raw ?? "";
  if (isView(v)) return v;
  return Object.hasOwn(RENAMED, v) ? (RENAMED[v] ?? null) : null;
}

/** The hash's own text, `#/` stripped — "" when the URL carries none. */
function hashView(): string {
  return location.hash.replace(/^#\/?/, "");
}

function parseHash(): View | null {
  return normalize(hashView());
}

/**
 * The four views as real URLs (#/board, #/log, #/dream, #/memory) — Q6.
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
 *
 * Both seeds are VALIDATED against VIEWS rather than merely tested for
 * truthiness, and that is the whole defense against the removed DAG view: a
 * browser still holding `kraken-view: "dag"` (or sitting on `#/dag`) has a
 * stale-but-truthy value, which an `|| "board"` guard passes through happily
 * and which then matches no <main> at all — every view hidden, a blank page,
 * no error. It falls back to the board instead, and the first navigate()
 * overwrites the stale key. Same rule the vanilla board states at
 * web/static/app.js:4164. A RENAMED view (see RENAMED above) is the one thing
 * that is not stale — it resolves to its new name instead of to the board.
 */
export function useHashRoute(): [View, (v: View) => void] {
  const [view, setView] = useState<View>(() => {
    const fromHash = parseHash();
    if (fromHash) return fromHash;
    return normalize(readLS(LEGACY_KEY)) ?? "board";
  });

  // Stamp the hash on first mount whenever it does not already SPELL the view
  // being rendered, so the address bar agrees with the screen and a reload is
  // stable. That covers three cases with one comparison: no hash at all, a
  // stale one that fell back to the board, and a RENAMED one — `#/graph` is a
  // perfectly good route to the memory view, but leaving it in the bar would
  // hand out a link under a name the tab no longer uses.
  useEffect(() => {
    if (hashView() !== view) location.replace(`#/${view}`);
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

  // Compared against the hash's own TEXT, not against parseHash(): a legacy
  // `#/graph` parses to "memory" already, so the parsed comparison would call
  // a click on MEMORY a no-op and leave the old name in the address bar for
  // the rest of the session.
  const navigate = useCallback((v: View) => {
    setView(v);
    writeLS(LEGACY_KEY, v);
    if (hashView() !== v) location.hash = `#/${v}`;
  }, []);

  return [view, navigate];
}
