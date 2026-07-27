import type { WebJob } from "../../api/types.ts";
import { useLocalStorageFlag } from "../../hooks/useLocalStorage.ts";
import { jobKey } from "../../lib/jobKey.ts";
import { ARCHIVED } from "../../lib/moves.ts";
import { ArchivedCard } from "./ArchivedCard.tsx";
import type { BoardActions } from "./BoardView.tsx";
import { useColumnDrop } from "./DragProvider.tsx";

export interface ArchiveDrawerProps {
  archived: WebJob[];
  actions: BoardActions;
}

/**
 * The collapsible ARCHIVED drawer, inside the DONE column's section. Port of
 * archiveDrawerHTML (app.js:885).
 *
 * Always rendered, even with zero archived jobs, since it doubles as the drop
 * target for a done→archived drag; `data-col="archived"` mirrors the `.col`
 * sections' own data-col so the drag handlers can treat both uniformly.
 *
 * The header stays `disabled` when the drawer is empty. style.css gives a
 * disabled `.archive-head` `pointer-events: none`, which under native HTML5
 * DnD was load-bearing — a disabled control swallows drag events and the
 * header IS the empty drawer's whole drop area. dnd-kit resolves drops
 * geometrically against registered rects rather than by hit-testing events, so
 * the rule is now belt and braces rather than the mechanism; it stays because
 * style.css is byte-identical to the vanilla sheet.
 */
export function ArchiveDrawer({ archived, actions }: ArchiveDrawerProps) {
  const [open, setOpen] = useLocalStorageFlag("kraken-archive-open", false);
  const hasArchived = archived.length > 0;
  const drop = useColumnDrop(ARCHIVED);

  return (
    <div
      ref={drop.setNodeRef}
      className={"archive-drawer" + (drop.className ? " " + drop.className : "")}
      data-col="archived"
    >
      <button
        className="archive-head"
        id="archive-toggle"
        disabled={!hasArchived}
        onClick={() => setOpen(!open)}
      >
        <span className="tri">{hasArchived ? (open ? "▾" : "▸") : "·"}</span> ARCHIVED{" "}
        <span className="n">{archived.length}</span>
      </button>
      {hasArchived ? (
        <div className="archive-cards" hidden={!open}>
          {archived.map((j) => (
            <ArchivedCard key={jobKey(j)} job={j} actions={actions} />
          ))}
        </div>
      ) : null}
    </div>
  );
}
