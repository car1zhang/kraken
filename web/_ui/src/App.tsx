import { useCallback, useMemo, useRef, useState } from "react";

import { fetchUsage } from "./api/client.ts";
import type { WebJob, WebUsage } from "./api/types.ts";
import { BoardView, type BoardActions } from "./components/board/BoardView.tsx";
import { JobCard } from "./components/board/JobCard.tsx";
import { DragProvider } from "./components/board/DragProvider.tsx";
import { Chrome } from "./components/chrome/Chrome.tsx";
import { DagView } from "./components/dag/DagView.tsx";
import { LogView } from "./components/log/LogView.tsx";
import { ArtifactOverlay } from "./components/overlays/ArtifactOverlay.tsx";
import { EditorOverlay } from "./components/overlays/EditorOverlay.tsx";
import type { Mutate } from "./components/overlays/Overlay.tsx";
import {
  SteerOverlay,
  type SteerMode,
} from "./components/overlays/SteerOverlay.tsx";
import { ThreadOverlay } from "./components/overlays/ThreadOverlay.tsx";
import { showToast, Toast } from "./components/overlays/Toast.tsx";
import { UsageOverlay } from "./components/overlays/UsageOverlay.tsx";
import { useBoard } from "./hooks/useBoard.ts";
import { useHashRoute } from "./hooks/useHashRoute.ts";
import { useLocalStorageSet } from "./hooks/useLocalStorage.ts";
import { usePoll } from "./hooks/usePoll.ts";
import { columnFor } from "./lib/format.ts";
import { jobKey } from "./lib/jobKey.ts";
import {
  answerQuestion,
  deleteFeedback,
  deleteJob,
  dequeueJob,
  editFeedback,
  openTmux,
  retryJob,
  unarchiveJob,
} from "./api/client.ts";

const USAGE_POLL_MS = 60000;

/**
 * The whole board, wired together.
 *
 * This file owns exactly two things and deliberately nothing else: WHICH
 * overlay is open (one piece of state per overlay, never a stack), and the
 * callbacks that let a card or a drop reach one. Every component below takes
 * props and holds no global state — there is no context provider for app
 * state, no store, and no singleton beyond the toast emitter.
 *
 * Views are HIDDEN rather than unmounted, matching the vanilla board: the log
 * view keeps its paged history and its scroll position across a trip to the
 * board and back, which unmounting would throw away on every tab switch.
 */
