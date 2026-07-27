import { useCallback, useEffect, useRef, useState } from "react";
import { answerDocQuestion } from "../../api/client.ts";
import type { WebDocQuestion, WebJob } from "../../api/types.ts";
import type { Mutate } from "./Overlay.tsx";

// The answerable questions panel (app.js:255 docQuestionsHTML and friends).
//
// MODAL-ONLY, and that is the point of the component's existence: the board
// card renders a COUNT CHIP and nothing else (docQuestionsChipHTML), because a
// card that inlined the whole panel grew taller than everything around it and
// buried the one thing the board exists to show. This panel is the drilldown
// that chip advertises, and it renders in exactly two places — the steer modal
// and the job editor. web/boardsurface_test.go pins that rule on the vanilla
// side; the React equivalent is BoardView's own test (not this file's).
//
// Answers are editable forever (Q13), live or done, which is why an answered
// row is a click target that reopens it as an editable one.

const DQ_IMPORTANCE_RANK: Record<string, number> = { P0: 0, P1: 1, P2: 2 };

/**
 * Open questions first (P0 < P1 < P2, ties keep file order — Array#sort is
 * stable), then answered ones last in file order. app.js:174.
 */
export function sortDocQuestions(list: WebDocQuestion[]): WebDocQuestion[] {
  const open: { q: WebDocQuestion; i: number }[] = [];
  const answered: { q: WebDocQuestion; i: number }[] = [];
  list.forEach((q, i) => (q.answer ? answered : open).push({ q, i }));
  open.sort(
    (a, b) =>
      (DQ_IMPORTANCE_RANK[a.q.importance] ?? 3) -
        (DQ_IMPORTANCE_RANK[b.q.importance] ?? 3) || a.i - b.i,
  );
  return [...open.map((x) => x.q), ...answered.map((x) => x.q)];
}

const draftKey = (slug: string, id: string) => JSON.stringify([slug, id]);

/**
 * Unsent answers, surviving a modal close/reopen. app.js:720 (`dqDrafts`).
 *
 * Module-level for the same reason it was there: the panel is modal-only, so a
 * closing modal has nowhere on the board to hand its draft to — and the closes
 * that matter most are not the user's. checkStaleModals force-closes the modal
 * the instant the job changes state, which is precisely when someone is
 * mid-answer. An entry is dropped when its answer posts, or when the panel
 * unmounts with that input EMPTY (a deliberately cleared answer must not come
 * back on the next open from a stale stash).
 */
const dqDrafts = new Map<string, string>();

/** Test seam: reset the cross-open draft stash. */
export function __resetDocQuestionDrafts(): void {
  dqDrafts.clear();
}

export interface DocQuestionsProps {
  /** The job whose questions these are. Nothing renders if it has none. */
  job: WebJob;
  /** See Overlay.tsx — the injected post() with board-invalidation policy. */
  mutate: Mutate;
}

/**
 * Renders `<div class="doc-questions">` or nothing. The wrapper with the id
 * (`#steer-docq` / `#editor-docq`) belongs to the overlay, not to this.
 */
