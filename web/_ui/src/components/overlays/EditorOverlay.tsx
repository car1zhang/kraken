import { useCallback, useEffect, useRef, useState } from "react";
import {
  archiveJob,
  createJob,
  dequeueJob,
  fetchJobForm,
  queueJob,
  saveJobForm,
  uploadImage,
} from "../../api/client.ts";
import type { JobForm, JobFormPrereq, WebJob } from "../../api/types.ts";
import { insertIntoTextarea } from "../../hooks/useCmdHighlight.ts";
import { useDebouncedCallback } from "../../hooks/useDebouncedDraft.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
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
  /** See Overlay.tsx. */
  mutate: Mutate;
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
  const editable = !!form?.editable;
  const readOnly = !!form && !editable;

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
    if (!slug) return;
    const ac = new AbortController();
    load(slug, ac.signal).catch((e: unknown) => {
      if (ac.signal.aborted) return;
      setNoteTone("");
      setNote(e instanceof Error ? e.message : String(e));
    });
    return () => ac.abort();
  }, [load, slug]);

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

  const hint = readOnly
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

  const repos = form?.repos || [];
  const hasDocq = !!job?.doc_questions?.length;

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="editor-title">
          {isNew ? "new job" : slug + ".job.md"}
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
                placeholder="claude-opus-5"
                spellCheck={false}
                title="claude model for zeus (--model value) — applied once at cell start; empty = kraken's default, claude-opus-5"
                disabled={readOnly}
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
            </label>
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
        <option value="claude-opus-5" />
        <option value="fable" />
        <option value="opus" />
        <option value="sonnet" />
        <option value="haiku" />
      </datalist>
    </>
  );
}
