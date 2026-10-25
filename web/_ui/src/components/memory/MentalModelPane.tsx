// MODEL mode — the second pane of the memory view, beside LIBRARY.
//
// The mental model is one row per thing carl knows. Since schema v39 most
// rows are written by a MIND RUN: one headless pass inside kraken serve that
// reads a finished job's conversations, its ★-saved artifacts and the read
// marks carl made with the ✓ read button (or, once a day, whatever no job run
// swept) and writes items straight in — adding new ones, and superseding or
// retiring ones carl's newer words contradict. Such rows carry a one-line
// `title` (a claim), a `scope` (`universal`, or a repo plus a repo-relative
// dir), the `quote` it rests on and the `run_id` that wrote it. Rows from
// before the mind layer carry none of those and headline their content's
// first line.
//
// Above the rows sit three sections. FLAGS: contradictions raised to carl,
// each offering `drop item` (the item is invalidated, reason `contradicted`),
// `keep` (the flag closes) and `open job`. RUNS: the last mind runs, each
// expandable to its report and raw answer, with a dry-run `replay` that
// judges the recorded input again under the current mind skill and shows
// what would change. PENDING MARKS: what the next runs will read, each
// removable until a run consumes it.
//
// Nothing here is a local latch: the lists are the server's, every write is
// followed by a refetch, and the row's next state is whatever that says.

import { useEffect, useMemo, useState } from "react";

import {
  fetchMMRun,
  fetchMMSource,
  removeMMMark,
  replayMMRun,
  resolveMMFlag,
} from "../../api/client.ts";
import type {
  WebMindExclusion,
  WebMindOp,
  WebMindReplay,
  WebMindRun,
  WebMMMark,
} from "../../api/types.ts";
import {
  useMM,
  useMMFlags,
  useMMMarks,
  useMMRuns,
  type MMModel,
} from "../../hooks/useMM.ts";
import type { MMCaptureSource, MMFlag, MMItem, MMSourceView } from "../../lib/mm.ts";
import { age } from "../../lib/format.ts";
import { useRenderedMD } from "../../hooks/useRenderedMD.ts";
import { shortDate } from "../../lib/dates.ts";

/**
 * The kind chips. Four are a KIND (every item has exactly one) and the rest
 * cut across them — an invalidated reading is still a reading, and an item a
 * mind run wrote (`pass`, source `nightly` on the wire) is still a decision or
 * a note.
 */
export type MMKindFilter = "all" | "decision" | "reading" | "note" | "invalidated" | "pass";

const FILTERS: { key: MMKindFilter; label: string }[] = [
  { key: "all", label: "all" },
  { key: "decision", label: "decisions" },
  { key: "reading", label: "readings" },
  { key: "note", label: "notes" },
  { key: "invalidated", label: "invalidated" },
  { key: "pass", label: "pass" },
];

/** How long the filter box waits after the last keystroke before it asks. */
const FILTER_DEBOUNCE_MS = 250;

/** How many runs the RUNS section lists. */
const RUNS_SHOWN = 10;

/** Live is the ABSENCE of the key — the wire has no boolean to read. */
const isLive = (it: MMItem) => !it.invalidated_at;

