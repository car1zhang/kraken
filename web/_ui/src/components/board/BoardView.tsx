import type { BoardModel } from "../../hooks/useBoard.ts";
import { useLocalStorageSet } from "../../hooks/useLocalStorage.ts";
import { COLUMNS } from "../../lib/columns.ts";
import { jobKey } from "../../lib/jobKey.ts";
import type { SteerMode } from "../../lib/moves.ts";
import { ArchiveDrawer } from "./ArchiveDrawer.tsx";
import { BoardColumn } from "./Column.tsx";
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
  onOpenSteer(slug: string, mode: SteerMode, opts?: { requireText?: boolean }): void;
  /** Job editor. `null` opens it in new-job mode (the "+ NEW JOB" button). */
  onOpenEditor(slug: string | null): void;
  /** Artifact viewer. `name` is already prefixed "<label>/<artifact>". */
  onOpenArtifact(url: string, name: string): void;
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
  /** POST /api/dequeue — the queued card's ✕. */
  onDequeue(slug: string): void;
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
  /** The view router hides the board rather than unmounting it, as today. */
  hidden?: boolean;
  /**
   * Artifact-chip expansion state, keyed by jobKey(). Optional: the board owns
   * it by default. Hoist it only if another surface must share the same
   * toggles (the steer modal renders the same chip row).
   */
  expanded?: { keys: Set<string>; toggle(key: string): void };
}

/**
 * The board: five columns plus the archive drawer under DONE. Port of
 * renderBoardDOM (app.js:814).
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
 */
export function BoardView({ board, actions, hidden, expanded }: BoardViewProps) {
  const [ownKeys, ownToggle] = useLocalStorageSet("kraken-expanded-artifacts");
  const expandedKeys = expanded?.keys ?? ownKeys;
  const toggleExpanded = expanded?.toggle ?? ownToggle;

  const { byColumn, archived } = board;

  return (
    <main className="board" id="board" hidden={hidden}>
      {COLUMNS.map((col) => {
        const jobs = byColumn[col];
        return (
          <BoardColumn
            key={col}
            col={col}
            jobs={jobs}
            onNewJob={() => actions.onOpenEditor(null)}
            footer={
              col === "done" ? (
                <ArchiveDrawer
                  archived={archived}
                  actions={actions}
                  expandedKeys={expandedKeys}
                  onToggleExpanded={toggleExpanded}
                />
              ) : undefined
            }
            renderCard={(j) => {
              const key = jobKey(j);
              return (
                <JobCard
                  key={key}
                  job={j}
                  col={col}
                  actions={actions}
                  expanded={expandedKeys.has(key)}
                  onToggleExpanded={toggleExpanded}
                />
              );
            }}
          />
        );
      })}
    </main>
  );
}
