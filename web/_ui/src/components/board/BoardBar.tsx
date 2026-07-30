import { useEffect, useRef } from "react";

import type { JobSearchModel } from "../../hooks/useJobSearch.ts";

export interface BoardBarProps {
  search: JobSearchModel;
  /**
   * True while ANY modal overlay is open. App.tsx owns that state — one field
   * per overlay — and passes the disjunction down rather than this component
   * asking the DOM, which would make the guard depend on markup two slices
   * away.
   */
  overlayOpen?: boolean;
  /** The view router hides the board rather than unmounting it, as today. */
  hidden?: boolean;
}

/**
 * The board's search strip: one input, a clear button and the match readout.
 *
 * cmd+K / ctrl+K focuses and SELECTS the input, so a second press followed by
 * typing replaces the query rather than appending to it. It fires whatever is
 * focused — unlike a bare `/` it collides with nothing the composers type —
 * with two exceptions that are both about not stealing focus from something
 * more important: while a modal overlay is open (the steer composer is the
 * one carl types into for minutes at a time), and while the board itself is
 * hidden behind the LOG or DREAM view, where this input is unreachable anyway.
 * Both exceptions return BEFORE preventDefault, so in those states the browser
 * keeps its own binding.
 *
 * preventDefault otherwise: Chrome gives cmd+K to the omnibox, and without it
 * the address bar takes the keystroke and the caret never arrives here.
 *
 * Escape inside the input clears the query and stops there — stopPropagation
 * keeps it off the document, where useEscapeStack would read it as "close the
 * top overlay". Same shape as the thread rename input's Escape handler, and
 * for the same reason: a key with a local meaning must not also fire the
 * global one.
 */
export function BoardBar({ search, overlayOpen, hidden }: BoardBarProps) {
  const { query, setQuery, matched, total, active } = search;
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "k" && e.key !== "K") return;
      if (!e.metaKey && !e.ctrlKey) return;
      if (overlayOpen || hidden) return;
      e.preventDefault();
      inputRef.current?.focus();
      inputRef.current?.select();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [overlayOpen, hidden]);

  return (
    <div className="boardbar" id="boardbar" hidden={hidden}>
      <span className="bb-search">
        <input
          ref={inputRef}
          className="bb-input"
          id="job-search"
          type="text"
          // A text input rather than type="search": webkit renders its own
          // clear affordance on a search field, which would sit beside the ✕
          // below saying the same thing in a different shape.
          placeholder="search jobs…"
          aria-label="search jobs"
          autoComplete="off"
          spellCheck={false}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Escape") return;
            e.stopPropagation();
            setQuery("");
          }}
        />
        {active ? (
          <button
            className="bb-clear"
            id="job-search-clear"
            type="button"
            title="clear the search"
            aria-label="clear the search"
            onClick={() => setQuery("")}
          >
            ✕
          </button>
        ) : null}
      </span>
      {active ? null : <span className="bb-hint">⌘K</span>}
      <span className="bb-read">
        {active ? (
          <>
            <b>{matched}</b> of {total} jobs
          </>
        ) : (
          `${total} jobs`
        )}
      </span>
    </div>
  );
}