export function matchesFilter(it: MMItem, f: MMKindFilter): boolean {
  switch (f) {
    case "all":
      return true;
    case "invalidated":
      return !isLive(it);
    case "pass":
      return it.source === "nightly";
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
  nightly: "pass",
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
  const [open, setOpen] = useState(false);
  // The `source` drill-down: null = not asked, "loading" while in flight.
  const [src, setSrc] = useState<MMSourceView | "loading" | { error: string } | null>(
    null,
  );
  const live = isLive(it);
  const title = itemTitle(it);
  const scope = scopeLabel(it);
  const capSrc = captureSource(it);
  /**
   * A legacy marked SELECTION expands as markdown (its content is a span of an
   * artifact's markdown source); a committed item's body is prose the mind
   * thread wrote, which is markdown too, and so is a pass item's body,
   * written by a mind run. A code reading's content is source in some
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
            {it.run_id ? ` · run #${it.run_id}` : ""}
            {it.supersedes ? ` · supersedes #${it.supersedes}` : ""}
            {it.superseded_by ? ` · superseded by #${it.superseded_by}` : ""}
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

/** `HH:MM` local, the runs list's time column; "" for a zero stamp. */
function clock(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** What a run covered: its job, or the once-a-day sweep of everything else. */
export function runSubject(r: WebMindRun): string {
  if (r.job_id && r.slug) return r.slug;
  if (r.trigger === "daily" || !r.job_id) return "daily catch-all";
  return r.slug || `job ${r.job_id}`;
}

/** The run's counts line: `+added · N rewritten · N retired · N excluded`. */
export function runCounts(r: WebMindRun): string {
  return `+${r.added ?? 0} · ${r.superseded ?? 0} rewritten · ${r.retired ?? 0} retired · ${r.excluded ?? 0} excluded`;
}

/** One answered op as one line: `supersede #68 — <title or quote>`. */
function opLine(o: WebMindOp): string {
  const what = (o.item?.title ?? "").trim() || (o.quote ?? "").trim() || (o.note ?? "").trim();
  return `${o.op}${o.target ? ` #${o.target}` : ""}${what ? ` — ${what}` : ""}`;
}

function exclusionLine(x: WebMindExclusion): string {
  return `${x.rung}: «${x.quote}»${x.why ? ` — ${x.why}` : ""}`;
}

/** A dry-run replay's diff, rendered as three lists plus the two prompt shas. */
function ReplayDiff({ d }: { d: WebMindReplay }) {
  const added = d.diff?.ops_added ?? [];
  const lost = d.diff?.ops_lost ?? [];
  const moved = d.diff?.exclusions_changed ?? [];
  const same = d.old_prompt_sha === d.new_prompt_sha;
  return (
    <div className="mm-replay" aria-label="replay diff">
      <div className="mm-ctx">
        prompt {short(d.old_prompt_sha)}
        {same ? " (unchanged)" : ` → ${short(d.new_prompt_sha)}`}
        {d.new_run ? ` · written as run #${d.new_run}` : " · dry run, nothing written"}
      </div>
      {!added.length && !lost.length && !moved.length ? (
        <div className="mm-ctx">no difference: the current skill answers the same</div>
      ) : (
        <>
          {added.map((o, i) => (
            <div key={"a" + i} className="mm-ctx mm-diffadd">
              + {opLine(o)}
            </div>
          ))}
          {lost.map((o, i) => (
            <div key={"l" + i} className="mm-ctx mm-difflost">
              − {opLine(o)}
            </div>
          ))}
          {moved.map((x, i) => (
            <div key={"x" + i} className="mm-ctx">
              ~ excluded {exclusionLine(x)}
            </div>
          ))}
        </>
      )}
    </div>
  );
}

type Loadable<T> = null | "loading" | { error: string } | T;

/** One mind run: what it covered, its counts, and on expand its record. */
function RunRow({ r }: { r: WebMindRun }) {
  const [open, setOpen] = useState(false);
  const [record, setRecord] = useState<Loadable<{ report: string; answer: string }>>(null);
  const [replay, setReplay] = useState<Loadable<WebMindReplay>>(null);

  const toggle = async () => {
    const next = !open;
    setOpen(next);
    if (!next || (record !== null && record !== "loading" && !("error" in record))) return;
    setRecord("loading");
    try {
      const [rep, ans] = await Promise.all([fetchMMRun(r.id, "report"), fetchMMRun(r.id, "answer")]);
      setRecord({ report: rep.blob ?? "", answer: ans.blob ?? "" });
    } catch (e) {
      setRecord({ error: e instanceof Error ? e.message : String(e) });
    }
  };

  const runReplay = async () => {
    if (replay === "loading") return;
    setReplay("loading");
    try {
      setReplay(await replayMMRun(r.id));
    } catch (e) {
      setReplay({ error: e instanceof Error ? e.message : String(e) });
    }
  };

  const reportHTML = useRenderedMD(
    record && record !== "loading" && !("error" in record) && record.report
      ? record.report
      : undefined,
  );

  const unfinished = r.state !== "done";
  return (
    <div className="mm-run" data-run-id={String(r.id)}>
      <div className="mm-rowtop">
        <button
          className="mm-open"
          aria-expanded={open}
          title={open ? "collapse" : "show this run's report and answer"}
          onClick={() => void toggle()}
        >
          <span className="mm-rowhead">
            <span className="mm-runtime">{clock(r.created_at)}</span>
            <span className="mm-headline">{runSubject(r)}</span>
            <span className="mm-meta">
              {r.trigger} · run #{r.id}
              {unfinished ? ` · ${r.state}` : ""}
            </span>
          </span>
          <span className="mm-meta">
            {r.state === "done" ? runCounts(r) : r.note || r.state}
            {r.created_at ? ` · ${age(Math.floor(r.created_at / 1000))}` : ""}
          </span>
        </button>
        <button
          className="mm-act mm-replaybtn"
          disabled={!r.has_bundle || replay === "loading"}
          title={
            r.has_bundle
              ? "judge this run's recorded input again under the current mind skill — a dry run, nothing is written"
              : "this run's recorded input is gone (pruned, or never recorded), so it cannot be replayed"
          }
          onClick={() => void runReplay()}
        >
          {replay === "loading" ? "replaying…" : "replay"}
        </button>
      </div>
      {open ? (
        <div className="mm-frozen">
          {record === "loading" || record === null ? (
            <div className="mm-ctx">loading the run's record…</div>
          ) : "error" in record ? (
            <div className="mm-ctx">couldn't load the run: {record.error}</div>
          ) : (
            <>
              <span className="mm-ctxlbl">report</span>
              {record.report ? (
                <div className="mm-frozenmd md" dangerouslySetInnerHTML={reportHTML} />
              ) : (
                <div className="mm-ctx">no report (the run did not finish)</div>
              )}
              <span className="mm-ctxlbl">answer</span>
              {record.answer ? (
                <pre className="mm-frozentext">{record.answer}</pre>
              ) : (
                <div className="mm-ctx">no answer recorded</div>
              )}
              <div className="mm-ctx">
                {r.model ? `${r.model} · ` : ""}
                {r.prompt_sha ? `prompt ${short(r.prompt_sha)} · ` : ""}
                {`${r.in_tokens ?? 0} in / ${r.out_tokens ?? 0} out tokens`}
                {r.source_run ? ` · replay of run #${r.source_run}` : ""}
              </div>
            </>
          )}
        </div>
      ) : null}
      {replay !== null && replay !== "loading" ? (
        <div className="mm-frozen">
          {"error" in replay ? (
            <div className="mm-ctx">replay failed: {replay.error}</div>
          ) : (
            <ReplayDiff d={replay} />
          )}
        </div>
      ) : null}
    </div>
  );
}

/** Where a mark's text came from, in a few words. */
function markWhere(m: WebMMMark): string {
  const s = m.source;
  const parts: string[] = [m.slug || "no job"];
  if (s?.kind === "artifact" && s.name) parts.push(s.name);
  else if (s?.kind && s.kind !== "manual") parts.push(s.kind);
  return parts.join(" · ");
}

/** One pending read mark, removable until a run consumes it. */
function MarkRow({
  m,
  busy,
  onRemove,
}: {
  m: WebMMMark;
  busy: boolean;
  onRemove: (id: number) => void;
}) {
  return (
    <div className="mm-mark" data-mark-id={String(m.id)}>
      <div className="mm-markbody">
        <span className="mm-meta">{markWhere(m)}</span>
        <span className="mm-quote">«{m.text}»</span>
        <span className="mm-meta">
          {m.created_at ? age(Math.floor(m.created_at / 1000)) : ""}
        </span>
      </div>
      <button
        className="mm-act mm-markrm"
        disabled={busy}
        title="remove this mark — no run will read it"
        aria-label={`remove mark ${m.id}`}
        onClick={() => onRemove(m.id)}
      >
        remove
      </button>
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
   * Accepted and ignored: mind runs write items live, so there is no confirm
   * button any more. Kept optional so an older caller still compiles.
   */
  onConfirm?: (id: number) => Promise<void>;
  /** Open the steer modal of a flag's job. Absent hides `open job`. */
  onOpenJob?: (slug: string) => void;
}

export function MentalModelPane({
  hidden,
  mm,
  onAddNote,
  onRemove,
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
  const [unmarking, setUnmarking] = useState<ReadonlySet<number>>(
    () => new Set<number>(),
  );
  const [markErr, setMarkErr] = useState<string | null>(null);
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
  const runs = useMMRuns(!hidden);
  const marks = useMMMarks(!hidden);

  const counts = useMemo(() => {
    const c: Record<MMKindFilter, number> = {
      all: source.items.length,
      decision: 0,
      reading: 0,
      note: 0,
      invalidated: 0,
      pass: 0,
    };
    for (const it of source.items) {
      if (it.kind === "decision" || it.kind === "reading" || it.kind === "note") {
        c[it.kind]++;
      }
      if (!isLive(it)) c.invalidated++;
      if (it.source === "nightly") c.pass++;
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

  const unmark = async (id: number) => {
    if (unmarking.has(id)) return;
    setUnmarking((prev) => new Set(prev).add(id));
    setMarkErr(null);
    try {
      await removeMMMark(id);
    } catch (e) {
      // 409 once a run consumed it between the poll and the press
      setMarkErr(e instanceof Error ? e.message : String(e));
    }
    await marks.refetch();
    setUnmarking((prev) => {
      const next = new Set(prev);
      next.delete(id);
      return next;
    });
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
          : "nothing captured yet — highlight text anywhere and press ✓ read, or + note here; when you click Done on a job, a mind run reads its conversations and your marks and writes items here";

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
        <div className="mm-runs" aria-label="mind runs">
            <div className="graph-lbl mm-flagslbl">runs</div>
            <div className="mm-ctx">
              A run is one pass of the mind skill over what you said and kept — a
              finished job&apos;s conversations, ★ saves and ✓ read marks, or once a
              day whatever no job run covered — writing items straight into this
              model. Replay judges a run&apos;s recorded input again under the
              current skill without writing anything.
            </div>
            {runs.error ? (
              <div className="mm-ctx mm-flagerr">
                couldn&apos;t load the runs: {runs.error.message}
              </div>
            ) : null}
            {!runs.error && !runs.loading && !runs.runs.length ? (
              <div className="mm-ctx">no runs yet</div>
            ) : null}
            {runs.runs.slice(0, RUNS_SHOWN).map((r) => (
              <RunRow key={r.id} r={r} />
            ))}
          </div>
        {marks.marks.length || markErr ? (
          <div className="mm-marks" aria-label="pending marks">
            <div className="graph-lbl mm-flagslbl">
              pending marks · {marks.marks.length}
            </div>
            <div className="mm-ctx">
              text you marked with ✓ read that no run has read yet
            </div>
            {markErr ? <div className="mm-ctx mm-flagerr">{markErr}</div> : null}
            {marks.marks.map((m) => (
              <MarkRow
                key={m.id}
                m={m}
                busy={unmarking.has(m.id)}
                onRemove={(id) => void unmark(id)}
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
