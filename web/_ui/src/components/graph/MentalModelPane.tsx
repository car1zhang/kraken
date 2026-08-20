// MENTAL MODEL mode — the third pane of the memory view, beside GRAPH and
// LIBRARY (40-ui-mockups.html §4).
//
// The three are the same object at three removes: the graph is what kraken's
// agents worked out, the library is what carl chose to keep, and this is what
// he has actually READ — one row per thing in his head, each frozen at the
// moment he marked it. Built on the LIBRARY pane's recipe (a header line with
// a filter, one scroller, one empty state) and namespaced `mm-` for the same
// reason the rest of this view is namespaced `graph-`.
//
// Nothing here is a local latch: the list IS the server's, every write goes
// through the integrator's callbacks and the row's next state is whatever the
// refetched list says. And nothing here needs the job a row came from — an
// item outlives its job's teardown, archive and hard delete (no FK, no
// cascade), which is exactly why the frozen content is stored on the row
// rather than looked up. The pane therefore renders a long-gone job's readings
// with no gaps and no lookups.

import { useMemo, useState } from "react";

import type { MMModel } from "../../hooks/useMM.ts";
import type { MMItem } from "../../lib/mm.ts";
import { age } from "../../lib/format.ts";
import { useRenderedMD } from "../../hooks/useRenderedMD.ts";
import { shortDate } from "./graphModel.ts";

/**
 * The filter chips, in the mockup's order. Four are a KIND (every item has
 * exactly one) and the fifth cuts across all of them — an invalidated reading
 * is still a reading, and 12 + 29 + 6 is `all`, with `invalidated` a subset of
 * that same total rather than a sixth bucket.
 */
type MMFilter = "all" | "decision" | "reading" | "note" | "invalidated";

const FILTERS: { key: MMFilter; label: string }[] = [
  { key: "all", label: "all" },
  { key: "decision", label: "decisions" },
  { key: "reading", label: "readings" },
  { key: "note", label: "notes" },
  { key: "invalidated", label: "invalidated" },
];

/** Live is the ABSENCE of the key — the wire has no boolean to read. */
const isLive = (it: MMItem) => !it.invalidated_at;

const matchesFilter = (it: MMItem, f: MMFilter) =>
  f === "all" ? true : f === "invalidated" ? !isLive(it) : it.kind === f;

/** The row's badge. `reading` reads as READ — it is what carl did, not a genre. */
const KIND_LABEL: Record<string, string> = {
  decision: "decision",
  reading: "read",
  note: "note",
};

/**
 * The headline: what this row IS, in one line.
 *
 * For a code reading and a whole-artifact reading the frozen CONTENT is the
 * payload (a block of source, an artifact's name) and the useful headline is
 * where it came from — the anchor's own coordinates, which is what the mockup
 * shows and what carl recognizes. For a decision and a note the content IS the
 * thing, so it leads and the source line goes below.
 */
function headline(it: MMItem): string {
  if (it.source === "code") {
    const sha = it.anchor?.anchor_sha;
    return it.source_ref + (sha ? ` @ ${sha.slice(0, 7)}` : "");
  }
  if (it.source === "artifact") return `${it.slug}/${it.source_ref}`;
  if (it.source === "artifact-element") return elementHeadline(it);
  return it.content;
}

/** How much of a marked selection's first line the collapsed row shows. */
const ELEMENT_HEAD_MAX = 80;

/**
 * A marked selection's one-line headline: its FIRST non-empty line, trimmed
 * and hard-truncated.
 *
 * It needs one of its own for the same reason `code` and `artifact` do — the
 * frozen content is the payload, not the label. Since the locate amendment
 * that content is a block of markdown SOURCE (a whole pipe table, a whole
 * list), and returning it as the headline both filled the collapsed row with a
 * table's worth of raw pipes and, because `bodyText` drops a body that equals
 * its own headline, left the expansion — the markdown rendering carl asked for
 * at the gate — with nothing to render.
 *
 * The first line rather than a "selection from <source_ref>" label: the
 * provenance is already on the meta line below, and what the reader needs in a
 * scannable list is WHICH selection this is. A table's first line is its
 * header row, a list's is its first item — both recognizable at a glance.
 * Truncation counts CODE POINTS, so a cut never lands inside a surrogate pair.
 */
