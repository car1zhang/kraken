import { useMemo, useRef, useState } from "react";

import { postGoals } from "../../api/client.ts";
import type { WebGoals, WebJobGoal } from "../../api/types.ts";
import type { BoardModel } from "../../hooks/useBoard.ts";
import { useLocalStorageFlag } from "../../hooks/useLocalStorage.ts";
import { goalChipText, groupGoals, type Goal } from "../../lib/goals.ts";

/**
 * The goal chip — ONE token, `◎` plus the goal's text cut to chip width, the
 * full text in `title`, muted when the goal is done.
 *
 * It lives in this file rather than in `chips/` because it is the goals
 * feature's own surface. Its one renderer is the job card's band 2, which
 * draws ONE of these per linked goal — the steer modal's head carried the same
 * chip until the links became editable there, where GoalMultiPicker
 * (overlays/GoalMultiPicker.tsx) now makes the same statement as a control:
 * this job is filed under those goals.
 *
 * A plain span, not a `.chip` button: `.chip` is the artifact overlay's click
 * target, and this one must fall THROUGH to whatever owns the click around it
 * — the card's open-the-drilldown handler. Same choice `.ct-chip` and
 * `.fb-chip` made, for the same reason.
 */
export function GoalChip({ goal }: { goal: WebJobGoal }) {
  return (
    <span
      className={"goal-chip" + (goal.done ? " done" : "")}
      // the FULL text, and nothing else: the tooltip is what pays for the
      // truncation, so it may not spend its one line on anything the chip's
      // own styling already says (done-ness is the muting)
      title={goal.text}
    >
      ◎ {goalChipText(goal.text)}
    </span>
  );
}

/** The board `mutate` ritual, exactly as useBoard publishes it. */
type BoardMutate = BoardModel["mutate"];

export interface GoalsPanelProps {
  /**
   * The board payload's `goals` key. Undefined is a legal state and not an
   * error — a server predating goals sends no key — and it renders as an empty
   * panel with its add row live rather than as nothing at all.
   */
  goals: WebGoals | undefined;
  /** BoardModel.mutate. Every write below goes through it; see useBoard. */
  mutate: BoardMutate;
  /**
   * The goal the board is currently filtered to, or null. The panel is the
   * filter's SETTER and not its owner — App holds the state and the board bar
   * shows it, because this panel collapses to a one-line pill and a filter
   * whose only sign is inside a collapsed box is a board that has silently
   * stopped showing half its cards.
   */
  goalFilter: number | null;
  /** Set (or, with null, clear) the board's goal filter. */
  onGoalFilter: (id: number | null) => void;
  /**
   * goal id → how many jobs on the board link it (`goalJobCounts`, called by
   * BoardView off the payload it already has — every job, archived ones
   * included, and their `goals` arrays alone, which is what keeps this number
   * in step with what the filter actually shows). The number beside a row
   * answers "is there anything behind this filter" before carl spends a click
   * finding out. Ids with no jobs are simply ABSENT, which is how a row knows
   * to draw no badge at all.
   */
  counts: Map<number, number>;
}