export function DocQuestions({ job, mutate }: DocQuestionsProps) {
  const list = job.doc_questions || [];
  const slug = job.slug;

  // Which already-answered questions are reopened for a re-answer. Component
  // state, not module state: the vanilla `reansweringDocQ` Set had to be
  // module-level only because the 3s poll rebuilt #board wholesale and would
  // otherwise have wiped it. React re-renders in place, so this survives a
  // props change by construction — which is exactly the property the port
  // brief asks for.
  const [reanswering, setReanswering] = useState<Set<string>>(new Set());

  // Live text per question id. Seeded from dqDrafts on mount, and — critically
  // — NOT reset when `job` changes identity on a poll tick. A new payload
  // arriving every 3 seconds must never wipe what is being typed.
  const [values, setValues] = useState<Record<string, string>>(() => {
    const seed: Record<string, string> = {};
    for (const q of list) {
      const d = dqDrafts.get(draftKey(slug, q.id));
      if (d) seed[q.id] = d;
    }
    return seed;
  });

  const [busy, setBusy] = useState<Set<string>>(new Set());

  // stash/drop on unmount — see dqDrafts' comment
  const stashRef = useRef({ slug, values });
  stashRef.current = { slug, values };
  useEffect(
    () => () => {
      const { slug: s, values: v } = stashRef.current;
      for (const [id, text] of Object.entries(v)) {
        const key = draftKey(s, id);
        if (text) dqDrafts.set(key, text);
        else dqDrafts.delete(key);
      }
    },
    [],
  );

  const send = useCallback(
    async (id: string, text: string) => {
      if (busy.has(id)) return;
      // disable the row's controls synchronously so a double-click or a
      // double-Enter cannot double-post
      setBusy((prev) => new Set(prev).add(id));
      const ok = await mutate(() => answerDocQuestion(slug, id, text));
      setBusy((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
      if (ok) {
        // leave re-answer mode so the row reads back as answered, and drop the
        // stashed draft so reopening the modal cannot re-seed text already sent
        setReanswering((prev) => {
          const next = new Set(prev);
          next.delete(id);
          return next;
        });
        dqDrafts.delete(draftKey(slug, id));
        setValues((prev) => {
          const next = { ...prev };
          delete next[id];
          return next;
        });
      }
      // a failed post leaves the row open, its text intact, for a retry — the
      // server's own sentence is already in the status bar
    },
    [busy, mutate, slug],
  );

  if (!list.length) return null;
  const sorted = sortDocQuestions(list);
  const hasOpen = sorted.some((q) => !q.answer);

  return (
    <div className="doc-questions">
      {sorted.map((q) => {
        const editable = !q.answer || reanswering.has(q.id);
        if (!editable) {
          return (
            <AnsweredRow
              key={q.id}
              slug={slug}
              q={q}
              onReopen={() => {
                setReanswering((prev) => new Set(prev).add(q.id));
                setValues((prev) => ({ ...prev, [q.id]: q.answer || "" }));
              }}
            />
          );
        }
        return (
          <EditableRow
            key={q.id}
            slug={slug}
            q={q}
            reanswering={reanswering.has(q.id)}
            disabled={busy.has(q.id)}
            value={values[q.id] ?? (q.answer && reanswering.has(q.id) ? q.answer : "")}
            onChange={(v) => setValues((prev) => ({ ...prev, [q.id]: v }))}
            onSend={(text) => void send(q.id, text)}
            onCancelReanswer={() => {
              setReanswering((prev) => {
                const next = new Set(prev);
                next.delete(q.id);
                return next;
              });
              setValues((prev) => {
                const next = { ...prev };
                delete next[q.id];
                return next;
              });
              dqDrafts.delete(draftKey(slug, q.id));
            }}
          />
        );
      })}
      {hasOpen ? (
        <div className="dq-hint">
          answers are read by the agent next time it works
        </div>
      ) : null}
    </div>
  );
}

function EditableRow({
  slug,
  q,
  reanswering,
  disabled,
  value,
  onChange,
  onSend,
  onCancelReanswer,
}: {
  slug: string;
  q: WebDocQuestion;
  reanswering: boolean;
  disabled: boolean;
  value: string;
  onChange: (v: string) => void;
  onSend: (text: string) => void;
  onCancelReanswer: () => void;
}) {
  const imp = (q.importance || "P2").toLowerCase();
  return (
    <div className="dq-row">
      <div className="dq-head">
        <span className={`dq-imp ${imp}`}>{q.importance || "P2"}</span>
        <span className="dq-q" title={q.text}>
          {q.id} — {q.text}
        </span>
      </div>
      {q.context ? (
        <div className="dq-ctx" title={q.context}>
          {q.context}
        </div>
      ) : null}
      {q.recommendation ? (
        <div className="dq-rec" title={q.recommendation}>
          rec: {q.recommendation}
        </div>
      ) : null}
      <div className="dq-answer">
        {q.recommendation ? (
          <button
            className="dq-accept"
            data-slug={slug}
            data-id={q.id}
            data-text={q.recommendation}
            disabled={disabled}
            onClick={() => onSend(q.recommendation as string)}
          >
            accept rec
          </button>
        ) : null}
        <input
          className="dq-input"
          type="text"
          placeholder="answer…"
          value={value}
          data-slug={slug}
          data-id={q.id}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            // Enter-to-send. Guard IME composition (a CJK candidate's commit
            // Enter also fires key:"Enter") and modifier combos BEFORE
            // preventDefault: this is prose, and swallowing a composition
            // commit or a Shift/Alt+Enter would silently eat what was typed.
            if (e.key !== "Enter") return;
            if (
              e.nativeEvent.isComposing ||
              e.nativeEvent.keyCode === 229 ||
              e.shiftKey ||
              e.altKey
            ) {
              return;
            }
            e.preventDefault();
            const text = value.trim();
            if (!text) return;
            onSend(text);
          }}
        />
        <button
          className="dq-send"
          data-slug={slug}
          data-id={q.id}
          disabled={disabled}
          onClick={() => {
            const text = value.trim();
            if (!text) return;
            onSend(text);
          }}
        >
          send
        </button>
        {reanswering ? (
          <button
            className="dq-reanswer-cancel"
            data-slug={slug}
            data-id={q.id}
            disabled={disabled}
            onClick={onCancelReanswer}
          >
            cancel
          </button>
        ) : null}
      </div>
    </div>
  );
}

function AnsweredRow({
  slug,
  q,
  onReopen,
}: {
  slug: string;
  q: WebDocQuestion;
  onReopen: () => void;
}) {
  const imp = (q.importance || "P2").toLowerCase();
  const title = q.answered_at
    ? `answered ${String(q.answered_at)} — click to re-answer`
    : "click to re-answer";
  return (
    <div
      className="dq-row dq-answered"
      data-slug={slug}
      data-id={q.id}
      title={title}
      onClick={onReopen}
    >
      <div className="dq-head">
        <span className={`dq-imp ${imp}`}>{q.importance || "P2"}</span>
        <span className="dq-q" title={q.text}>
          {q.id} — {q.text}
        </span>
      </div>
      <div className="dq-answered-text">✓ {q.answer}</div>
    </div>
  );
}
