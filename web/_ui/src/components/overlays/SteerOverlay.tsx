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
import type {
  JobForm,
  WebJob,
  WebJobAccount,
  WebJobLoad,
  WebUsage,
  WebUsageAccount,
} from "../../api/types.ts";
import {
  JOB_COLORS,
  SCOPE_ANCHORS,
  UNBLOCK_ANCHORS,
  age,
  loadRatioText,
  loadROI,
  usageColorClass,
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
import type { MMItem } from "../../lib/mm.ts";
import { esc, renderMD } from "../../lib/markdown.ts";
import { jobKey } from "../../lib/jobKey.ts";
import type { SteerMode } from "../../lib/moves.ts";
import { GoalMultiPicker } from "./GoalMultiPicker.tsx";
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
  /**
   * The chrome's 60s `/api/usage` poll, threaded in so the account section can
   * put live session/Fable bars beside the slot the job is bound to. Undefined
   * until the first poll lands, and permanently undefined on an older server
   * (404) or with the daemon down.
   *
   * OPTIONAL, unlike the two required action props below, and the difference is
   * what the absence MEANS. An unfilled action slot is a broken feature that
   * looks like a job with nothing in it — that is the hazard those props were
   * made required for. Absent usage is a real, expected state the section
   * already renders for: no rows, no bars, and never a fabricated percentage.
   * The section's own answer — which account this cell is bound to — comes from
   * `job` and does not depend on this at all.
   */
  usage?: WebUsage;
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

  // ---- mental model --------------------------------------------------------
  // App's one mm poll and the two writes this modal can make against it. All
  // three OPTIONAL, unlike the two actions above, and for the reason
  // DocQuestions' `onAskBranched` is: an unwired integrator loses a button
  // rather than gaining a dead one, because each affordance is hidden outright
  // without its callback. The list itself is the only state any of them
  // renders from — there is no local latch anywhere in this family.
  /** The mental model as the server currently reports it. */
  mm?: MMItem[];
  /** Freeze what the cell last said about itself (Q1: the detail IS markable). */
  onMarkDetail?: (slug: string, text: string) => Promise<void>;
  /** Mint one answered doc question as a decision. */
  onMarkDecision?: (slug: string, qid: string) => Promise<void>;
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
  usage,
  onClose,
  onRenamed,
  mutate,
  onOpenArtifact,
  onOpenThread,
  mm = [],
  onMarkDetail,
  onMarkDecision,
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
  // Which pool account this cell is spending on. Unlike every other term in
  // showContext this one is NOT a presence test on a wire key, and deliberately
  // so: an unbound cell is not a cell with no account, it is a cell on the
  // machine's MAIN login — the lane every cell ran in before per-cell binding —
  // and "main (machine default)" is a real answer this section owes, not a
  // blank. So the question applies to any steerable cell, bound or not.
  //
  // `reactivate` is the one mode it does not apply in, and that IS a fact about
  // the world rather than a rendering choice: reactivate targets a dead cell,
  // which is burning nothing at all, and the binder re-decides at the respawn —
  // so whatever the row still says would be the account the NEXT run may not
  // get. Answering it there would be the one thing this section must not do,
  // which is state a dead fact as a current one.
  const hasAccount = mode !== "reactivate";

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
    hasAccount ||
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

  // ---- the detail's read mark ---------------------------------------------
  //
  // What zeus says it is stuck on is the one piece of agent prose the board
  // shows outside an artifact, and Q1 answered that it IS markable — this is
  // often where the fact carl learns actually arrives.
  //
  // The text marked is the detail as RENDERED: `textContent` off the same
  // element the reader is looking at, so what is frozen is the sentence
  // without its markdown punctuation. Read in an effect rather than during
  // render for the obvious reason (the element does not exist on the first
  // pass) and one more: `textContent` is layout-independent, so it answers the
  // same string under jsdom as in a browser, which `innerText` would not.
  // Setting the same string is a no-op re-render, so this is free on the 3s
  // poll's identical HTML.
  const [detailText, setDetailText] = useState("");
  const [markingDetail, setMarkingDetail] = useState(false);
  useEffect(() => {
    const host = document.getElementById("steer-detail");
    setDetailText((host?.textContent || detail || "").trim());
  }, [detailHTML, detail]);
  // Server state, the ☆ save rule: a live agent-output item whose frozen
  // content IS this text. Compared by content rather than by a source_ref the
  // client would have to sha256 to reproduce — and content equality is the
  // same question that identity is asking.
  const detailMarked =
    !!detailText &&
    mm.some(
      (it) =>
        it.source === "agent-output" &&
        !it.invalidated_at &&
        it.content.trim() === detailText,
    );
  const markDetailNow = useCallback(async () => {
    if (!onMarkDetail || !detailText || markingDetail) return;
    setMarkingDetail(true);
    await onMarkDetail(jobSlug, detailText);
    setMarkingDetail(false);
  }, [onMarkDetail, detailText, markingDetail, jobSlug]);

  /**
   * The questions panel's decision mint, re-addressed from (qid) to
   * (slug, qid). The panel knows the question and this modal knows the job.
   */
  const markDecision = useCallback(
    async (qid: string) => {
      await onMarkDecision?.(jobSlug, qid);
    },
    [onMarkDecision, jobSlug],
  );

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="steer-title">
          {jobSlug + TITLE_SUFFIX[mode]}
        </span>
        {/* the job's goals — the same picker the editor's form carries
            (GoalMultiPicker), HERE because the board routes a live
            working/blocked card to THIS modal and never to the editor, so a
            picker that lived only in the editor was reachable exactly when
            filing a job under a goal matters least. /api/job/goal is ungated
            precisely so a RUNNING job can be linked, and like the colour strip
            below it rides no mode check — linking is valid in steer, unblock
            and reactivate alike — and never sends a steer message. Withheld
            for an archived job: its synthetic slug is not a job the route can
            answer for, and the wire withholds its goals anyway. The wrapper
            span exists only to carry the FULL goal text as a tooltip
            (the picker has no title prop) — the chip this replaced paid for
            its truncation the same way, and with several links it lists them
            all, which is the one place the count on the button can be spelled
            out. It reads `goals` ALONE, exactly as the picker inside it does
            and unlike the card's chips, which fall back to the legacy single
            `goal` (api/types.ts states which key means what): a tooltip is
            what the button abbreviates, so a tooltip naming a goal the button
            beneath it reads as "—" would be the one control contradicting
            itself. cut=20: the head is ONE LINE with every control rigid and
            only the title giving (style.css says so at .panel-head), so the
            label's cap is what bounds the strip, and the editor form row's 28
            is too wide a rigid block here. */}
        {job?.archived ? null : (
          <span title={(job?.goals ?? []).map((g) => g.text).join(" · ") || undefined}>
            <GoalMultiPicker
              id="steer-goal"
              slug={jobSlug}
              goals={job?.goals}
              mutate={mutate}
              cut={20}
            />
          </span>
        )}
        {/* The same nine-swatch colour strip as the job editor's. It has to be
            HERE and not only in the editor, for the goal picker's exact
            reason — and /api/job/color is ungated precisely so a RUNNING job
            can be recoloured. A pick POSTs the colour and never sends a
            steer message. */}
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
          {/* Under the detail rather than in the head: it is an action ON that
              paragraph, and the head's controls are all about the JOB. Hidden
              without a callback (see the props block) and without text to
              freeze — a detail that renders to nothing is nothing to know. */}
          {detail && onMarkDetail && detailText ? (
            <div className="steer-detail-mark">
              <button
                className={"mm-markbtn" + (detailMarked ? " on" : "")}
                id="steer-detail-mark"
                aria-pressed={detailMarked}
                disabled={markingDetail || detailMarked}
                title={
                  detailMarked
                    ? "already in your mental model — drop it from the memory view"
                    : "keep what the cell just said as something you know"
                }
                onClick={() => void markDetailNow()}
              >
                {detailMarked ? "✓ known" : "○ mark known"}
              </button>
            </div>
          ) : null}
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
            {job && hasDocq ? (
              <DocQuestions
                job={job}
                mutate={mutate}
                // the same door ThreadChips opens threads through, so the
                // thread overlay stacks above this modal exactly as a chip
                // click does — no close/reopen choreography needed here
                onAskBranched={onOpenThread}
                mm={mm}
                // undefined without the integrator's callback, which is what
                // hides the → decision button rather than offering a press
                // that can only fail
                onMarkDecision={onMarkDecision ? markDecision : undefined}
              />
            ) : null}
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
          {/* which pool account this cell is burning, and how much of that
              account is left. Ambient and READ-ONLY — there is no switch
              control here and there is deliberately not going to be one: a
              running claude does not re-read a swapped credential, so a button
              that "moved" a live cell would change a row and nothing else.
              Directly above the tokens panel because the two are the same kind
              of fact read in the same breath — whose quota, then how much of it
              this job has spent. */}
          <div className="steer-account" id="steer-account" hidden={!hasAccount}>
            {hasAccount ? <AccountPanel job={job} usage={usage} /> : null}
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
 * One percentage off a `/api/usage` account row, or null.
 *
 * `WebUsageAccount` is `[k: string]: unknown` in types.ts (it mirrors
 * core.AccountUsage, which grows fields), so every window is narrowed by a
 * READER rather than by a cast — UsageOverlay's own rule, and the reason a
 * renamed server-side field degrades to a missing bar here instead of a crash.
 * The one field this panel needs is `percent`, so this is deliberately narrower
 * than UsageOverlay's `readWindow`: no `resets_at`, because a countdown beside
 * a job's account would be a second, quieter copy of the usage drilldown's own.
 */
function readPercent(v: unknown): number | null {
  if (!v || typeof v !== "object") return null;
  const p = (v as Record<string, unknown>).percent;
  return typeof p === "number" ? p : null;
}

/**
 * The Fable window's percentage off an account row's `models` array, or null.
 *
 * Matched case-INSENSITIVELY on the name containing "fable" rather than on an
 * exact id: the per-model window is named by whatever `/usage` reports, and
 * `core.ZeusModel` is an exact model id that moves when the fast tier moves.
 * Absent — an account that has spent nothing on the tier, or a server that
 * reports no per-model windows — renders no bar at all, never a 0%.
 */
function readFablePercent(v: unknown): number | null {
  if (!Array.isArray(v)) return null;
  for (const m of v) {
    if (!m || typeof m !== "object") continue;
    const name = (m as Record<string, unknown>).name;
    if (typeof name !== "string" || !name.toLowerCase().includes("fable")) continue;
    const p = readPercent(m);
    if (p !== null) return p;
  }
  return null;
}

/**
 * A window's `resets_at` (unix millis), or null. Read beside `readPercent`
 * rather than folded into it because the panel still draws no countdown: the
 * stamp is used only to tell two codex windows apart (see `codexBars`).
 */
function readResetsAt(v: unknown): number | null {
  if (!v || typeof v !== "object") return null;
  const r = (v as Record<string, unknown>).resets_at;
  return typeof r === "number" ? r : null;
}

interface AccountBarSpec {
  label: string;
  percent: number;
}

/** The claude pool's bar set: the 5h session window and the Fable model window. */
function claudeBars(row: WebUsageAccount | undefined): AccountBarSpec[] {
  const out: AccountBarSpec[] = [];
  const session = readPercent(row?.session);
  if (session !== null) out.push({ label: "session 5h", percent: session });
  const fable = readFablePercent(row?.models);
  if (fable !== null) out.push({ label: "fable 7d", percent: fable });
  return out;
}

/**
 * The codex pool's bar set: the weekly window, plus the session window only
 * when it is a genuinely different window.
 *
 * `core.CodexSlotUsage` copies a weekly-only reading into `session` as well, so
 * a build that reports one 7-day window arrives here with two identical
 * windows. Printing both would show the same number twice under two labels,
 * which reads as two independent quotas. So the session bar is drawn only when
 * something about it differs from weekly — its percent or its reset stamp —
 * which is exactly when the slot really did report a shorter window.
 *
 * A row that has never been MEASURED draws nothing at all. `fetched_at` is that
 * signal: a slot kraken can bind but codex has never opened a session in has
 * usable creds and no rollout to read limits from, so core reports it with a
 * `session` of 0% and no stamp. That 0 is an absence of evidence, not a
 * measurement, and a `session 0%` bar under a live cell is a fabricated number
 * — the one thing this panel's own header promises never to show. The accounts
 * drilldown says the same thing in words, with its `no usage yet` chip; both
 * surfaces read the same field so neither can start claiming a reading the
 * other withholds.
 *
 * Exported because it is the ONE rule for which windows a codex row shows, and
 * it has two consumers: this panel and the accounts drilldown's codex section.
 * The drilldown keeps its own `Bar` component and its own countdown — what it
 * takes from here is only the SELECTION, so the two surfaces can never disagree
 * about whether a lone weekly window gets drawn twice.
 */
export function codexBars(
  row: WebUsageAccount | undefined,
): { label: string; percent: number }[] {
  const out: { label: string; percent: number }[] = [];
  const fetched = row?.fetched_at;
  if (typeof fetched !== "number" || fetched <= 0) return out;
  const weekly = readPercent(row?.weekly);
  if (weekly !== null) out.push({ label: "weekly", percent: weekly });
  const session = readPercent(row?.session);
  if (session === null) return out;
  const same =
    weekly !== null &&
    session === weekly &&
    readResetsAt(row?.session) === readResetsAt(row?.weekly);
  if (!same) out.push({ label: "session", percent: session });
  return out;
}

/**
 * One usage bar. The MARKUP SHAPE is UsageOverlay's `Bar` — same three-part
 * `.u-row`, same `usageColorClass` fill — rather than an import of it, and the
 * split is deliberate: that component owns the drilldown's row, with its
 * `resets_at` countdown and its own label column, and this panel wants the bar
 * without the countdown. Sharing the CLASSES is what keeps the two looking like
 * one thing; sharing the component would have meant a prop to suppress half of
 * it. `usageColorClass` itself IS shared, because a threshold rendered in two
 * places is exactly the thing that must not be written twice (its own header in
 * lib/format.ts says so).
 */
function AccountBar({ label, percent }: { label: string; percent: number }) {
  const pct = Math.max(0, Math.min(100, percent));
  return (
    <div className="u-row">
      <span className="u-row-label">{label}</span>
      <span className="u-bar">
        <span
          className={`u-bar-fill ${usageColorClass(percent)}`}
          style={{ width: `${pct}%` }}
        ></span>
      </span>
      <span className="u-row-val">{Math.round(percent)}%</span>
    </div>
  );
}

/**
 * The usage row a job's binding points at — the ONE join between `/api/board`'s
 * `webJob.account` and `/api/usage`'s two account arrays.
 *
 * Joined on AGENT plus slug rather than on slug alone, and that is a
 * correctness fix rather than a tidy-up: a pool slug is a slugified email, so
 * one person's account can name a slot in `~/.claude-pool` AND one in
 * `~/.codex-pool`. Matched by slug alone, a codex cell bound to a codex slot
 * would draw the CLAUDE slot's bars — a plausible-looking percentage belonging
 * to a quota the cell is not spending. Each agent therefore reads its own pool
 * and never falls back to the other one: no row is the honest answer, a row
 * from the wrong pool is not.
 *
 * Exported because the bar-picking rule is the thing worth pinning, and the
 * panel that uses it is not reachable on its own.
 */
export function pickUsageRow(
  usage: WebUsage | undefined,
  job: Pick<WebJob, "agent" | "account">,
): WebUsageAccount | undefined {
  const slug = job.account?.slug;
  if (!usage || !slug) return undefined;
  const rows = job.agent === "codex" ? usage.codex_accounts : usage.accounts;
  return (rows ?? []).find((a) => a.slug === slug);
}

/**
 * Which pool account this cell is spending on, in the steer modal's context
 * block — the drilldown half of the card's `.ac-chip`.
 *
 * Two states, and the second is not an error:
 *   - BOUND — slot slug, the account's email, and how long ago the binding was
 *     stamped. The age is the part that makes a rebind legible: the limit
 *     rescue moves one cell mid-run, so a fresh stamp on an old job is the
 *     whole story of what just happened to it.
 *   - UNBOUND — "main (machine default)" for a claude cell, "~/.codex (machine
 *     login)" for a codex one. A cell with no binding is not a cell with no
 *     account; it is the lane every cell ran in before per-cell binding
 *     existed, which happens when the pool was dry or fully excluded at spawn
 *     and whenever rotation is pinned manual. Saying so is the answer; a blank
 *     section would read as the feature being broken. The two wordings differ
 *     because the two lanes are different things: claude's machine default is a
 *     pool slot promoted to main, codex's is the single `~/.codex` login, and
 *     there is no codex equivalent of "main" to name.
 *
 * The bars come from the chrome's 60s `/api/usage` poll, joined to the binding
 * by `pickUsageRow` — agent plus slug. A slot the poll does not report (an
 * older server, the daemon down, a slot added since the last fetch) renders NO
 * bars rather than zeros: this panel never fabricates a percentage, which is
 * the same rule TokensPanel applies to an unmeasured job. The poll's lag is
 * stated in the note rather than hidden, because a number that can be a minute
 * old and does not say so is one a reader will act on as current.
 *
 * The bar SET is per-agent, because the two pools measure different things. A
 * claude row carries a 5h session window and the per-model Fable window the
 * cell's lead actually spends on. A codex row carries a weekly window and,
 * only when the slot reports a genuinely shorter one, a session window — the
 * codex build measured for this feature reports a single 7-day window, which
 * `core.CodexSlotUsage` copies into both fields, so drawing both unconditionally
 * would print one number twice under two labels. There is no Fable bar on a
 * codex row at all: Fable is not a codex model.
 *
 * Read-only throughout, with no switch control, and that is a fact about the
 * world rather than a scope cut: a RUNNING agent resolves its credential at
 * session start and does not re-read a swapped one, so a control that "moved" a
 * live cell would change a database row and nothing about what the cell bills.
 */
function AccountPanel({
  job,
  usage,
}: {
  job: WebJob | undefined;
  usage: WebUsage | undefined;
}) {
  const codex = job?.agent === "codex";
  const account: WebJobAccount | undefined = job?.account;
  if (!account) {
    return (
      <div className="ac-panel" data-testid="account-panel">
        <div className="ac-row">
          <span className="ac-k">bound to</span>
          {codex ? (
            <span className="ac-v">
              ~/.codex <span className="ac-dim">(machine login)</span>
            </span>
          ) : (
            <span className="ac-v">
              main <span className="ac-dim">(machine default — no per-cell binding)</span>
            </span>
          )}
        </div>
      </div>
    );
  }
  const row = pickUsageRow(usage, job ?? {});
  // `at` is unix MILLIS and age() takes SECONDS — the same divide LoadDrill and
  // TokensPanel each spell out. A zero stamp drops the row rather than
  // rendering "0s ago", which would date the binding to right now.
  const when = age(account.at ? Math.floor(account.at / 1000) : 0);
  const bars = codex ? codexBars(row) : claudeBars(row);
  return (
    <div className="ac-panel" data-testid="account-panel">
      <div className="ac-row">
        <span className="ac-k">bound to</span>
        <span className="ac-v">
          {account.slug}
          {account.email ? <span className="ac-dim"> · {account.email}</span> : null}
          {codex ? <span className="ac-dim"> · codex pool</span> : null}
        </span>
      </div>
      {when ? (
        <div className="ac-row">
          <span className="ac-k">since</span>
          <span className="ac-v">{when} ago</span>
        </div>
      ) : null}
      {bars.length ? (
        <div className="u-rows">
          {bars.map((b) => (
            <AccountBar key={b.label} label={b.label} percent={b.percent} />
          ))}
        </div>
      ) : null}
      {/* The provenance line, and it is only ever said where a NUMBER was
          shown: with no bars there is nothing here that can be stale, and a
          note about a poll that contributed nothing would read as an
          explanation for the missing bars rather than as a caveat on present
          ones. */}
      {bars.length ? (
        <div className="ac-note">
          {codex
            ? "usage from the slot's newest codex rollout — updates when the cell's next turn lands"
            : "usage from the 60s /api/usage poll — may lag a minute"}
        </div>
      ) : null}
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
    // the fallback is the agent-matched default: a codex cell with no model:
    // runs on kraken's pinned codex default, not the claude one
    ["model", esc(d.model || (d.agent === "codex" ? "gpt-6-astra" : "claude-fable-5-1"))],
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