function elementHeadline(it: MMItem): string {
  const first = it.content.split("\n").map((l) => l.trim()).find((l) => l !== "");
  if (!first) return `selection from ${it.source_ref}`;
  const runes = Array.from(first);
  return runes.length > ELEMENT_HEAD_MAX
    ? runes.slice(0, ELEMENT_HEAD_MAX).join("") + "…"
    : first;
}

/**
 * The one-line provenance under the headline. `manual` is a note carl typed,
 * `code` came in from the editor, the rest name the board surface that minted
 * it — which is the part that says how much to trust the frozen text.
 */
const SOURCE_LABEL: Record<string, string> = {
  question: "question",
  artifact: "artifact",
  "artifact-element": "selection",
  code: "code",
  "agent-output": "agent",
  manual: "manual",
};

function MMRow({
  it,
  removing,
  onRemove,
}: {
  it: MMItem;
  removing: boolean;
  onRemove: (id: number) => void;
}) {
  // Every row expands, not just the invalidated ones: the headline of a code
  // reading is coordinates, and the whole point of freezing the text is being
  // able to read it back. What the CONTRACT requires is the invalidated case —
  // frozen content plus `invalid_reason` — and that is the same expansion with
  // one more line in it.
  const [open, setOpen] = useState(false);
  const live = isLive(it);
  const head = headline(it);
  /**
   * A marked SELECTION expands as markdown, everything else as frozen text
   * (carl, gate R1: "marking a list or table doesn't preserve the structure").
   *
   * Only `artifact-element`, and the reason is what the two contents ARE. A
   * selection is a span of an artifact's markdown SOURCE — the server locates
   * it in the file and stores the source lines, pipes and list markers intact
   * — so rendering it is showing what carl marked. A code reading's content is
   * source in some other language, where `#` is a comment and `*` is a
   * dereference; running that through a markdown renderer would eat characters
   * that matter. A note and a decision are prose carl typed, and neither has a
   * headline-plus-body split that reaches this branch anyway.
   *
   * A LEGACY element mark — one captured before the server started locating
   * the source span — holds the rendered text, so it comes out as one
   * paragraph. That is the honest rendering of what was actually frozen, not a
   * degradation to repair.
   *
   * The hook is called unconditionally with `undefined` off the branch, as
   * hooks must be, and `useRenderedMD` memoises on the source string — so a
   * row that is not an element mark parses nothing at all, and the 30s mm poll
   * leaves an open expansion's DOM (and its scroll) alone.
   *
   * `asMD` keys on the SOURCE rather than on `bodyText`, which is what made it
   * dead code: an element's content used also to be its headline, so the
   * body-equals-headline rule below emptied it and the branch could never run.
   * The headline is now a summary of that content (`elementHeadline`), so the
   * two are separate strings — but the source is the honest condition either
   * way, and this branch must not depend on a truncation rule to be reachable.
   */
  const asMD = it.source === "artifact-element" && it.content !== "";
  const bodyHTML = useRenderedMD(asMD ? it.content : undefined);
  /**
   * A note's or a decision's headline IS its content, so repeating it in the
   * expansion would be an empty gesture; the context is what is left to show.
   * A marked selection is the exception in both directions — its headline is a
   * truncated first line, and the expansion is the whole point of the row.
   */
  const bodyText = asMD ? it.content : head === it.content ? "" : it.content;
  return (
    <div className={"mm-row" + (live ? "" : " dead")}>
      <div className="mm-rowtop">
        <button
          className="mm-open"
          aria-expanded={open}
          title={open ? "collapse" : "show what was frozen"}
          onClick={() => setOpen((v) => !v)}
        >
          <span className="mm-rowhead">
            <span className={"mm-kind " + it.kind}>
              {KIND_LABEL[it.kind] ?? it.kind}
            </span>
            <span className="mm-headline">{head}</span>
          </span>
          {live ? null : (
            <span className="mm-dead">
              invalidated
              {it.invalid_reason ? ` · ${it.invalid_reason}` : ""}
              {it.invalid_detail ? ` · ${it.invalid_detail}` : ""}
            </span>
          )}
          <span className="mm-meta">
            {SOURCE_LABEL[it.source] ?? it.source}
            {it.slug && it.source !== "artifact" ? ` · ${it.slug}` : ""}
            {it.source_ref && it.source !== "code" && it.source !== "artifact"
              ? ` · ${it.source_ref}`
              : ""}
            {it.created_at ? ` · ${age(Math.floor(it.created_at / 1000))}` : ""}
          </span>
        </button>
        {/* Outside the expand button, never nested in it: a button inside a
            button is invalid markup and swallows its own click (the library
            row's ✕ makes the same split). */}
        <button
          className="mm-rm"
          title="drop this mark — the row stays, invalidated"
          aria-label={`drop the mark on ${head}`}
          disabled={removing || !live}
          onClick={() => onRemove(it.id)}
        >
          ✕
        </button>
      </div>
      {/* A SIBLING of the button rather than its child, for the ✕'s reason:
          the expansion carries a link, and interactive content inside a
          button is invalid markup that swallows its own clicks. */}
      {open ? (
        <div className="mm-frozen">
          {/* The FROZEN text — for an invalidated row this is the whole
              payoff: what carl knew, kept exactly as it was on the day he read
              it, which the source it came from no longer says. The "and now"
              half is deliberately absent: the current bytes are not on this
              wire, and inventing a diff from a list payload would be a claim
              this pane cannot make.

              Verbatim in a <pre> for every kind but one — a marked selection
              renders as markdown, so a marked table expands as a table and a
              marked list as a list. See `asMD` above for why that is exactly
              one source and not a default. */}
          {asMD ? (
            <div
              className="mm-frozenmd md"
              // renderMD escapes before it transforms, so a marked passage of
              // agent-written markdown is inert — the same argument every
              // other `.md` body in this app rests on (lib/markdown.ts).
              dangerouslySetInnerHTML={bodyHTML}
            />
          ) : bodyText ? (
            <pre className="mm-frozentext">{bodyText}</pre>
          ) : null}
          {it.context ? <div className="mm-ctx">{it.context}</div> : null}
          {it.source === "artifact" ? (
            <a
              className="mm-snap"
              href={`/api/mm/snapshot?id=${it.id}`}
              target="_blank"
              rel="noreferrer"
            >
              open the frozen copy ↗
            </a>
          ) : null}
          <div className="mm-ctx">
            {it.created_at ? `marked ${shortDate(it.created_at)}` : ""}
            {!live && it.invalidated_at
              ? ` · invalidated ${shortDate(it.invalidated_at)}`
              : ""}
          </div>
        </div>
      ) : null}
    </div>
  );
}

