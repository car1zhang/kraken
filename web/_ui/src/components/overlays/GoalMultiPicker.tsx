import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { fetchGoals, postJobGoal } from "../../api/client.ts";
import type { WebJobGoal } from "../../api/types.ts";
import { useMenuDismiss } from "../../hooks/useMenuDismiss.ts";
import { goalChipText, type Goal } from "../../lib/goals.ts";
import type { Mutate } from "./Overlay.tsx";

export interface GoalMultiPickerProps {
  /** Lands on the menu's BUTTON, so each host keeps its own stable id
   * ("f-goal" in the editor, "steer-goal" in the steer head). */
  id: string;
  slug: string | null;
  /** The job's linked goals off the board payload, in link-position order —
   * the picker's current value, and the rows it appends for an off-period
   * link. Undefined is the unlinked state (the wire drops the key). */
  goals: WebJobGoal[] | undefined;
  mutate: Mutate;
  /** False skips the option-list fetch — the archived editor's case: an
   * archived job's links cannot be changed (`/api/job/goal` resolves live jobs
   * only), so a menu of pickable goals would be an offer the server refuses.
   * The fetch itself (GET /api/goals) is not per-job; what the gate saves is
   * the pointless ask, not a failing one. The WRITE stays live like the
   * colour strip's (see the editor's f-colors comment): a pick on an archived
   * job is refused server-side, and the pending set snaps back. */
  active?: boolean;
  /** Label truncation width, in goalChipText's terms. Default 28, the editor
   * form row's width; the steer HEAD passes less, because there the label is
   * a rigid block in a one-line strip where only the title gives. */
  cut?: number;
}

/**
 * The em-dash this control has always used for "no goal", read TWICE and
 * meaning something slightly different each time — which is why it is one
 * constant rather than two literals that could drift apart on screen.
 *
 * On the leading MENU ROW it is an action: clear every link. On the BUTTON it
 * is a state: this job links nothing. They are the same glyph on purpose —
 * the row is the button's reading turned into a thing you can press.
 */
const EMPTY = "—";

/**
 * The one control that files a job under GOALS, shared by the job editor's
 * form and the steer modal's head. It lives in its own file because its two
 * hosts make the same statement — "this job is filed under those goals" — and
 * two copies of the label-building and pick logic would drift on the first
 * change to either. The board routes a DRAFT to the editor but a live
 * working/blocked card to the steer modal, so a picker that existed only in
 * the editor was reachable exactly when filing a job matters least.
 * `/api/job/goal` is its own ungated one-field route precisely so a RUNNING
 * job can be linked; this component is what puts that within reach on both
 * surfaces.
 *
 * MULTI-SELECT, and the shape of the write follows from that: each row is a
 * toggle posting ONE link — `add` for an unticked row, `remove` for a ticked
 * one — rather than a set the button posts whole. A full-replace write from a
 * client holding a stale list would silently drop a link some other surface
 * (the other host, another tab, a cell) made in between; a per-link op cannot.
 * The menu also stays OPEN across picks, because filing a job under two goals
 * is one gesture in carl's head and a menu that closed on the first would make
 * it two.
 *
 * It is a sibling of FieldSelect rather than a mode of it: FieldSelect speaks
 * in strings and a single value, and both of those are the wrong shape here
 * (ids, and a set). It wears the same `.colorsel*` skin so the two read as one
 * family — the ticks and the menu-stays-open behaviour are the only visible
 * differences.
 *
 * The option list — the CURRENT week's and today's goals — is fetched straight
 * from GET /api/goals rather than read off the board payload: both hosts are
 * handed ONE job and no payload, and an optional `goals` prop the integrator
 * has to remember to pass is a picker that renders empty and says nothing. A
 * failure degrades to the linked goals alone rather than painting an error
 * over a surface that is otherwise fine: the goal links are not what carl
 * opened either modal for.
 *
 * Not refetched after a pick. The LIST is the period's goals and a pick does
 * not change it; what the pick changes is the job's links, which arrive on the
 * board refetch `mutate` already performs.
 */
