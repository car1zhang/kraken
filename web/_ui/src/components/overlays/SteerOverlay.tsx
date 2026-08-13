import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
} from "react";
import {
  answerQuestion,
  deleteFeedback,
  doneJob,
  editFeedback,
  fetchDraft,
  fetchJobForm,
  killJob,
  openInEditor,
  reactivateJob,
  retryJob,
  saveDraft,
  setJobColor,
  steerJob,
  uploadImage,
} from "../../api/client.ts";
import type { JobForm, WebJob, WebJobLoad } from "../../api/types.ts";
import {
  JOB_COLORS,
  SCOPE_ANCHORS,
  UNBLOCK_ANCHORS,
  age,
  loadRatioText,
  loadROI,
  type LoadBand,
} from "../../lib/format.ts";
import { SwatchStrip } from "./SwatchStrip.tsx";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { useDebouncedDraft } from "../../hooks/useDebouncedDraft.ts";
import { useHTML, useRenderedMD } from "../../hooks/useRenderedMD.ts";
import {
  decorateArtifactRefs,
  matchArtifactRef,
} from "../../lib/artifactRefs.ts";
import { codeRefTarget } from "../../lib/codeRefs.ts";
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
import {
  RenameButton,
  RenameRow,
  renameControlVisible,
  useRenameControl,
  type RenameControlState,
} from "./RenameControl.tsx";
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
import { showToast } from "./Toast.tsx";

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
  /**
   * A rename succeeded — the caller re-points `slug` at the SAME job under its
   * new name.
   *
   * The one optional callback on this interface, and it does NOT re-open the
   * unfilled-slot hazard the block below describes: the rename control is
   * hidden outright without it (renameControlVisible), so an unwired
   * integrator loses a visible button rather than gaining a broken one.
   */
  onRenamed?: (slug: string) => void;
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
  /**
   * Held here rather than in the body for EditorOverlay's reason, which
   * applies verbatim: a successful rename re-points `slug`, the body is keyed
   * on it, so it remounts in the same commit the response lands in — and the
   * `skipped` notice the response may carry is the only record anywhere that a
   * preserved artifact copy was left behind under the old name.
   */
  const rename = useRenameControl(props.slug, props.onRenamed, {
    // see EditorOverlay's: this component is never unmounted either, so the
    // overlay closing is what resets the control between jobs
    open: props.open,
  });
  return (
    <Overlay
      id="steer-overlay"
      open={props.open}
      priority={ESC_PRIORITY.steer}
      onClose={props.onClose}
      {...panelDropHandlers(sink)}
    >
      {props.slug ? (
        <SteerBody
          key={props.slug + props.mode}
          {...props}
          fileSink={sink}
          rename={rename}
        />
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
  onRenamed,
  mutate,
  onOpenArtifact,
  onOpenThread,
  fileSink,
  rename,
}: SteerOverlayProps & { fileSink: FileSink; rename: RenameControlState }) {
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
      // The baseline is taken on the first pass, not compared against — the
      // vanilla equivalent ran inside the 3s poll, never at open time.
      //
      // Capture is LAZY, matching EditorBody's: a job that is not in the board
      // payload yet has no state to baseline against, and `undefined` is not
      // one. A rename is exactly that case — this body remounts on the new
      // slug immediately, while the board still carries the old one for up to
      // one poll — so an eager capture would read the job's ARRIVAL as a state
      // change and close the modal seconds after every successful rename.
      if (!job) return;
      captured.current = true;
      openState.current = job.state;
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

  // Q5's answer: the rename lives on BOTH surfaces. A live working/blocked
  // card routes here and never to the editor, so without this copy the control
  // would be reachable only while the cell is down — the same argument the
  // colour strip below the title already carries.
  // The state is the overlay's (it outlives this body's remount — see the
  // hoist there); only the visibility rule, which needs `job`, is decided here.
  const showRename = renameControlVisible(job, false, onRenamed);

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
  // Zeus' triage estimate, spelled out. The CARD carries the same numbers as a
  // bare `⚡ 4.0× 2→8` badge — a chip has room for a ratio and not for two
  // sentences — so this modal is the only surface where the two rungs are
  // NAMED, which is the half that makes them mean anything to a reader who is
  // not the agent that wrote them.
  //
  // Unlike the chip (LoadChip), no column check: `loadChipVisible` demands the
  // card read as blocked because a stale estimate sitting in the blocked column
  // reads as a current fact about a job that has since started running. That
  // argument is about a card being SCANNED, not about a drilldown being opened
  // deliberately — and this modal opens in unblock mode on exactly the jobs
  // that have one. The estimate stays whatever loadROI accepts: an absent key,
  // the cleared both-zero state and a half-estimate all render nothing at all.
  const load = job?.load;
  const loadDerived = loadROI(load);
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
    (!!load && !!loadDerived) ||
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

  /**
   * A code ref clicked in the detail or the spec (lib/codeRefs.ts). Both bodies
   * are innerHTML, so this is one delegated handler per surface rather than a
   * prop on anything — codeRefTarget answers null for every click that is not
   * on a ref, including a plain `<code>`, so it is inert the rest of the time.
   *
   * Only these two surfaces. The THREAD and DREAM bodies render the same
   * markdown through the same renderer and deliberately get nothing: what zeus
   * says it is stuck on and what the spec asks for are statements about THIS
   * job's checkout, which `jobSlug` resolves; a thread's answer or a night
   * report ranges across the whole board and has no one checkout to open a
   * relative path under.
   *
   * `jobSlug` and not `job?.slug`: the modal opens on an archived job too, and
   * the slug is the one thing present in every mode. The server does NOT treat
   * the two alike, though — `archived` picks the namespace it resolves that
   * slug in, and an archived job whose freed slug a new live job has since
   * taken would otherwise resolve to the live one's checkout. So the flag rides
   * along, from `job?.archived`.
   *
   * Duplicated from ArtifactOverlay's `openCodeRef` rather than shared, which
   * is the trade the two overlays already make everywhere else — a shared
   * helper would be a component-to-component import between two modals that
   * otherwise know nothing about each other, over eight lines whose whole body
   * is one call and one toast.
   */
  const onCodeRefClick = useCallback(
    (ev: ReactMouseEvent<HTMLDivElement>) => {
      const el = ev.target;
      if (!(el instanceof Element)) return;
      // Artifact refs first: a span naming one of THIS job's artifacts opens
      // it in the viewer (the same call the chips below make), and only a
      // span naming nothing in the list falls through to the editor path.
      const code = el.closest("code");
      if (code && !code.closest("pre")) {
        const hit = matchArtifactRef(
          code.textContent ?? "",
          job?.artifacts ?? [],
        );
        if (hit) {
          onOpenArtifact(hit.url, `${label}/${hit.name}`, key);
          return;
        }
      }
      const target = codeRefTarget(el);
      if (!target) return;
      void (async () => {
        try {
          const res = await openInEditor(
            jobSlug,
            target.path,
            target.line,
            target.col,
            job?.archived,
          );
          if (res?.note) showToast(res.note);
        } catch (e) {
          showToast(e instanceof Error ? e.message : String(e));
        }
      })();
    },
    [job?.archived, job?.artifacts, jobSlug, label, key, onOpenArtifact],
  );

  /**
   * Dress artifact refs in the two wired bodies after each real change — the
   * renderer is pure, so membership in this job's artifact list is decided
   * here, exactly as the artifact viewer does it. Both containers are looked
   * up by their pinned ids (this modal is singular), and the poll-path guard
   * is the same fingerprint trick: `job.artifacts` is a fresh array every 3s.
   */
  const lastSweep = useRef<{
    detail: { __html: string } | null;
    spec: { __html: string } | null;
    key: string;
  }>({ detail: null, spec: null, key: "" });
  useEffect(() => {
    const artifacts = job?.artifacts ?? [];
    const akey = artifacts.map((a) => a.url).join("\n");
    const cur = lastSweep.current;
    if (cur.detail === detailHTML && cur.spec === specHTML && cur.key === akey)
      return;
    let found = false;
    for (const id of ["steer-detail", "steer-spec-body"]) {
      const host = document.getElementById(id);
      if (host) {
        decorateArtifactRefs(host, artifacts);
        found = true;
      }
    }
    // Stamp only when a host was actually swept: both divs are hidden-toggled
    // today so this always fires, but a future conversion to the conditional
    // rendering their sibling panels use must not poison the guard with a
    // sweep that dressed nothing.
    if (found) lastSweep.current = { detail: detailHTML, spec: specHTML, key: akey };
  }, [detailHTML, specHTML, job?.artifacts]);

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
        <RenameButton id="steer-rename" state={rename} visible={showRename} />
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
        {/* above the context block, directly under the head button that opens
            it — the same placement the editor gives it, and the same reason:
            it is about the job the title names, not about the cell's state */}
        <RenameRow id="steer-rename-row" state={rename} />
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
            onClick={onCodeRefClick}
            // renderMD escapes before transforming, so agent text is inert
            dangerouslySetInnerHTML={detailHTML}
          />
          <div className="last-sent" id="steer-lastsent" hidden={!lastSent}>
            {lastSent ? `me→zeus: ${lastSent}` : ""}
          </div>
          {/* Conditional rather than a `hidden={…}` wrapper like its siblings
              above, and deliberately so: "no estimate" is the absence of the
              element, not an element with nothing in it. A hidden wrapper keeps
              its children in the DOM, so a job with no load would still answer
              a [data-testid="load-drill"] query — the shared contract says it
              must not. Placed under the detail/last-sent pair because that is
              the sequence a reader is deciding on: what zeus says it is stuck
              on, what was last said to it, and what unsticking it costs. */}
          {load && loadDerived ? (
            <LoadDrill load={load} roi={loadDerived.roi} band={loadDerived.band} />
          ) : null}
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
            onClick={onCodeRefClick}
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
 * The load estimate with its rungs NAMED: a `roi 4.0×` headline over one row
 * per half — `unblock 2/10` beside "Read one page, pick", `next step 8/10`
 * beside "Full phase-2 of a substantial spec" — then the note.
 *
 * The labels come from the two anchor ladders in lib/format.ts, which are the
 * board's copy of `.claude/skills/load/SKILL.md` and are pinned byte-identical
 * against it and against the iOS app by a Go test. Rendering the label rather
 * than restating the ladder here is what keeps that pin meaningful: a fourth
 * spelling of "what does 4 mean" is a fourth thing to drift.
 *
 * Both raw numbers stay on screen beside their labels. The label alone would be
 * the friendlier read, but the number is what zeus wrote, what `kraken load
 * set` takes and what the card's badge shows — dropping it would leave no
 * surface where the two representations can be checked against each other.
 *
 * The AGE line is the same clause loadBadgeTitle carries and for the same
 * reason: nothing ever refreshes or clears an estimate, so a job that blocked,
 * was answered, and was filed back under blocked hours later still shows the
 * estimate written for the FIRST block. Without the age there is nothing here
 * that separates that from a fresh one. It is dropped rather than rendered as
 * "estimated  ago" when the stamp is absent, which a present `load` never is
 * (WebJobLoad) — the same defensive-only category as loadROI's zero arms.
 */
function LoadDrill({
  load,
  roi,
  band,
}: {
  load: WebJobLoad;
  roi: number;
  band: LoadBand;
}) {
  // The wire promises 1..10 whenever the object is present, and loadROI has
  // already refused anything at or below 0 — the fallback is for the number
  // that is neither: an out-of-range rung from some later server would
  // otherwise print the literal "undefined" as that rung's MEANING, which is a
  // worse answer than the bare number with no label beside it.
  const unblockAnchor = UNBLOCK_ANCHORS[load.unblock - 1] ?? "";
  const scopeAnchor = SCOPE_ANCHORS[load.scope - 1] ?? "";
  // `at` is unix MILLIS and age() takes SECONDS — the same divide, and the same
  // hazard, that loadBadgeTitle's header spells out at length.
  const when = age(load.at ? Math.floor(load.at / 1000) : 0);
  // What the two numbers MEAN, said once rather than as two more visible lines:
  // the panel is read while deciding whether to answer this job next, and the
  // definitions are the part you need exactly once.
  //
  // It rides on BOTH `title` and `aria-label`, which are not two spellings of
  // one thing here: `title` is the pointer's tooltip and reaches nothing else —
  // touch has no hover at all, and screen readers announce a title
  // inconsistently and only as a fallback when no better name exists. So the
  // sentence is also the element's accessible NAME.
  //
  // `role="group"` is what makes that name exist. A bare <div> is role=generic,
  // which PROHIBITS naming — an aria-label on one is dropped, silently, which
  // is the same shape of failure as a title on a touch device. group is also
  // the right role rather than the cheapest one: it names a container without
  // replacing it, so the rows below are still read individually. Putting the
  // label on a leaf element instead would have SWALLOWED them — the numbers and
  // their rungs are the payload, and an accessible name that hides them is
  // worse than none. Contrast the card's badge (LoadChip), where aria-label
  // deliberately does replace the visible text, because there the visible text
  // is `⚡ 4.0× 2→8` and has no spoken form worth hearing.
  const summary =
    "zeus' triage estimate for this block. unblock is what answering costs CARL — reading, context, decisions. " +
    "next step is how big the claude work that answer buys is likely to be. roi is the second over the first, " +
    "so a cheap answer buying a long session ranks above an expensive one buying a message.";
  return (
    <div
      className={"ld-drill " + band}
      data-testid="load-drill"
      role="group"
      aria-label={summary}
      title={summary}
    >
      <div className="ld-drill-head">
        <span className="ld-drill-roi">roi {loadRatioText(roi)}</span>
        {when ? <span className="ld-drill-age">estimated {when} ago</span> : null}
      </div>
      <div className="ld-drill-row">
        <span className="ld-drill-k">unblock {load.unblock}/10</span>
        <span className="ld-drill-v">{unblockAnchor}</span>
      </div>
      <div className="ld-drill-row">
        <span className="ld-drill-k">next step {load.scope}/10</span>
        <span className="ld-drill-v">{scopeAnchor}</span>
      </div>
      {/* dropped entirely when empty rather than left as a blank line: `note`
          may legitimately be "", exactly as loadBadgeTitle's trailing clause is
          omitted rather than rendered as a dangling " — ". */}
      {load.note ? <div className="ld-drill-note">{load.note}</div> : null}
    </div>
  );
}

/**
 * The job-colour picker: the SwatchStrip dropdown — "no colour" plus /color's
 * own eight, in /color's own order. Generated from JOB_COLORS rather than
 * hand-written, so this picker and the editor's cannot drift in palette,
 * order or markup. "No colour" is a real row (value "") rather than an
 * absence: an unrecognised colour from the wire falls back to it instead of
 * leaving the picker with nothing selected and no way back to neutral.
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
        // Vestigial since the strip became a dropdown (see SwatchStripProps.name);
        // kept because the prop is frozen in the component's signature.
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
  // The `prereqs` row stood here — a "wide" row spanning both meta columns,
  // since a prereq list runs long. The field is off the wire: prereqs gated
  // the QUEUE, and a job reaching this modal is a live cell that was started
  // by hand. The `wide` modifier is still honoured below and still styled, so
  // the next long row costs nothing to add.
  return `<div class="spec-meta">${rows
    .map(
      ([k, v, w]) =>
        `<div class="spec-meta-row${w ? " " + w : ""}"><span class="k">${k}</span><span class="v">${v}</span></div>`,
    )
    .join("")}</div>`;
}
