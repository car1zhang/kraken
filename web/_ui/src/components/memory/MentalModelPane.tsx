// MODEL mode — the second pane of the memory view, beside LIBRARY.
//
// The mental model is one row per thing carl knows. Since the mind layer
// (schema v33) a row is COMMITTED by a mind thread — carl highlights text
// anywhere on the board, the ◆ mind button forks zeus into a mind thread, and
// that thread runs `kraken mm commit` — so most rows carry a one-line `title`
// (a claim), a `scope` (`universal`, or a repo plus a repo-relative dir), the
// highlighted `quote` and a `capture_source` pointer. Rows from before the
// rework carry none of those and headline their content's first line.
//
// Above the rows sits the FLAGS section: contradictions an agent noticed
// (`kraken mm flag`) and zeus raised to carl. Each offers `drop item` (the
// item is invalidated, reason `contradicted`), `keep` (the flag closes), and
// `open job` (the steer modal of the job whose zeus raised it).
//
// Nothing here is a local latch: the lists are the server's, every write is
// followed by a refetch, and the row's next state is whatever that says.

import { useEffect, useMemo, useState } from "react";

import { fetchMMSource, resolveMMFlag } from "../../api/client.ts";
import { useMM, useMMFlags, type MMModel } from "../../hooks/useMM.ts";
import type { MMCaptureSource, MMFlag, MMItem, MMSourceView } from "../../lib/mm.ts";
import { age } from "../../lib/format.ts";
import { useRenderedMD } from "../../hooks/useRenderedMD.ts";
import { shortDate } from "../../lib/dates.ts";

/**
 * The kind chips. Four are a KIND (every item has exactly one) and the rest
 * cut across them — an invalidated reading is still a reading, and a nightly
 * or proposed item is still a decision or a note.
 */
export type MMKindFilter =
  | "all"
  | "decision"
  | "reading"
  | "note"
  | "invalidated"
  | "nightly"
  | "proposed";

const FILTERS: { key: MMKindFilter; label: string }[] = [
  { key: "all", label: "all" },
  { key: "decision", label: "decisions" },
  { key: "reading", label: "readings" },
  { key: "note", label: "notes" },
  { key: "invalidated", label: "invalidated" },
  { key: "nightly", label: "nightly" },
  { key: "proposed", label: "proposed" },
];

/** How long the filter box waits after the last keystroke before it asks. */
const FILTER_DEBOUNCE_MS = 250;

/** Live is the ABSENCE of the key — the wire has no boolean to read. */
const isLive = (it: MMItem) => !it.invalidated_at;

/**
 * A nightly mind-pass item still waiting for carl's confirm. Held out of the
 * rules files and `kraken mm get` until then; an absent `state` is live.
 */
export const isProposed = (it: MMItem) => it.state === "proposed";

export function matchesFilter(it: MMItem, f: MMKindFilter): boolean {
  switch (f) {
    case "all":
      return true;
    case "invalidated":
      return !isLive(it);
    case "nightly":
      return it.source === "nightly";
    case "proposed":
      return isProposed(it);
    default:
      return it.kind === f;
  }
}

/** The row's badge. `reading` reads as READ — it is what carl did, not a genre. */
const KIND_LABEL: Record<string, string> = {
  decision: "decision",
  reading: "read",
  note: "note",
};

/** The first non-empty line of `s`, trimmed; "" when there is none. */
export function firstLine(s: string): string {
  return (
    s
      .split("\n")
      .map((l) => l.trim())
      .find((l) => l !== "") ?? ""
  );
}

/**
 * The headline: the item's `title`, or — for a legacy row with none — the
 * first line of its content. A row with neither (an empty content is possible
 * on a whole-artifact reading) falls back to where it came from.
 */
export function itemTitle(it: MMItem): string {
  const t = (it.title ?? "").trim();
  if (t) return t;
  const first = firstLine(it.content ?? "");
  if (first) return first;
  if (it.source === "artifact") return `${it.slug}/${it.source_ref}`;
  return it.source_ref || `mm #${it.id}`;
}