export function GoalMultiPicker({
  id,
  slug,
  goals,
  mutate,
  active = true,
  cut = 28,
}: GoalMultiPickerProps) {
  const [goalList, setGoalList] = useState<Goal[]>([]);
  useEffect(() => {
    if (!slug || !active) return;
    const ac = new AbortController();
    fetchGoals(ac.signal)
      .then((d) => {
        if (ac.signal.aborted) return;
        setGoalList([...(d.goals?.week ?? []), ...(d.goals?.day ?? [])]);
      })
      .catch(() => {
        // deliberately silent — see above
      });
    return () => ac.abort();
  }, [active, slug]);

  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLSpanElement | null>(null);
  useMenuDismiss(open, setOpen, rootRef);

  /**
   * The pick, shown before the server has answered.
   *
   * `null` is "nothing pending" and hands authority to the payload; a Set is
   * carl's in-flight view of the link set. It is a SET rather than one id
   * because two rows can be picked before either write lands, and the second
   * pick has to see the first one's effect or it would post against a link
   * set that no longer matches what is on screen. A REFUSED post snaps the
   * whole thing back to whatever the server actually has, since the pending
   * set is dropped once every write in flight has settled — counted, so an
   * earlier write finishing cannot hand the payload authority back while a
   * later one is still out.
   */
  const [pending, setPending] = useState<ReadonlySet<number> | null>(null);
  const inFlight = useRef(0);

  const linked = useMemo<ReadonlySet<number>>(
    () => pending ?? new Set((goals ?? []).map((g) => g.id)),
    [pending, goals],
  );

  /**
   * One label per goal, and the map back to its id.
   *
   * Labels carry the scope (`wk` / `day`) the way the mockup's menu does, and
   * are cut to chip width so a long goal cannot stretch the host row. A goal
   * linked from a period that is no longer current is appended WITHOUT a scope
   * prefix (its scope is not on the wire — a linked goal is id/text/done) and
   * only when the current-period list does not already hold it, so an
   * off-period link round-trips instead of silently disappearing from the
   * menu. Collisions (two goals whose truncated text matches) are broken with
   * the id, since two identical rows would be a picker that cannot say which
   * one it picked.
   */
  const rows = useMemo(() => {
    const out: { id: number; label: string }[] = [];
    const seen = new Set<number>();
    const labels = new Set<string>();
    // gid, not id — that name is taken by the component's own prop, and a
    // shared component is where a shadow costs the next reader most
    const push = (gid: number, text: string, prefix: string) => {
      if (seen.has(gid)) return;
      const base = prefix + goalChipText(text, cut);
      const label = labels.has(base) ? `${base} #${gid}` : base;
      out.push({ id: gid, label });
      seen.add(gid);
      labels.add(label);
    };
    for (const g of goalList) push(g.id, g.text, g.scope === "week" ? "wk " : "day ");
    for (const g of goals ?? []) push(g.id, g.text, "");
    return out;
  }, [goalList, goals, cut]);

  /**
   * The button's own reading of the link set: nothing, the one goal's text, or
   * a bare count. A count at two links and up rather than a joined list — the
   * button is a rigid block in a one-line strip, and two truncated texts side
   * by side say less than one number does.
   */
  const label = useMemo(() => {
    if (linked.size === 0) return EMPTY;
    if (linked.size > 1) return `${linked.size} goals`;
    const only = [...linked][0]!;
    const text =
      goals?.find((g) => g.id === only)?.text ??
      goalList.find((g) => g.id === only)?.text;
    // A pending link to a goal neither list can name (a stale payload beside a
    // failed option fetch) still has to read as something: the id is the one
    // fact in hand, and it is what the collision suffix already uses.
    return text ? goalChipText(text, cut) : `#${only}`;
  }, [linked, goals, goalList, cut]);

  /**
   * One pick: show it, send it, and hand authority back when the last write
   * out has settled.
   *
   * `next` is an UPDATER rather than a finished set, and that is the whole
   * difference between two picks agreeing and the second erasing the first:
   * two rows clicked inside one React batch both build on the pending value
   * React actually holds, where a set computed from the render snapshot would
   * be built twice from the same pre-pick state. (The server ends up right
   * either way — the two posts are independent per-link ops — so the bug this
   * shape prevents is a transiently wrong DISPLAY, which is exactly what an
   * optimistic set exists to get right.) An updater must be pure, so the op
   * itself is decided by the caller and only the set is computed here.
   *
   * The counter is released in `finally` because the reset is an INVARIANT of
   * this function rather than a favour from `mutate`: `mutate` is documented
   * never to throw, but a leaked count would strand the pending set forever —
   * every later pick optimistic and nothing ever snapping back to the payload
   * — and that is too quiet a failure to inherit from another module's
   * docblock.
   */
  const post = useCallback(
    async (
      op: () => Promise<unknown>,
      next: (prev: ReadonlySet<number>) => ReadonlySet<number>,
    ) => {
      setPending((prev) => next(prev ?? new Set((goals ?? []).map((g) => g.id))));
      inFlight.current += 1;
      try {
        await mutate(op);
      } finally {
        inFlight.current -= 1;
        // only the LAST write in flight hands authority back to the payload —
        // an earlier one doing it would flash the pre-pick state under a pick
        // that is still going out
        if (inFlight.current === 0) setPending(null);
      }
    },
    [goals, mutate],
  );

  const toggle = useCallback(
    (gid: number) => {
      if (!slug) return;
      // WHICH op is decided from what this render shows, since that is what
      // carl clicked: a ticked row means remove, an unticked one add.
      const on = linked.has(gid);
      void post(
        () => postJobGoal(slug, gid, on ? "remove" : "add"),
        (prev) => {
          const next = new Set(prev);
          if (on) next.delete(gid);
          else next.add(gid);
          return next;
        },
      );
    },
    [linked, post, slug],
  );

  const clearAll = useCallback(() => {
    if (!slug) return;
    // goal_id 0 with no op is the clear-all the route has always taken — one
    // request rather than N removes, so a job filed under several goals is
    // unfiled in one write that cannot half-succeed
    // the one updater that ignores `prev`: clear-all is absolute, so whatever
    // an in-flight pick left pending is exactly what this is undoing
    void post(() => postJobGoal(slug, 0), () => new Set<number>());
  }, [post, slug]);

  const row = (key: string, text: string, ticked: boolean, onPick: () => void) => (
    <span
      key={key}
      className={ticked ? "colorsel-row gm-row sel" : "colorsel-row gm-row"}
      role="option"
      aria-selected={ticked}
      // NOT closed on a pick — the whole point of the multi-select. The menu
      // is dismissed by the button, an outside press or Escape, all of which
      // useMenuDismiss and the button below already own.
      onClick={onPick}
    >
      {/* an empty box rather than a transparent ✓ when unticked: the width is
          held by the class, and a glyph nobody can see would still be read out
          by a screen reader and would still land in the row's accessible
          name */}
      <span className="gm-tick" aria-hidden="true">
        {ticked ? "✓" : ""}
      </span>
      <span className="gm-lbl">{text}</span>
    </span>
  );

  return (
    <span ref={rootRef} className="colorsel gm">
      <button
        type="button"
        id={id}
        className="colorsel-btn"
        // composed with the current reading, exactly as FieldSelect does it:
        // aria-label OVERRIDES content, so a bare field name would swallow the
        // selection
        aria-label={`goals: ${label}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        {label} <span className="caret">▾</span>
      </button>
      {open ? (
        <span className="colorsel-menu gm-menu" role="listbox" aria-multiselectable="true">
          {/* the clear-all leads the menu, where the single-select picker's
              "none" row led it: same position, and now the only row that is
              not a toggle */}
          {row("__clear__", EMPTY, false, clearAll)}
          {rows.map((r) => row(String(r.id), r.label, linked.has(r.id), () => toggle(r.id)))}
        </span>
      ) : null}
    </span>
  );
}
