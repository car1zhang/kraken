import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type MutableRefObject,
} from "react";
import {
  archiveJob,
  createJob,
  deleteFeedback,
  editFeedback,
  fetchJobForm,
  queueJob,
  saveJobForm,
  setJobColor,
  uploadImage,
} from "../../api/client.ts";
import { FeedbackPanel } from "../board/chips/FeedbackPanel.tsx";
import {
  type JobForm,
  type WebArtifact,
  type WebJob,
} from "../../api/types.ts";
import { insertIntoTextarea } from "../../hooks/useCmdHighlight.ts";
import { useDebouncedCallback } from "../../hooks/useDebouncedDraft.ts";
import { FieldSelect } from "./FieldSelect.tsx";
import { GoalMultiPicker } from "./GoalMultiPicker.tsx";
import { SwatchStrip } from "./SwatchStrip.tsx";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { jobKey } from "../../lib/jobKey.ts";
import {
  RenameButton,
  RenameRow,
  renameControlVisible,
  useRenameControl,
  type RenameControlState,
} from "./RenameControl.tsx";
import { RemoteEditorBody, VisibilityControl } from "./RemoteSteer.tsx";
// useRenameControl is called by EditorOverlay, above the keyed body — see the
// comment on the hoist there.
import { CmdComposer } from "./CmdComposer.tsx";
import { DocQuestions } from "./DocQuestions.tsx";
import {
  AttChips,
  Overlay,
  panelDropHandlers,
  pastedFiles,
  pickImageFiles,
  usePasteLayer,
  type AttChip,
  type FileSink,
  type Mutate,
} from "./Overlay.tsx";

// The job editor (app.js:3124 openEditor). THREE modes:
//
//   slug == null            the new-job row: a slug field and "create"
//   slug, editable          the form — repo/base/model/solo/spec
//   slug, !editable         the same form, every field locked: a cell is in
//                           flight and the file belongs to the crew
//
// Autosave, not a save button: any change inside the form debounces 600ms into
// `POST /api/job/saveform` with **mtime: 0**, which deliberately opts OUT of the
// optimistic lock. That is not an oversight — the editor is the only writer of
// the file while it is open, and a conflict dialog mid-typing is worse than
// last-writer-wins on a single-user board.
//
// `ready` and `prereqs` are gone from the form in both directions. Prereqs
// were the queue's gating mechanism and the queue is gone; `ready` was the
// flag the old queue button set, and the start button below starts the cell
// outright instead of marking anything.
//
// Nothing on the server would catch them coming back: the save route decodes
// with a plain json.Decode and no DisallowUnknownFields, so a re-added field
// is silently dropped and answered 200 — it would look saved and read back
// empty. (The by-name refusal is on `/api/job/draft`, a different route.) The
// payload built below is the whole guard, which is why it is assembled
// key-by-key rather than spread from state.

export interface EditorOverlayProps {
  open: boolean;
  /** null opens the new-job row. */
  slug: string | null;
  /**
   * The job's freshest board entry, by jobKey(). Undefined is MEANINGFUL and
   * not an error: a job created seconds ago is not on the board yet, and the
   * arming logic treats that as a fresh draft rather than as unarmable.
   */
  job: WebJob | undefined;
  onClose: () => void;
  /** A `create` succeeded — the caller re-points `slug` at the new job. */
  onCreated: (slug: string) => void;
  /**
   * A `rename` succeeded — the caller re-points `slug` at the SAME job under
   * its new name, exactly as `onCreated` re-points at a new one.
   *
   * Optional, and the control is HIDDEN without it rather than rendering an
   * unwired button — see renameControlVisible, which owns that argument.
   */
  onRenamed?: (slug: string) => void;
  /**
   * The ↺ button. Reactivate is a COMPOSER flow, not a one-shot post, so it
   * hands off to the steer modal in reactivate mode (optional message folded
   * into the resume prompt) exactly as the card's own ↺ does.
   */
  onReactivate: (slug: string) => void;
  /**
   * Open one of the job's artifacts in the artifact viewer.
   *
   * The third argument is the job's jobKey() and it is not decorative: the
   * viewer's pager rebuilds the job's ORDERED list from it, so a chip opened
   * without one degrades to a singleton with the pager hidden.
   *
   * Optional only so the board can be wired incrementally — absent, the list
   * still renders (it is the settled job's inventory either way) and a click
   * does nothing.
   */
  onOpenArtifact: (url: string, name: string, jobKey: string) => void;
  /** See Overlay.tsx. */
  mutate: Mutate;
  /**
   * The OWNER node of a remote card. Present → the editor's remote mode
   * (raw text through /api/nodes/job/raw, saved via the proxy's `save`
   * action) instead of the local form; see RemoteEditorBody.
   */
  node?: string;
}

