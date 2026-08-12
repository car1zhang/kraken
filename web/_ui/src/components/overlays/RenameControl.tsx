import { useCallback, useEffect, useRef, useState } from "react";

import { renameJob } from "../../api/client.ts";
import type { WebJob } from "../../api/types.ts";
import { slugify } from "../../lib/slug.ts";

// The rename affordance, shared by the two surfaces a job's identity is
// reachable from: the editor overlay (its title row) and the steer overlay
// (the live cell's own modal). One implementation rather than two, because the
// behaviour they owe is identical down to the refusal text — and a second copy
// is a second place for the re-point to be forgotten.
//
// It is split into a HOOK plus two renderers, and the split is the panel head's
// doing rather than a preference: style.css pins `.panel-head` to ONE line with
// `flex-wrap: nowrap`, so only the rigid trigger button can live up there. The
// input, its slug preview and any refusal need a row of their own in the panel
// BODY, which is a different place in the DOM with the same state behind it.
//
// Every rename is a direct POST rather than a `mutate` call — see
// api/client.ts's renameJob for why the refusal has to come back as text.
//
// The hook MUST be held above whatever the re-point remounts. Both overlays
// key their body on the slug, so a successful rename unmounts that body within
// the same commit as the response lands — state kept inside it (the `skipped`
// notice below, most of all) would be destroyed before it could ever be read.
// Both call `useRenameControl` in the OUTER component and pass the state down.
//
// That hoist costs something the body's key used to pay for free: both
// overlays are PERMANENTLY MOUNTED (App renders them unconditionally and
// Overlay only gates its children on `open`), so this state now lives for the
// life of the page and nothing resets it between jobs. Two guards replace the
// remount, and they are deliberately not one — see `noticeFor` and the
// close effect in the hook.

/** What `useRenameControl` hands the two renderers. Opaque to callers. */
export interface RenameControlState {
  /** The job's CURRENT slug — what the POST names, and what the input seeds from. */
  slug: string;
  open: boolean;
  value: string;
  /** `util.Slugify(value)` — what the server would store. "" disarms the button. */
  preview: string;
  /** The server's own refusal sentence, or "" while there is none. */
  error: string;
  busy: boolean;
  /**
   * The preserved artifact copies the rename could NOT move, straight off the
   * response, empty when there were none.
   *
   * Each entry is the SOURCE path — the file still sitting under the old name
   * in ~/Documents/pluto, which is the one a human can act on — and the list
   * holds true name CONFLICTS only; a copy whose rename FAILED outright is in
   * `notes` beside it instead.
   */
  skipped: string[];
  /**
   * The post-commit steps that FAILED, each with its cause: a preserved copy
   * whose rename failed outright, a tmux convergence step that did not take, a
   * zeus notice that did not deliver.
   *
   * Rendered as its own block rather than folded into `skipped`, because the
   * reader's move is different in kind — a skipped entry is a file to move or
   * delete, a note is something that broke — and because merging them is what
   * the round-3 fix undid on the server side: a failed move used to be listed
   * as a conflict, under a sentence that was false about it.
   */
  notes: string[];
  begin: () => void;
  cancel: () => void;
  setValue: (v: string) => void;
  submit: () => void;
  /**
   * Drop the whole post-rename notice — both lists — because carl has read it.
   * One verb rather than two: they are produced by one action, shown together,
   * and there is no state in which dismissing half of them means anything.
   */
  dismissNotice: () => void;
}

// Both lists above share one property that is easy to lose: they OUTLIVE the
// action that produced them. The rename COMMITTED, so there is no refusal on
// screen to read them off, the server only logs them, and the person who can
// act is the one being told — so the row stays after the input closes, until
// it is dismissed. They are also both empty for any job the notice was not
// produced for, whatever is in the state behind them: see the two guards in
// `useRenameControl`.