export function App() {
  const board = useBoard();
  const [view, navigate] = useHashRoute();

  // Artifact-chip expansion is hoisted here rather than owned by BoardView
  // because the steer modal renders the same chip row for the same job; if
  // each surface kept its own set, expanding "+3 more" on a card and then
  // opening that job would show it collapsed again.
  const [expandedKeys, toggleExpanded] = useLocalStorageSet(
    "kraken-expanded-artifacts",
  );
  const expanded = useMemo(
    () => ({ keys: expandedKeys, toggle: toggleExpanded }),
    [expandedKeys, toggleExpanded],
  );

  const usage = usePoll<WebUsage>((signal) => fetchUsage(signal), USAGE_POLL_MS);

  // ------------------------------------------------------------- overlays
  //
  // Flat state, one field per overlay, rather than a discriminated union of
  // "the open overlay". They genuinely stack: the artifact viewer opens on
  // top of the steer modal (a chip in the steer context block), and the
  // thread overlay outlives the job-state change that force-closes steer.
  const [steer, setSteer] = useState<{
    slug: string;
    mode: SteerMode;
    requireText: boolean;
  } | null>(null);
  const [editor, setEditor] = useState<{ slug: string | null } | null>(null);
  const [artifact, setArtifact] = useState<{ url: string; name: string } | null>(
    null,
  );
  const [thread, setThread] = useState<{ slug: string; tid: string | null } | null>(
    null,
  );
  const [usageOpen, setUsageOpen] = useState(false);

  /**
   * The one place the board-refresh policy lives (see Overlay.tsx's Mutate).
   * Resolves to the parsed body on success and null on failure, having already
   * refetched the board either way and parked the failure text in the status
   * corner — so no overlay has to own a try/catch or decide when to refresh.
   */
  const mutate = useCallback<Mutate>(
    async (op) => {
      let value: Awaited<ReturnType<typeof op>> | null = null;
      const err = await board.mutate(async () => {
        value = await op();
        return value;
      });
      return err ? null : value;
    },
    [board],
  );

  // The job editor autosaves on a 600ms debounce, and a self-update reload
  // drops whatever is still in flight. EditorOverlay registers its flusher
  // here so Chrome can await it before location.reload().
  const editorFlush = useRef<(() => Promise<void>) | null>(null);
  const onBeforeReload = useCallback(async () => {
    await editorFlush.current?.();
  }, []);

  const openSteer = useCallback(
    (slug: string, mode: SteerMode, opts?: { requireText?: boolean }) =>
      setSteer({ slug, mode, requireText: opts?.requireText ?? false }),
    [],
  );

  /**
   * "Open this job" — the routing behind a plain click on a DAG node, ported
   * from app.js:3550-3557, which is byte-for-byte the same rule the board
   * card's own fallback uses (app.js:1659-1665). Both route on the job's LIVE
   * display column, which is NOT its state: an unhealthy live cell reads as
   * blocked whatever the job file says (liveDisplayColumn, app.js:2057), so a
   * working-but-sick job correctly opens the UNBLOCK composer.
   *
   * A job that is not live has no composer to open and falls through to the
   * editor, which renders itself read-only when the server says so.
   */
  const openJob = useCallback(
    (job: WebJob) => {
      const liveCol = job.live ? (job.health ? "blocked" : job.state) : null;
      if (liveCol === "working" || liveCol === "blocked") {
        openSteer(job.slug, liveCol === "blocked" ? "unblock" : "steer");
      } else {
        setEditor({ slug: job.slug });
      }
    },
    [openSteer],
  );

  const actions = useMemo<BoardActions>(
    () => ({
      onOpenSteer: openSteer,
      onOpenEditor: (slug) => setEditor({ slug }),
      onOpenArtifact: (url, name) => setArtifact({ url, name }),
      onOpenThread: (slug, tid) => setThread({ slug, tid }),
      onAnswerQuestion: (slug, i) => void board.mutate(() => answerQuestion(slug, i)),
      onRetry: (slug) => void board.mutate(() => retryJob(slug)),
      onDequeue: (slug) => void board.mutate(() => dequeueJob(slug)),
      onOpenTmux: (slug) => void board.mutate(() => openTmux({ slug })),
      onSaveFeedback: (slug, id, note) =>
        board.mutate(() => editFeedback(slug, id, note)),
      onDeleteFeedback: (slug, id) =>
        void board.mutate(() => deleteFeedback(slug, id)),
      // unarchive/delete carry the BARE archive name, never the jobKey — that
      // name is what the server resolves against the archive directory.
      onUnarchive: (slug) => void board.mutate(() => unarchiveJob(slug)),
      onDeleteForever: (slug) => void board.mutate(() => deleteJob(slug)),
    }),
    [board, openSteer],
  );

  const jobFor = (slug: string | null | undefined) =>
    slug == null ? undefined : board.byKey.get(jobKey({ slug }));

  return (
    <>
      <Chrome
        board={board}
        view={view}
        onNavigate={navigate}
        usage={usage.data}
        onOpenUsage={() => setUsageOpen(true)}
        onToast={showToast}
        onBeforeReload={onBeforeReload}
      />

      {/*
        renderOverlay is passed, not defaulted. DragProvider ships a minimal
        stub for it, but that stub is a slug in a box — and style.css:346-349
        states the contract the vanilla board met: "the native drag ghost
        carries the 'real' card under the cursor". The HTML5 drag image was a
        bitmap of the whole card, so anything less is a visible regression in
        a port whose entire premise is parity. JobCard lives in another
        module than DragProvider, which is why the real card is injected here
        rather than imported there.
      */}
      <DragProvider
        board={board}
        onOpenSteer={openSteer}
        onToast={showToast}
        renderOverlay={(job) =>
          job ? (
            <JobCard
              job={job}
              // columnFor, not `job.state as Column` — and the health rule with
              // it. `col` drives JobCard's `editable`, its queued-✕ and its
              // done-reactivate branches, so a ghost built from the raw state
              // would render a DIFFERENT card than the one it is ghosting for
              // exactly the job that motivated this whole review round: a
              // health-flagged `working` cell that bucketJobs puts in BLOCKED.
              col={columnFor(job.health ? "blocked" : job.state)}
              actions={actions}
              expanded={expandedKeys.has(jobKey(job))}
              onToggleExpanded={toggleExpanded}
            />
          ) : null
        }
      >
        <BoardView
          board={board}
          actions={actions}
          hidden={view !== "board"}
          expanded={expanded}
        />
      </DragProvider>

      <DagView
        board={board.board}
        active={view === "dag"}
        onNodeClick={openJob}
      />
      <LogView active={view === "log"} />

      <SteerOverlay
        open={steer !== null}
        slug={steer?.slug ?? null}
        mode={steer?.mode ?? "steer"}
        requireText={steer?.requireText ?? false}
        job={jobFor(steer?.slug)}
        onClose={() => setSteer(null)}
        mutate={mutate}
      />

      <EditorOverlay
        open={editor !== null}
        slug={editor?.slug ?? null}
        job={jobFor(editor?.slug)}
        onClose={() => setEditor(null)}
        onCreated={(slug) => setEditor({ slug })}
        onReactivate={(slug) => {
          setEditor(null);
          openSteer(slug, "reactivate", { requireText: true });
        }}
        mutate={mutate}
      />

      <ArtifactOverlay
        open={artifact !== null}
        url={artifact?.url ?? ""}
        name={artifact?.name ?? ""}
        onClose={() => setArtifact(null)}
        mutate={mutate}
      />

      <ThreadOverlay
        open={thread !== null}
        slug={thread?.slug ?? null}
        tid={thread?.tid ?? null}
        job={jobFor(thread?.slug)}
        onClose={() => setThread(null)}
        onBranched={(tid) =>
          setThread((t) => (t ? { slug: t.slug, tid } : t))
        }
        mutate={mutate}
      />

      <UsageOverlay
        open={usageOpen}
        data={usage.data}
        onClose={() => setUsageOpen(false)}
        refetch={usage.refetch}
      />

      <Toast />
    </>
  );
}