export interface MentalModelPaneProps {
  /**
   * Hidden rather than unmounted, for the library pane's reason: the filter
   * and any half-typed note are component state, and a toggle back to the
   * graph would otherwise throw them away.
   */
  hidden: boolean;
  /** App's ONE mm poll, shared with every capture surface. */
  mm: MMModel;
  /** Write a note. Routed through the shared mutate + refetch by the integrator. */
  onAddNote: (text: string) => Promise<void>;
  /** Drop one mark by id (invalidates it — nothing here deletes). */
  onRemove: (id: number) => Promise<void>;
}

export function MentalModelPane({
  hidden,
  mm,
  onAddNote,
  onRemove,
}: MentalModelPaneProps) {
  const [filter, setFilter] = useState<MMFilter>("all");
  const [noteOpen, setNoteOpen] = useState(false);
  const [note, setNote] = useState("");
  const [adding, setAdding] = useState(false);
  // Which rows have a remove in flight — the library pane's per-row guard, and
  // for its reason: the rows are independent, and locking the whole list
  // because one is mid-request would be a worse lie than the one it fixes.
  const [removing, setRemoving] = useState<ReadonlySet<number>>(
    () => new Set<number>(),
  );

  const counts = useMemo(() => {
    const c: Record<MMFilter, number> = {
      all: mm.items.length,
      decision: 0,
      reading: 0,
      note: 0,
      invalidated: 0,
    };
    for (const it of mm.items) {
      if (it.kind === "decision" || it.kind === "reading" || it.kind === "note") {
        c[it.kind]++;
      }
      if (!isLive(it)) c.invalidated++;
    }
    return c;
  }, [mm.items]);

  // ARRIVAL ORDER, narrowed and never re-sorted: the server serves newest
  // first and a list that reorders itself is a list you cannot aim at.
  const rows = useMemo(
    () => mm.items.filter((it) => matchesFilter(it, filter)),
    [mm.items, filter],
  );

  const remove = async (id: number) => {
    if (removing.has(id)) return;
    setRemoving((prev) => new Set(prev).add(id));
    // No try/catch and no failure branch: the caller routes this through the
    // shared mutate, which parks the server's own sentence in the clock corner
    // and never rejects. The row's fate is whatever the refetched list says.
    await onRemove(id);
    setRemoving((prev) => {
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
  };

  const addNote = async () => {
    const text = note.trim();
    if (!text || adding) return;
    setAdding(true);
    await onAddNote(text);
    setAdding(false);
    // Cleared unconditionally, like the artifact composer's send: a refusal
    // has already said so in the clock corner, and re-typing a note is cheaper
    // than wondering whether the box in front of you was sent.
    setNote("");
    setNoteOpen(false);
  };

  // An endpoint failure says so IN PLACE OF the empty state — "nothing marked
  // yet" over a failed read would be a claim this pane cannot make.
  const empty = mm.error
    ? "couldn't load the mental model: " + mm.error.message
    : !mm.loaded
      ? "loading…"
      : mm.items.length
        ? "nothing in that filter"
        : "nothing marked yet — ✓ read in the artifact viewer, → decision on an answered question, or + note here";

  return (
    <section className="graph-lib mm-pane" hidden={hidden}>
      <div className="graph-libhdr">
        <span className="graph-lbl">Mental model</span>
        <span className="mm-chips">
          {FILTERS.map((f) => (
            <button
              key={f.key}
              className={"mm-chip" + (filter === f.key ? " on" : "")}
              aria-pressed={filter === f.key}
              onClick={() => setFilter(f.key)}
            >
              {f.label} {counts[f.key]}
            </button>
          ))}
        </span>
        <span className="spacer" />
        <button
          className="mm-addnote"
          aria-expanded={noteOpen}
          title="write a note into the mental model"
          onClick={() => setNoteOpen((v) => !v)}
        >
          + note
        </button>
      </div>
      {noteOpen ? (
        <div className="mm-noteform">
          <textarea
            className="mm-noteinput"
            rows={2}
            autoFocus
            placeholder="something you know that no artifact says…"
            value={note}
            disabled={adding}
            onChange={(ev) => setNote(ev.target.value)}
            onKeyDown={(ev) => {
              if (ev.key !== "Enter" || !(ev.metaKey || ev.ctrlKey)) return;
              // the ⌘⏎ pair every composer in this app makes — a CJK
              // candidate's commit Enter also fires key:"Enter"
              if (ev.nativeEvent.isComposing || ev.nativeEvent.keyCode === 229) {
                return;
              }
              ev.preventDefault();
              void addNote();
            }}
          />
          <button
            className="mm-notesend"
            disabled={adding || !note.trim()}
            title="⌘⏎"
            onClick={() => void addNote()}
          >
            add
          </button>
        </div>
      ) : null}
      <div className="graph-libscroll">
        {rows.length ? (
          rows.map((it) => (
            <MMRow
              key={it.id}
              it={it}
              removing={removing.has(it.id)}
              onRemove={(id) => void remove(id)}
            />
          ))
        ) : (
          <div className="graph-libempty">{empty}</div>
        )}
      </div>
    </section>
  );
}