/**
 * The visibility rule, stated once for both surfaces: a rename control renders
 * for any non-archived EXISTING job WHOSE RE-POINT IS WIRED.
 *
 * It is deliberately NOT gated on the form lock. Identity is not a form field —
 * `saveform` refuses a job whose cell is in flight because the file belongs to
 * the crew, while a rename touches the row, the tmux session and the pluto
 * artifact names and works fine on a live cell. Renaming the job carl is
 * actually watching is the main thing the feature is for, which is the same
 * argument the colour strip's ungated endpoint already carries.
 *
 * Archived is the one hard no (the server refuses it and says "unarchive"), and
 * so is the new-job row, whose slug is the thing the create button is for.
 *
 * The `onRenamed` arm is the third rule and the one that is about US rather
 * than about the job: the re-point is App's state, not this component's, so an
 * overlay handed no callback would rename the job and then go on addressing a
 * name the server no longer has as a current one — silently. Both overlays
 * therefore declare the prop OPTIONAL (a required one would have to be spelled
 * into forty existing test call sites, two of them cross-source contract
 * files) and pay for that with this gate: unwired is a control that does not
 * render, which is a visible absence rather than a silent misbehaviour.
 */
export function renameControlVisible(
  job: WebJob | undefined,
  isNew: boolean,
  onRenamed: ((slug: string) => void) | undefined,
): boolean {
  return !isNew && !job?.archived && !!onRenamed;
}

/**
 * Everything a completed rename left for its reader — both lists — plus the
 * JOBS it is true of.
 *
 * ONE piece of state for two lists, because they are one event: a single
 * response produces them, they are shown together, they are guarded together
 * and they are dismissed together. Two `useState`s would have been two places
 * to forget a guard, and the guards are the whole reason this lives up here.
 *
 * Two slugs rather than one, and the pair is the point: the notice is produced
 * by a rename, so the job it describes answers to the name it was renamed FROM
 * and the name it was renamed TO, and which of those `slug` currently holds
 * depends on whether the integrator's re-point has landed yet. Both are this
 * one job — a former slug is reserved against every other active job, so no
 * second job can ever match — and any THIRD name means the overlay has moved
 * on and the notice is not about what is on screen.
 */
interface RenameNotice {
  slugs: string[];
  /** SOURCE paths of preserved copies that kept the old name. */
  skipped: string[];
  /** Post-commit steps that failed, each with its cause. */
  notes: string[];
}

const noNotice: RenameNotice = { slugs: [], skipped: [], notes: [] };

/**
 * @param opts.open the OVERLAY's open flag, not the input row's. Defaults to
 * true, which means "never auto-clear" for a caller that does not track it.
 *
 * @param opts.beforeSubmit ran once, awaited, immediately BEFORE the POST goes
 * out.
 *
 * That one is the editor's pending autosave, and it is not a nicety. The
 * rename re-points the overlay, which unmounts the body, which flushes the
 * 600ms debounce on its way out — so the last edits post under the OLD slug,
 * AFTER the row has already moved, and land on a 404 whose message renders
 * into a DOM that is being thrown away. Typing in the spec and renaming within
 * the debounce window silently lost the delta. Flushing here is the honest fix
 * (the save is genuinely still owed under the old name), and it is exactly the
 * discipline the queue button already applies two hundred lines away.
 *
 * Held in a ref so an inline arrow — every caller passes one — does not churn
 * `submit`'s identity on every render.
 */