export interface ArtifactListProps {
  artifacts: WebArtifact[] | undefined;
  /**
   * The job's jobKey() — NEVER a bare slug, or an archived job and an active
   * one that reused its freed slug would open each other's artifacts.
   */
  jobKey: string;
  /** Prefixes each chip's name; differs from the slug for a renamed archive. */
  label: string;
  onOpen?: (url: string, name: string, jobKey: string) => void;
}

/**
 * `artifactListHTML` (app.js:659) — EVERY one of a job's artifacts as an
 * openable chip, with no cap and no "+N more" toggle.
 *
 * This is the DRILLDOWN renderer, deliberately different from the card's:
 * since the card reorg the card carries a bare COUNT, and a modal scrolls, so
 * it can just show the list where the card had to bound it.
 */
export function ArtifactList({
  artifacts,
  jobKey,
  label,
  onOpen,
}: ArtifactListProps) {
  return (
    <>
      {(artifacts ?? []).map((a) => {
        const name = `${label}/${a.name}`;
        return (
          <button
            key={a.url}
            className="chip"
            data-url={a.url}
            data-key={jobKey}
            data-name={name}
            onClick={() => onOpen?.(a.url, name, jobKey)}
          >
            ▤ {a.name}
          </button>
        );
      })}
    </>
  );
}

export function EditorOverlay(props: EditorOverlayProps) {
  // published by the body so a Files drop anywhere on the panel reaches it, and
  // left unset while the form is read-only so a locked job refuses the drop
  const sink = useRef<((files: FileList | File[]) => void) | null>(null);
  /**
   * Published by the body the same way, and read only by the rename control:
   * cancel the pending 600ms autosave and land it NOW.
   *
   * It has to cross this boundary because the two halves sit on opposite sides
   * of it — the debounce belongs to the body, and the rename control has to
   * outlive the body (see below) — and it is a ref rather than a prop for the
   * same reason `fileSink` is: the direction is upward.
   */
  const flushAutosave = useRef<(() => Promise<void>) | null>(null);
  const beforeRename = useCallback(async () => {
    await flushAutosave.current?.();
  }, []);
  /**
   * The rename control lives HERE, above the keyed body, and that placement is
   * load-bearing rather than tidy: a successful rename re-points `slug`, which
   * remounts `EditorBody` in the same commit the response lands in. State held
   * inside the body would be destroyed before it could be rendered — and one
   * piece of it, the `skipped` notice, is the only record anywhere that a
   * preserved artifact copy was left behind under the old name.
   */
  const rename = useRenameControl(props.slug, props.onRenamed, {
    // `open` is what resets the control between jobs: this component is never
    // unmounted, so a closed editor must not carry a half-typed rename or a
    // read notice into the next job it is opened for.
    open: props.open,
    beforeSubmit: beforeRename,
  });
  return (
    <Overlay
      id="editor-overlay"
      open={props.open}
      priority={ESC_PRIORITY.editor}
      onClose={props.onClose}
      {...panelDropHandlers(sink)}
    >
      {/* keyed on slug: reopening for another job is a fresh mount, which is
          openEditor's wholesale reset expressed once */}
      {props.node && props.slug ? (
        // a remote PUBLIC card's job file, raw, through the fleet proxy
        <RemoteEditorBody
          key={props.node + "/" + props.slug}
          node={props.node}
          slug={props.slug}
          job={props.job}
          mutate={props.mutate}
          onClose={props.onClose}
        />
      ) : (
        <EditorBody
          key={props.slug ?? "__new__"}
          {...props}
          fileSink={sink}
          flushAutosave={flushAutosave}
          rename={rename}
        />
      )}
    </Overlay>
  );
}