/**
 * The GOALS panel: a persistent box pinned to the board's BOTTOM-RIGHT corner,
 * collapsible to a one-line pill, default EXPANDED.
 *
 * Fixed to the viewport rather than placed in the column grid — it stays put
 * while the board scrolls, which is carl's whole reason for wanting it: the
 * week's goals should be readable from wherever the board happens to be
 * scrolled to. `position: fixed` also means it is OUT of the columns' flow, so
 * it can never widen or reflow them.
 *
 * Z-ORDER is the other half of that: style.css parks it under the overlay band
 * (20+), so any open modal covers it. A floating box that outranked the steer
 * modal would be a permanent chip out of the corner of every drilldown.
 *
 * BOARD VIEW ONLY. BoardView drops it entirely while the board is hidden — a
 * `fixed` element is not inside the `hidden` container that would otherwise
 * hide it, so the LOG, DREAM and GRAPH views would each get a goals panel
 * floating over them.
 *
 * THE WRITE SURFACE FOR GOALS, full stop. Nothing else in the product creates
 * one: `kraken goals get` reads, the editor's picker only links a job to a
 * goal that already exists, and the iOS drawer only ticks one off. So the
 * panel carries all three adds — a week goal (the week lane's row), a day goal
 * under a week goal (each week row's nested row), and an unassigned day goal
 * (the bottom row) — plus rename, done and delete on every row. Take an add
 * row away and the shape it creates becomes unreachable from a fresh install.
 *
 * THE BOARD FILTER'S SETTER, too. On a row that is NOT mid-rename, a click
 * anywhere outside its four click-stopping controls — the text span (rename),
 * the checkbox (done), the × (delete) and, while it is up, the inline-edit
 * input — filters the board to that goal, and a second such click on the row
 * already filtering clears it. Those controls keep their meanings by stopping
 * the click themselves, which is the same locally-expressed precedence the job
 * card's chips use; a delegated "was the target the text?" test here would have
 * to know every future control the row grows. The mid-rename EXCEPTION is the
 * one thing that gesture yields to: while this row is the one being edited it
 * carries no filter click at all, because there its dead space already means
 * "abandon the edit" (the input's blur), and one press doing both would fire
 * two unrelated things, one of them silently.
 *
 * WRITES. Every one is a POST /api/goals through the board `mutate` ritual —
 * pessimistic, no optimistic local copy: the goals live on the board payload,
 * so the refetch `mutate` performs IS the panel's next render, and a refused
 * write leaves the panel showing what the server actually has with the
 * server's own sentence in the status corner. That is the same trade every
 * other board write makes, and it is why there is no goal state in this
 * component beyond which row is being edited and what is typed into the add
 * inputs.
 */
