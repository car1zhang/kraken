import { useState } from "react";

import type { WebFeedback, WebJob } from "../../../api/types.ts";

export interface FeedbackPanelProps {
  job: WebJob;
  /** Resolves to null on success, or the failure the status bar already has. */
  onSaveFeedback(slug: string, id: number, note: string): Promise<Error | null>;
  onDeleteFeedback(slug: string, id: number): void;
}

/**
 * One feedback entry. Port of feedbackRowHTML (app.js:316).
 *
 * An addressed entry (the nightly dream pass posted addressed/addressed_at for
 * a durable fix it shipped — no other agent writes that column) is dimmed
 * and terminal — carl can still edit or delete it, same as an open one, since
 * a note staying accurate is more useful than freezing it the moment zeus
 * reacts to it.
 *
 * The editing flag is local component state. The vanilla board had to hold it
 * in a module-level Set because every 3s poll replaced #board's innerHTML
 * wholesale and would otherwise have wiped both the open editor and whatever
 * was typed into it — the same rebuild the snapshotDocInputs/restoreDocInputs
 * machinery existed to survive. React reconciles instead of replacing, so the
 * row keeps its own state across polls and none of that is needed.
 */
function FeedbackRow({ job, fb, onSaveFeedback, onDeleteFeedback }: FeedbackPanelProps & { fb: WebFeedback }) {
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(fb.note);
  const [note, setNote] = useState("");
  const [saving, setSaving] = useState(false);

  if (editing) {
    return (
      <div className="fb-row" data-fb-id={fb.id}>
        <div className="fb-head">
          <span className="fb-artifact">{fb.artifact}</span>
        </div>
        <textarea
          className="fb-edit-input"
          data-slug={job.slug}
          data-id={fb.id}
          value={text}
          onChange={(ev) => setText(ev.target.value)}
          // A click into the free-text field must focus/type into it, not fall
          // through to the card's open-the-drilldown handler, which would drop
          // a modal on top of it and steal focus.
          onClick={(ev) => ev.stopPropagation()}
        />
        <div className="fb-edit-actions">
          <span className="fb-edit-note">{note}</span>
          <button
            className="fb-cancel"
            data-id={fb.id}
            onClick={(ev) => {
              ev.stopPropagation();
              setEditing(false);
              setText(fb.note);
              setNote("");
            }}
          >
            cancel
          </button>
          <button
            className="fb-save"
            data-slug={job.slug}
            data-id={fb.id}
            disabled={saving}
            onClick={async (ev) => {
              ev.stopPropagation();
              const trimmed = text.trim();
              if (!trimmed) {
                setNote("feedback is empty");
                return;
              }
              setSaving(true);
              const err = await onSaveFeedback(job.slug, fb.id, trimmed);
              setSaving(false);
              // The row survives the refetch either way (React keeps it
              // mounted), so unlike the vanilla handler there is no orphaned
              // node to re-query — the message just lands where it was written.
              if (err) setNote("save failed — see status bar");
              else {
                setEditing(false);
                setNote("");
              }
            }}
          >
            save
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className={"fb-row" + (fb.addressed ? " fb-addressed" : "")} data-fb-id={fb.id}>
      <div className="fb-head">
        <span className="fb-artifact">{fb.artifact}</span>
        <span className="fb-actions">
          <button
            className="fb-edit"
            data-id={fb.id}
            onClick={(ev) => {
              ev.stopPropagation();
              setText(fb.note);
              setEditing(true);
            }}
          >
            edit
          </button>
          <button
            className="fb-delete"
            data-slug={job.slug}
            data-id={fb.id}
            onClick={(ev) => {
              ev.stopPropagation();
              if (!window.confirm("delete this feedback note? this can't be undone.")) return;
              onDeleteFeedback(job.slug, fb.id);
            }}
          >
            delete
          </button>
        </span>
      </div>
      <div className="fb-note">{fb.note}</div>
      {fb.addressed ? (
        <div
          className="fb-addressed-text"
          title={fb.addressed_at ? "addressed " + fb.addressed_at : undefined}
        >
          ✓ {fb.addressed}
        </div>
      ) : null}
    </div>
  );
}

/**
 * A job's feedback panel — its own neutral panel, and the same
 * empty-renders-nothing convention docQuestionsHTML used, so a job with no
 * feedback array (an older server, or simply no notes yet) looks exactly as
 * before. Port of feedbackHTML (app.js:349).
 */
export function FeedbackPanel(props: FeedbackPanelProps) {
  const list = props.job.feedback ?? [];
  if (!list.length) return null;
  return (
    <div
      className="job-feedback"
      // NOT a bug and not removable (see 53-port-checklist.md): browsers treat
      // a press-and-drag gesture starting on or over a form control inside a
      // draggable ancestor as dragging the ANCESTOR rather than selecting
      // text, which turned "select my typed note to fix a word" into a real
      // column move. Panels with text inputs never start a drag.
      onDragStart={(ev) => {
        ev.preventDefault();
        ev.stopPropagation();
      }}
    >
      {list.map((f) => (
        <FeedbackRow key={f.id} fb={f} {...props} />
      ))}
    </div>
  );
}
