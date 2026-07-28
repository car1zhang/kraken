import { useCallback, useMemo, useRef, useState } from "react";

import { fetchUsage } from "./api/client.ts";
import type { WebUsage } from "./api/types.ts";
import { BoardView, type BoardActions } from "./components/board/BoardView.tsx";
import { JobCard } from "./components/board/JobCard.tsx";
import { DragProvider } from "./components/board/DragProvider.tsx";
import { Chrome } from "./components/chrome/Chrome.tsx";
import { DreamView } from "./components/dream/DreamView.tsx";
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
  // `key` is the jobKey the artifact was opened with, NOT a bare slug: the
  // pager rebuilds the job's ordered artifact list from it, and an archived
  // job plus a live one that reused its freed slug would otherwise page
  // through each other's artifacts. It must SURVIVE a step — stepping within
  // a sequence changes url/name but never the job it belongs to.
  const [artifact, setArtifact] = useState<{
    url: string;
    name: string;
    key: string;
  } | null>(null);
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

  const actions = useMemo<BoardActions>(
    () => ({
      onOpenSteer: openSteer,
      onOpenEditor: (slug) => setEditor({ slug }),
      onOpenArtifact: (url, name, key) => setArtifact({ url, name, key }),
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

      {/* Both stay MOUNTED across view switches, hidden rather than
          unmounted: LogView keeps its paged history and scroll position, and
          DreamView keeps which report dates are expanded plus any half-typed
          watchlist comment. Unmounting would throw all of that away on every
          tab switch — the vanilla board kept them in module state for the same
          reason. */}
      <LogView active={view === "log"} />
      <DreamView active={view === "dream"} mutate={mutate} onToast={showToast} />

      <SteerOverlay
        open={steer !== null}
        slug={steer?.slug ?? null}
        mode={steer?.mode ?? "steer"}
        requireText={steer?.requireText ?? false}
        job={jobFor(steer?.slug)}
        // Both REQUIRED, deliberately: these replaced optional `artifactChips`
        // / `threadChips` slots that App never filled, which left the modal's
        // artifact list and its branch button rendering nothing at all. An
        // optional slot the integrator must remember to fill is exactly how
        // that shipped silently — required props make the omission a compile
        // error instead.
        onOpenArtifact={(url, name, key) => setArtifact({ url, name, key })}
        onOpenThread={(slug, tid) => setThread({ slug, tid })}
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
        // The editor is the only surface a settled job's artifacts are
        // reachable from now that the card carries a bare count, so leaving
        // this unpassed made every chip in that list a silent no-op.
        onOpenArtifact={(url, name, key) => setArtifact({ url, name, key })}
        mutate={mutate}
      />

      <ArtifactOverlay
        open={artifact !== null}
        url={artifact?.url ?? ""}
        name={artifact?.name ?? ""}
        // looked up by jobKey, never bare slug — see the artifact state above
        job={artifact ? board.byKey.get(artifact.key) : undefined}
        // a step keeps the jobKey it opened with; only url/name move
        onNavigate={(url, name) =>
          setArtifact((a) => (a ? { ...a, url, name } : a))
        }
        // A question asked from the viewer branched a thread: hand the reader
        // over to it. This CLOSES rather than stacking — see onAskBranched's
        // own docblock for why it cannot be left open underneath.
        onAskBranched={(slug, tid) => {
          setArtifact(null);
          setThread({ slug, tid });
        }}
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
