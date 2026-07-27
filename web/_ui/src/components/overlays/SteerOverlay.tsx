import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  doneJob,
  fetchDraft,
  fetchJobForm,
  killJob,
  reactivateJob,
  saveDraft,
  steerJob,
  uploadImage,
} from "../../api/client.ts";
import type { JobForm, WebJob } from "../../api/types.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { useDebouncedDraft } from "../../hooks/useDebouncedDraft.ts";
import { esc, renderMD } from "../../lib/markdown.ts";
import type { SteerMode } from "../../lib/moves.ts";
import { CmdComposer } from "./CmdComposer.tsx";
import { DocQuestions } from "./DocQuestions.tsx";
import {
  AttChips,
  Overlay,
  pastedFiles,
  pickImageFiles,
  panelDropHandlers,
  useOpenTmux,
  usePasteLayer,
  type AttChip,
  type FileSink,
  type Mutate,
} from "./Overlay.tsx";

// Message zeus. ONE modal, three modes (app.js:2097 openSteer), which is why
// the mode is a discriminator on almost everything below rather than three
// components: the composer, the draft sync, the attachment flow, the context
// block, the spec drawer and the close paths are identical in all three, and
// only these five things differ.
//
//   mode          title suffix                        endpoint          text
//   ----------    ---------------------------------   ---------------   ------
//   steer         "— message zeus"                    /api/steer        required
//   unblock       "— unblock"                         /api/steer        required
//   reactivate    "— reactivate (same zeus session)"   /api/reactivate   optional*
//
// * unless `requireText`, which is set ONLY by a done→working drag. The card's
//   and editor's ↺ buttons open reactivate without it, and their optional-text
//   behaviour is unaffected.

/**
 * Re-exported, not redeclared: `lib/moves.ts` owns this union because it is the
 * MOVES table's own vocabulary (a blocked→working drop opens "unblock", a
 * done→working drop opens "reactivate"). Two identical unions break nothing
 * until one of them gains a mode.
 */
export type { SteerMode };

const TITLE_SUFFIX: Record<SteerMode, string> = {
  steer: " — message zeus",
  unblock: " — unblock",
  reactivate: " — reactivate (same zeus session)",
};

const PLACEHOLDER: Record<SteerMode, string> = {
  steer:
    "message zeus directly — steering / pivot instruction, delivered straight to its pane",
  unblock: "tell the crew how to proceed — routed straight to the cell",
  reactivate:
    "optional: what to adjust — delivered as the resume prompt to the job's original zeus session (leave empty to just wake it)",
};

const SEND_LABEL: Record<SteerMode, string> = {
  steer: "send ⌘⏎",
  unblock: "unblock ⌘⏎",
  reactivate: "reactivate ⌘⏎",
};

/** Which endpoint a mode sends to. Pinned by SteerOverlay.test.tsx. */
export function steerEndpoint(mode: SteerMode): string {
  return mode === "reactivate" ? "/api/reactivate" : "/api/steer";
}

/** app.js:2205 — is an empty message a mistake in this mode? */
export function steerRequiresText(mode: SteerMode, requireText: boolean): boolean {
  return mode !== "reactivate" || requireText;
}

/**
 * app.js:2057 liveDisplayColumn — render()'s column bucketing for one job: an
 * unhealthy live cell displays as blocked whatever its file state says. null
 * for a job that is not live, which the caller reads as "not steerable".
 */
function liveDisplayColumn(j: WebJob | undefined): string | null {
  if (!j || !j.live) return null;
  return j.health ? "blocked" : j.state;
}

export interface SteerOverlayProps {
  open: boolean;
  /** The job being steered. null while closed. */
  slug: string | null;
  mode: SteerMode;
  /** Only a done→working drag sets this. See the table above. */
  requireText?: boolean;
  /**
   * The job's freshest board entry, looked up by jobKey() — undefined once it
   * leaves the board, which is one of the two ways this modal auto-closes.
   */
  job: WebJob | undefined;
  onClose: () => void;
  /** See Overlay.tsx. */
  mutate: Mutate;

  // ---- context-block slots -------------------------------------------------
  // The steer modal shows the same crew line / PR chips / thread chips /
  // artifact chips the CARD shows, and those renderers belong to the board
  // slice, not this one. They arrive as children rather than being duplicated
  // here: one renderer, two mount points, exactly the split the vanilla board
  // already used (threadChipsHTML filling both `.threads` on the card and
  // `#steer-threads` in this modal).
  //
  // Each slot is the INNER content only — the wrapper div with its id and its
  // `hidden` rule lives here, because that rule is per-mode.
  /** crewHTML(j) — hidden in reactivate mode (the cell is gone). */
  crew?: ReactNode;
  /** prChipHTML per pr — shown in EVERY mode: PRs are job history. */
  prChips?: ReactNode;
  /** threadChipsHTML(j) — every mode; the ⑂ branch button self-hides. */
  threadChips?: ReactNode;
  /** chipsHTML(artifacts, …) — hidden in reactivate mode. */
  artifactChips?: ReactNode;
  /** steerActionsHTML: the retry button + questionHTML. steer/unblock only. */
  actions?: ReactNode;
}

