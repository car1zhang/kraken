import { useMemo } from "react";

import type { BoardModel } from "../../hooks/useBoard.ts";
import type { JobSearchModel } from "../../hooks/useJobSearch.ts";
import { COLUMNS } from "../../lib/columns.ts";
import { goalJobCounts } from "../../lib/goals.ts";
import { jobKey } from "../../lib/jobKey.ts";
import type { SteerMode } from "../../lib/moves.ts";
import { ArchiveDrawer } from "./ArchiveDrawer.tsx";
import { BoardBar } from "./BoardBar.tsx";
import { BoardColumn } from "./Column.tsx";
import { GoalsPanel } from "./GoalsPanel.tsx";
import { JobCard } from "./JobCard.tsx";

/**
 * Everything a card can do that this slice does not own. All of them are
 * props: the overlays (steer, editor, artifact viewer, thread) live in another
 * slice, and the mutations live behind BoardModel.mutate, so the board surface
 * itself performs no fetches and holds no global state.
 */
export interface BoardActions {
  /**
   * Steer / unblock / reactivate composer for one job. The options bag matches
   * what DragProvider passes, so ONE callback can serve both the cards and the
   * drop table; `requireText` is only ever set by a done→working drag.
   */
  onOpenSteer(
    slug: string,
    mode: SteerMode,
    opts?: {
      requireText?: boolean;
      /**
       * The OWNER node of a remote card (its `owner.node_id`). Present only
       * when a remote card opened the modal; it is half the card's identity
       * (lib/jobKey.ts), so a slug alone would find the local namesake.
       */
      node?: string;
    },
  ): void;
  /** Job editor. `null` opens it in new-job mode (the "+ NEW JOB" button). */
  onOpenEditor(slug: string | null): void;
  /**
   * Artifact viewer. `name` is already prefixed "<label>/<artifact>"; `jobKey`
   * is the key of the job it belongs to — jobKey(), never a bare slug, since
   * the viewer's pager rebuilds that job's ORDERED artifact list from it and
   * an archived job plus a live one that reused its freed slug would page
   * through each other's artifacts.
   *
   * No card calls this any more: band 2's artifact surface is an inert COUNT
   * and clicking the card opens the drilldown instead (that is what
   * `expect(actions.onOpenArtifact).not.toHaveBeenCalled()` pins in the card
   * tests). It stays on BoardActions because the drilldown surfaces reached
   * FROM a card — the steer modal's chip row and the editor's artifact list —
   * take the same action, and one shape across every entry point is what keeps
   * the jobKey from being dropped at one of them.
   */
  onOpenArtifact(url: string, name: string, jobKey: string): void;
  /** Side-thread overlay. `tid` null opens the branch dialog. */
  onOpenThread(slug: string, tid: string | null): void;
  /**
   * POST /api/question with the 1-based option index. Returning the mutation's
   * result (`Promise<Error | null>`, as onSaveFeedback does) is STRONGLY
   * preferred over `void`: the option buttons disable themselves the moment one
   * is clicked, and only a returned failure can bring them back — see
   * QuestionBlock.
   */
  onAnswerQuestion(slug: string, optionIndex: number): void | Promise<Error | null>;
  /** POST /api/retry. */
  onRetry(slug: string): void;
  /** POST /api/peers/open {slug} — raises zeus' pane in Ghostty. */
  onOpenTmux(slug: string): void;
  /** POST /api/feedback {id, feedback}. Resolves to the failure, or null. */
  onSaveFeedback(slug: string, id: number, note: string): Promise<Error | null>;
  /** POST /api/feedback {id, delete:true}. The confirm() already happened. */
  onDeleteFeedback(slug: string, id: number): void;
  /** POST /api/unarchive with the bare ARCHIVE name (never the jobKey). */
  onUnarchive(slug: string): void;
  /** POST /api/delete with the bare archive name. The confirm() happened. */
  onDeleteForever(slug: string): void;
}