export function GoalsPanel({
  goals,
  mutate,
  goalFilter,
  onGoalFilter,
  counts,
}: GoalsPanelProps) {
  // DEFAULT OPEN — the panel is the point of the feature, so the state carl
  // never touches is the one that shows the goals.
  const [open, setOpen] = useLocalStorageFlag("kraken-goals-open", true);
  // Which row is in inline-edit, and what is typed into it. Panel-level rather
  // than per-row so the edit is EXCLUSIVE: clicking a second goal's text ends
  // the first edit rather than leaving two live inputs on screen.
  const [editing, setEditing] = useState<number | null>(null);
  const [draft, setDraft] = useState("");

  const groups = useMemo(
    () => groupGoals(goals?.week ?? [], goals?.day ?? []),
    [goals],
  );
  const { weekDone, weekTotal, dayDone, dayTotal } = groups.counts;
  // `countsLine`, not `counts`: that name belongs to the per-goal job counts
  // this panel now takes as a prop, and the head's done/total summary is a
  // different number about a different thing.
  const countsLine = `week ${weekDone}/${weekTotal} · today ${dayDone}/${dayTotal}`;

  const post = (body: unknown) => void mutate(() => postGoals(body));

  /**
   * Ids with a done-toggle in flight.
   *
   * A ref rather than state, because it is a GUARD and not a rendered fact: it
   * has to be read and written synchronously within one click handler, where
   * a state update is still queued and the second click of a double-click
   * would read the old value and post again. That second post is not
   * harmless — the checkbox stays visually unchecked until the board refetch
   * lands, so the goal reads as untouched for a beat, and the pair of writes
   * resolves to `done` or `not done` depending on which POST the server
   * happens to answer last. (The iOS drawer guards the same way, for the same
   * reason.)
   */
  const toggling = useRef<Set<number>>(new Set());
  const toggleDone = async (g: Goal) => {
    if (toggling.current.has(g.id)) return;
    toggling.current.add(g.id);
    try {
      await mutate(() => postGoals({ op: "update", id: g.id, done: !g.done }));
    } finally {
      // released on failure too: a refused toggle must be retryable, and
      // `mutate` has already put the server's sentence in the status corner
      toggling.current.delete(g.id);
    }
  };
  /**
   * Delete, and — when the board is filtered to the goal being deleted — the
   * clearing of that filter in the same gesture. The row is about to leave the
   * panel and the server drops every link with it (`ON DELETE CASCADE`), so a
   * filter left pointing at the id survives as a board showing no cards, a bar
   * chip that can only name the goal `#<id>`, and no row anywhere to reclick
   * — every way back gone at once. Cleared BEFORE the post rather than after:
   * the filter is local state that nothing on the server owns, so it does not
   * wait on a write, and a refused delete leaves an unfiltered board rather
   * than an empty one.
   */
  const remove = (g: Goal) => {
    if (goalFilter === g.id) onGoalFilter(null);
    post({ op: "remove", id: g.id });
  };

  const startEdit = (g: Goal) => {
    setEditing(g.id);
    setDraft(g.text);
  };
  const cancelEdit = () => {
    setEditing(null);
    setDraft("");
  };
  /**
   * Enter on an inline edit. An empty (or whitespace-only) draft is NOT a
   * delete — the store refuses empty text, and a stray ⌫-then-Enter must not
   * be a way to lose a goal — so it reverts, exactly as Esc does. Text that
   * did not change posts nothing: an UpdateGoal would still stamp updated_at,
   * and a no-op write that moves a timestamp is a lie about when carl last
   * touched the goal.
   */
  const commitEdit = (g: Goal) => {
    const text = draft.trim();
    if (text && text !== g.text) post({ op: "update", id: g.id, text });
    cancelEdit();
  };

  /**
   * The one add. THREE rows call it and they differ only in their arguments:
   * the week lane's row adds a `week` goal, the bottom row adds an unassigned
   * `day` goal, and each week row's nested row adds a `day` goal already
   * parented to that week goal.
   *
   * `period` is deliberately never sent: the server defaults an absent period
   * to the CURRENT one for that scope, computed from its own clock in its own
   * timezone. A period computed here would be the browser's idea of today's
   * date and of which Monday this week starts on, which is the same
   * arithmetic done twice, in two places, against two clocks.
   */
  const add = (scope: Goal["scope"], text: string, parentID?: number) =>
    post({
      op: "add",
      goal: {
        scope,
        text,
        // omitted rather than sent as 0: 0 and "no parent" are the same
        // statement on the wire and the omission is the honest one
        ...(parentID ? { parent_id: parentID } : {}),
      },
    });

  const row = (g: Goal, sub: boolean) => {
    const filtering = goalFilter === g.id;
    const n = counts.get(g.id) ?? 0;
    // A row mid-rename has NO filter gesture. Its dead space is the abandon-
    // the-edit target — the input's own blur reverts, which is the whole
    // interaction — and letting the same press also filter the board would
    // fire two unrelated things off one click, one of them silently.
    const editingThis = editing === g.id;
    return (
      <div
        key={g.id}
        className={
          "goal" +
          (g.done ? " done" : "") +
          (sub ? " sub" : "") +
          (filtering ? " filtering" : "")
        }
        data-goal={g.id}
        // the row's OWN gesture: filter the board to this goal, or clear the
        // filter when this row is already the one filtering. Every control
        // inside stops its own click below, so this handler only ever sees a
        // press on the row's dead space — and while THIS row is being renamed
        // that dead space means "abandon the edit" instead (see editingThis).
        title={
          editingThis
            ? undefined
            : filtering
              ? "clear the goal filter"
              : "filter the board to this goal"
        }
        onClick={editingThis ? undefined : () => onGoalFilter(filtering ? null : g.id)}
      >
        {sub ? <span className="arr">↳</span> : null}
        <input
          type="checkbox"
          className="cb"
          checked={g.done}
          title={g.done ? "mark not done" : "mark done"}
          aria-label={g.text}
          onChange={() => void toggleDone(g)}
          // a tick is a tick, not a filter: the change fires a click that would
          // otherwise bubble to the row and quietly filter the board too
          onClick={(e) => e.stopPropagation()}
        />
        {editing === g.id ? (
          <input
            // `.goal-edit` alone, NOT `t goal-edit`: style.css's single
            // cursor-owner list carries `.goal .t` for the clickable text span,
            // and a class that matched this input too would beat the sheet's
            // text-field cursor rule in dark theme (0,2,0 over
            // `input:not([type])`'s 0,1,1) and put the pointer reticle on a live
            // text field. The layout `t` used to give it is declared on
            // `.goal-edit` itself.
            className="goal-edit"
            value={draft}
            autoFocus
            spellCheck={false}
            aria-label="edit goal"
            // clicking into the text being edited is not a click on the row —
            // caret placement must not also toggle the board's filter
            onClick={(e) => e.stopPropagation()}
            onChange={(e) => setDraft(e.target.value)}
            // Esc reverts and Enter saves, per the panel's contract. BLUR
            // reverts too — the same answer Esc gives — because the alternative
            // (save on blur) turns clicking any other control on the board,
            // including this row's own ×, into a silent write of half-typed
            // text.
            onBlur={cancelEdit}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                commitEdit(g);
              } else if (e.key === "Escape") {
                e.preventDefault();
                // stop the ESC from also reaching the view's escape stack — an
                // abandoned edit is not a request to close anything
                e.stopPropagation();
                cancelEdit();
              }
            }}
          />
        ) : (
          // the FULL text in the tooltip, the same rule the card chip follows:
          // the row truncates with an ellipsis at panel width, so the tooltip is
          // what pays for that cut and cannot spend its one line on "click to
          // edit" — an affordance the pointer cursor already states
          <span
            className="t"
            title={g.text}
            onClick={(e) => {
              // the text span still means RENAME, so it takes the click before
              // the row's filter gesture ever sees it
              e.stopPropagation();
              startEdit(g);
            }}
          >
            {g.text}
          </span>
        )}
        {/* the filter affordance: invisible until the row is hovered (or is
            the one filtering), where it marks the gesture the dead space
            carries. Purely decorative — the whole row is the click target, so
            a glyph a screen reader read out would be one more thing to skip
            past. Dropped entirely while this row is being renamed, since the
            gesture it marks is not on offer there. */}
        {editingThis ? null : (
          <span className="funnel" aria-hidden="true">
            ⧩
          </span>
        )}
        {/* how many jobs link this goal, shown only when there ARE any: a
            badge reading 0 on every fresh goal would be noise on the panel's
            most common state, and an empty filter is exactly the click worth
            not spending */}
        {n > 0 ? (
          <span className="njobs" title={`${n} job${n === 1 ? "" : "s"} linked`}>
            {n}
          </span>
        ) : null}
        <button
          className="x"
          title="delete this goal"
          aria-label={`delete ${g.text}`}
          // delete is a delete: without this the same press would also file
          // the board under a goal that is about to stop existing
          onClick={(e) => {
            e.stopPropagation();
            remove(g);
          }}
        >
          ×
        </button>
      </div>
    );
  };

  const body = (
    <div className="gs-body">
      {/* the WEEK lane heads the panel and is rendered UNCONDITIONALLY, even
          with no week goals in it — its add row is the only surface anywhere
          that creates one. (`kraken goals get` reads; the editor picker only
          links a job to a goal that already exists; the iOS drawer only
          toggles.) Hiding the lane until a week goal existed left the nesting
          half of the data model unreachable from a fresh install. The period
          rides the head only once there is a goal to date. */}
      <div className="lane-h">
        week{" "}
        {weekTotal > 0 ? (
          <span className="sub">{groups.week[0]!.goal.period} →</span>
        ) : null}
      </div>
      {groups.week.map((wg) => (
        <div className="goal-group" key={wg.goal.id}>
          {row(wg.goal, false)}
          {wg.days.map((d) => row(d, true))}
          {/* the nested add: whatever is typed here becomes a day goal
              ALREADY parented to this week goal, which is the only way the
              panel creates a nested one */}
          <AddGoalRow
            sub
            placeholder="day goal…"
            onAdd={(text) => add("day", text, wg.goal.id)}
          />
        </div>
      ))}
      {/* the week lane's own add row, LAST in the lane so it reads as the
          bottom of that list rather than as a header control */}
      <AddGoalRow placeholder="week goal…" onAdd={(text) => add("week", text)} />
      <div className="lane-h">
        today — unassigned{" "}
        {groups.unassigned.length ? (
          <span className="sub">{groups.unassigned[0]!.period}</span>
        ) : null}
      </div>
      {groups.unassigned.map((d) => row(d, false))}
      {weekTotal === 0 && dayTotal === 0 ? (
        <div className="gs-empty">no goals yet — add one below</div>
      ) : null}
      {/* the plain add row: an UNASSIGNED day goal, per the mockup — today's
          work that is not filed under any of the week's goals. Filing it under
          one is the nested row above; there is no scope switch on this input,
          because a row that could quietly create either kind is a row you have
          to read before typing into. */}
      <AddGoalRow placeholder="today's goal…" onAdd={(text) => add("day", text)} />
    </div>
  );

  return (
    // the id is on the DOCK, not on the inner `.goals-panel` box: the box is
    // gone while the panel is collapsed, and a hook that disappears with the
    // state it is used to inspect is no hook at all
    <div className="goals-dock" id="goals-dock">
      {open ? (
        <div className="goals-panel">
          <button
            className="gs-head"
            id="goals-toggle"
            title="collapse the goals panel"
            onClick={() => setOpen(false)}
          >
            <span className="tri">▾</span> GOALS <span className="n">{countsLine}</span>
          </button>
          {body}
        </div>
      ) : (
        <button
          className="goals-pill"
          id="goals-toggle"
          title="expand the goals panel"
          onClick={() => setOpen(true)}
        >
          <span className="tri">▸</span> GOALS <span className="n">{countsLine}</span>
        </button>
      )}
    </div>
  );
}