/**
 * The scope chip: `universal`, or `<repo>/<dir>` (`<repo>` alone at the repo
 * root). Null when the row names no repo and is not universal — every legacy
 * row, whose scope column defaulted to `repo` with no repo to name.
 */
export function scopeLabel(it: MMItem): string | null {
  if (it.scope === "universal") return "universal";
  const repo = (it.repo ?? "").trim();
  if (!repo) return null;
  const dir = (it.dir ?? "").trim().replace(/^\/+|\/+$/g, "");
  return dir ? `${repo}/${dir}` : repo;
}

/**
 * The capture source as `/api/mm` serves it: its own `capture_source` key
 * (contract amendment 1), null on a legacy row. `source` stays the kind string.
 */
function captureSource(it: MMItem): MMCaptureSource | null {
  return it.capture_source && typeof it.capture_source === "object"
    ? it.capture_source
    : null;
}

const short = (s: string | undefined, n = 8) => (s ? s.slice(0, n) + (s.length > n ? "…" : "") : "");

/** One line saying where a captured item's text came from. */
export function sourceSummary(src: MMCaptureSource): string {
  switch (src.kind) {
    case "artifact":
      return (
        "artifact · " +
        (src.name ?? "?") +
        (src.sha ? ` @ ${short(src.sha)}` : "") +
        (src.slug ? ` · ${src.slug}` : "")
      );
    case "transcript":
      return (
        "transcript" +
        (src.tid ? ` · thread ${src.tid}` : "") +
        (src.session ? ` · session ${short(src.session)}` : "") +
        (src.uuid ? ` · record ${short(src.uuid)}` : "")
      );
    case "dir":
      return `dir · ${src.repo ?? "?"}${src.dir ? "/" + src.dir : ""}`;
    case "item":
      return `item · mm #${src.item_id ?? "?"}`;
    case "manual":
      return "manual";
    default:
      return src.kind || "unknown";
  }
}

/** The one-line provenance of a LEGACY row, by the board surface that minted it. */
const SOURCE_LABEL: Record<string, string> = {
  question: "question",
  artifact: "artifact",
  "artifact-element": "selection",
  code: "code",
  "agent-output": "agent",
  manual: "manual",
  capture: "mind",
  nightly: "nightly",
};