export interface BoardViewProps {
  board: BoardModel;
  actions: BoardActions;
  /**
   * The search state and its derived render lists. Owned by App.tsx rather
   * than by this component, because DragProvider — which sits ABOVE the board
   * — needs `visibleKeys` from the same instance; two useJobSearch calls would
   * be two query states, and the drag layer would read the one nobody typed
   * into.
   */
  search: JobSearchModel;
  /** The view router hides the board rather than unmounting it, as today. */
  hidden?: boolean;
  /**
   * The goal the board is filtered to, and its setter. App owns the state
   * because the same value has to reach `useJobSearch` — which lives up there
   * with it, for DragProvider's sake — and this view only relays it to the two
   * surfaces that show and set it (the bar's chip, the panel's rows).
   *
   * Optional so a bare `<BoardView board actions search />` still renders a
   * board: without them the filter simply has no setter, which is exactly the
   * state a harness that never wired one is in.
   */
  goalFilter?: number | null;
  onGoalFilter?: (id: number | null) => void;
  /**
   * True while any modal overlay is open, straight from App.tsx's per-overlay
   * state. Only the search bar reads it, and only to keep cmd+K from pulling
   * focus out of a composer — see BoardBar.
   */
  overlayOpen?: boolean;
  /**
   * The fleet's hide-others toggle, relayed to the board bar. App owns the
   * state (and its kraken.hideOthers persistence) because the filter itself
   * runs in useBoard, above this view.
   */
  hideOthers?: boolean;
  onHideOthers?: (hide: boolean) => void;
  /**
   * @deprecated Dead, and accepted only so App.tsx keeps compiling until the
   * lead drops the call site. Band 2 carries a bare artifact COUNT now
   * (ArtifactCountChip), so there is no "+N more" toggle and no per-job
   * expansion state to share with anything. Delete this together with
   * App.tsx's `useLocalStorageSet("kraken-expanded-artifacts")` and the
   * `expanded`/`onToggleExpanded` props on JobCard/ArchivedCard.
   */
  expanded?: { keys: Set<string>; toggle(key: string): void };
}

/**
 * The board: four columns plus the archive drawer under DONE. Port of
 * renderBoardDOM (app.js:814), minus the QUEUED column — submit is
 * synchronous, so there is no parked state between draft and working for a
 * column to hold.
 *
 * The buckets are read straight off BoardModel — which builds them with
 * bucketJobs, so they carry the rule that an unhealthy cell is BLOCKED
 * regardless of its file state (app.js:614). This view used to bucket the flat
 * `jobs` array itself, which was correct in isolation and wrong in aggregate:
 * the drag layer keys its drop table off BoardModel's buckets, so two
 * bucketings meant the column a card was DRAWN in could differ from the column
 * it was DRAGGED from. One bucketing, one answer.
 *
 * Everything is keyed by jobKey(), never by bare slug. An archived job rides
 * the same `jobs` array under a slug that is unique only within the archive,
 * so archiving `foo` and creating a new active `foo` puts two entries with
 * slug "foo" on the board — and a bare-slug key hands the archived one a
 * last-writer-wins victory over the live job, which is exactly how a live
 * blocked job once stopped being steerable.
 *
 * Drag-and-drop is NOT plumbed through props: `<Column>`, `<ArchiveDrawer>`
 * and `<JobCard>` each call the hook DragProvider publishes for them
 * (useColumnDrop / useSortableCard), and those hooks fall back to an inert
 * identity when no `<DragProvider>` is above them — so a board rendered
 * without one still lists every card in server order.
 *
 * The search bar sits above the columns and narrows what is RENDERED, nothing
 * else. Every list below comes from the `search` model App hands in; `board` is
 * passed on untouched, which is what keeps the drag layer — reading
 * `board.byColumn` for both its drop table and its reorder body — seeing whole
 * columns while the screen shows a subset. All four columns still render under
 * any query: there is no status filter, so the grid template never changes.
 */
