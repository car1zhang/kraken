import {
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  archiveJob,
  createJob,
  deleteFeedback,
  dequeueJob,
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
  type JobFormPrereq,
  type WebArtifact,
  type WebJob,
} from "../../api/types.ts";
import { insertIntoTextarea } from "../../hooks/useCmdHighlight.ts";
import { useDebouncedCallback } from "../../hooks/useDebouncedDraft.ts";
import { SwatchStrip } from "./SwatchStrip.tsx";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { jobKey } from "../../lib/jobKey.ts";
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
//   slug, editable          the form — repo/base/model/solo/prereqs/spec
//   slug, !editable         the same form, every field locked: a cell is in
//                           flight and the file belongs to the crew
//
// Autosave, not a save button: any change inside the form debounces 600ms into
// `POST /api/job/saveform` with **mtime: 0**, which deliberately opts OUT of the
// optimistic lock. That is not an oversight — the editor is the only writer of
// the file while it is open, and a conflict dialog mid-typing is worse than
// last-writer-wins on a single-user board.

/**
 * app.js:2952 — one prereq row.
 *
 * `id` is a client-side row identity, never sent: `job` cannot be the React key
 * because "+ add" appends an EMPTY row and two empty rows would collide, which
 * breaks removal (React would reconcile the wrong row away). It is a counter
 * rather than an index so removing row 1 does not renumber row 2's identity out
 * from under its input.
 */
interface PrereqRow {
  id: number;
  job: string;
  gated: boolean;
}

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
      <EditorBody key={props.slug ?? "__new__"} {...props} fileSink={sink} />
    </Overlay>
  );
}

