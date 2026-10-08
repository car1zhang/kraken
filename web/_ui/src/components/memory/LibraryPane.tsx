// LIBRARY mode — the first pane of the memory view, beside MODEL. Moved out of
// the retired graph view's file verbatim; its `graph-lib*` class names are the
// chrome MentalModelPane shares, so they keep the old prefix.

import { useMemo, useState } from "react";

import type { LibraryModel } from "../../hooks/useLibrary.ts";
import { shortDate } from "../../lib/dates.ts";
import { filterLibrary } from "../../lib/library.ts";

/**
 * LIBRARY mode: the saved artifacts, newest first, as the server served them.
 *
 * Built on the DREAM view's pane recipe (a header line with a count, one
 * scroller, one empty state) because it is the same object — a list of things
 * to read — and namespaced `graph-lib*`.
 *
 * The list is rendered in ARRIVAL ORDER and the filter only narrows it: the
 * server sorts saved_at DESC (ties id DESC) and nothing here re-sorts, so a row
 * never moves under the cursor that is aiming at it.
 */
export function LibraryPane({
  hidden,
  library,
  onOpenArtifact,
  onRemoveSaved,
}: {
  /**
   * Hidden rather than unmounted, for the reason App keeps this whole view
   * mounted: the filter box is component state and a toggle to the model
   * would otherwise throw away what carl had typed into it.
   */
  hidden: boolean;
  library: LibraryModel;
  onOpenArtifact: (url: string, name: string) => void;
  onRemoveSaved: (id: number) => Promise<void>;
}) {
  const [q, setQ] = useState("");
  // Which rows have a remove in flight — the double-press guard. A set rather
  // than one boolean: the rows are independent, and locking the whole list
  // because one row is mid-request would be a worse lie than the one it fixes.
  const [removing, setRemoving] = useState<ReadonlySet<number>>(
    () => new Set<number>(),
  );
  const rows = useMemo(
    () => filterLibrary(library.items, q),
    [library.items, q],
  );

  const remove = async (id: number) => {
    if (removing.has(id)) return;
    setRemoving((prev) => new Set(prev).add(id));
    // No try/catch and no failure branch: the caller routes this through the
    // shared mutate, which parks the server's own sentence in the clock corner
    // and never rejects. The row's fate is whatever the refetched list says.
    await onRemoveSaved(id);
    setRemoving((prev) => {
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
  };

  // An endpoint failure says so IN PLACE OF the empty state — "nothing saved
  // yet" over a failed read would be a claim this pane cannot make.
  const empty = library.error
    ? "couldn't load the library: " + library.error.message
    : !library.loaded
      ? "loading…"
      : library.items.length
        ? "nothing matches that filter"
        : "nothing saved yet — ☆ save in the artifact viewer keeps a frozen copy here";

  return (
    <section className="graph-lib" hidden={hidden}>
      <div className="graph-libhdr">
        <span className="graph-lbl">Saved artifacts</span>
        <span className="graph-stat">
          {library.items.length
            ? `${rows.length} of ${library.items.length}`
            : ""}
        </span>
        <span className="spacer" />
        <input
          className="graph-libfilter"
          type="search"
          value={q}
          placeholder="filter by name or job…"
          aria-label="filter saved artifacts"
          onChange={(ev) => setQ(ev.target.value)}
        />
      </div>
      <div className="graph-libscroll">
        {rows.length ? (
          rows.map((it) => (
            <div className="graph-librow" key={it.id}>
              {/* the whole row opens it — the ✕ beside it is the one part
                  that does not, so it sits outside this button rather than
                  nested inside it (a button in a button is invalid markup and
                  swallows its own click) */}
              <button
                className="graph-libopen"
                title={`${it.slug} / ${it.name}`}
                onClick={() =>
                  onOpenArtifact(it.url, `${it.slug}/${it.name}`)
                }
              >
                <span className="graph-libname">{it.name}</span>
                <span className="graph-libmeta">
                  {it.slug} · {shortDate(it.saved_at)}
                </span>
              </button>
              <button
                className="graph-librm"
                title="remove from the library"
                aria-label={`remove ${it.name}`}
                disabled={removing.has(it.id)}
                onClick={() => void remove(it.id)}
              >
                ✕
              </button>
            </div>
          ))
        ) : (
          <div className="graph-libempty">{empty}</div>
        )}
      </div>
    </section>
  );
}
