import { useEffect, useRef } from "react";

import type { JobSearchModel } from "../../hooks/useJobSearch.ts";
import { goalChipText } from "../../lib/goals.ts";

export interface BoardBarProps {
  search: JobSearchModel;
  /**
   * The goal the board is filtered to, resolved to its text by the caller —
   * absent when nothing is filtered, which is the ONLY state in which this bar
   * renders exactly what it rendered before the filter existed.
   *
   * The chip is the filter's visible OWNER wherever the filter was set from:
   * the goals panel that sets it collapses to a one-line pill, so a board
   * quietly showing a third of its cards needs something that cannot be
   * collapsed to say so and to undo it.
   */
  goalChip?: { id: number; text: string };
  /** Clear the goal filter — the chip's ✕. Nothing else calls it. */
  onClearGoal?: () => void;
  /**
   * True while ANY modal overlay is open. App.tsx owns that state — one field
   * per overlay — and passes the disjunction down rather than this component
   * asking the DOM, which would make the guard depend on markup two slices
   * away.
   */
  overlayOpen?: boolean;
  /** The view router hides the board rather than unmounting it, as today. */
  hidden?: boolean;
  /** The fleet's "hide others' jobs" toggle state (App owns and persists it). */
  hideOthers?: boolean;
  /** Its setter. Absent → no toggle rendered (singleplayer, or a bare harness). */
  onHideOthers?: (hide: boolean) => void;
}

/**
 * The board's search strip: one input, a clear button, the goal filter's chip
 * when one is set, and the match readout — which counts the COMBINED
 * predicate, since the query and the goal filter narrow the same lists.
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
export function BoardBar({
  search,
  goalChip,
  onClearGoal,
  overlayOpen,
  hidden,
  hideOthers,
  onHideOthers,
}: BoardBarProps) {
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
      {/* the goal filter's chip, between the input and the readout: its own
          `gf-` class family rather than the card's `.goal-chip`, because that
          one is an inert label that must fall THROUGH to the card's click and
          this one is a control with a button in it. Absent entirely with no
          filter, which is what keeps the at-rest bar byte-identical to the one
          that predates the feature. */}
      {goalChip ? (
        <span className="gf-chip" id="goal-filter-chip" title={goalChip.text}>
          ◎ {goalChipText(goalChip.text)}
          <button
            className="gf-x"
            id="goal-filter-clear"
            type="button"
            title="clear the goal filter"
            aria-label="clear the goal filter"
            onClick={() => onClearGoal?.()}
          >
            ✕
          </button>
        </span>
      ) : null}
      {active ? null : <span className="bb-hint">⌘K</span>}
      {/* The fleet's one board-bar control: hide every card another kraken
          polled in, on THIS browser only (persisted as kraken.hideOthers by
          App). Rendered only when the host wires it, which BoardView does
          when the registry is non-empty (or the toggle is on) — a lone node's
          bar stays byte-identical to the one before the fleet existed. */}
      {onHideOthers ? (
        <label className="bb-fleet check" title="hide every job owned by another kraken on the tailnet (this browser only)">
          <input
            type="checkbox"
            id="hide-others"
            aria-label="hide others' jobs"
            checked={!!hideOthers}
            onChange={(e) => onHideOthers(e.target.checked)}
          />
          hide others' jobs
        </label>
      ) : null}
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
