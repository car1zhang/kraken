import { useCallback, useEffect, useMemo, useState } from "react";

import { fetchGoals, postJobGoal } from "../../api/client.ts";
import type { WebJobGoal } from "../../api/types.ts";
import { goalChipText, type Goal } from "../../lib/goals.ts";
import { FieldSelect } from "./FieldSelect.tsx";
import type { Mutate } from "./Overlay.tsx";

export interface GoalPickerProps {
  /** Lands on FieldSelect's button, so each host keeps its own stable id
   * ("f-goal" in the editor, "steer-goal" in the steer head). */
  id: string;
  slug: string | null;
  /** The job's linked goal off the board payload — the picker's current
   * value, and the row it appends for an off-period link. */
  goal: WebJobGoal | undefined;
  mutate: Mutate;
  /** False skips the option-list fetch — the archived editor's case: an
   * archived job's link cannot be changed (`/api/job/goal` resolves live jobs
   * only), so a menu of pickable goals would be an offer the server refuses.
   * The fetch itself (GET /api/goals) is not per-job; what the gate saves is
   * the pointless ask, not a failing one. The WRITE stays live like the
   * colour strip's (see the editor's f-colors comment): a pick on an archived
   * job is refused server-side, and the pending value snaps back. */
  active?: boolean;
  /** Label truncation width, in goalChipText's terms. Default 28, the editor
   * form row's width; the steer HEAD passes less, because there the label is
   * a rigid block in a one-line strip where only the title gives. */
  cut?: number;
}

/**
 * The one control that links a job to a goal, shared by the job editor's form
 * and the steer modal's head. It lives in its own file because its two hosts
 * make the same statement — "this job is filed under that goal" — and two
 * copies of the label-building and pick logic would drift on the first change
 * to either. The board routes a DRAFT to the editor but a live
 * working/blocked card to the steer modal, so a picker that existed only in
 * the editor was reachable exactly when filing a job under a goal matters
 * least. `/api/job/goal` is its own ungated one-field route precisely so a
 * RUNNING job can be linked; this component is what puts that within reach on
 * both surfaces.
 *
 * The option list — the CURRENT week's and today's goals — is fetched
 * straight from GET /api/goals rather than read off the board payload: both
 * hosts are handed ONE job and no payload, and an optional `goals` prop the
 * integrator has to remember to pass is a picker that renders empty and says
 * nothing. A failure degrades to the linked goal alone rather than painting
 * an error over a surface that is otherwise fine: the goal link is not what
 * carl opened either modal for.
 *
 * Not refetched after a pick. The LIST is the period's goals and a pick does
 * not change it; what the pick changes is `job.goal`, which arrives on the
 * board refetch `mutate` already performs.
 */
export function GoalPicker({ id, slug, goal, mutate, active = true, cut = 28 }: GoalPickerProps) {
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

  /**
   * FieldSelect speaks in STRINGS, so each goal needs one label that is both
   * what the menu row reads and what the button shows when it is selected —
   * the two cannot differ, or the tick would never land on the current row.
   *
   * Labels carry the scope (`wk` / `day`) the way the mockup's menu does, and
   * are cut to chip width so a long goal cannot stretch the host row. A goal
   * linked from a period that is no longer current is appended WITHOUT a scope
   * prefix (its scope is not on the wire — `job.goal` is id/text/done) and
   * only when the current-period list does not already hold it, so an
   * off-period link round-trips instead of silently reading as "—". Collisions
   * (two goals whose truncated text matches) are broken with the id, since two
   * identical rows would be a picker that cannot say which one it picked.
   */
  const goalOptions = useMemo(() => {
    const options: string[] = [];
    const idByLabel = new Map<string, number>();
    const labelById = new Map<number, string>();
    // gid, not id — that name is taken by the component's own prop, and a
    // shared component is where a shadow costs the next reader most
    const push = (gid: number, text: string, prefix: string) => {
      if (labelById.has(gid)) return;
      const base = prefix + goalChipText(text, cut);
      const label = idByLabel.has(base) ? `${base} #${gid}` : base;
      options.push(label);
      idByLabel.set(label, gid);
      labelById.set(gid, label);
    };
    for (const g of goalList) push(g.id, g.text, g.scope === "week" ? "wk " : "day ");
    if (goal) push(goal.id, goal.text, "");
    return { options, idByLabel, labelById };
  }, [goalList, goal, cut]);

  /**
   * The pick, on the COLOUR strip's pattern rather than any debounced form
   * autosave: `/api/job/goal` is its own ungated one-field route, so it works
   * while a cell is in flight — which is when filing a running job under a
   * goal is most useful — and a full-replace `saveform` can never wipe the
   * link by omitting it.
   *
   * `pendingGoal` shows the pick instantly and is cleared when the write
   * settles, handing authority back to the board payload: a REFUSED pick snaps
   * back to whatever the server actually has. `null` is "nothing pending",
   * which is why it is not 0 — 0 is the unlink carl may genuinely have picked.
   */
  const [pendingGoal, setPendingGoal] = useState<number | null>(null);
  const pickGoal = useCallback(
    async (label: string) => {
      if (!slug) return;
      const id = label ? (goalOptions.idByLabel.get(label) ?? 0) : 0;
      setPendingGoal(id);
      await mutate(() => postJobGoal(slug, id));
      setPendingGoal(null);
    },
    [goalOptions, mutate, slug],
  );
  const goalValue =
    goalOptions.labelById.get(pendingGoal ?? goal?.id ?? 0) ?? "";

  return (
    <FieldSelect
      id={id}
      label="goal"
      value={goalValue}
      options={goalOptions.options}
      onPick={(g) => void pickGoal(g)}
    />
  );
}
