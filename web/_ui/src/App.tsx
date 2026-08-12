import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { fetchUsage } from "./api/client.ts";
import type { WebUsage } from "./api/types.ts";
import { BoardView, type BoardActions } from "./components/board/BoardView.tsx";
import { JobCard } from "./components/board/JobCard.tsx";
import { DragProvider } from "./components/board/DragProvider.tsx";
import { Chrome } from "./components/chrome/Chrome.tsx";
import { DreamView } from "./components/dream/DreamView.tsx";
import { GraphView } from "./components/graph/GraphView.tsx";
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
import { useJobSearch } from "./hooks/useJobSearch.ts";
import { useLocalStorageSet } from "./hooks/useLocalStorage.ts";
import { usePoll } from "./hooks/usePoll.ts";
import { columnFor } from "./lib/format.ts";
import { jobKey } from "./lib/jobKey.ts";
import { installSfxListeners } from "./lib/sfx.ts";
import {
  answerQuestion,
  deleteFeedback,
  deleteJob,
  editFeedback,
  openTmux,
  retryJob,
  unarchiveJob,
} from "./api/client.ts";

const USAGE_POLL_MS = 60000;

/**
 * The whole board, wired together.
 *
 * What lives here is what more than one child needs, and nothing else: WHICH
 * overlay is open (one piece of state per overlay, never a stack), the
 * callbacks that let a card or a drop reach one, the board SEARCH (DragProvider
 * and BoardView need the same instance), and the usage poll (the chrome and its
 * overlay). Each of those STATE pieces carries its own note below naming the
 * surfaces that forced it up here; the callbacks are not state and carry none.
 * The `kraken-expanded-artifacts` set below is NOT in that list: it is dead —
 * every file still DECLARING the prop marks it `@deprecated` (BoardView,
 * JobCard, ArchivedCard), and all of them are optional, so it is kept only so
 * App's own call sites below still compile. Delete it, those call sites, and
 * the three props together — nothing downstream needs it. Every component below
 * takes props and holds no global state — there is no context provider for app
 * state, no store, and no singleton beyond the toast emitter.
 *
 * Views are HIDDEN rather than unmounted, matching the vanilla board: the log
 * view keeps its paged history and its scroll position across a trip to the
 * board and back, which unmounting would throw away on every tab switch.
 */
export function App() {
  const board = useBoard();
  const [view, navigate] = useHashRoute();
  // the sfx module's document-level capture listeners; the returned
  // uninstaller keeps strict-mode's double mount from stacking a second set
  useEffect(() => installSfxListeners(), []);

  // DEAD, kept only so App's own call sites below still compile. This used to
  // be hoisted because the card and the steer modal shared one "+3 more"
  // toggle, but band 2 carries a bare artifact COUNT now, so nothing reads it:
  // BoardView.tsx and JobCard.tsx accept the prop, mark it `@deprecated` and
  // drop it on the floor, ArchivedCard.tsx declares it and is never even passed
  // one, and the steer modal never receives it at all. Every one of those props
  // is optional, so deleting this and its two call sites leaves all three
  // compiling untouched.
  const [expandedKeys, toggleExpanded] = useLocalStorageSet(
    "kraken-expanded-artifacts",
  );
  const expanded = useMemo(
    () => ({ keys: expandedKeys, toggle: toggleExpanded }),
    [expandedKeys, toggleExpanded],
  );

  // The board search lives HERE rather than inside BoardView because two
  // children need one instance of it: BoardView renders the filtered lists,
  // and DragProvider — which wraps the board — needs the same instance's
  // `visibleKeys` to know where "the end of the column" is for a drop on empty
  // space. Two calls would be two query states, and the drag layer would read
  // the one nobody typed into. `board` itself is still passed to DragProvider
  // WHOLE: search narrows the render, never the model a reorder posts.
  const search = useJobSearch(board);

  // One poll, two readers: the chrome's usage corner and the overlay it opens
  // (which also takes `refetch`, so its refresh button re-uses this poll rather
  // than starting a second one).
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

  // Is anything modal open? The board's search bar is the only reader: cmd+K
  // focuses its input from anywhere, and must not pull focus out of a composer
  // carl is mid-sentence in. Derived here because this file is the one place
  // that knows about every overlay — the alternative was BoardBar asking the
  // DOM, which would tie a keyboard shortcut to another slice's markup.
  const overlayOpen =
    steer !== null ||
    editor !== null ||
    artifact !== null ||
    thread !== null ||
    usageOpen;

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
        visibleKeys={search.active ? search.visibleKeys : undefined}
        renderOverlay={(job) =>
          job ? (
            <JobCard
              job={job}
              // columnFor, not `job.state as Column` — and the health rule with
              // it. `col` drives JobCard's `editable` and its done-reactivate
              // branches, so a ghost built from the raw state
              // would render a DIFFERENT card than the one it is ghosting for
              // exactly the job that motivated this whole review round: a
              // health-flagged `working` cell that bucketJobs puts in BLOCKED.
              col={columnFor(job.health ? "blocked" : job.state)}
              actions={actions}
              // The search decoration travels with the card, for the same
              // parity reason: a text-matched card carries a marker line and
              // marked words, and a ghost without them would change shape and
              // lose a line the instant it lifts off the column.
              matchReason={search.reasonByKey.get(jobKey(job))}
              matchTerms={search.terms}
              expanded={expandedKeys.has(jobKey(job))}
              onToggleExpanded={toggleExpanded}
            />
          ) : null
        }
      >
        <BoardView
          board={board}
          actions={actions}
          search={search}
          hidden={view !== "board"}
          overlayOpen={overlayOpen}
          expanded={expanded}
        />
      </DragProvider>

      {/* All three stay MOUNTED across view switches, hidden rather than
          unmounted: LogView keeps its paged history and scroll position,
          DreamView keeps which report dates are expanded plus any half-typed
          watchlist comment, and GraphView keeps its filter rail, its selection
          and — the expensive one — its sigma WebGL context and camera
          position. Unmounting would throw all of that away on every tab
          switch — the vanilla board kept them in module state for the same
          reason. */}
      <LogView active={view === "log"} />
      <DreamView active={view === "dream"} mutate={mutate} onToast={showToast} />
      <GraphView active={view === "graph"} />

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
