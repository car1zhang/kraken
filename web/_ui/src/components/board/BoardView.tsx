import { useEffect, useMemo } from "react";

import type { BoardModel } from "../../hooks/useBoard.ts";
import type { JobSearchModel } from "../../hooks/useJobSearch.ts";
import { useLocalStorage } from "../../hooks/useLocalStorage.ts";
import { COLUMNS } from "../../lib/columns.ts";
import { isFrozen } from "../../lib/format.ts";
import { addArchivedGoalCounts, goalJobCounts } from "../../lib/goals.ts";
import { jobKey } from "../../lib/jobKey.ts";
import type { SteerMode } from "../../lib/moves.ts";
import { ArchiveDrawer } from "./ArchiveDrawer.tsx";
import { BoardBar, type BoardViewMode } from "./BoardBar.tsx";
import { BoardColumn } from "./Column.tsx";
import { FrozenDrawer } from "./FrozenDrawer.tsx";
import { GoalsPanel } from "./GoalsPanel.tsx";
import { JobCard } from "./JobCard.tsx";
import { MapView } from "./MapView.tsx";

/** localStorage key holding which view the board area shows. */
const BOARD_VIEW_KEY = "kraken.boardView";

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
  /**
   * POST /api/peers/open {slug} — raises zeus' pane in Ghostty. Returning
   * the mutation's promise (as onAnswerQuestion does) is what lets the card
   * disable its button while the open is in flight: the server answers only
   * once the window's tmux client has appeared, and a second click before
   * then opened a second window that kicked the first.
   */
  onOpenTmux(slug: string): void | Promise<Error | null>;
  /** POST /api/feedback {id, feedback}. Resolves to the failure, or null. */
  onSaveFeedback(slug: string, id: number, note: string): Promise<Error | null>;
  /** POST /api/feedback {id, delete:true}. The confirm() already happened. */
  onDeleteFeedback(slug: string, id: number): void;
  /** POST /api/unarchive with the bare ARCHIVE name (never the jobKey). */
  onUnarchive(slug: string): void;
  /** POST /api/delete with the bare archive name. The confirm() happened. */
  onDeleteForever(slug: string): void;
  /**
   * POST /api/job/freeze: put a blocked job on hold (`true`) or take it off
   * (`false`). Optional, and a card offers neither button without it, so a
   * host that never wired it (a harness, a test) renders the card it always
   * has.
   */
  onFreeze?(slug: string, frozen: boolean): void;
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
   * The fleet's machine selection and its setter, relayed to the board bar's
   * machines dropdown. App owns the state (and its kraken.fleetNodes
   * persistence) because the filter itself runs in useBoard, above this view.
   */
  fleetNodes?: readonly string[];
  onFleetNodes?: (next: string[]) => void;
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
 * The board: four columns, the frozen drawer under BLOCKED (blockedSplit
 * below) and the archive drawer under DONE. Port of
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
 *
 * The bar's board/map toggle swaps the four columns for the agent map
 * (MapView), which lays the same filtered cards out on a free plane. The
 * choice is per browser (`kraken.boardView`). The map draws only active
 * cards, so the archive drawer has no place on it.
 */
