import type { ReactNode } from "react";

import type { WebJob } from "../../api/types.ts";
import { useLocalStorageFlag } from "../../hooks/useLocalStorage.ts";
import { FROZEN } from "../../lib/moves.ts";
import { useColumnDrop } from "./DragProvider.tsx";

export interface FrozenDrawerProps {
  /** The frozen jobs to RENDER, already narrowed by the search if one is running. */
  frozen: WebJob[];
  /**
   * How many jobs are frozen before the search narrowed them. Present only
   * while a query is active; it switches the head to `shown/total`, the same
   * spelling the column heads and the archive drawer take.
   */
  total?: number;
  /**
   * Render open regardless of the stored flag, set while a search matches a
   * frozen job. It overrides without overwriting, so clearing the query
   * returns the drawer to however it was left.
   */
  forceOpen?: boolean;
  renderCard(job: WebJob): ReactNode;
}

/**
 * The collapsible FROZEN drawer at the foot of the BLOCKED column: blocked
 * jobs the operator put on hold because they wait on something or someone
 * else. The archive drawer's sibling, and built the same way.
 *
 * Its cards are the ordinary JobCard, not a drawer-specific one. A frozen job
 * is a live blocked job: its click opens the same unblock modal, and steering
 * it there is one way to thaw it. JobCard itself draws the `❄ frozen` token
 * and the unfreeze button, and takes no part in drag while frozen.
 *
 * Always rendered, even empty, because it is the drop target for a
 * blocked → frozen drag. The head is disabled whenever a press could reveal
 * nothing (no frozen cards on screen) or the search is what is holding it
 * open, which is ArchiveDrawer's rule for the same reason: a press there
 * would only edit the stored flag invisibly.
 *
 * Closed by default. The point of freezing a card is to get it out of the
 * way, so the drawer shows a count until it is opened.
 */
export function FrozenDrawer({ frozen, total, forceOpen, renderCard }: FrozenDrawerProps) {
  const [stored, setStored] = useLocalStorageFlag("kraken-frozen-open", false);
  const open = stored || !!forceOpen;
  const shown = frozen.length;
  const hasFrozen = (total ?? shown) > 0;
  const drop = useColumnDrop(FROZEN);

  return (
    <div
      ref={drop.setNodeRef}
      className={"frozen-drawer" + (drop.className ? " " + drop.className : "")}
      data-col="frozen"
    >
      <button
        className="frozen-head"
        id="frozen-toggle"
        disabled={shown === 0 || !!forceOpen}
        onClick={() => setStored(!stored)}
      >
        <span className="tri">{hasFrozen ? (open ? "▾" : "▸") : "·"}</span> FROZEN{" "}
        <span className="n">{total === undefined ? shown : `${shown}/${total}`}</span>
      </button>
      {shown > 0 ? (
        <div className="frozen-cards" hidden={!open}>
          {frozen.map(renderCard)}
        </div>
      ) : null}
    </div>
  );
}
