import { useEffect } from "react";

import type { WebJob } from "../../api/types.ts";
import { useLocalStorageFlag } from "../../hooks/useLocalStorage.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { ARCHIVED } from "../../lib/moves.ts";
import { ArchivedCard } from "./ArchivedCard.tsx";
import type { BoardActions } from "./BoardView.tsx";
import { useColumnDrop } from "./DragProvider.tsx";

export interface ArchiveDrawerProps {
  /** The archived jobs to RENDER — already narrowed by the search, if one is running. */
  archived: WebJob[];
  actions: BoardActions;
  /**
   * How many jobs the archive HOLDS, before the search narrowed it. Present
   * only while a query is active, and what it buys is the `shown/total`
   * spelling on the head — the same one BoardColumn's head takes. That count
   * is what keeps a query matching nothing archived from reading as an EMPTY
   * archive: `0/1` says the archive has a job and this query is not it, where
   * a bare `ARCHIVED 0` would say there is nothing there at all — on the one
   * surface where the drawer opening is a search's only route into the
   * archive.
   */
  total?: number;
  /**
   * Render open regardless of the stored flag — set while a search matches an
   * archived job. It overrides WITHOUT overwriting: the flag is left alone, so
   * clearing the query returns the drawer to whatever carl had it set to. The
   * header goes `disabled` for as long as it holds; see the docblock.
   */
  forceOpen?: boolean;
  /**
   * How many jobs the archive holds when no query is running, from the board
   * payload. The archived cards are fetched only once something needs them,
   * so with the drawer shut `archived` is empty while the archive is not:
   * this is the head's number then, and what keeps the head pressable.
   * Absent reads as `archived.length` (every card already in hand).
   */
  count?: number;
  /**
   * Called while the drawer is open: asks the board for the archived cards
   * (`BoardModel.needArchived`). Until they land an open drawer over a
   * non-empty archive shows a loading line.
   */
  onNeed?: () => void;
}

/**
 * The collapsible ARCHIVED drawer, inside the DONE column's section. Port of
 * archiveDrawerHTML (app.js:885).
 *
 * Always rendered, even with zero archived jobs, since it doubles as the drop
 * target for a done→archived drag; `data-col="archived"` mirrors the `.col`
 * sections' own data-col so the drag handlers can treat both uniformly.
 *
 * Open-ness is the stored flag OR the `forceOpen` override, and only the flag
 * is ever written — see the prop.
 *
 * The header is DISABLED whenever a press could not show anything, which is
 * ONE rule covering three states: nothing archived at all, a query holding the
 * drawer open (collapsing is not the toggle's call while the search is what
 * opened it), and a query that matched nothing archived (the drawer has no
 * cards to reveal). All three would otherwise write `kraken-archive-open` from
 * a press whose only visible effect is the triangle flipping — or, with nothing
 * archived at all, no visible effect whatsoever, since the head renders a `·`
 * rather than a triangle then. Either way it is a preference edited silently,
 * which lands the drawer somewhere carl never asked for once the query clears.
 * The head still says what is THERE, which is the part disabling would
 * otherwise cost: the `shown/total` count distinguishes "no matches" from
 * "nothing archived". It does not explain the disabled state and is not trying
 * to — see the tooltip note below.
 *
 * There is deliberately no tooltip explaining the disabled state. style.css
 * gives a disabled `.archive-head` `pointer-events: none`, so it never
 * receives a hover, and `disabled` takes it out of the tab order too — a
 * `title` there would be unreachable by mouse and by keyboard alike. So the
 * disabled state goes unexplained on purpose: a dead head next to an honest
 * count is better than a live one that edits a preference invisibly.
 *
 * That same `pointer-events: none` was load-bearing under native HTML5 DnD — a
 * disabled control swallows drag events and the header IS the empty drawer's
 * whole drop area. dnd-kit resolves drops geometrically against registered
 * rects rather than by hit-testing events, so the rule is now belt and braces
 * rather than the mechanism; it stays because the rules this sheet ported are
 * kept as the vanilla sheet had them.
 */
export function ArchiveDrawer({
  archived,
  actions,
  total,
  forceOpen,
  count,
  onNeed,
}: ArchiveDrawerProps) {
  const [stored, setStored] = useLocalStorageFlag("kraken-archive-open", false);
  const open = stored || !!forceOpen;
  const shown = archived.length;
  // The archive's size with no query running: the board's count, since the
  // cards themselves may not be loaded yet.
  const size = count ?? shown;
  // Does the ARCHIVE hold anything, as opposed to the query having matched
  // something in it — the triangle's question, and the one place the two
  // differ.
  const hasArchived = (total ?? size) > 0;
  // What a press could reveal: the matches under a query, else the archive.
  const revealable = total === undefined ? size : shown;
  const drop = useColumnDrop(ARCHIVED);
  // an open drawer needs its cards; the board fetches them once and keeps them
  useEffect(() => {
    if (open) onNeed?.();
  }, [open, onNeed]);

  return (
    <div
      ref={drop.setNodeRef}
      className={"archive-drawer" + (drop.className ? " " + drop.className : "")}
      data-col="archived"
    >
      <button
        className="archive-head"
        id="archive-toggle"
        // What a press could reveal, not always the archive's size: under a
        // query it can only reveal the matches, and there are none at zero.
        disabled={revealable === 0 || !!forceOpen}
        // Reachable only when `forceOpen` is off, so `open` and `stored` are
        // the same value here and the toggle is a plain flip of the flag.
        onClick={() => setStored(!stored)}
      >
        <span className="tri">{hasArchived ? (open ? "▾" : "▸") : "·"}</span> ARCHIVED{" "}
        <span className="n">{total === undefined ? size : `${shown}/${total}`}</span>
      </button>
      {/* open over an archive whose cards have not landed yet */}
      {open && shown === 0 && total === undefined && size > 0 ? (
        <div className="archive-loading">loading…</div>
      ) : null}
      {/* the CARDS follow the query, not the archive: a drawer held open over
          zero matches renders its head and nothing else, which is what the
          `0/1` count is there to explain. */}
      {shown > 0 ? (
        <div className="archive-cards" hidden={!open}>
          {archived.map((j) => (
            <ArchivedCard key={jobKey(j)} job={j} actions={actions} />
          ))}
        </div>
      ) : null}
    </div>
  );
}