export function useRenameControl(
  slug: string | null,
  onRenamed: ((slug: string) => void) | undefined,
  opts: {
    open?: boolean;
    beforeSubmit?: () => void | Promise<void>;
  } = {},
): RenameControlState {
  const { open: overlayOpen = true, beforeSubmit } = opts;
  const current = slug ?? "";
  const [open, setOpen] = useState(false);
  const [value, setValueRaw] = useState(current);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<RenameNotice>(noNotice);

  /**
   * Guard one of two against the hoist (see the module header): the notice
   * renders only while the overlay is still addressing the job it was produced
   * for. Clearing on a `current` CHANGE instead would be wrong in the one case
   * that matters — the successful rename's own re-point IS a change of
   * `current`, arriving in the same commit as the notice, so that rule would
   * erase every notice it ever produced.
   *
   * Both lists pass through the one test, so a later third list cannot be
   * added on the other side of it by accident.
   */
  const mine = notice.slugs.includes(current);
  const skipped = mine ? notice.skipped : [];
  const notes = mine ? notice.notes : [];

  /**
   * Guard two: a CLOSED overlay holds nothing. Both overlays outlive every job
   * they show, so without this a half-typed rename, a stale refusal and a read
   * notice all sit waiting to reappear the next time one is opened — for a
   * different job, since the whole panel is reused. It is not redundant with
   * the guard above: that one is about WHOSE notice this is, this one is about
   * a modal that was dismissed rather than answered.
   */
  useEffect(() => {
    if (overlayOpen) return;
    setOpen(false);
    setError("");
    setNotice(noNotice);
  }, [overlayOpen]);

  const beforeRef = useRef(beforeSubmit);
  beforeRef.current = beforeSubmit;

  /**
   * The double-submit guard, and it is a REF rather than the `busy` state
   * beside it on purpose: `busy` is what disables the two buttons, but it is
   * read through a closure captured at render time, so a second Enter or a
   * fast double-click dispatched before React has committed the first
   * `setBusy(true)` still sees `false` and fires a second POST. That second
   * request 404s (the job has already moved) and its refusal lands on a row
   * the first success has closed — an invisible error about nothing. A ref is
   * written synchronously, so the second call cannot get past this line.
   */
  const inFlight = useRef(false);

  const begin = useCallback(() => {
    // seeded with the current slug, so the common edit is a word rather than a
    // retype, and re-opening after a refusal starts from the truth again
    setValueRaw(current);
    setError("");
    setNotice(noNotice);
    setOpen(true);
  }, [current]);

  const cancel = useCallback(() => {
    setOpen(false);
    setError("");
  }, []);

  const setValue = useCallback((v: string) => {
    setValueRaw(v);
    // the refusal described the text that WAS there; typing makes it stale
    setError("");
  }, []);

  const dismissNotice = useCallback(() => setNotice(noNotice), []);

  const submit = useCallback(() => {
    if (!current || inFlight.current) return;
    if (!slugify(value)) return; // the button is disarmed for this — belt and braces
    inFlight.current = true;
    setBusy(true);
    setError("");
    setNotice(noNotice);
    void (async () => {
      try {
        // land anything the overlay still owes under the OLD name first
        await beforeRef.current?.();
        const res = await renameJob(current, value);
        setOpen(false);
        // Both lists in one write: the rename COMMITTED either way, so neither
        // is a failure of it — one is files to move, the other is steps that
        // broke. Filed under BOTH names — `current` is what the overlay still
        // holds this render, `res.slug` is what the re-point below is about to
        // make it.
        setNotice({
          slugs: [current, res.slug],
          skipped: res.skipped ?? [],
          notes: res.notes ?? [],
        });
        // The response's slug, never `slugify(value)`: the server stores what
        // it stores, and re-pointing to a name it did not confirm is how a
        // board card and its overlay end up addressing two different jobs.
        onRenamed?.(res.slug);
      } catch (e: unknown) {
        // stays OPEN on the old slug, holding the typed text, so the refusal
        // can be read beside the thing it refused
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        inFlight.current = false;
        setBusy(false);
      }
    })();
  }, [current, onRenamed, value]);

  return {
    slug: current,
    open,
    value,
    preview: slugify(value),
    error,
    busy,
    skipped,
    notes,
    begin,
    cancel,
    setValue,
    submit,
    dismissNotice,
  };
}

/** The panel-head trigger. Sits with the head's other rigid `.btn.ghost`s. */
export function RenameButton({
  id,
  state,
  visible,
}: {
  id: string;
  state: RenameControlState;
  visible: boolean;
}) {
  return (
    <button
      className="btn ghost"
      id={id}
      hidden={!visible || state.open}
      title="rename this job — the slug, its tmux session and its preserved artifact names; the branch, its PRs and the job's history stay attached"
      onClick={state.begin}
    >
      ✎ rename
    </button>
  );
}

