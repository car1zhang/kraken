import { useCallback, useEffect, useRef, useState } from "react";
import { answerDocQuestion, threadOp } from "../../api/client.ts";
import type { WebDocQuestion, WebJob } from "../../api/types.ts";
import { questionThreadSeed, questionThreadTitle } from "./askThread.ts";
import type { Mutate } from "./Overlay.tsx";
import { showToast } from "./Toast.tsx";

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

/**
 * Unsent ASK texts, same lifetime rules as dqDrafts. A separate map — unlike
 * the artifact composer's single per-url draft, whose one box merely switches
 * mode — because here both inputs are on screen at once: a stash keyed only
 * by question id would restore one sentence into the other's box.
 */
const dqAskDrafts = new Map<string, string>();

/** Test seam: reset the cross-open draft stashes. */
export function __resetDocQuestionDrafts(): void {
  dqDrafts.clear();
  dqAskDrafts.clear();
}

export interface DocQuestionsProps {
  /** The job whose questions these are. Nothing renders if it has none. */
  job: WebJob;
  /** See Overlay.tsx — the injected post() with board-invalidation policy. */
  mutate: Mutate;
  /**
   * An ask about a question branched a side thread: show it. Wired by the
   * steer modal (through its onOpenThread, the same door ThreadChips uses, so
   * the thread overlay stacks above the modal exactly as a chip click does).
   * ABSENT hides the whole ask affordance — the job editor renders this panel
   * for settled jobs it has no thread door for, and there the rows stay as
   * they were. Presence alone is not enough: branching forks the cell's zeus,
   * so the affordance also needs `job.live` — the same gate ThreadChips puts
   * on the ⑂ branch button it renders.
   */
  onAskBranched?: (slug: string, tid: string) => void;
  /**
   * Render the rows with no way to write: no answer input, no send / accept /
   * re-answer, no ask. Another node's job — answers are its owner's to give,
   * whatever the job's visibility.
   */
  readOnly?: boolean;
  // The answered rows' → decision / ◆ decided mint was retired with the
  // board's other one-shot marks (PR 2, Q10): a decision now reaches the
  // mental model by highlighting the answer and pressing ◆ mind.
}

/**
 * Renders `<div class="doc-questions">` or nothing. The wrapper with the id
 * (`#steer-docq` / `#editor-docq`) belongs to the overlay, not to this.
 */
