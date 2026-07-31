import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  answerQuestion,
  deleteFeedback,
  doneJob,
  editFeedback,
  fetchDraft,
  fetchJobForm,
  killJob,
  reactivateJob,
  retryJob,
  saveDraft,
  setJobColor,
  steerJob,
  uploadImage,
} from "../../api/client.ts";
import type { JobForm, WebJob } from "../../api/types.ts";
import { JOB_COLORS } from "../../lib/format.ts";
import { SwatchStrip } from "./SwatchStrip.tsx";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { useDebouncedDraft } from "../../hooks/useDebouncedDraft.ts";
import { useHTML, useRenderedMD } from "../../hooks/useRenderedMD.ts";
import { esc, renderMD } from "../../lib/markdown.ts";
import { jobKey } from "../../lib/jobKey.ts";
import type { SteerMode } from "../../lib/moves.ts";
import { CrewChips, crewChipsVisible } from "../board/chips/CrewPills.tsx";
import { FeedbackPanel } from "../board/chips/FeedbackPanel.tsx";
import { PRChip } from "../board/chips/PRChips.tsx";
import { QuestionBlock } from "../board/chips/QuestionBlock.tsx";
import { ThreadChips } from "../board/chips/ThreadChips.tsx";
import { CmdComposer } from "./CmdComposer.tsx";
import { DocQuestions } from "./DocQuestions.tsx";
import { TokensPanel } from "./TokensPanel.tsx";
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

  // ---- drilldown actions ---------------------------------------------------
  // These two, and ONLY these two. Every surface this modal shows — the crew
  // line, the PR chips, the thread chips and their ⑂ branch button, the
  // artifact list, the feedback panel, the retry button, the question block
  // and the colour strip — renders from `job` inside the component, and every
  // mutation behind them goes through `mutate` and `api/client.ts`. What is
  // left here is the two things this component genuinely cannot do: open
  // ANOTHER overlay, which is App's state and not this one's.
  //
  // There are deliberately NO optional `ReactNode` slots left. Five of them
  // existed (`crew`, `prChips`, `threadChips`, `artifactChips`, `actions`) and
  // App.tsx filled NONE, so the whole context block rendered empty: no crew,
  // no PRs, no threads, no branch button, no artifact list, no retry and no
  // AskUserQuestion — every one of them a finished component wired to nothing,
  // and every one silent, because a slot left unfilled renders exactly like a
  // job with nothing in it. A surface that renders itself cannot be omitted;
  // an action the integrator must supply is REQUIRED, so the omission is a
  // compile error rather than a missing feature.
  /**
   * Open one artifact in the viewer. `name` arrives already prefixed
   * "<label>/<artifact>"; `jobKey` is jobKey(job) and NEVER a bare slug — the
   * viewer's pager rebuilds that job's ORDERED artifact list from it, and a
   * chip opened without one degrades the pager to a singleton (see
   * web/artifactpager_test.go). Same signature as BoardActions.onOpenArtifact
   * so one shape serves every entry point.
   */
  onOpenArtifact(url: string, name: string, jobKey: string): void;
  /** Side-thread overlay. `tid` null opens the ⑂ branch dialog. */
  onOpenThread(slug: string, tid: string | null): void;
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
  onOpenArtifact,
  onOpenThread,
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
  const showCrew = mode !== "reactivate" && !!job && crewChipsVisible(job);
  // PRs are job history rather than cell state, so they show in EVERY mode —
  // "what already shipped" is exactly the reactivation context.
  const prs = job?.prs ?? [];
  const artifacts = job?.artifacts ?? [];
  const showArtifacts = mode !== "reactivate" && artifacts.length > 0;
  // Side threads are job history too (a closed thread outlives its cell), so
  // like PRs they render in every mode. ThreadChips itself drops the ⑂ branch
  // affordance once the cell isn't live, which is the only part of the row a
  // reactivate-mode open couldn't act on — so the container shows for a live
  // job with no threads yet (the branch button alone) and for a dead job with
  // closed ones (the chips alone), and hides only when both are empty.
  const threads = job?.threads ?? [];
  const showThreads = !!job && (threads.length > 0 || job.live);
  // carl's per-artifact feedback: job history, every mode, and this modal is
  // one of only TWO surfaces in the app that renders it at all now that the
  // card carries a bare count — those rows are the dream loop's inbound
  // channel from the board, so "read it somewhere else" is not an option.
  const hasFeedback = (job?.feedback ?? []).length > 0;
  const detail = job?.detail;
  const lastSent = job?.last_sent;
  const hasDocq = !!job?.doc_questions?.length;
  // Token spend is job history in the strongest sense — summed from
  // transcripts that outlive the cell dir — so like PRs it shows in EVERY
  // mode, reactivate included: "what did this already cost" is exactly the
  // question a reactivation asks. Absent means never measured, which is not a
  // measured zero (see TokensPanel), and keeps the container hidden.
  const tok = job?.tokens;
  const hasTokens =
    !!tok &&
    !!(
      (tok.in || 0) + (tok.out || 0) + (tok.cache_write || 0) + (tok.cache_read || 0) ||
      tok.sessions
    );
  // steerActionsHTML (app.js): the retry button plus the pending
  // AskUserQuestion. steer/unblock only — reactivate targets a dead cell, so
  // there is nothing on it to retry and nothing waiting on an answer.
  //
  // `retryable` is the SAME rule the card's band 6 applies: an unhealthy cell
  // is retryable unless the health text says the cell is beyond retrying
  // ("is gone" / "startup prompt"), which a retry would only re-fail on.
  const retryable = !!job?.health && !/is gone|startup prompt/.test(job.health);
  const question = job?.question;
  const showActions = mode !== "reactivate" && (retryable || !!question);

  const showContext =
    showCrew ||
    prs.length > 0 ||
    showThreads ||
    !!detail ||
    !!lastSent ||
    showArtifacts ||
    hasDocq ||
    hasFeedback ||
    hasTokens;

  // ---- feedback save / delete ---------------------------------------------
  // Deliberately NOT props. FeedbackPanel's two callbacks are pure mutations
  // with no App-owned state behind them, so routing them through the
  // integrator would have added two more slots to forget — and one forgotten
  // slot is why this panel had no caller in the whole app. `mutate` already
  // owns the refetch-and-report-failure policy (Overlay.tsx), so this is the
  // same POST App.tsx's BoardActions makes, minus the chance to omit it.
  //
  // The shape inverts: Mutate resolves the parsed body or null on FAILURE,
  // while FeedbackPanel wants null on SUCCESS and an Error on failure (its
  // row paints "save failed" and stays open on one). The status bar already
  // carries the real message, so the Error here is only a flag.
  const saveFeedback = useCallback(
    async (s: string, id: number, note: string): Promise<Error | null> => {
      const res = await mutate(() => editFeedback(s, id, note));
      return res === null ? new Error("save failed") : null;
    },
    [mutate],
  );
  const removeFeedback = useCallback(
    (s: string, id: number) => {
      void mutate(() => deleteFeedback(s, id));
    },
    [mutate],
  );

  // Same inversion as saveFeedback's, and here it is load-bearing rather than
  // cosmetic: QuestionBlock kills every option the moment one is clicked, and
  // ONLY a returned failure brings them back. Answering `void` would leave a
  // failed answer's question unanswerable from the board short of a reload.
  const answer = useCallback(
    async (s: string, optionIndex: number): Promise<Error | null> => {
      const res = await mutate(() => answerQuestion(s, optionIndex));
      return res === null ? new Error("answer failed") : null;
    },
    [mutate],
  );

  // Both bodies below are innerHTML, and both are re-derived on every 3s poll
  // — memoised so an unchanged string leaves the DOM (and the reader's scroll
  // and selection) alone. See useRenderedMD.
  const detailHTML = useRenderedMD(detail);
  const specHTML = useHTML(
    form ? specMetaHTML(form) + renderMD(form.spec || "") : "",
  );

  const key = job ? jobKey(job) : jobSlug;
  // display_slug || slug — a renamed archive entry prefixes its chips with the
  // name it was archived UNDER, which is what the artifact viewer's title
  // shows and what its pager pages within.
  const label = job ? job.display_slug || job.slug : jobSlug;

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="steer-title">
          {jobSlug + TITLE_SUFFIX[mode]}
        </span>
        {/* The same nine-swatch colour strip as the job editor's. It has to be
            HERE and not only in the editor: the board routes a live
            working/blocked card to this modal and never to the editor, so
            without this copy the picker would be reachable only while the cell
            is down — and /api/job/color is ungated precisely so a RUNNING job
            can be recoloured. It rides no mode check (recolouring is valid in
            steer, unblock and reactivate alike) and touches nothing else: a
            pick POSTs the colour and never sends a steer message. */}
        <ColorPicker slug={jobSlug} color={job?.color} mutate={mutate} />
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
          {/* `crew`, not `steer-crew`: .crew is the class on this element
              itself, exactly as .threads is on #steer-threads, and style.css
              says so at the .crew rule. Both are MODAL-ONLY — on a card the
              same tokens render bare in band 2's .chips row. */}
          <div className="crew" id="steer-crew" hidden={!showCrew}>
            {job && showCrew ? <CrewChips job={job} /> : null}
          </div>
          {/* PRChip, not PRChips: the plural renders its own `.prs` wrapper
              for the CARD's band 5, and this container already carries the
              class — nesting them would double the row's padding. */}
          <div className="prs" id="steer-prs" hidden={!prs.length}>
            {prs.map((pr, i) => (
              <PRChip key={`${pr.url}|${i}`} pr={pr} />
            ))}
          </div>
          {/* The card passes withBranch={false}, delegating ⑂ to this modal —
              so this is the ONLY mount point of BranchButton in the app, and
              with it unrendered no control anywhere could branch a thread.
              Default withBranch here (no argument), which is exactly the split
              BranchButton's header states. */}
          <div className="threads" id="steer-threads" hidden={!showThreads}>
            {job && showThreads ? (
              <ThreadChips job={job} onOpenThread={onOpenThread} />
            ) : null}
          </div>
          <div
            className="steer-detail"
            id="steer-detail"
            hidden={!detail}
            // renderMD escapes before transforming, so agent text is inert
            dangerouslySetInnerHTML={detailHTML}
          />
          <div className="last-sent" id="steer-lastsent" hidden={!lastSent}>
            {lastSent ? `me→zeus: ${lastSent}` : ""}
          </div>
          {/* artifactListHTML (app.js) — EVERY artifact as an openable chip,
              no cap and no "+N more": the card's band-2 surface is now a bare
              COUNT and a modal scrolls, so the drilldown just shows the list
              where the card had to bound it.

              Not shared with EditorOverlay's ArtifactList even though the
              markup matches, and that is a deliberate hold rather than an
              oversight: the two drilldowns are owned by different slices right
              now, and the renderer's real home is the board chips module
              beside ThreadChips/PRChips. Collapse the two there in one commit;
              until then the data-* attributes and the class are pinned by both
              test files.

              `key` is jobKey(job) and not job.slug — see onOpenArtifact. */}
          <div className="chips" id="steer-chips" hidden={!showArtifacts}>
            {showArtifacts
              ? artifacts.map((a) => {
                  const name = `${label}/${a.name}`;
                  return (
                    <button
                      key={a.url}
                      className="chip"
                      data-url={a.url}
                      data-key={key}
                      data-name={name}
                      onClick={() => onOpenArtifact(a.url, name, key)}
                    >
                      ▤ {a.name}
                    </button>
                  );
                })
              : null}
          </div>
          <div className="steer-docq" id="steer-docq" hidden={!hasDocq}>
            {job && hasDocq ? <DocQuestions job={job} mutate={mutate} /> : null}
          </div>
          {/* carl's per-artifact feedback notes: a drilldown surface, like the
              questions panel above it. The card carries only a count chip
              whose title says "click the card to read them" — this is where
              they are read, edited and deleted. */}
          <div className="steer-feedback" id="steer-feedback" hidden={!hasFeedback}>
            {job && hasFeedback ? (
              <FeedbackPanel
                job={job}
                onSaveFeedback={saveFeedback}
                onDeleteFeedback={removeFeedback}
              />
            ) : null}
          </div>
          {/* last because it is ambient: nothing here is actionable, and a
              job that was never measured leaves this hidden entirely rather
              than showing a zero — see TokensPanel. */}
          <div className="steer-tokens" id="steer-tokens" hidden={!hasTokens}>
            <TokensPanel job={job} />
          </div>
        </div>

        {/* Both children are DIRECT children of this element on purpose:
            style.css scopes them as `.steer-actions > .btn.retry` and
            `.steer-actions > .question`, the latter so the question block
            cannot collide with the zeus status pill's own `question` class. */}
        <div className="steer-actions" id="steer-actions" hidden={!showActions}>
          {showActions && retryable ? (
            <button
              className="btn retry"
              data-slug={jobSlug}
              onClick={() => void mutate(() => retryJob(jobSlug))}
            >
              ↻ retry
            </button>
          ) : null}
          {showActions && job && question ? (
            // Keyed on the question's identity so a NEW question always mounts
            // a fresh block: the old one may have disabled its options on a
            // click that has since been answered in the pane.
            <QuestionBlock
              key={question.tool_use_id}
              job={job}
              onAnswerQuestion={answer}
            />
          ) : null}
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
            dangerouslySetInnerHTML={specHTML}
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
 * The job-colour picker: nine radios — "no colour" plus /color's own eight, in
 * /color's own order. Port of swatchStripHTML + setSwatchStrip + initSwatchStrip
 * (app.js:107-155), collapsed into one component because React makes the
 * "reflect the job's colour into the strip" half a render rather than a call.
 *
 * Generated from JOB_COLORS rather than hand-written, so this strip and the
 * editor's cannot drift in palette, order or markup.
 *
 * `name` must differ per strip — both strips can be in the DOM at once and
 * radios sharing a name are ONE group, so a reused name would make a pick here
 * silently unpick the editor's. `steer-color` is this one's, `f-color` the
 * editor's, exactly as in vanilla.
 *
 * "No colour" is a REAL radio (value "") rather than an absence: an
 * unrecognised colour from the wire falls back to it instead of leaving the
 * strip with nothing checked and no way to click back to neutral.
 *
 * The pick POSTs immediately and is deliberately NOT the debounced form
 * autosave every editor control rides: saveform refuses any job whose cell is
 * live — which is exactly when recolouring is useful — a full-replace save
 * from an older client could wipe the colour by omitting the key, and a
 * discrete one-click choice has nothing to debounce.
 */
function ColorPicker({
  slug,
  color,
  mutate,
}: {
  slug: string;
  color: string | undefined;
  mutate: Mutate;
}) {
  // The optimistic pick, held only while the POST is in flight. Cleared on
  // BOTH outcomes because `mutate` has refetched the board by then, so `color`
  // is authoritative either way: on success it is the new colour, and on
  // failure it is the old one — which is how the strip can never be left
  // showing a colour the server rejected.
  const [pick, setPick] = useState<string | null>(null);
  const known = (v: string | undefined): string =>
    v && (JOB_COLORS as readonly string[]).includes(v) ? v : "";
  const current = pick ?? known(color);
  const choose = async (v: string) => {
    setPick(v);
    await mutate(() => setJobColor(slug, v));
    setPick(null);
  };
  return (
    <span className="f-swatches compact" id="steer-swatches" title="job color">
      <SwatchStrip
        // `steer-color` vs the editor's `f-color`: both strips can be in the
        // DOM at once and radios sharing a name are ONE group, so a shared
        // name would make a pick here silently unpick the editor's.
        name="steer-color"
        value={current}
        onPick={(v) => void choose(v)}
        compact
      />
    </span>
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
    ["model", esc(d.model || "claude-fable-5")],
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