function EditorBody({
  slug,
  job,
  onClose,
  onCreated,
  onRenamed,
  onReactivate,
  onOpenArtifact,
  mutate,
  fileSink,
  flushAutosave,
  rename,
}: EditorOverlayProps & {
  fileSink: FileSink;
  flushAutosave: MutableRefObject<(() => Promise<void>) | null>;
  rename: RenameControlState;
}) {
  const isNew = !slug;
  const [newSlug, setNewSlug] = useState("");
  const [note, setNote] = useState("");
  // three tones, matching the vanilla editorNote.style.color writes:
  // "dim" while a save is in flight, "ok" for a clean sync, "" for anything
  // that needs to read as a problem (a warn, a failure, a refusal).
  const [noteTone, setNoteTone] = useState<"" | "dim" | "ok">("");
  const [form, setForm] = useState<JobForm | null>(null);
  const [repo, setRepo] = useState("");
  const [base, setBase] = useState("");
  const [model, setModel] = useState("");
  const [agent, setAgent] = useState("");
  const [solo, setSolo] = useState(false);
  const [spec, setSpec] = useState("");
  const [chips, setChips] = useState<AttChip[]>([]);
  const [pending, setPending] = useState(0);
  const [busy, setBusy] = useState(false);
  // true when the freshest save/load says the current spec won't run (loadForm's
  // d.err, or a save's d.warn) — one of the submit button's arming gates
  const [syncWarn, setSyncWarn] = useState(false);

  const specRef = useRef<HTMLTextAreaElement | null>(null);
  const attInput = useRef<HTMLInputElement | null>(null);
  /**
   * The ARCHIVED branch (app.js:3815). An archived job's key is jobKey's
   * synthetic `archive:<slug>` — there is no such job file — so there is no
   * form to load: the panels below (artifacts, questions, feedback) ARE the
   * whole modal for it. Asking the server for one 404s and paints "no such
   * job" over a view that is working exactly as intended.
   *
   * `readOnly` is forced TRUE from here rather than being left to fall out of
   * `form`, and that ordering is the load-bearing part the vanilla comment
   * insists on. Three paths gate on this flag ALONE rather than on the form
   * being hidden — the panel's dragover, its drop, and the document paste
   * layer, all funnelling into addFiles — and with `form` null the
   * `!!form && !editable` rule below yields FALSE, leaving every one of them
   * armed. A ⌘V on this read-only modal would then upload against the
   * synthetic `archive:<slug>` key and paint the server's second error over
   * a modal that just said it was read-only.
   */
  const archived = !!job?.archived;
  const editable = !!form?.editable;
  const readOnly = archived || (!!form && !editable);

  // ---- load ---------------------------------------------------------------
  const load = useCallback(
    async (s: string, signal?: AbortSignal) => {
      const d = await fetchJobForm(s, signal);
      if (signal?.aborted) return;
      setForm(d);
      setRepo(d.repo || "");
      setBase(d.base || "");
      setModel(d.model || "");
      setAgent(d.agent || "");
      setSolo(!!d.solo);
      setSpec(d.spec || "");
      setSyncWarn(!!d.err);
      if (d.err) {
        setNoteTone("");
        setNote("current file won't run: " + d.err);
      }
    },
    [],
  );

  useEffect(() => {
    if (!slug || archived) return; // see the archived branch above
    const ac = new AbortController();
    load(slug, ac.signal).catch((e: unknown) => {
      if (ac.signal.aborted) return;
      setNoteTone("");
      setNote(e instanceof Error ? e.message : String(e));
    });
    return () => ac.abort();
  }, [archived, load, slug]);

  // ---- autosave -----------------------------------------------------------
  const save = useCallback(async () => {
    if (!slug || readOnly || !form) return;
    const body: Partial<JobForm> & { slug: string } = {
      slug,
      repo: repo.trim(),
      base: base.trim(),
      model: model.trim(),
      agent: agent.trim(),
      solo,
      spec,
      mtime: 0, // no optimistic lock — see the header comment
    };
    try {
      const d = await saveJobForm(body);
      setSyncWarn(!!d.warn);
      setNoteTone(d.warn ? "" : "ok");
      setNote(d.warn ? "synced — won't run: " + d.warn : "synced ✓");
    } catch (e) {
      setNoteTone("");
      setNote("sync failed: " + (e instanceof Error ? e.message : e));
    }
  }, [agent, base, form, model, readOnly, repo, slug, solo, spec]);

  const autosave = useDebouncedCallback(() => void save(), 600);
  const { call: scheduleSave, cancel: cancelSave } = autosave;

  // Published upward for the rename control, which posts from ABOVE this body
  // and would otherwise re-point the overlay while a debounced save is still
  // sitting in the window — the flush that fires on the resulting unmount
  // posts the old slug into a 404 and paints its failure into a DOM that is
  // already gone. Cancel first, exactly as doQueue does, so the pending timer
  // cannot fire a second identical save behind this one.
  flushAutosave.current = async () => {
    cancelSave();
    await save();
  };

  // Any change to any field is a change to the payload. Watching the values
  // replaces the vanilla delegated input/change listener on #editor-form, and
  // fixes the one gap that listener had: a programmatic write (the image
  // insert) needed its own explicit onEditorFormChange() call, which is exactly
  // the class of bug this port is meant to remove.
  const firstPass = useRef(true);
  useEffect(() => {
    if (!slug || readOnly || !form) return;
    if (firstPass.current) {
      firstPass.current = false; // the values just LOADED — not an edit
      return;
    }
    setNoteTone("dim");
    setNote("syncing…");
    scheduleSave();
  }, [repo, base, model, agent, solo, spec, slug, readOnly, form, scheduleSave]);

  // ---- checkStaleModals (app.js:567) --------------------------------------
  // Same rule as steer's, with one wrinkle: capture is LAZY. A freshly-created
  // job is not on the board yet when the form first loads for it, and the very
  // next poll is when the baseline can honestly be taken — misreading "not in
  // the payload yet" as a state change would close the editor on the job the
  // user just created.
  const openState = useRef<string | undefined>(undefined);
  const captured = useRef(false);
  useEffect(() => {
    if (isNew) return;
    if (!captured.current) {
      if (job) {
        openState.current = job.state;
        captured.current = true;
      }
      return;
    }
    if (!job || job.state !== openState.current) onClose();
  }, [isNew, job, onClose]);

  // ---- attachments --------------------------------------------------------
  // The reference is spliced into the spec TEXT immediately, and the chip is
  // only visual confirmation: removing the ✕ does NOT remove the text, because
  // that text is now user-editable prose in the spec. (Steer is the opposite —
  // there the chips ARE the message, composed at send time.)
  const addFiles = useCallback(
    (files: FileList | File[]) => {
      if (!slug || readOnly) return;
      const all = [...files];
      const imgs = pickImageFiles(all);
      if (all.length && !imgs.length) {
        setNoteTone("");
        setNote("images only (png/jpeg/gif/webp)");
        return;
      }
      for (const file of imgs) {
        setPending((n) => n + 1);
        uploadImage(slug, file)
          .then((c) => {
            setChips((prev) => [...prev, c]);
            insertIntoTextarea(
              specRef.current,
              "[image: " + c.path + "]\n",
              setSpec,
              { ownLineWhenUnfocused: true },
            );
          })
          .catch((e: unknown) => {
            setNoteTone("");
            setNote("upload failed: " + (e instanceof Error ? e.message : e));
          })
          .finally(() => setPending((n) => n - 1));
      }
    },
    [readOnly, slug],
  );

  // left null while read-only: the drop is refused rather than silently
  // uploading into a file the crew owns
  fileSink.current = readOnly ? null : addFiles;

  const onPaste = useCallback(
    (e: ClipboardEvent) => {
      if (readOnly) return;
      const files = pastedFiles(e);
      if (!files.length) return;
      e.preventDefault();
      addFiles(files);
    },
    [addFiles, readOnly],
  );
  // steer outranks the editor: if both were somehow open, a paste lands in one
  usePasteLayer(!readOnly, ESC_PRIORITY.editor, onPaste);

  // ---- the submit affordance (app.js:3031 updateEditorQueue) --------------
  // Derived, not snapshotted. The vanilla one-shot computation read a board
  // that could be missing a job created moments earlier, which forced a
  // close/reopen before it could be submitted.
  //
  // ONE button now, not two: the queued state it used to toggle between is
  // gone, and with it the ✕ cancel that returned a booking to draft. Submit
  // starts the cell, so there is nothing to cancel — only a request that has
  // either been answered or not.
  const formVisible = !isNew && !!form;
  // missing from the board means just-created — treat that as a fresh draft
  const state = job ? job.state : "draft";
  const live = job ? !!job.live : false;
  // a board-level err (an unhealthy cell), distinct from syncWarn's form-level
  // d.err / d.warn
  const boardErr = job ? !!job.err : false;
  const draftMode = formVisible && !readOnly && state === "draft" && !live;
  // a draft's submit button is always PRESENT — merely disabled until the spec
  // is non-empty and clean, so the affordance is discoverable from the moment
  // the modal opens
  const armed = draftMode && spec.trim() !== "" && !syncWarn && !boardErr;

  // the archived case is FIRST, which is this expression's whole equivalent of
  // vanilla calling setFormReadOnly BEFORE writing #editor-hint: both branches
  // are read-only, and the archived wording ("unarchive to edit" — there is no
  // cell) has to win over the in-flight one
  const hint = archived
    ? "read-only · unarchive to edit"
    : readOnly
      ? "read-only — cell in flight"
      : draftMode
        ? busy
          ? "starting cell…"
          : armed
            ? "synced ✓ — ready to start"
            : spec.trim() === ""
              ? "write a spec to start"
              : ""
        : "";

  const doSubmit = useCallback(async () => {
    if (!slug) return;
    if (pending > 0) {
      setNoteTone("");
      setNote("uploading image…");
      return;
    }
    setBusy(true);
    // land any edit still sitting in the debounce window BEFORE submitting —
    // cancel first so the pending timer cannot also fire and post twice
    cancelSave();
    await save();
    // This POST now cuts the worktree and spawns the cell before it answers,
    // so it holds for seconds. `busy` is what says so: it disables the button
    // (a second click would be a second cell) and swaps the hint to "starting
    // cell…". The note is written first for the same reason — the corner would
    // otherwise still read "synced ✓" throughout the wait.
    setNoteTone("dim");
    setNote("starting cell…");
    const res = await mutate(() => queueJob(slug));
    setBusy(false);
    if (res) onClose();
    else {
      // A refusal is a 409 whose body IS the reason, and mutate has already
      // parked that sentence in the status corner. The job stayed a draft, so
      // the modal stays open on it rather than closing on a move that did not
      // happen.
      setNoteTone("");
      setNote("start refused — see status bar");
    }
  }, [cancelSave, mutate, onClose, pending, save, slug]);

  const doArchive = useCallback(async () => {
    if (!slug) return;
    const res = await mutate(() => archiveJob(slug));
    if (res) onClose();
    else {
      setNoteTone("");
      setNote("archive failed — see status bar");
    }
  }, [mutate, onClose, slug]);

  const doCreate = useCallback(async () => {
    const s = newSlug.trim();
    if (!s) {
      setNoteTone("");
      setNote("give it a slug");
      return;
    }
    try {
      const d = await createJob(s);
      onCreated(d.slug);
    } catch (e) {
      setNoteTone("");
      setNote(e instanceof Error ? e.message : String(e));
    }
  }, [newSlug, onCreated]);

  // Identity, not a form field — so it is armed for an EXISTING non-archived
  // job whatever `readOnly` says. See renameControlVisible. The state itself
  // is the overlay's (it has to outlive this body's remount); only the
  // visibility rule, which needs `job` and `isNew`, is decided here.
  const showRename = renameControlVisible(job, isNew, onRenamed);

  const showReactivate =
    !!job && !job.archived && job.state === "done" && !!job.reactivatable;

  /**
   * The feedback panel's two writes, taken straight through `mutate` rather
   * than through props the integrator has to remember to pass.
   *
   * That is deliberate and it is the lesson of the pager: an optional callback
   * left unwired is a feature that renders and does nothing, with nothing
   * failing anywhere to say so. These need no state App owns — only the
   * board-refresh policy, which `mutate` already IS — so there is nothing for
   * a prop to carry but the risk of being forgotten. (`BoardActions`'
   * onSaveFeedback/onDeleteFeedback remain the card's path, if the card ever
   * takes the panel back.)
   *
   * FeedbackPanel wants `Error | null`, matching mutate's null-on-failure with
   * the sign flipped: mutate has already parked the server's own sentence in
   * the status corner, so the Error here only has to be non-null — the row
   * prints its own "save failed — see status bar" and never this text.
   */
  const onSaveFeedback = useCallback(
    async (s: string, id: number, note_: string) =>
      (await mutate(() => editFeedback(s, id, note_)))
        ? null
        : new Error("save failed"),
    [mutate],
  );
  const onDeleteFeedback = useCallback(
    (s: string, id: number) => void mutate(() => deleteFeedback(s, id)),
    [mutate],
  );

  /**
   * The colour pick (app.js:141 initSwatchStrip). Deliberately NOT the
   * debounced form autosave every other control in this form rides:
   * `saveform` refuses any job whose cell is live — which is exactly when
   * recolouring is useful — and a full-replace save from an older client could
   * wipe the colour by omitting the key. A discrete one-click choice has
   * nothing to debounce either.
   *
   * `pending` is what the strip shows while the POST is in flight, so the pick
   * lands instantly instead of waiting a round trip; clearing it hands
   * authority back to the board payload, which means a REFUSED pick snaps back
   * to whatever the server actually has. The strip can therefore never sit
   * showing a colour the server rejected.
   */
  const [pendingColor, setPendingColor] = useState<string | null>(null);
  const pickColor = useCallback(
    async (c: string) => {
      if (!slug) return;
      setPendingColor(c);
      await mutate(() => setJobColor(slug, c));
      setPendingColor(null);
    },
    [mutate, slug],
  );

  const repos = form?.repos || [];
  const hasDocq = !!job?.doc_questions?.length;
  const artifacts = job?.artifacts ?? [];
  const hasFeedback = !!job?.feedback?.length;
  // an archived job's `slug` prop is the synthetic key, so the title is built
  // from the job's own label — "old — archived", never "archive:old.job.md"
  const title = isNew
    ? "new job"
    : archived && job
      ? `${job.display_slug || job.slug} — archived`
      : slug + ".job.md";

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="editor-title">
          {title}
        </span>
        {/* beside the title, which is the job's identity surface — and BEFORE
            the hint, so "cell in flight — fields locked" reads as a statement
            about the form rather than about this button */}
        <RenameButton id="editor-rename" state={rename} visible={showRename} />
        <span className="stat dim" id="editor-hint">
          {hint}
        </span>
        <span className="spacer"></span>
        <button
          className="btn ghost"
          id="editor-reactivate"
          hidden={!showReactivate}
          title="resume this done job's original zeus session in a fresh cell"
          onClick={() => {
            if (slug) onReactivate(slug);
          }}
        >
          ↺ reactivate
        </button>
        {/* archive lives HERE, inside the opened job, on purpose: no
            card-level shortcut. The server still refuses a job with a cell in
            flight, and the button is hidden in read-only mode anyway. */}
        <button
          className="btn ghost"
          id="editor-archive"
          hidden={!formVisible || readOnly}
          title="move this job (and its preserved artifacts) into the archive — frees the slug; reversible from the ARCHIVED drawer under DONE"
          onClick={() => void doArchive()}
        >
          archive
        </button>
        <button
          className="btn"
          id="editor-submit"
          hidden={!draftMode}
          disabled={!armed || busy}
          // "start", not "submit", in every user-visible string on this
          // control: the id and the handler keep the code name, but what carl
          // reads has to be the one word the board uses for this action.
          title="start this job's cell now — cuts the worktree and spawns the cell (takes a few seconds); same move as dragging the card to WORKING"
          onClick={() => void doSubmit()}
        >
          {busy ? "starting…" : "start ⌘⏎"}
        </button>
      </div>
      {/* the drop target is the whole PANEL — see panelDropHandlers */}
      <div className="panel-body editor-body">
        <div className="editor-newrow" id="editor-newrow" hidden={!isNew}>
          <input
            id="editor-slug"
            placeholder="job-slug"
            spellCheck={false}
            autoFocus={isNew}
            value={newSlug}
            onChange={(e) => setNewSlug(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void doCreate();
              }
            }}
          />
          <button className="btn" id="editor-create" onClick={() => void doCreate()}>
            create
          </button>
        </div>

        {/* First in the body, directly under the head button that opens it —
            a rename is about the thing the title names, so it sits above the
            artifacts, questions and form rather than below them. */}
        <RenameRow id="editor-rename-row" state={rename} />

        {/* The job's artifacts. This modal is the ONLY way to reach a done or
            archived job's docs: the board card carries a bare count and a
            settled job never opens the steer modal, whose #steer-chips is the
            live cell's equivalent. Before the card gave up its chip list this
            container did not exist, and a click on a done card landed on a
            spec textarea with no path to the twelve artifacts the job had just
            shipped — 69 archived jobs' worth of stranded docs.

            Rendered once per open with no snapshot/restore, unlike the two
            panels below it: nothing re-runs it (there is no half-typed input
            and no scroll position inside it to survive a replacement), where
            the questions and feedback panels are re-rendered after every
            answer and every post. */}
        <div className="editor-chips" id="editor-chips" hidden={!artifacts.length}>
          {job && artifacts.length ? (
            <>
              {/* .editor-docq-head, the same class both sibling panels emit —
                  a shared class, not a shared rule, so the three labels cannot
                  drift apart */}
              <div className="editor-docq-head">artifacts</div>
              <div className="chips">
                <ArtifactList
                  artifacts={artifacts}
                  jobKey={jobKey(job)}
                  label={job.display_slug || job.slug}
                  onOpen={onOpenArtifact}
                />
              </div>
            </>
          ) : null}
        </div>

        {/* Questions ABOVE the form, not below it: on a done job they are what
            you clicked in for, and the spec textarea would push them past the
            fold. Rendered for ANY job that has them, not just done ones — this
            modal is also what a click on a draft or dead card opens. */}
        <div className="editor-docq" id="editor-docq" hidden={!hasDocq}>
          {job && hasDocq ? (
            <>
              <div className="editor-docq-head">questions</div>
              {/* no onAskBranched: this modal has no thread door (no
                  onOpenThread prop), and its usual subjects — draft, done,
                  dead cards — fail the affordance's job.live gate anyway.
                  The steer modal is where a live job's questions are asked
                  into. */}
              <DocQuestions job={job} mutate={mutate} />
            </>
          ) : null}
        </div>

        {/* the editor's copy of the feedback panel — the click-into surface for
            a done or archived job's notes, which is where most of them live
            (they are left on shipped artifacts, and a shipped artifact belongs
            to a settled job). Without it the board has no reader for a feedback
            row AT ALL: the card carries only a count chip whose title says to
            click in, and these rows are the dream loop's inbound channel from
            the board. Same split as the questions panel above. */}
        <div className="editor-feedback" id="editor-feedback" hidden={!hasFeedback}>
          {job && hasFeedback ? (
            <>
              <div className="editor-docq-head">feedback</div>
              <FeedbackPanel
                job={job}
                onSaveFeedback={onSaveFeedback}
                onDeleteFeedback={onDeleteFeedback}
              />
            </>
          ) : null}
        </div>

        <div
          className="editor-form"
          id="editor-form"
          hidden={!formVisible}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              if (armed && !busy) void doSubmit();
            }
          }}
        >
          <div className="f-row">
            {/* a DIV, not a <label> like its neighbours: FieldSelect's menu
                rows sit inside the wrapper, and a label's activation behaviour
                would forward a row's click to the button — the labelable
                descendant — reopening the menu the pick just closed. .f-field
                carries the column layout `.editor-form label` would have
                given it. */}
            <div className="f-field">
              repo{" "}
              <FieldSelect
                id="f-repo"
                label="repo"
                disabled={readOnly}
                value={repo}
                onPick={setRepo}
                // an existing job with an unusual repo still round-trips
                // instead of silently resetting to "—"
                options={
                  form?.repo && !repos.includes(form.repo)
                    ? [...repos, form.repo]
                    : repos
                }
              />
            </div>
            <label>
              base{" "}
              <input
                id="f-base"
                placeholder="origin/main"
                spellCheck={false}
                disabled={readOnly}
                value={base}
                onChange={(e) => setBase(e.target.value)}
              />
            </label>
            <label>
              model{" "}
              <input
                id="f-model"
                list="model-list"
                placeholder={agent === "codex" ? "gpt-6-astra" : "claude-fable-5-1"}
                spellCheck={false}
                title={
                  agent === "codex"
                    ? "codex model for zeus (-m value) — applied once at cell start; empty = kraken's default for a codex zeus, gpt-6-astra"
                    : "claude model for zeus (--model value) — applied once at cell start; empty = kraken's default for zeus, claude-fable-5-1 (the crew it spawns is opus regardless)"
                }
                disabled={readOnly}
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
            </label>
            {/* a DIV for the repo field's reason (FieldSelect menu rows live
                inside the wrapper; a label would forward a row's click back to
                the button). The empty value means claude — the wire's default
                spelling — so emptyLabel names it instead of the "—" the repo
                field shows. */}
            <div
              className="f-field"
              title="which coding agent zeus runs — claude (default) or codex; applied once at cell start"
            >
              agent{" "}
              <FieldSelect
                id="f-agent"
                label="agent"
                disabled={readOnly}
                value={agent}
                onPick={setAgent}
                options={["codex"]}
                emptyLabel="claude"
              />
            </div>
            {/* job colour. It is the ONE control in this form that is not
                disabled while read-only, and that is deliberate rather than an
                oversight (setFormReadOnly says so out loud): everything else
                here is job-file state the crew owns while a cell is in flight,
                so the server refuses it, whereas a colour is carl's own label
                on the card riding its own ungated endpoint — and recolouring a
                RUNNING job is the main thing the feature is for. */}
            <div className="f-colors">
              color
              <SwatchStrip
                name="f-color"
                value={pendingColor ?? job?.color}
                onPick={(c) => void pickColor(c)}
              />
            </div>
            {/* the goal links. A DIV rather than a <label>, for the repo
                field's reason (a label would forward a menu row's click to the
                button and reopen the menu the pick just closed) — and, like
                the colour strip beside it, NOT disabled while read-only: the
                links are carl's filing of the job rather than job-file state
                the crew owns, they ride their own ungated route, and filing a
                RUNNING job under a goal is most of what the control is for.
                The control itself is shared with the steer head — see
                GoalMultiPicker. */}
            <div className="f-field">
              goals{" "}
              <GoalMultiPicker
                id="f-goal"
                slug={slug}
                goals={job?.goals}
                mutate={mutate}
                active={!archived}
              />
            </div>
            {/* visibility — the steer modal's three-way control, HERE as well
                because a DRAFT card opens this editor and never the steer
                modal, so without it a draft could not be made public or
                private from the board at all. Like colour and goals it rides
                its own ungated one-field route (setJobVisibility) and is NOT
                disabled while read-only; it is never part of the form save,
                which the server deliberately makes ignore `visibility`. Withheld
                for an archived job, which no route answers for. */}
            {slug && job && !archived ? (
              <div className="f-field f-visibility">
                <VisibilityControl
                  id="f-visibility"
                  slug={slug}
                  visibility={job.visibility}
                  mutate={mutate}
                  compact
                />
              </div>
            ) : null}
            <span className="f-toggles">
              <label className="check">
                <input
                  type="checkbox"
                  id="f-solo"
                  disabled={readOnly}
                  checked={solo}
                  onChange={(e) => setSolo(e.target.checked)}
                />{" "}
                solo
              </label>
            </span>
          </div>
          {/* The prereqs block stood here. It was the QUEUE's gating
              mechanism — "hold this job until those jobs are done" only means
              anything while something else decides when a job starts. Carl
              decides now, by submitting, so a prereq had nothing left to gate
              and the field is off the wire in both directions. */}

          <CmdComposer
            id="editor-text"
            chipsId="editor-cmd-chips"
            value={spec}
            onChange={setSpec}
            readOnly={readOnly}
            hideChips={readOnly}
            spellCheck={false}
            textareaRef={specRef}
            onSubmit={() => {
              if (armed && !busy) void doSubmit();
            }}
            wrapper={(composer) => (
              <label className="f-block grow" htmlFor="editor-text">
                spec — zeus' initial prompt, as fleshed out as possible
                {composer}
              </label>
            )}
          />

          <div className="att-zone" id="editor-att-zone">
            <AttChips
              id="editor-chips-att"
              chips={chips}
              onRemove={(path) =>
                setChips((prev) => prev.filter((c) => c.path !== path))
              }
            />
            <div className="att-bar">
              <button
                className="btn ghost att-btn"
                id="editor-att-btn"
                type="button"
                title="attach images"
                disabled={readOnly}
                onClick={() => attInput.current?.click()}
              >
                📎 attach
              </button>
              <span className="att-hint">paste (⌘V) or drop images</span>
              <input
                type="file"
                id="editor-att-input"
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
        </div>
        <div
          className="answer-note"
          id="editor-note"
          style={
            noteTone === "ok"
              ? { color: "var(--solved)" }
              : noteTone === "dim"
                ? { color: "var(--text-2)" }
                : undefined
          }
        >
          {note}
        </div>
      </div>
      {/* `job-list` — the other-job-slugs datalist — went with the prereq
          input above, which was its only `list=` consumer. `JobForm.slugs`
          stays on the wire type: whether the server keeps sending it is the Go
          slice's call, and an unread key costs nothing here. */}
      {/* agent-aware: the suggestions follow the agent toggle, so a codex
          job's model field offers codex ids and a claude job's the claude
          set — the typed value itself stays free-form wire text either way. */}
      <datalist id="model-list">
        {agent === "codex" ? (
          <>
            <option value="gpt-6-astra" />
            <option value="gpt-5.6-sol" />
            <option value="gpt-5.6-luna" />
            <option value="gpt-5.6-terra" />
            <option value="gpt-5.5" />
          </>
        ) : (
          <>
            <option value="claude-fable-5-1" />
            <option value="claude-opus-5" />
            <option value="fable" />
            <option value="opus" />
            <option value="sonnet" />
            <option value="haiku" />
          </>
        )}
      </datalist>
    </>
  );
}