export function DocQuestions({
  job,
  mutate,
  onAskBranched,
  readOnly = false,
}: DocQuestionsProps) {
  const list = job.doc_questions || [];
  const slug = job.slug;
  // The same job.live gate ThreadChips puts on its ⑂ branch button: a branch
  // forks the cell's zeus, so it needs a live cell — and it needs somewhere
  // to open the thread, which is the integrator's onAskBranched. Absent
  // either, the rows render as before.
  const askable = !!onAskBranched && job.live;

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

  // Which rows have the ask box open. Component state like `reanswering`, and
  // deliberately NOT stashed across a close: the box reopens in one click, and
  // what must survive is the TEXT, which dqAskDrafts carries.
  const [asking, setAsking] = useState<Set<string>>(new Set());

  // Live ask text per question id — dqAskDrafts' in-render half, seeded and
  // poll-proof exactly as `values` is.
  const [askValues, setAskValues] = useState<Record<string, string>>(() => {
    const seed: Record<string, string> = {};
    for (const q of list) {
      const d = dqAskDrafts.get(draftKey(slug, q.id));
      if (d) seed[q.id] = d;
    }
    return seed;
  });

  const [askBusy, setAskBusy] = useState<Set<string>>(new Set());

  // An ANSWERED question's open ask box is stale state: the row renders as
  // AnsweredRow (no box), and a later click to re-answer would remount the
  // box — its autoFocus pulling the caret into a box the click never asked
  // for. So the id is dropped the moment a row reads as
  // answered, whether by carl's own send or by a poll payload carrying an
  // answer written from another window. Returns `prev` untouched when
  // nothing is stale, so the every-3s poll re-render sets no state.
  useEffect(() => {
    setAsking((prev) => {
      let next: Set<string> | null = null;
      for (const id of prev) {
        const q = list.find((x) => x.id === id);
        if (q?.answer && !reanswering.has(id)) {
          next ??= new Set(prev);
          next.delete(id);
        }
      }
      return next ?? prev;
    });
  }, [list, reanswering]);

  // stash/drop on unmount — see dqDrafts' comment
  const stashRef = useRef({ slug, values, askValues });
  stashRef.current = { slug, values, askValues };
  useEffect(
    () => () => {
      const { slug: s, values: v, askValues: av } = stashRef.current;
      for (const [id, text] of Object.entries(v)) {
        const key = draftKey(s, id);
        if (text) dqDrafts.set(key, text);
        else dqDrafts.delete(key);
      }
      for (const [id, text] of Object.entries(av)) {
        const key = draftKey(s, id);
        if (text) dqAskDrafts.set(key, text);
        else dqAskDrafts.delete(key);
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

  /**
   * The ask box's send: branch a side thread seeded with the question row.
   *
   * Nothing here is new on the wire — it is the SAME `/api/threads` branch the
   * artifact viewer's ask composer posts (ArtifactOverlay's `ask`), with the
   * title and seed built from the question row instead of the open artifact.
   * `text` may be EMPTY, unlike there: "I don't understand this question" is
   * the common case, and the seed builder supplies it as the default ask.
   */
  const askSend = useCallback(
    async (q: WebDocQuestion, text: string) => {
      if (askBusy.has(q.id)) return;
      setAskBusy((prev) => new Set(prev).add(q.id));
      const res = await mutate(() =>
        threadOp(slug, "branch", {
          title: questionThreadTitle(q.id, q.text, text),
          text: questionThreadSeed(q, text),
        }),
      );
      setAskBusy((prev) => {
        const next = new Set(prev);
        next.delete(q.id);
        return next;
      });
      // a failed branch leaves the box open, its text intact, for a retry —
      // same posture as a failed answer post above
      if (!res) return;
      setAsking((prev) => {
        const next = new Set(prev);
        next.delete(q.id);
        return next;
      });
      dqAskDrafts.delete(draftKey(slug, q.id));
      setAskValues((prev) => {
        const next = { ...prev };
        delete next[q.id];
        return next;
      });
      // No tid came back (defensive — the branch op always returns one): the
      // thread exists and the board lists it, so say so out of band, exactly
      // as the artifact composer does.
      if (res.tid) onAskBranched?.(slug, res.tid);
      else showToast("branched — the thread is listed on the job");
    },
    [askBusy, mutate, onAskBranched, slug],
  );

  if (!list.length) return null;
  const sorted = sortDocQuestions(list);
  const hasOpen = sorted.some((q) => !q.answer);

  if (readOnly) {
    return (
      <div className="doc-questions">
        {sorted.map((q) =>
          q.answer ? (
            <AnsweredRow key={q.id} slug={slug} q={q} />
          ) : (
            <ReadOnlyOpenRow key={q.id} q={q} />
          ),
        )}
      </div>
    );
  }

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
            askable={askable}
            asking={asking.has(q.id)}
            askValue={askValues[q.id] ?? ""}
            askDisabled={askBusy.has(q.id)}
            onToggleAsk={() =>
              setAsking((prev) => {
                const next = new Set(prev);
                if (next.has(q.id)) next.delete(q.id);
                else next.add(q.id);
                return next;
              })
            }
            onAskChange={(v) =>
              setAskValues((prev) => ({ ...prev, [q.id]: v }))
            }
            onAskSend={(text) => void askSend(q, text)}
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
  askable,
  asking,
  askValue,
  askDisabled,
  onToggleAsk,
  onAskChange,
  onAskSend,
}: {
  slug: string;
  q: WebDocQuestion;
  reanswering: boolean;
  disabled: boolean;
  value: string;
  onChange: (v: string) => void;
  onSend: (text: string) => void;
  onCancelReanswer: () => void;
  askable: boolean;
  asking: boolean;
  askValue: string;
  askDisabled: boolean;
  onToggleAsk: () => void;
  onAskChange: (v: string) => void;
  onAskSend: (text: string) => void;
}) {
  const imp = (q.importance || "P2").toLowerCase();
  return (
    <div className="dq-row">
      <div className="dq-head">
        <span className={`dq-imp ${imp}`}>{q.importance || "P2"}</span>
        <span className="dq-q" title={q.text}>
          {q.id} — {q.text}
        </span>
        {/* ⑂ alone, not "⑂ ask": the head is a cramped flex row and the
            tooltip carries the words. Same glyph every thread affordance
            wears (BranchButton, the artifact viewer's head toggle). */}
        {askable ? (
          <button
            className="dq-ask-toggle"
            data-slug={slug}
            data-id={q.id}
            title="branch a side thread to ask about this question"
            aria-label="ask about this question"
            aria-expanded={asking}
            onClick={onToggleAsk}
          >
            ⑂
          </button>
        ) : null}
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
      {/* `askable` too, not just `asking`: the cell can die while the box is
          open, and the toggle — the only control that closes it — unmounts
          with `askable`. A box left behind would be uncloseable and could
          only post a branch the server refuses. */}
      {asking && askable ? (
        <div className="dq-ask">
          <input
            className="dq-input dq-ask-input"
            type="text"
            // the parenthetical is the affordance's whole pitch: branching
            // with nothing typed is legitimate here, unlike the artifact
            // composer — the seed supplies the "unpack this" ask
            placeholder="what's unclear? (optional)"
            value={askValue}
            data-slug={slug}
            data-id={q.id}
            disabled={askDisabled}
            autoFocus
            onChange={(e) => onAskChange(e.target.value)}
            onKeyDown={(e) => {
              // Enter-to-branch, with the answer input's own IME/modifier
              // guards — this is prose too. No empty-text bail: an empty
              // Enter is a deliberate "just unpack it" branch.
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
              onAskSend(askValue.trim());
            }}
          />
          <button
            className="dq-ask-send"
            data-slug={slug}
            data-id={q.id}
            disabled={askDisabled}
            onClick={() => onAskSend(askValue.trim())}
          >
            ⑂ branch
          </button>
        </div>
      ) : null}
    </div>
  );
}

/**
 * An OPEN question in a read-only panel: EditableRow's head, context and
 * recommendation with no answer row and no ask toggle beneath them.
 */
function ReadOnlyOpenRow({ q }: { q: WebDocQuestion }) {
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
  /** absent in a read-only panel: the row is then not a re-answer target */
  onReopen?: () => void;
}) {
  const imp = (q.importance || "P2").toLowerCase();
  const answered = q.answered_at ? `answered ${String(q.answered_at)}` : "";
  const title = onReopen
    ? answered
      ? `${answered} — click to re-answer`
      : "click to re-answer"
    : answered || undefined;
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