export function BoardView({
  board,
  actions,
  search,
  hidden,
  goalFilter = null,
  onGoalFilter = () => {},
  overlayOpen,
  fleetNodes = [],
  onFleetNodes,
}: BoardViewProps) {
  // Columns or the agent map, per browser. Anything but "map" in storage —
  // an old value, a hand edit — reads as the board, the safe default.
  const [storedView, setStoredView] = useLocalStorage(BOARD_VIEW_KEY, "board");
  const view: BoardViewMode = storedView === "map" ? "map" : "board";
  const setView = (v: BoardViewMode) => setStoredView(v);

  /**
   * How many jobs link each goal, and every goal's text — from the cards this
   * view already holds, so the count cannot disagree with the cards on screen.
   *
   * The counting itself is `goalJobCounts` (lib/goals.ts), a pure function
   * over a flat job array rather than a loop here: it is the number the
   * panel's badges promise and the board's filter has to deliver, so it is
   * worth being nameable and testable on its own. It is fed every local card,
   * active and archived alike — not the bucketed columns — because an
   * archived job links goals like any other and the archive drawer is on this
   * board. The one case the cards cannot answer is an archive not loaded yet,
   * which the payload's own per-goal tally covers (see below).
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
  //
  // The archived cards are not on the poll: the active ones come off the
  // payload and the archived ones off the model (`board.archived`), which is
  // empty until something has asked for them. Until then the payload's own
  // tally of archived jobs per goal (`archived_goal_counts`) stands in, so a
  // badge reads the same before and after the archive loads.
  const allJobs = board.board?.jobs;
  const archivedJobs = board.archived;
  const archivedLoaded = board.archivedLoaded ?? true;
  const archivedTally = board.board?.archived_goal_counts;
  const jobs = useMemo(
    () => [...(allJobs ?? []).filter((j) => !j.owner && !j.archived), ...archivedJobs],
    [allJobs, archivedJobs],
  );
  const goalIndex = useMemo(() => {
    const counts = goalJobCounts(jobs);
    if (!archivedLoaded) addArchivedGoalCounts(counts, archivedTally);
    const text = new Map<number, string>();
    for (const g of board.board?.goals?.week ?? []) text.set(g.id, g.text);
    for (const g of board.board?.goals?.day ?? []) text.set(g.id, g.text);
    for (const job of jobs) {
      for (const g of job.goals ?? []) if (!text.has(g.id)) text.set(g.id, g.text);
      if (job.goal && !text.has(job.goal.id)) text.set(job.goal.id, job.goal.text);
    }
    return { counts, text };
  }, [jobs, archivedLoaded, archivedTally, board.board?.goals]);

  // A search or a goal filter reaches into the archive, so either one asks
  // for the archived cards; an open archive drawer asks for itself.
  const needArchived = board.needArchived;
  useEffect(() => {
    if (search.active) needArchived?.();
  }, [search.active, needArchived]);

  /**
   * The BLOCKED column, split for rendering: the cards that need the operator
   * stay in the column, the ones on hold go to the FROZEN drawer at its foot.
   *
   * The split is this view's alone. `board.byColumn.blocked` and
   * `search.byColumn.blocked` keep every frozen card, so the drag layer still
   * sees one whole column (its reorder posts the full order, hidden cards
   * included, exactly as it does under a search), and the map, which reads
   * `search.byColumn`, still draws them.
   *
   * Memoized on the search's own array so the column's sortable list is not
   * re-registered on every render, the reason useJobSearch hands back the
   * board's arrays by identity when idle.
   */
  const searchBlocked = search.byColumn.blocked;
  const blockedSplit = useMemo(() => {
    const frozen = searchBlocked.filter(isFrozen);
    // No frozen card: hand the column the very array it was getting.
    if (frozen.length === 0) return { open: searchBlocked, frozen };
    return { open: searchBlocked.filter((j) => !isFrozen(j)), frozen };
  }, [searchBlocked]);
  const boardBlocked = board.byColumn.blocked;
  const frozenTotal = useMemo(() => boardBlocked.filter(isFrozen).length, [boardBlocked]);

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
        view={view}
        onView={setView}
        // offered only once there IS another kraken (or a selection is
        // already persisted, so it can always be cleared); the picker counts
        // against the UNFILTERED cards, which is why it gets `allJobs`
        fleet={
          onFleetNodes && ((board.board?.nodes?.length ?? 0) > 0 || fleetNodes.length > 0)
            ? {
                nodes: board.board?.nodes ?? [],
                selfLabel: board.board?.node_label,
                operator: board.board?.operator,
                jobs: allJobs ?? [],
                selected: fleetNodes,
                onChange: onFleetNodes,
              }
            : undefined
        }
      />
      {view === "map" ? (
        <MapView
          search={search}
          actions={actions}
          boardNodeID={board.board?.node_id}
          hidden={hidden}
        />
      ) : (
        <main className="board" id="board" hidden={hidden}>
          {COLUMNS.map((col) => {
            // The FILTERED list, so SortableColumn registers only cards that are
            // actually in the DOM — a sortable id with no node measures as a null
            // rect and the drop animation misbehaves. `total` is the unfiltered
            // length, and passing it is what switches the head to `shown/total`;
            // idle passes undefined and the head reads exactly as it does today.
            // BLOCKED renders only the cards not on hold; the frozen ones are
            // in its footer drawer, and its head count and `total` leave them
            // out so the number is the cards that need the operator.
            const jobs = col === "blocked" ? blockedSplit.open : search.byColumn[col];
            const colTotal =
              board.byColumn[col].length - (col === "blocked" ? frozenTotal : 0);
            const renderCard = (j: (typeof jobs)[number]) => (
              <JobCard
                key={jobKey(j)}
                job={j}
                col={col}
                actions={actions}
                matchReason={search.reasonByKey.get(jobKey(j))}
                matchTerms={search.terms}
                boardNodeID={board.board?.node_id}
              />
            );
            return (
              <BoardColumn
                key={col}
                col={col}
                jobs={jobs}
                total={search.active ? colTotal : undefined}
                onNewJob={() => actions.onOpenEditor(null)}
                footer={
                  col === "blocked" ? (
                    <FrozenDrawer
                      frozen={blockedSplit.frozen}
                      total={search.active ? frozenTotal : undefined}
                      // Same reason the archive drawer takes it: a match
                      // inside a collapsed drawer would be counted in the
                      // readout and invisible.
                      forceOpen={search.active && blockedSplit.frozen.length > 0}
                      renderCard={renderCard}
                    />
                  ) : col === "done" ? (
                    <ArchiveDrawer
                      archived={search.archived}
                      actions={actions}
                      total={search.active ? (board.archivedCount ?? board.archived.length) : undefined}
                      count={board.archivedCount}
                      onNeed={board.needArchived}
                      // A search reaches an archived job ONLY through this: with
                      // no status filter, a match inside a collapsed drawer would
                      // otherwise be counted in the readout and invisible.
                      forceOpen={search.active && search.archived.length > 0}
                    />
                  ) : undefined
                }
                renderCard={renderCard}
              />
            );
          })}
        </main>
      )}
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