/**
 * The panel-body row: the input, its live slug preview, and any refusal —
 * plus, after a success, the two post-rename lists, which are the things here
 * that outlive the input that produced them.
 *
 * Renders nothing at all while closed AND empty-handed, rather than sitting
 * hidden, so a modal that has never opened it has no empty row in its stack.
 */
export function RenameRow({
  id,
  state,
}: {
  id: string;
  state: RenameControlState;
}) {
  if (!state.open && !state.skipped.length && !state.notes.length) return null;
  const dismissable = !!state.skipped.length || !!state.notes.length;
  return (
    <div className="rename-row" id={id}>
      {state.skipped.length ? (
        // Every path here is the file that STAYED — the source, still under
        // the old name, which is the one a human can act on. The server sends
        // sources for exactly that reason (jobs.RenameResult.Skipped), and it
        // sends true name CONFLICTS only: a preserved copy whose rename failed
        // outright is in the notes block below instead, so this sentence can
        // state the cause without hedging.
        //
        // "at the matching new-name path in the same directory" is doing real
        // work: the blocker itself is not on the wire (the server sends the
        // source, deliberately), so this is what lets carl find the file in
        // the way without being handed a path the client would have to invent.
        <div className="rename-hint warn" id={id + "-skipped"}>
          ⚠ renamed, but {state.skipped.length} preserved artifact{" "}
          {state.skipped.length === 1 ? "copy" : "copies"} kept the old name —
          something already sits at the matching new-name path in the same
          directory.{" "}
          {state.skipped.length === 1
            ? "The path below is that copy, still under the old name; move or delete it by hand:"
            : "The paths below are those copies, still under the old name; move or delete them by hand:"}
          <ul className="rename-skipped">
            {state.skipped.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {state.notes.length ? (
        // A separate block in a different hue, because the reader's move is
        // different in kind: the list above is files to move, this is steps
        // that BROKE, each carrying its own cause. Folding them together is
        // exactly what the server stopped doing — a failed move used to be
        // reported as a conflict, under a sentence that was false about it.
        <div className="rename-hint fail" id={id + "-notes"}>
          ✗ the rename itself landed; {state.notes.length}{" "}
          {state.notes.length === 1 ? "step" : "steps"} after it did not:
          <ul className="rename-skipped">
            {state.notes.map((n) => (
              <li key={n}>{n}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {/* one dismiss for the whole notice: both lists come from one response
          and there is no state in which clearing half of them means anything */}
      {dismissable ? (
        <button className="btn ghost rename-dismiss" onClick={state.dismissNotice}>
          dismiss
        </button>
      ) : null}
      {state.open ? <RenameInput state={state} /> : null}
    </div>
  );
}

/** The input, its buttons and the preview/refusal line under them. */
function RenameInput({ state }: { state: RenameControlState }) {
  return (
    <>
      <div className="rename-line">
        <input
          className="rename-input"
          placeholder="new name"
          spellCheck={false}
          autoFocus
          value={state.value}
          onChange={(e) => state.setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              state.submit();
            }
          }}
        />
        <button
          className="btn"
          disabled={!state.preview || state.busy}
          onClick={state.submit}
        >
          rename
        </button>
        <button className="btn ghost" disabled={state.busy} onClick={state.cancel}>
          cancel
        </button>
      </div>
      {state.error ? (
        <div className="rename-hint err">✗ {state.error}</div>
      ) : (
        <div className="rename-hint">
          {/* the preview is what makes a typed "My Job!" unsurprising; an empty
              one is what disarms the button above, so it says so rather than
              rendering a bare arrow into nothing */}
          {state.preview ? (
            <>
              → slug will be <span className="slug">{state.preview}</span> · branch,
              PRs and history stay attached
            </>
          ) : (
            "→ a name needs at least one letter or digit"
          )}
        </div>
      )}
    </>
  );
}