export function SteerOverlay(props: SteerOverlayProps) {
  // published by the body so a Files drop anywhere on the panel reaches it —
  // see panelDropHandlers
  const sink = useRef<((files: FileList | File[]) => void) | null>(null);
  return (
    <Overlay
      id="steer-overlay"
      open={props.open}
      priority={ESC_PRIORITY.steer}
      onClose={props.onClose}
      {...panelDropHandlers(sink)}
    >
      {props.slug ? (
        <SteerBody key={props.slug + props.mode} {...props} fileSink={sink} />
      ) : null}
    </Overlay>
  );
}

/**
 * Keyed on slug+mode so reopening for another job (or flipping steer→unblock)
 * is a fresh mount. That is `openSteer`'s wholesale reset — text, chips,
 * pending count, note, spec drawer — expressed once instead of as eleven
 * imperative assignments.
 */
function SteerBody({
  slug,
  mode,
  requireText = false,
  job,
  onClose,
  mutate,
  crew,
  prChips,
  threadChips,
  artifactChips,
  actions,
  fileSink,
}: SteerOverlayProps & { fileSink: FileSink }) {
  const jobSlug = slug as string;
  const [text, setText] = useState("");
  const [note, setNote] = useState("");
  const [chips, setChips] = useState<AttChip[]>([]);
  const [pending, setPending] = useState(0);
  const [sending, setSending] = useState(false);
  const [killing, setKilling] = useState(false);
  const [markingDone, setMarkingDone] = useState(false);
  const openTmuxTarget = useOpenTmux(mutate);
  const attInput = useRef<HTMLInputElement | null>(null);
  const [form, setForm] = useState<JobForm | null>(null);
  const [specOpen, setSpecOpen] = useState(false);

  const draft = useDebouncedDraft(jobSlug, text, (s, t) => {
    // fire-and-forget: the next keystroke's debounce is the retry
    void saveDraft(s, t).catch(() => {});
  });
  const { markSynced, flush: flushDraft, cancel: cancelDraft } = draft;

  // ---- the shared server-side draft ---------------------------------------
  // Fetched AFTER the composer is already showing empty, and landed only if
  // nothing has been typed in the meantime. An AbortController replaces the
  // vanilla `steerOverlay.hidden || steerSlug !== slug` staleness check: an
  // unmount aborts it, so a result for a modal that has moved on never lands.
  useEffect(() => {
    const ac = new AbortController();
    fetchDraft(jobSlug, ac.signal)
      .then((d) => {
        if (ac.signal.aborted) return;
        setText((cur) => {
          if (cur !== "") return cur;
          markSynced(d.text || "");
          return d.text || "";
        });
      })
      .catch(() => {});
    return () => ac.abort();
  }, [jobSlug, markSynced]);

  // ---- the collapsible spec drawer ----------------------------------------
  // reactivate skips it: /api/job/form describes the cell that would run, and
  // reactivate targets one that is already gone.
  useEffect(() => {
    if (mode === "reactivate") return;
    const ac = new AbortController();
    fetchJobForm(jobSlug, ac.signal)
      .then((d) => {
        if (ac.signal.aborted) return;
        if (d.err || !d.spec) return;
        setForm(d);
      })
      .catch(() => {});
    return () => ac.abort();
  }, [jobSlug, mode]);

  // ---- checkStaleModals (app.js:557) --------------------------------------
  // Close when the job moves to a different state than it had at open time:
  // the card behind the modal already reflects the new state, so an "unblock"
  // composer sitting on a job that unblocked itself is just confusing. A job
  // that leaves the board entirely closes it too (`!j`).
  //
  // NOTE the asymmetry with ThreadOverlay, which closes ONLY on `!j` — a
  // thread outlives working→blocked→done, and a closed one outlives the cell.
  const openState = useRef<string | undefined>(undefined);
  const captured = useRef(false);
  useEffect(() => {
    if (!captured.current) {
      // the baseline is taken on the first pass, not compared against — the
      // vanilla equivalent ran inside the 3s poll, never at open time
      captured.current = true;
      openState.current = job?.state;
      return;
    }
    if (!job || job.state !== openState.current) onClose();
  }, [job, onClose]);

  // ---- attachments --------------------------------------------------------
  // These compose into the message BODY at send time as `[image: <path>]`
  // lines — the chip is the message. (The editor does the opposite: it splices
  // the reference into the spec text immediately and its chip is only visual
  // confirmation. Removing a chip there does not remove the text.)
  const addFiles = useCallback(
    (files: FileList | File[]) => {
      const all = [...files];
      const imgs = pickImageFiles(all);
      if (all.length && !imgs.length) {
        setNote("images only (png/jpeg/gif/webp)");
        return;
      }
      for (const file of imgs) {
        setPending((n) => n + 1);
        uploadImage(jobSlug, file)
          .then((c) => {
            setChips((prev) => [...prev, c]);
            setNote("");
          })
          .catch((e: unknown) => {
            setNote("upload failed: " + (e instanceof Error ? e.message : e));
          })
          .finally(() => setPending((n) => n - 1));
      }
    },
    [jobSlug],
  );

  fileSink.current = addFiles;

  // steer wins the ⌘V race against the editor — see usePasteLayer.
  const onPaste = useCallback(
    (e: ClipboardEvent) => {
      const files = pastedFiles(e);
      if (!files.length) return;
      e.preventDefault();
      addFiles(files);
    },
    [addFiles],
  );
  usePasteLayer(true, ESC_PRIORITY.steer, onPaste);

  // ---- send ---------------------------------------------------------------
  const send = useCallback(async () => {
    if (pending > 0) {
      setNote("uploading image…");
      return;
    }
    const raw = text.trim();
    // an images-only steer (no typed text) is still a legitimate send
    const imgLines = chips.map((c) => "[image: " + c.path + "]").join("\n");
    const body = chips.length ? (raw ? raw + "\n\n" + imgLines : imgLines) : raw;
    if (!body && steerRequiresText(mode, requireText)) {
      setNote("message is empty");
      return;
    }
    setSending(true);
    // cancel FIRST — a debounce firing mid-send would land after the server's
    // own draft clear and resurrect the message that was just sent
    cancelDraft();
    const res = await mutate(() =>
      mode === "reactivate"
        ? reactivateJob(jobSlug, body)
        : steerJob(jobSlug, body),
    );
    setSending(false);
    if (res) {
      // cancel again: a keystroke during the in-flight send scheduled a fresh
      // sync that the pre-send cancel never saw
      cancelDraft();
      onClose();
    } else {
      setNote("send failed — see status bar");
    }
  }, [
    cancelDraft,
    chips,
    jobSlug,
    mode,
    mutate,
    onClose,
    pending,
    requireText,
    text,
  ]);

  const kill = useCallback(async () => {
    if (
      !window.confirm(
        `kill ${jobSlug}? the cell is torn down (artifacts preserved) and the job returns to draft.`,
      )
    ) {
      return;
    }
    setKilling(true);
    const res = await mutate(() => killJob(jobSlug));
    setKilling(false);
    // kill does NOT clear the draft server-side — land it rather than lose it
    if (res) {
      flushDraft();
      onClose();
    } else {
      setNote("cancel failed — see status bar");
    }
  }, [flushDraft, jobSlug, mutate, onClose]);

  const markDone = useCallback(async () => {
    setMarkingDone(true);
    const res = await mutate(() => doneJob(jobSlug));
    setMarkingDone(false);
    if (res) {
      cancelDraft(); // done also clears the draft server-side
      onClose();
    }
  }, [cancelDraft, jobSlug, mutate, onClose]);

  const liveCol = liveDisplayColumn(job);
  const killable =
    mode !== "reactivate" && (liveCol === "working" || liveCol === "blocked");
  const showTmux = mode !== "reactivate" && !!job?.live;
  const showDone = mode !== "reactivate" && liveCol === "blocked";

  // reactivate targets a dead cell: no crew to show, no artifacts to act on,
  // and nothing to retry or mark done. PRs, threads, the two messages and the
  // questions are job history and show in every mode.
  const showCrew = mode !== "reactivate" && !!crew;
  const showArtifacts = mode !== "reactivate" && !!artifactChips;
  const detail = job?.detail;
  const lastSent = job?.last_sent;
  const hasDocq = !!job?.doc_questions?.length;
  const showContext =
    showCrew ||
    !!prChips ||
    !!threadChips ||
    !!detail ||
    !!lastSent ||
    showArtifacts ||
    hasDocq;

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="steer-title">
          {jobSlug + TITLE_SUFFIX[mode]}
        </span>
        <span className="spacer"></span>
        <button
          className="btn tmux-open open-tmux"
          id="steer-tmux"
          hidden={!showTmux}
          title="raise zeus' pane in Ghostty"
          data-slug={jobSlug}
          onClick={() => void openTmuxTarget({ slug: jobSlug })}
        >
          ⧉ tmux
        </button>
        <button
          className="btn steer-done"
          id="steer-markdone"
          hidden={!showDone}
          title="mark this job done"
          data-slug={jobSlug}
          disabled={markingDone}
          onClick={() => void markDone()}
        >
          ✓ mark done
        </button>
        <button
          className="btn ghost danger"
          id="steer-kill"
          hidden={!killable}
          disabled={killing}
          onClick={() => void kill()}
        >
          ✕ cancel job
        </button>
      </div>
      <div className="panel-body answer-body">
        <div className="steer-context" id="steer-context" hidden={!showContext}>
          <div className="steer-crew" id="steer-crew" hidden={!showCrew}>
            {showCrew ? crew : null}
          </div>
          <div className="prs" id="steer-prs" hidden={!prChips}>
            {prChips}
          </div>
          <div className="threads" id="steer-threads" hidden={!threadChips}>
            {threadChips}
          </div>
          <div
            className="steer-detail"
            id="steer-detail"
            hidden={!detail}
            // renderMD escapes before transforming, so agent text is inert
            dangerouslySetInnerHTML={{ __html: detail ? renderMD(detail) : "" }}
          />
          <div className="last-sent" id="steer-lastsent" hidden={!lastSent}>
            {lastSent ? `me→zeus: ${lastSent}` : ""}
          </div>
          <div className="chips" id="steer-chips" hidden={!showArtifacts}>
            {showArtifacts ? artifactChips : null}
          </div>
          <div className="steer-docq" id="steer-docq" hidden={!hasDocq}>
            {job && hasDocq ? <DocQuestions job={job} mutate={mutate} /> : null}
          </div>
        </div>

        <div
          className="steer-actions"
          id="steer-actions"
          hidden={mode === "reactivate" || !actions}
        >
          {mode === "reactivate" ? null : actions}
        </div>

        <div className="steer-spec" id="steer-spec" hidden={!form}>
          <button
            className="steer-spec-head"
            id="steer-spec-toggle"
            onClick={() => setSpecOpen((v) => !v)}
          >
            <span className="tri">{specOpen ? "▾" : "▸"}</span> spec
          </button>
          <div
            className="steer-spec-body"
            id="steer-spec-body"
            hidden={!specOpen}
            dangerouslySetInnerHTML={{
              __html: form ? specMetaHTML(form) + renderMD(form.spec || "") : "",
            }}
          />
        </div>

        <CmdComposer
          id="steer-text"
          chipsId="steer-cmd-chips"
          value={text}
          onChange={setText}
          rows={4}
          placeholder={PLACEHOLDER[mode]}
          onSubmit={() => void send()}
        />

        {/* the drop target is the whole PANEL, not this zone — see
            panelDropHandlers in Overlay.tsx */}
        <div className="att-zone" id="steer-att-zone">
          <AttChips
            id="steer-chips-att"
            chips={chips}
            onRemove={(path) =>
              setChips((prev) => prev.filter((c) => c.path !== path))
            }
          />
          <div className="att-bar">
            <button
              className="btn ghost att-btn"
              id="steer-att-btn"
              type="button"
              title="attach images"
              onClick={() => attInput.current?.click()}
            >
              📎 attach
            </button>
            <span className="att-hint">paste (⌘V) or drop images</span>
            <input
              type="file"
              id="steer-att-input"
              ref={attInput}
              accept="image/png,image/jpeg,image/gif,image/webp"
              multiple
              hidden
              onChange={(e) => {
                if (e.target.files) addFiles(e.target.files);
                e.target.value = ""; // allow re-picking the same file
              }}
            />
          </div>
        </div>

        <button
          className="btn btn-big"
          id="steer-send"
          disabled={sending}
          onClick={() => void send()}
        >
          {SEND_LABEL[mode]}
        </button>
        <div className="answer-note" id="steer-note">
          {note}
        </div>
      </div>
    </>
  );
}

/**
 * app.js:2080 — compact read-only rows from the /api/job/form payload the spec
 * drawer already fetched, so the modal answers "what is this cell running
 * with" without leaving it. An HTML string because it is concatenated with
 * renderMD(spec) into one innerHTML, which is how the two stay one scroll
 * region rather than two.
 */
export function specMetaHTML(d: JobForm): string {
  const rows: [string, string, string?][] = [
    ["repo", esc(d.repo || "—")],
    ["base", esc(d.base || "origin/main")],
    ["model", esc(d.model || "claude-opus-5")],
    ["solo", d.solo ? "✓" : "✗"],
  ];
  const prereqs = d.prereqs || [];
  if (prereqs.length) {
    // wide: prereq lists run long, so the row spans both meta columns
    rows.push([
      "prereqs",
      prereqs.map((p) => esc(p.job) + (p.gated ? " gated ⚑" : "")).join(", "),
      "wide",
    ]);
  }
  return `<div class="spec-meta">${rows
    .map(
      ([k, v, w]) =>
        `<div class="spec-meta-row${w ? " " + w : ""}"><span class="k">${k}</span><span class="v">${v}</span></div>`,
    )
    .join("")}</div>`;
}