function EditorBody({
  slug,
  job,
  onClose,
  onCreated,
  onReactivate,
  onOpenArtifact,
  mutate,
  fileSink,
}: EditorOverlayProps & { fileSink: FileSink }) {
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
  const [solo, setSolo] = useState(false);
  const [prereqs, setPrereqs] = useState<PrereqRow[]>([]);
  const [spec, setSpec] = useState("");
  const [chips, setChips] = useState<AttChip[]>([]);
  const [pending, setPending] = useState(0);
  const [busy, setBusy] = useState(false);
  // true when the freshest save/load says the current spec won't run (loadForm's
  // d.err, or a save's d.warn) — one of the queue button's arming gates
  const [syncWarn, setSyncWarn] = useState(false);

  const specRef = useRef<HTMLTextAreaElement | null>(null);
  const attInput = useRef<HTMLInputElement | null>(null);
  const prereqsRef = useRef<HTMLDivElement | null>(null);
  const nextPrereqID = useRef(0);
  // set by "+ add" so the row that appears takes focus — an added row is there
  // to be typed into, and an empty one is dropped from the payload anyway
  const focusNewPrereq = useRef(false);
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
      setSolo(!!d.solo);
      setPrereqs(
        (d.prereqs || []).map((p) => ({
          id: nextPrereqID.current++,
          job: p.job,
          gated: !!p.gated,
        })),
      );
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
      // the ready row is gone from the form; it round-trips as loaded and the
      // queue button is what actually sets it
      ready: !!form.ready,
      solo,
      prereqs: prereqs
        .filter((p) => p.job.trim())
        .map<JobFormPrereq>((p) => ({ job: p.job.trim(), gated: p.gated })),
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
  }, [base, form, model, prereqs, readOnly, repo, slug, solo, spec]);

  const autosave = useDebouncedCallback(() => void save(), 600);
  const { call: scheduleSave, cancel: cancelSave } = autosave;

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
  }, [repo, base, model, solo, prereqs, spec, slug, readOnly, form, scheduleSave]);

  useEffect(() => {
    if (!focusNewPrereq.current) return;
    focusNewPrereq.current = false;
    const rows = prereqsRef.current?.querySelectorAll<HTMLInputElement>(
      '.prereq-row input[type="text"]',
    );
    rows?.[rows.length - 1]?.focus();
  }, [prereqs]);

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

  // ---- the queue / dequeue affordance (app.js:3031 updateEditorQueue) -----
  // Derived, not snapshotted. The vanilla one-shot computation read a board
  // that could be missing a job created moments earlier, which forced a
  // close/reopen before it could be queued.
  const formVisible = !isNew && !!form;
  // missing from the board means just-created — treat that as a fresh draft
  const state = job ? job.state : "draft";
  const live = job ? !!job.live : false;
  // a board-level err (an unhealthy cell), distinct from syncWarn's form-level
  // d.err / d.warn
  const boardErr = job ? !!job.err : false;
  const queuedMode = formVisible && !readOnly && state === "queued" && !live;
  const draftMode =
    formVisible && !readOnly && !queuedMode && state === "draft" && !live;
  // a draft's queue button is always PRESENT — merely disabled until the spec
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
      : queuedMode
        ? "queued — cancel returns it to draft"
        : draftMode
          ? armed
            ? "synced ✓ — queueable"
            : spec.trim() === ""
              ? "write a spec to queue"
              : ""
          : "";

  const doQueue = useCallback(async () => {
    if (!slug) return;
    if (pending > 0) {
      setNoteTone("");
      setNote("uploading image…");
      return;
    }
    setBusy(true);
    // land any edit still sitting in the debounce window BEFORE queueing —
    // cancel first so the pending timer cannot also fire and post twice
    cancelSave();
    await save();
    const res = await mutate(() => queueJob(slug));
    setBusy(false);
    if (res) onClose();
    else {
      setNoteTone("");
      setNote("queue failed — see status bar");
    }
  }, [cancelSave, mutate, onClose, pending, save, slug]);

  const doDequeue = useCallback(async () => {
    if (!slug) return;
    setBusy(true);
    // rebase the staleness baseline BEFORE posting: mutate() awaits the board
    // refresh, so the stale check runs against the new queued→draft state while
    // we are still awaiting — a stale "queued" baseline there would read our
    // own action as someone else's change and close the editor
    const prevState = openState.current;
    const prevCaptured = captured.current;
    openState.current = "draft";
    captured.current = true;
    const res = await mutate(() => dequeueJob(slug));
    setBusy(false);
    if (!res) {
      openState.current = prevState;
      captured.current = prevCaptured;
      setNoteTone("");
      setNote("cancel failed — see status bar");
    }
  }, [mutate, slug]);

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
          className="btn ghost"
          id="editor-dequeue"
          hidden={!queuedMode}
          disabled={busy}
          title="cancel — return this queued job to draft (spec is kept)"
          onClick={() => void doDequeue()}
        >
          ✕ cancel queue
        </button>
        <button
          className="btn"
          id="editor-queue"
          hidden={!draftMode}
          disabled={!armed || busy}
          title="mark this job ready and queue it — same move as dragging the card to QUEUED"
          onClick={() => void doQueue()}
        >
          queue ⌘⏎
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
            modal is also what a click on a draft/queued/dead card opens. */}
        <div className="editor-docq" id="editor-docq" hidden={!hasDocq}>
          {job && hasDocq ? (
            <>
              <div className="editor-docq-head">questions</div>
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
              if (armed && !busy) void doQueue();
            }
          }}
        >
          <div className="f-row">
            <label>
              repo{" "}
              <select
                id="f-repo"
                disabled={readOnly}
                value={repo}
                onChange={(e) => setRepo(e.target.value)}
              >
                <option value="">—</option>
                {repos.map((x) => (
                  <option value={x} key={x}>
                    {x}
                  </option>
                ))}
                {/* an existing job with an unusual repo still round-trips
                    instead of silently resetting to "—" */}
                {form?.repo && !repos.includes(form.repo) ? (
                  <option value={form.repo}>{form.repo}</option>
                ) : null}
              </select>
            </label>
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
                placeholder="claude-fable-5"
                spellCheck={false}
                title="claude model for zeus (--model value) — applied once at cell start; empty = kraken's default for zeus, claude-fable-5 (the crew it spawns is opus regardless)"
                disabled={readOnly}
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
            </label>
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
          <div className="f-block">
            <span className="f-label">
              prereqs{" "}
              <button
                className="mini-add"
                id="f-add-prereq"
                disabled={readOnly}
                onClick={() => {
                  setPrereqs((prev) => [
                    ...prev,
                    { id: nextPrereqID.current++, job: "", gated: false },
                  ]);
                  focusNewPrereq.current = true;
                }}
              >
                + add
              </button>
            </span>
            <div id="f-prereqs" ref={prereqsRef}>
              {prereqs.map((p, i) => (
                <div className="prereq-row" key={p.id}>
                  <input
                    type="text"
                    list="job-list"
                    placeholder="job-slug"
                    spellCheck={false}
                    disabled={readOnly}
                    value={p.job}
                    onChange={(e) =>
                      setPrereqs((prev) =>
                        prev.map((row, k) =>
                          k === i ? { ...row, job: e.target.value } : row,
                        ),
                      )
                    }
                  />
                  <label className="check">
                    <input
                      type="checkbox"
                      disabled={readOnly}
                      checked={p.gated}
                      onChange={(e) =>
                        setPrereqs((prev) =>
                          prev.map((row, k) =>
                            k === i ? { ...row, gated: e.target.checked } : row,
                          ),
                        )
                      }
                    />{" "}
                    gated ⚑
                  </label>
                  <button
                    className="rm"
                    title="remove"
                    disabled={readOnly}
                    onClick={() =>
                      setPrereqs((prev) => prev.filter((_, k) => k !== i))
                    }
                  >
                    ✕
                  </button>
                </div>
              ))}
            </div>
          </div>

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
              if (armed && !busy) void doQueue();
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
      <datalist id="job-list">
        {(form?.slugs || []).map((x) => (
          <option value={x} key={x} />
        ))}
      </datalist>
      <datalist id="model-list">
        <option value="claude-fable-5" />
        <option value="claude-opus-5" />
        <option value="fable" />
        <option value="opus" />
        <option value="sonnet" />
        <option value="haiku" />
      </datalist>
    </>
  );
}
