import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { fetchUsage } from "./api/client.ts";
import type { WebUsage } from "./api/types.ts";
import { BoardView, type BoardActions } from "./components/board/BoardView.tsx";
import { JobCard } from "./components/board/JobCard.tsx";
import { openSpawned } from "./components/board/chips/SpawnChips.tsx";
import { DragProvider } from "./components/board/DragProvider.tsx";
import { Chrome } from "./components/chrome/Chrome.tsx";
import MindButton from "./components/chrome/MindButton.tsx";
import { DreamView } from "./components/dream/DreamView.tsx";
import { LogView } from "./components/log/LogView.tsx";
import { MemoryView } from "./components/memory/MemoryView.tsx";
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
import { DevicesOverlay } from "./components/overlays/DevicesOverlay.tsx";
import { readFleetNodes, writeFleetNodes } from "./lib/fleet.ts";
import { useBoard } from "./hooks/useBoard.ts";
import { useBoardEvents } from "./hooks/useBoardEvents.ts";
import { useHashRoute } from "./hooks/useHashRoute.ts";
import { useJobSearch } from "./hooks/useJobSearch.ts";
import { useLibrary } from "./hooks/useLibrary.ts";
import { useLocalStorageSet } from "./hooks/useLocalStorage.ts";
import { useMM } from "./hooks/useMM.ts";
import { usePoll } from "./hooks/usePoll.ts";
import { stampBackground } from "./lib/background.ts";
import { columnFor } from "./lib/format.ts";
import { jobKey } from "./lib/jobKey.ts";
import { installSfxListeners } from "./lib/sfx.ts";
import {
  addMMNote,
  answerQuestion,
  deleteFeedback,
  deleteJob,
  editFeedback,
  openTmux,
  removeFromLibrary,
  removeMM,
  confirmMM,
  retryJob,
  saveToLibrary,
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
  // The fleet filter's one per-browser preference: the machines whose cells
  // the board shows (empty is every machine). Up here because two surfaces
  // share it — the board bar's machines dropdown, which sets it, and useBoard,
  // which applies it to the columns — and persisted in localStorage
  // (lib/fleet.ts, try/catch-wrapped: a private window simply forgets it).
  const [fleetNodes, setFleetNodesState] = useState<string[]>(readFleetNodes);
  const setFleetNodes = useCallback((next: string[]) => {
    setFleetNodesState(next);
    writeFleetNodes(next);
  }, []);
  const fleet = useMemo(() => ({ nodes: fleetNodes }), [fleetNodes]);
  const board = useBoard(fleet);
  const [view, navigate] = useHashRoute();
  // the sfx module's document-level capture listeners; the returned
  // uninstaller keeps strict-mode's double mount from stacking a second set
  useEffect(() => installSfxListeners(), []);

  // The board background image: <html data-bg> plus the --bg-image custom
  // property, both driven by the payload's `background.updated_at` stamp
  // (stampBackground, lib/background.ts). style.css keys the whole image
  // treatment off [data-bg], so with no background set this effect leaves
  // the document byte-identical to a board that never had the feature. React
  // is the ONE writer of this attribute — unlike data-theme/data-accent
  // there is no pre-paint IIFE half, because the image is server state that
  // cannot be known before the first payload. Keyed on the stamp, not the
  // object: the payload allocates a fresh `background` object every poll,
  // and the stamp is what actually changes when the image does (it doubles
  // as backgroundURL's cache-buster).
  const bgAt = board.board?.background?.updated_at;
  useEffect(() => stampBackground(bgAt), [bgAt]);
  // The board's own voice, mounted HERE and exactly once: it diffs consecutive
  // poll snapshots, so a second instance would diff the same snapshots again
  // and double every sound. This is the only place `useBoard()` runs — the
  // views below all take the model as a prop — and it is handed `board.board`,
  // the raw payload straight off the poll, because a snapshot's IDENTITY is
  // what tells the hook a new one arrived.
  useBoardEvents(board.board);

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

  // The goal the board is filtered to, or null. It lives up here for the
  // search's exact reason and one of its own: the goals PANEL sets it, the
  // board BAR shows and clears it, and the search hook composes it with the
  // query — three surfaces, one value. NOTHING persists it (no localStorage,
  // no hash), matching the query beside it: a reload lands on an unfiltered
  // board, which is the only resting state a card set this volatile can
  // safely have.
  const [goalFilter, setGoalFilter] = useState<number | null>(null);

  // The board search lives HERE rather than inside BoardView because two
  // children need one instance of it: BoardView renders the filtered lists,
  // and DragProvider — which wraps the board — needs the same instance's
  // `visibleKeys` to know where "the end of the column" is for a drop on empty
  // space. Two calls would be two query states, and the drag layer would read
  // the one nobody typed into. `board` itself is still passed to DragProvider
  // WHOLE: search narrows the render, never the model a reorder posts — and
  // the goal filter rides the same path for the same reason, which is why it
  // is an argument to this hook rather than a second filtering layer anywhere
  // below it.
  const search = useJobSearch(board, goalFilter);

  // One poll, two readers: the chrome's usage corner and the overlay it opens
  // (which also takes `refetch`, so its refresh button re-uses this poll rather
  // than starting a second one).
  const usage = usePoll<WebUsage>((signal) => fetchUsage(signal), USAGE_POLL_MS);
  // usePoll's refetch now reports whether it delivered a payload, which exists
  // for the board's mute handshake (useBoard.mutate) and means nothing to the
  // usage panel — its refresh button awaits this only to sequence a repaint.
  // Dropped to void here rather than widening UsageOverlay's prop, so a
  // board-sound mechanism stays out of a rotation control's type. useCallback
  // because the panel carries it in a handler's deps.
  const refetchUsage = useCallback(async () => {
    await usage.refetch();
  }, [usage.refetch]);

  // ------------------------------------------------------------- overlays
  //
  // Flat state, one field per overlay, rather than a discriminated union of
  // "the open overlay". They genuinely stack: the artifact viewer opens on
  // top of the steer modal (a chip in the steer context block), and the
  // thread overlay outlives the job-state change that force-closes steer.
  // `node` is set only when a REMOTE card opened the overlay: it is half that
  // card's identity (lib/jobKey.ts), so the overlay is looked up by owner plus
  // slug and never lands on a local job sharing the slug.
  const [steer, setSteer] = useState<{
    slug: string;
    mode: SteerMode;
    requireText: boolean;
    node?: string;
  } | null>(null);
  const [editor, setEditor] = useState<{ slug: string | null; node?: string } | null>(null);
  const [devicesOpen, setDevicesOpen] = useState(false);
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
  // `node` is set only for a remote job's thread, opened from its steer modal:
  // the overlay then reads it through the owner and resolves the job by
  // owner+slug, as the steer modal does
  const [thread, setThread] = useState<{
    slug: string;
    tid: string | null;
    node?: string;
  } | null>(null);
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
    usageOpen ||
    devicesOpen;

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

  // ------------------------------------------------------------- library
  //
  // ONE poll for the saved-artifact list, hoisted here for the reason the
  // search is: two children need the SAME instance. The memory view renders it
  // and the artifact viewer's ☆/★ button is a membership test against it, so
  // two polls would be two answers to "is this saved" — the star would go on
  // claiming a row that the list beside it had just removed.
  //
  // Gated on either surface being up: the memory view showing, or the artifact
  // viewer being open over anything at all (that is where the star lives).
  const library = useLibrary(view === "memory" || artifact !== null);

  // Both writes take the shared mutation path so a refusal lands in the clock
  // corner like every other one, then pull the list back at once rather than
  // waiting out the 30s tick — DreamView's verdict shape exactly. Neither
  // returns anything: the button's next state is whatever the refetched list
  // says, which is what keeps a failed save reading "unsaved" without a single
  // line of failure handling at the call site.
  const saveArtifact = useCallback(
    async (slug: string, home: string, name: string) => {
      await mutate(() => saveToLibrary(slug, home, name));
      await library.refetch();
    },
    [mutate, library.refetch],
  );
  const removeSaved = useCallback(
    async (id: number) => {
      await mutate(() => removeFromLibrary(id));
      await library.refetch();
    },
    [mutate, library.refetch],
  );

  // -------------------------------------------------------- mental model
  //
  // ONE unfiltered poll, read by the memory view's Model pane alone now that
  // the board's one-shot marks are retired — gated on that view showing, so
  // with it shut this poll costs nothing (the board payload carries no mm
  // keys).
  const mm = useMM(view === "memory");

  // Every write takes the shared mutation path so a refusal lands in the clock
  // corner like all the others, then pulls the list back at once rather than
  // waiting out the 30s tick — the library pair's shape exactly. None returns
  // anything: each control's next state is whatever the refetched list says,
  // which is what keeps a refused mark reading unmarked without a line of
  // failure handling at the call site.
  const afterMM = useCallback(
    async (op: () => Promise<unknown>) => {
      await mutate(op);
      await mm.refetch();
    },
    [mutate, mm.refetch],
  );
  // The board's one-shot marks (✓ read, mark selection, table ✓, ○ mark
  // known, → decision) were retired in PR 2 (Q10): capture goes through the
  // ◆ mind button below into a mind thread, which commits the item itself.
  const addNote = useCallback(
    (text: string) => afterMM(() => addMMNote(text)),
    [afterMM],
  );
  // Drops a mark by INVALIDATING it — the row survives in the memory pane, so
  // this is "I no longer know this", never a delete.
  const dropMark = useCallback(
    (id: number) => afterMM(() => removeMM(id)),
    [afterMM],
  );
  // Confirms a nightly `proposed` item so it goes live — the same mutate +
  // refetch the drop rides.
  const confirmMark = useCallback(
    (id: number) => afterMM(() => confirmMM(id)),
    [afterMM],
  );

  // The job editor autosaves on a 600ms debounce, and a self-update reload
  // drops whatever is still in flight. EditorOverlay registers its flusher
  // here so Chrome can await it before location.reload().
  const editorFlush = useRef<(() => Promise<void>) | null>(null);
  const onBeforeReload = useCallback(async () => {
    await editorFlush.current?.();
  }, []);

  const openSteer = useCallback(
    (slug: string, mode: SteerMode, opts?: { requireText?: boolean; node?: string }) =>
      setSteer({
        slug,
        mode,
        requireText: opts?.requireText ?? false,
        ...(opts?.node ? { node: opts.node } : {}),
      }),
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
      onOpenTmux: (slug) => board.mutate(() => openTmux({ slug })),
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

  // A remote card resolves by owner plus slug; a local one by its bare slug.
  // A fake owner object is enough for jobKey, which reads only `node_id`.
  const jobFor = (slug: string | null | undefined, node?: string) =>
    slug == null
      ? undefined
      : board.byKey.get(
          jobKey(
            node
              ? { slug, owner: { node_id: node, label: "", operator: "", online: false } }
              : { slug },
          ),
        );

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
        onOpenDevices={() => setDevicesOpen(true)}
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
              boardNodeID={board.board?.node_id}
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
          goalFilter={goalFilter}
          onGoalFilter={setGoalFilter}
          hidden={view !== "board"}
          overlayOpen={overlayOpen}
          expanded={expanded}
          fleetNodes={fleetNodes}
          onFleetNodes={setFleetNodes}
        />
      </DragProvider>

      {/* All three stay MOUNTED across view switches, hidden rather than
          unmounted: LogView keeps its paged history and scroll position,
          DreamView keeps which report dates are expanded plus any half-typed
          watchlist comment, and MemoryView keeps its segment, its library
          filter and any half-typed note. Unmounting would throw all of that
          away on every tab switch — the vanilla board kept them in module
          state for the same reason. */}
      <LogView active={view === "log"} />
      <DreamView active={view === "dream"} mutate={mutate} onToast={showToast} />
      <MemoryView
        active={view === "memory"}
        library={library}
        // A library row opens as a SINGLETON in the viewer: its `key` is the
        // empty string, which resolves to no job, which is exactly right — a
        // frozen snapshot belongs to no artifact SEQUENCE (the row it came from
        // may outlive the job entirely), so the pager and `⑂ ask` both hide.
        onOpenArtifact={(url, name) => setArtifact({ url, name, key: "" })}
        onRemoveSaved={removeSaved}
        mm={mm}
        onAddNote={addNote}
        onRemoveMM={dropMark}
        onConfirmMM={confirmMark}
        // a raised flag's `open job` — the steer modal of the job whose zeus
        // raised it, which stacks over the memory view like any overlay
        onOpenJob={(slug) => openSteer(slug, "steer")}
      />

      <SteerOverlay
        open={steer !== null}
        slug={steer?.slug ?? null}
        mode={steer?.mode ?? "steer"}
        requireText={steer?.requireText ?? false}
        job={jobFor(steer?.slug, steer?.node)}
        // a remote card's owner — selects the modal's remote body
        node={steer?.node}
        // a remote public card's ✎ edit: the editor's raw remote mode
        onOpenEditor={(slug, node) => {
          setSteer(null);
          setEditor({ slug, node });
        }}
        // The same poll the chrome's usage corner and the usage drilldown read
        // — a THIRD reader of one request, never a second request. The modal's
        // account section joins it to the job's binding by slot slug and draws
        // that account's session/Fable bars; undefined (first paint, older
        // server, daemon down) simply draws none.
        usage={usage.data}
        operator={board.board?.operator}
        boardNodeID={board.board?.node_id}
        // view mode's ↺: re-point the SAME overlay at the same job in
        // reactivate mode. The body is keyed on slug+mode, so this is a fresh
        // mount of the composer, exactly as the card's own ↺ button gives.
        onReactivate={(slug) => openSteer(slug, "reactivate")}
        // Both REQUIRED, deliberately: these replaced optional `artifactChips`
        // / `threadChips` slots that App never filled, which left the modal's
        // artifact list and its branch button rendering nothing at all. An
        // optional slot the integrator must remember to fill is exactly how
        // that shipped silently — required props make the omission a compile
        // error instead.
        onOpenArtifact={(url, name, key) => setArtifact({ url, name, key })}
        onOpenThread={(slug, tid, node) =>
          setThread(node ? { slug, tid, node } : { slug, tid })
        }
        // A spawn chip: re-point this modal at the child or parent through
        // the card's own click mapping (openSpawned) — the same openSteer
        // the cards call, so the body remounts on the new slug+mode; a draft
        // closes the modal and opens the editor, as the card's click would.
        onOpenJob={(slug, state) =>
          openSpawned(slug, state, {
            onOpenSteer: (s, mode) => openSteer(s, mode),
            onOpenEditor: (s) => {
              setSteer(null);
              setEditor({ slug: s });
            },
          })
        }
        onClose={() => setSteer(null)}
        // The rename's re-point, and the ONE reason it belongs up here: the
        // open overlay's identity is App's state, not the modal's, so a modal
        // that renamed itself in place would keep being handed the old slug on
        // the very next render. Synchronous — the mode and requireText the
        // modal opened with survive, only the name moves. The board still
        // carries the old slug until the poll after this, which is why the
        // refetch is kicked here and why both bodies capture their staleness
        // baseline lazily.
        onRenamed={(slug) => {
          setSteer((s) => (s ? { ...s, slug } : s));
          void board.refetch();
        }}
        mutate={mutate}
      />

      <EditorOverlay
        open={editor !== null}
        slug={editor?.slug ?? null}
        job={jobFor(editor?.slug, editor?.node)}
        node={editor?.node}
        onClose={() => setEditor(null)}
        onCreated={(slug) => setEditor({ slug })}
        // Byte-for-byte the create re-point: same state, same shape, and the
        // same effect on the body below, which is keyed on `slug` and so
        // remounts against the name the server just confirmed.
        onRenamed={(slug) => {
          setEditor({ slug });
          void board.refetch();
        }}
        onReactivate={(slug) => {
          setEditor(null);
          // NO requireText: SteerOverlay's mode table says a done→working drag
          // is the ONLY caller that sets it — the card's and editor's ↺ buttons
          // open reactivate with the message optional ("leave empty to just
          // wake it", the composer's own placeholder). This passed true from
          // the React port until 2026-08: an empty send then dead-ended on a
          // small "message is empty" note that contradicted the placeholder,
          // which read as the reactivate button doing nothing at all.
          openSteer(slug, "reactivate");
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
        library={library.items}
        onSaveArtifact={saveArtifact}
        onRemoveSaved={removeSaved}
        mutate={mutate}
      />

      <ThreadOverlay
        open={thread !== null}
        slug={thread?.slug ?? null}
        tid={thread?.tid ?? null}
        job={jobFor(thread?.slug, thread?.node)}
        onClose={() => setThread(null)}
        // spreads the open thread so a remote ask keeps its owner `node`
        onBranched={(tid) => setThread((t) => (t ? { ...t, tid } : t))}
        mutate={mutate}
        operator={board.board?.operator}
        node={thread?.node}
        boardNodeID={board.board?.node_id}
      />

      {/* ◆ mind — once, over every view. A capture lands in a mind thread:
          the thread overlay opens on it, and the artifact viewer (z-index
          30, above the thread overlay's 25) is closed first for the reason
          onAskBranched gives — a thread opened underneath it is invisible. */}
      <MindButton
        onOpened={(slug, tid) => {
          setArtifact(null);
          setThread({ slug, tid });
          void board.refetch();
        }}
      />

      <UsageOverlay
        open={usageOpen}
        data={usage.data}
        onClose={() => setUsageOpen(false)}
        refetch={refetchUsage}
      />

      <DevicesOverlay
        open={devicesOpen}
        onClose={() => setDevicesOpen(false)}
        board={board.board}
      />

      <Toast />
    </>
  );
}