function MMRow({
  it,
  removing,
  onRemove,
  confirming,
  onConfirm,
}: {
  it: MMItem;
  removing: boolean;
  onRemove: (id: number) => void;
  confirming: boolean;
  onConfirm: (id: number) => void;
}) {
  const [open, setOpen] = useState(false);
  // The `source` drill-down: null = not asked, "loading" while in flight.
  const [src, setSrc] = useState<MMSourceView | "loading" | { error: string } | null>(
    null,
  );
  const live = isLive(it);
  const proposed = isProposed(it);
  const title = itemTitle(it);
  const scope = scopeLabel(it);
  const capSrc = captureSource(it);
  /**
   * A legacy marked SELECTION expands as markdown (its content is a span of an
   * artifact's markdown source); a committed item's body is prose the mind
   * thread wrote, which is markdown too, and so is a nightly item's body,
   * written by the mind pass. A code reading's content is source in some
   * other language and stays verbatim.
   */
  const asMD =
    (it.source === "artifact-element" ||
      it.source === "capture" ||
      it.source === "nightly") &&
    it.content !== "";
  const bodyHTML = useRenderedMD(asMD ? it.content : undefined);
  // A note's headline IS its content; repeating it would be an empty gesture.
  const bodyText = asMD ? it.content : title === it.content.trim() ? "" : it.content;

  const showSource = async () => {
    if (src !== null && src !== "loading") {
      setSrc(null); // a second press folds it away
      return;
    }
    setSrc("loading");
    try {
      setSrc(await fetchMMSource(it.id));
    } catch (e) {
      setSrc({ error: e instanceof Error ? e.message : String(e) });
    }
  };

  return (
    <div
      className={"mm-row" + (live ? "" : " dead")}
      data-mind-kind="item"
      data-mind-item-id={String(it.id)}
      data-mind-slug={it.slug || undefined}
    >
      <div className="mm-rowtop">
        <button
          className="mm-open"
          aria-expanded={open}
          title={open ? "collapse" : "show the body"}
          onClick={() => setOpen((v) => !v)}
        >
          <span className="mm-rowhead">
            <span className={"mm-kind " + it.kind}>
              {KIND_LABEL[it.kind] ?? it.kind}
            </span>
            <span className="mm-headline">{title}</span>
            {proposed ? <span className="mm-proposed">proposed</span> : null}
          </span>
          {live ? null : (
            <span className="mm-dead">
              invalidated
              {it.invalid_reason ? ` · ${it.invalid_reason}` : ""}
              {it.invalid_detail ? ` · ${it.invalid_detail}` : ""}
            </span>
          )}
          <span className="mm-meta">
            {scope ? <span className="mm-scope">{scope}</span> : null}
            {`#${it.id} · `}
            {SOURCE_LABEL[it.source] ?? it.source}
            {it.slug && it.source !== "artifact" ? ` · ${it.slug}` : ""}
            {it.source_ref && it.source !== "artifact" && it.source !== "capture"
              ? ` · ${it.source_ref}`
              : ""}
            {it.created_at ? ` · ${age(Math.floor(it.created_at / 1000))}` : ""}
            {it.revised_at ? " · revised" : ""}
          </span>
        </button>
        {/* Outside the expand button, never nested in it: a button inside a
            button is invalid markup and swallows its own click. */}
        <button
          className="mm-act mm-source"
          title="show what this was captured from"
          aria-expanded={src !== null}
          onClick={() => void showSource()}
        >
          source
        </button>
        {proposed ? (
          <button
            className="mm-act mm-confirm"
            title="confirm this proposed item — it goes live in the mental model"
            aria-label={`confirm ${title}`}
            disabled={confirming || !live}
            onClick={() => onConfirm(it.id)}
          >
            confirm
          </button>
        ) : null}
        <button
          className="mm-act mm-rm"
          title="drop this item — the row stays, invalidated"
          aria-label={`drop ${title}`}
          disabled={removing || !live}
          onClick={() => onRemove(it.id)}
        >
          drop
        </button>
      </div>
      {open ? (
        <div className="mm-frozen">
          {asMD ? (
            <div
              className="mm-frozenmd md"
              // renderMD escapes before it transforms, so agent-written
              // markdown is inert (lib/markdown.ts)
              dangerouslySetInnerHTML={bodyHTML}
            />
          ) : bodyText ? (
            <pre className="mm-frozentext">{bodyText}</pre>
          ) : null}
          {it.quote ? (
            <div className="mm-quote">
              <span className="mm-ctxlbl">quote</span> «{it.quote}»
            </div>
          ) : null}
          {capSrc ? (
            <div className="mm-ctx">source: {sourceSummary(capSrc)}</div>
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
            {it.created_at ? `captured ${shortDate(it.created_at)}` : ""}
            {it.revised_at ? ` · revised ${shortDate(it.revised_at)}` : ""}
            {!live && it.invalidated_at
              ? ` · invalidated ${shortDate(it.invalidated_at)}`
              : ""}
          </div>
        </div>
      ) : null}
      {src !== null ? (
        <div className="mm-frozen mm-sourceview">
          {src === "loading" ? (
            <div className="mm-ctx">loading the source…</div>
          ) : "error" in src ? (
            <div className="mm-ctx">couldn't load the source: {src.error}</div>
          ) : !src.available ? (
            <div className="mm-ctx">
              source unavailable{src.kind ? ` (${src.kind})` : ""}
            </div>
          ) : (
            <>
              <div className="mm-ctx">
                {src.kind}
                {src.changed ? " · changed since capture" : ""}
              </div>
              <pre className="mm-frozentext">{src.text}</pre>
            </>
          )}
        </div>
      ) : null}
    </div>
  );
}

/** One raised flag: what zeus put in front of carl, and the three verdicts. */
function FlagRow({
  f,
  busy,
  onResolve,
  onOpenJob,
}: {
  f: MMFlag;
  busy: boolean;
  onResolve: (id: number, resolution: "drop" | "keep") => void;
  onOpenJob?: (slug: string) => void;
}) {
  const subject =
    (f.item_id ? `#${f.item_id}` : "") +
    (f.against_id ? `${f.item_id ? " vs " : ""}#${f.against_id}` : "");
  return (
    <div className="mm-flag" data-flag-id={String(f.id)}>
      <div className="mm-flagbody">
        <div className="mm-flagnote">
          ⚑ {subject ? `flag on ${subject} — ` : ""}
          {f.note}
        </div>
        <div className="mm-meta">
          {f.slug ? `raised by zeus of ${f.slug}` : "raised"}
          {f.raised_at ? ` · ${age(Math.floor(f.raised_at / 1000))}` : ""}
          {f.raise_note ? ` · «${f.raise_note}»` : ""}
        </div>
      </div>
      <div className="mm-flagacts">
        <button
          className="mm-act mm-flagdrop"
          // not gated on `item_id`: the server resolves a drop on a flag that
          // names no item as a plain close, so the verdict is always postable
          disabled={busy}
          title={
            f.item_id
              ? `invalidate mm #${f.item_id} as contradicted and close the flag`
              : "close the flag (it names no item to invalidate)"
          }
          onClick={() => onResolve(f.id, "drop")}
        >
          drop item
        </button>
        <button
          className="mm-act mm-flagkeep"
          disabled={busy}
          title="keep the item and close the flag"
          onClick={() => onResolve(f.id, "keep")}
        >
          keep
        </button>
        {onOpenJob && f.slug ? (
          <button
            className="mm-act mm-flagjob"
            title={`open ${f.slug}`}
            onClick={() => onOpenJob(f.slug)}
          >
            open job
          </button>
        ) : null}
      </div>
    </div>
  );
}

export interface MentalModelPaneProps {
  /**
   * Hidden rather than unmounted: the filter and any half-typed note are
   * component state, and a toggle back to the library would otherwise throw
   * them away.
   */
  hidden: boolean;
  /** App's ONE unfiltered mm poll. */
  mm: MMModel;
  /** Write a note. Routed through the shared mutate + refetch by the integrator. */
  onAddNote: (text: string) => Promise<void>;
  /** Drop one item by id (invalidates it — nothing here deletes). */
  onRemove: (id: number) => Promise<void>;
  /**
   * Confirm one `proposed` item (the nightly mind pass') so it goes live.
   * Routed through the same mutate + refetch as `onRemove`.
   */
  onConfirm: (id: number) => Promise<void>;
  /** Open the steer modal of a flag's job. Absent hides `open job`. */
  onOpenJob?: (slug: string) => void;
}

export function MentalModelPane({
  hidden,
  mm,
  onAddNote,
  onRemove,
  onConfirm,
  onOpenJob,
}: MentalModelPaneProps) {
  const [filter, setFilter] = useState<MMKindFilter>("all");
  const [q, setQ] = useState("");
  // The query the server is asked — `q` after the debounce settles.
  const [askedQ, setAskedQ] = useState("");
  const [noteOpen, setNoteOpen] = useState(false);
  const [note, setNote] = useState("");
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<ReadonlySet<number>>(
    () => new Set<number>(),
  );
  const [confirming, setConfirming] = useState<ReadonlySet<number>>(
    () => new Set<number>(),
  );
  const [resolving, setResolving] = useState<ReadonlySet<number>>(
    () => new Set<number>(),
  );
  const [flagErr, setFlagErr] = useState<string | null>(null);

  useEffect(() => {
    const t = q.trim();
    if (t === askedQ) return;
    const id = window.setTimeout(() => setAskedQ(t), FILTER_DEBOUNCE_MS);
    return () => window.clearTimeout(id);
  }, [q, askedQ]);

  // The FILTERED list: a second instance of the mm poll, alive only while the
  // box asks something, so an empty box costs nothing and shows App's list.
  const filtered = useMM(!hidden && askedQ !== "", { q: askedQ });
  const { refetch: refetchFiltered } = filtered;
  useEffect(() => {
    if (!hidden && askedQ !== "") void refetchFiltered();
  }, [askedQ, hidden, refetchFiltered]);
  const source: MMModel = askedQ !== "" ? filtered : mm;

  const flags = useMMFlags(!hidden);

  const counts = useMemo(() => {
    const c: Record<MMKindFilter, number> = {
      all: source.items.length,
      decision: 0,
      reading: 0,
      note: 0,
      invalidated: 0,
      nightly: 0,
      proposed: 0,
    };
    for (const it of source.items) {
      if (it.kind === "decision" || it.kind === "reading" || it.kind === "note") {
        c[it.kind]++;
      }
      if (!isLive(it)) c.invalidated++;
      if (it.source === "nightly") c.nightly++;
      if (isProposed(it)) c.proposed++;
    }
    return c;
  }, [source.items]);

  // ARRIVAL ORDER, narrowed and never re-sorted: the server serves newest
  // first and a list that reorders itself is a list you cannot aim at.
  const rows = useMemo(
    () => source.items.filter((it) => matchesFilter(it, filter)),
    [source.items, filter],
  );

  const refetchBoth = async () => {
    await mm.refetch();
    if (askedQ !== "") await refetchFiltered();
  };

  const remove = async (id: number) => {
    if (removing.has(id)) return;
    setRemoving((prev) => new Set(prev).add(id));
    // the caller routes this through the shared mutate, which parks a refusal
    // in the clock corner and never rejects
    await onRemove(id);
    if (askedQ !== "") await refetchFiltered();
    setRemoving((prev) => {
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
  };

  const confirm = async (id: number) => {
    if (confirming.has(id)) return;
    setConfirming((prev) => new Set(prev).add(id));
    // the integrator routes this through the shared mutate `remove` rides,
    // which parks a refusal in the clock corner rather than rejecting; the
    // refetch here is what moves the row whatever the caller did
    try {
      await onConfirm(id);
      await refetchBoth();
    } finally {
      // a rejection must not leave the button disabled until remount
      setConfirming((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    }
  };

  const resolve = async (id: number, resolution: "drop" | "keep") => {
    if (resolving.has(id)) return;
    setResolving((prev) => new Set(prev).add(id));
    setFlagErr(null);
    try {
      await resolveMMFlag(id, resolution);
    } catch (e) {
      setFlagErr(e instanceof Error ? e.message : String(e));
    }
    await flags.refetch();
    // a `drop` invalidated an item, so the rows move too
    if (resolution === "drop") await refetchBoth();
    setResolving((prev) => {
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
    if (askedQ !== "") await refetchFiltered();
    setAdding(false);
    setNote("");
    setNoteOpen(false);
  };

  const empty = source.error
    ? "couldn't load the mental model: " + source.error.message
    : !source.loaded
      ? "loading…"
      : askedQ !== ""
        ? `nothing matches “${askedQ}”`
        : mm.items.length
          ? "nothing in that filter"
          : "nothing captured yet — highlight text anywhere and press ◆ mind, or + note here; the nightly pass proposes items for you to confirm";

  return (
    <section className="graph-lib mm-pane" hidden={hidden}>
      <div className="graph-libhdr">
        <span className="graph-lbl">Mental model</span>
        <input
          className="graph-libfilter mm-filter"
          type="search"
          placeholder="filter…"
          aria-label="filter the mental model"
          value={q}
          onChange={(ev) => setQ(ev.target.value)}
        />
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
              // a CJK candidate's commit Enter also fires key:"Enter"
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
        {flags.flags.length || flagErr ? (
          <div className="mm-flags" aria-label="raised flags">
            <div className="graph-lbl mm-flagslbl">Flags</div>
            {flagErr ? <div className="mm-ctx mm-flagerr">{flagErr}</div> : null}
            {flags.flags.map((f) => (
              <FlagRow
                key={f.id}
                f={f}
                busy={resolving.has(f.id)}
                onResolve={(id, r) => void resolve(id, r)}
                onOpenJob={onOpenJob}
              />
            ))}
          </div>
        ) : null}
        {rows.length ? (
          rows.map((it) => (
            <MMRow
              key={it.id}
              it={it}
              removing={removing.has(it.id)}
              onRemove={(id) => void remove(id)}
              confirming={confirming.has(it.id)}
              onConfirm={(id) => void confirm(id)}
            />
          ))
        ) : (
          <div className="graph-libempty">{empty}</div>
        )}
      </div>
    </section>
  );
}

export default MentalModelPane;