export function BoardView({
  board,
  actions,
  search,
  hidden,
  goalFilter = null,
  onGoalFilter = () => {},
  overlayOpen,
  hideOthers = false,
  onHideOthers,
}: BoardViewProps) {
  /**
   * How many jobs link each goal, and every goal's text — both from the ONE
   * payload this view already holds, so a per-row count costs no wire key and
   * no second source that could disagree with the cards on screen.
   *
   * The counting itself is `goalJobCounts` (lib/goals.ts), a pure function
   * over the flat job array rather than a loop here: it is the number the
   * panel's badges promise and the board's filter has to deliver, so it is
   * worth being nameable and testable on its own. It is fed
   * `board.board?.jobs` — the flat array, not the bucketed columns — because
   * archived jobs ride that same array, link goals like any other, and the
   * archive drawer is on this board.
   *
   * The text index is this view's own half, and stays here because it is
   * board-shaped rather than count-shaped: the bar's chip has to NAME the
   * filtered goal even when it is not one of the current period's — an
   * off-period goal is absent from `board.goals` and reachable only through
   * the jobs that link it — and a filter whose chip could not name it would be
   * a filter carl can see the effect of and not the cause. Unlike the count it
   * reads the legacy single `goal` too: naming a goal costs nothing and a
   * missing name costs the chip, while COUNTING one the filter would not match
   * costs the badge its honesty.
   */
  // LOCAL cards only: a remote card's goal ids are its owner's goals, and
  // counting them here would credit this board's goal of the same id.
  const allJobs = board.board?.jobs;
  const jobs = useMemo(() => allJobs?.filter((j) => !j.owner), [allJobs]);
  const goalIndex = useMemo(() => {
    const counts = goalJobCounts(jobs ?? []);
    const text = new Map<number, string>();
    for (const g of board.board?.goals?.week ?? []) text.set(g.id, g.text);
    for (const g of board.board?.goals?.day ?? []) text.set(g.id, g.text);
    for (const job of jobs ?? []) {
      for (const g of job.goals ?? []) if (!text.has(g.id)) text.set(g.id, g.text);
      if (job.goal && !text.has(job.goal.id)) text.set(job.goal.id, job.goal.text);
    }
    return { counts, text };
  }, [jobs, board.board?.goals]);

  return (
    <>
      <BoardBar
        search={search}
        // a filtered id the payload cannot name still gets a chip, carrying
        // the id itself: the alternative is a filter with no visible owner
        // and no way back, which is the one state this chip exists to prevent
        goalChip={
          goalFilter === null
            ? undefined
            : { id: goalFilter, text: goalIndex.text.get(goalFilter) ?? `#${goalFilter}` }
        }
        onClearGoal={() => onGoalFilter(null)}
        overlayOpen={overlayOpen}
        hidden={hidden}
        hideOthers={hideOthers}
        // offered only once there IS another kraken (or the toggle is already
        // on, so it can always be switched back off)
        onHideOthers={
          onHideOthers && ((board.board?.nodes?.length ?? 0) > 0 || hideOthers)
            ? onHideOthers
            : undefined
        }
      />
      <main className="board" id="board" hidden={hidden}>
        {COLUMNS.map((col) => {
          // The FILTERED list, so SortableColumn registers only cards that are
          // actually in the DOM — a sortable id with no node measures as a null
          // rect and the drop animation misbehaves. `total` is the unfiltered
          // length, and passing it is what switches the head to `shown/total`;
          // idle passes undefined and the head reads exactly as it does today.
          const jobs = search.byColumn[col];
          return (
            <BoardColumn
              key={col}
              col={col}
              jobs={jobs}
              total={search.active ? board.byColumn[col].length : undefined}
              onNewJob={() => actions.onOpenEditor(null)}
              footer={
                col === "done" ? (
                  <ArchiveDrawer
                    archived={search.archived}
                    actions={actions}
                    total={search.active ? board.archived.length : undefined}
                    // A search reaches an archived job ONLY through this: with
                    // no status filter, a match inside a collapsed drawer would
                    // otherwise be counted in the readout and invisible.
                    forceOpen={search.active && search.archived.length > 0}
                  />
                ) : undefined
              }
              renderCard={(j) => (
                <JobCard
                  key={jobKey(j)}
                  job={j}
                  col={col}
                  actions={actions}
                  matchReason={search.reasonByKey.get(jobKey(j))}
                  matchTerms={search.terms}
                />
              )}
            />
          );
        })}
      </main>
      {/* The goals panel is `position: fixed`, so it is NOT inside the
          `hidden` <main> above and would keep floating over the LOG, DREAM and
          GRAPH views. Dropping it outright while the board is hidden is what
          makes it board-only — a `hidden` attribute on it would work too, but
          unmounting also stops a half-typed add row surviving a trip through
          another view. */}
      {hidden ? null : (
        <GoalsPanel
          goals={board.board?.goals}
          mutate={board.mutate}
          goalFilter={goalFilter}
          onGoalFilter={onGoalFilter}
          counts={goalIndex.counts}
        />
      )}
    </>
  );
}