/**
 * A bare add input that COMMITS ON ENTER AND STAYS PUT — it clears itself and
 * keeps focus, so a run of goals is typed without ever reaching for the mouse.
 * There is no add BUTTON beside it for the same reason: the row is one
 * keystroke wide.
 *
 * Its text is local state and nothing else's business: the write goes out
 * through the panel's `mutate`, and what comes back is a fresh board payload
 * with the new row already in it.
 *
 * THREE of these render, and the row itself knows about none of that — what a
 * given row creates lives entirely in the `onAdd` its lane passes (a week
 * goal, an unassigned day goal, or a day goal parented to one week goal). The
 * `placeholder` is therefore the row's only statement of what it makes, which
 * is why each one names its scope rather than saying "add…".
 */
function AddGoalRow({
  placeholder,
  sub,
  onAdd,
}: {
  placeholder: string;
  sub?: boolean;
  onAdd: (text: string) => void;
}) {
  const [text, setText] = useState("");
  return (
    <div className={"goal-add" + (sub ? " sub" : "")}>
      {sub ? <span className="arr">↳</span> : null}
      <span className="plus">+</span>
      <input
        className="t"
        placeholder={placeholder}
        aria-label={placeholder}
        spellCheck={false}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            setText("");
            return;
          }
          if (e.key !== "Enter") return;
          e.preventDefault();
          const t = text.trim();
          // an empty Enter is a no-op, not a 400: the store refuses blank text
          // and there is nothing to report about a key carl pressed by reflex
          if (!t) return;
          setText("");
          onAdd(t);
        }}
      />
    </div>
  );
}
