import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { createFeedback, openInEditor, threadOp } from "../../api/client.ts";
import type { WebArtifact, WebJob, WebLibraryItem } from "../../api/types.ts";
import { useArtifactSeq, type SeqEntry } from "../../hooks/useArtifactSeq.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { useRenderedMD } from "../../hooks/useRenderedMD.ts";
import {
  decorateArtifactRefs,
  matchArtifactRef,
  type ArtifactRefHit,
} from "../../lib/artifactRefs.ts";
import {
  codeRefTarget,
  wireCodeRefs,
  type CodeRefTarget,
} from "../../lib/codeRefs.ts";
import { cardCompilable } from "../../lib/isoscene/index.ts";
import { libraryMatch } from "../../lib/library.ts";
import { ArtifactPager, useArtifactKeys } from "./ArtifactPager.tsx";
import { askThreadSeed, askThreadTitle } from "./askThread.ts";
import { IsoSceneView } from "./IsoSceneView.tsx";
import { Overlay, type Mutate } from "./Overlay.tsx";
import { showToast } from "./Toast.tsx";

// The artifact viewer (app.js:1774 openArtifact). FIVE render modes — three
// picked from the URL's extension alone, two opt-in toggles:
//
//   png|jpe?g|gif|webp   → <img>
//   html|svg|pdf         → same-origin <iframe src> (for a remote html|svg,
//                          a sandboxed, opaque-origin one — below)
//   anything else        → fetched text, rendered as markdown
//   …with `raw` on       → the same fetched text, verbatim in a <pre>-ish body
//   …with `3D` on        → the isoscene-compiled scene (IsoSceneView), offered
//                          only for a LOCAL .html diagram card whose whole
//                          markup parses (lib/isoscene cardCompilable); a
//                          bespoke card gets no 3D affordance and displays
//                          as-is
//
// The iframe is deliberately UNSANDBOXED. web/serve.go:253 justifies it as
// "same trust as opening the file" — these are artifacts this machine's own
// agents wrote into this machine's own cell directory, served from the same
// origin, and a sandbox would break exactly the diagram cards the viewer
// exists for. Adding `sandbox` is a separate decision with its own
// consequences, not a porting cleanup.
//
// A REMOTE .html/.svg artifact is the exception, and gets `sandbox=""` —
// every restriction, scripts included (ArtifactFrame's `sandboxed`). Its bytes
// come from another node through this node's proxy, on THIS origin, so an
// unsandboxed frame would run a sibling's script with this board's rights —
// "same trust as opening the file" holds only for files this machine's own
// agents wrote. Scripts stay off too, not just same-origin: an opaque-origin
// script can still SEND a no-cors POST at this board. The gate refuses the
// writes among such requests (its JSON and Origin rules), and running no
// script at all keeps a sibling's page from depending on that. (A `.htm` is
// never framed — `isPage` takes html|svg|pdf — so it reaches the markdown
// branch as text.) The costs: a diagram card's own script does not run, the arrow keys and code
// refs cannot be wired inside the frame (its document is unreachable), so
// paging a remote page is the ‹ › buttons once focus is in the frame, and the
// 3D view is not offered (below). A remote PDF is framed WITHOUT a sandbox:
// Chromium's PDF viewer refuses to load in a sandboxed frame, and neither a
// PDF nor an image can script against this origin.
//
// Reading an artifact set is a SEQUENCE, so the viewer also pages: `job` is
// the job the open artifact belongs to and its artifact list is the ordered
// set ‹ › and ←/→ walk. See useArtifactSeq for what that derivation has to
// guarantee.

const isPage = (url: string) => /\.(html|svg|pdf)$/i.test(url);
const isImage = (url: string) => /\.(png|jpe?g|gif|webp)$/i.test(url);
// only .html pages can hold a compilable diagram card; .svg/.pdf never offer 3D
// exported for the fleet tests, which pin the tail test on a proxied url: a
// remote artifact's url ends in its `name` parameter, so the same test holds
// for `/api/nodes/artifact?…&name=10-arch.html`
export const isHtmlPage = (url: string) => /\.html?$/i.test(url);
// the page types that can carry script — what a remote frame is sandboxed for
const isScriptablePage = (url: string) => /\.(html?|svg)$/i.test(url);

// The 3D toggle's session stickiness: compilable cards open FLAT (carl's
// call — the flat card is the default, with the toggle opting in to the
// compiled scene). A reader who toggles on gets the next card in 3D too.
// Module state rather than per-artifact — the per-artifact reset block below
// deliberately re-seeds from this, exactly how `raw` deliberately does NOT
// persist (raw is a debugging posture, 3D is a reading preference).
let isoDefault = false;

export interface ArtifactParts {
  slug: string;
  home: string;
  name: string;
}

/**
 * The two things the head's one composer can be. They share a box because they
 * are the same gesture — "I have something to say about THIS artifact, while
 * looking at it" — and differ only in where it goes:
 *
 *   feedback  → a kraken.db feedback row the job's zeus reads on its next
 *               working session. Asynchronous, and no answer comes back.
 *   ask       → a side thread branched off zeus, seeded with the question.
 *               A second claude answers it in its own tmux window, WITHOUT
 *               disturbing the job.
 *
 * Neither is new machinery: both endpoints already exist and neither gained a
 * field for this. What is new is reaching the second one from the artifact you
 * are reading rather than from the job card, where you would have to re-type
 * which artifact you meant.
 */
export type ComposerMode = "feedback" | "ask";

/** An unsent composer, banked against the artifact it was typed about. */
interface ComposerDraft {
  mode: ComposerMode;
  text: string;
}

/** What the draft stash needs to know about the composer on screen. */
interface ComposerSnapshot {
  url: string;
  text: string;
  mode: ComposerMode | null;
}

// The ask composer's title/seed builders live in askThread.ts (imported with
// the rest of the head block), shared with the questions panel's per-row ask
// — one module owns the title budget and the surrogate-safe cap for every
// branch-to-ask surface.

/**
 * Recover {slug, home, name} from an /api/artifact URL (app.js:1641).
 *
 * The URL is the overlay's own source of truth for what is open, so a feedback
 * post addresses the exact artifact being viewed by re-deriving from it rather
 * than by threading a separate triple through every chip that can open one.
 *
 * Only the FIRST TWO segments split off: a cell-home artifact name may itself
 * contain slashes (a subpath artifact), and `(.+)` has to keep them.
 */
export function artifactParts(url: string): ArtifactParts | null {
  const m = url.match(/^\/api\/artifact\/([^/]+)\/([^/]+)\/(.+)$/);
  return m && m[1] && m[2] && m[3]
    ? { slug: m[1], home: m[2], name: m[3] }
    : null;
}

/**
 * A clicked code ref, sent to the editor on this machine.
 *
 * The url is the whole argument list: `artifactParts` yields both the slug the
 * server resolves the checkout from and the `home` segment that says WHICH
 * namespace to resolve it in. An archived job's artifact URLs carry its
 * ArchivedName as the slug segment — exactly the key the archive namespace is
 * indexed by — so a click here must say `archived`, or a live job that has
 * since taken the freed slug resolves first and the ref opens in the wrong
 * checkout with nothing on screen to say so.
 *
 * Deliberately NOT routed through `mutate`: opening a file changes nothing on
 * the board, so there is nothing to invalidate, and the one funnel `mutate`
 * offers — a failure line in the clock corner — is the wrong place for the two
 * things this call actually says. Both of them are about the ref you just
 * clicked ("that worktree is gone, I opened the primary checkout", "three files
 * are named cell.go"), so they go to the toast, where the reader is looking.
 *
 * A refusal arrives as a thrown ApiError carrying the server's own `{error}`
 * string (client.ts's parseError), which is why the catch toasts `message` and
 * not a sentence of its own. A bare `{ok:true}` is silent — the editor coming
 * to the front is the feedback.
 */
async function openCodeRef(url: string, t: CodeRefTarget): Promise<void> {
  const parts = artifactParts(url);
  if (!parts) return;
  try {
    const res = await openInEditor(
      parts.slug,
      t.path,
      t.line,
      t.col,
      parts.home === "archive",
    );
    if (res?.note) showToast(res.note);
  } catch (e) {
    showToast(e instanceof Error ? e.message : String(e));
  }
}

export interface ArtifactOverlayProps {
  open: boolean;
  /** The /api/artifact URL. */
  url: string;
  /** Display name — "<slug>/<artifact>", as the chip's data-name carries it. */
  name: string;
  /**
   * The job the open artifact belongs to, resolved from the jobKey the chip
   * carried — its `artifacts` list IS the pager's sequence, re-read on every
   * board poll so a live cell's new artifact comes within reach without a
   * reopen. Undefined (or a job that has left the board) opens a singleton:
   * the pager hides and the viewer behaves as it did before next/prev.
   *
   * It must be looked up by jobKey and NOT by bare slug — an archived job and
   * an active one that reused its freed slug would otherwise resolve to each
   * other's artifacts.
   */
  job: WebJob | undefined;
  /**
   * Step to another artifact of the same job. The caller re-points `url`/`name`
   * and MUST keep the jobKey it opened with: a step within a sequence must not
   * change which job the sequence came from.
   *
   * Absent = navigation is not wired, which is treated exactly like an
   * unresolvable job: singleton, pager hidden.
   */
  onNavigate: (url: string, name: string) => void;
  onClose: () => void;
  /**
   * A question about the open artifact branched a side thread: show it.
   *
   * The integrator must CLOSE this overlay as it opens the thread one, not
   * stack them — style.css puts `#overlay` at z-index 30 and `#thread-overlay`
   * at 25, so a thread opened underneath would be invisible, and the Escape
   * chain ranks the artifact layer above the thread layer too (ESC_PRIORITY),
   * so the one Escape you would reach for closes the wrong thing.
   */
  onAskBranched: (slug: string, tid: string) => void;
  /**
   * The saved-artifact library as the server currently reports it (App's one
   * poll, shared with the memory view's library mode).
   *
   * The head's ☆/★ button is `libraryMatch(library, url) !== null` and nothing
   * else — there is no local "I clicked it" latch, deliberately: a latch would
   * light the star on a save the server refused, and it would go on claiming
   * "saved" after the row was removed from the list in the other view.
   */
  library: WebLibraryItem[];
  /**
   * Freeze the open artifact into the library. REQUIRED rather than an optional
   * slot, for the reason App.tsx's own onOpenArtifact note gives: an integrator
   * that simply forgets one gets a compile error instead of a dead button.
   *
   * Both of these are expected to route through the shared `mutate` and then
   * refetch the library, which is why neither returns anything — the button's
   * next state comes from `library`, not from the answer.
   */
  onSaveArtifact: (slug: string, home: string, name: string) => Promise<void>;
  /** Drop one saved row by the id the library served. */
  onRemoveSaved: (id: number) => Promise<void>;
  // The head's ✓ read / mark selection button and the body's per-table ✓s
  // were retired with the board's one-shot marks (PR 2, Q10). The body now
  // carries `data-mind-*` attributes instead, and the global ◆ mind button
  // (components/chrome/MindButton.tsx) reads them off a selection.
  /** See Overlay.tsx. Feedback rides the board payload, so this invalidates. */
  mutate: Mutate;
}

export function ArtifactOverlay(props: ArtifactOverlayProps) {
  const { job, url, name, open, onNavigate } = props;

  /**
   * `feedbackDrafts` (app.js:1676) — an unsent composer per artifact url, now
   * carrying WHICH composer it was (feedback or ask) alongside its text.
   *
   * `‹ ›` now sit a few pixels from the text you are writing about, and every
   * open resets the composer, so without this one click on `›` silently
   * destroys a half-typed note with no undo. Refusing to step instead would
   * make the pager look broken; carrying the draft with its artifact is what
   * the rest of the board does.
   *
   * One draft per artifact rather than one per (artifact, mode): the head has
   * one box, and switching modes carries the text across rather than hiding it
   * — see the mode buttons.
   *
   * A Map on a ref, deliberately, not localStorage: surviving the tab is a
   * bigger promise than a scratch composer should make. It lives HERE rather
   * than in the body because the body remounts on every step — which is the
   * whole point of the remount — and it survives a close for the same reason
   * the vanilla module-level Map does.
   */
  const drafts = useRef<Map<string, ComposerDraft>>(new Map()).current;

  // `job` is the single signal for "no sequence resolvable": undefined means
  // a singleton with the pager hidden. onNavigate used to be part of this test
  // back when it was optional — now it is required, so an integrator that
  // wants the degenerate case says so by passing job={undefined} rather than
  // by forgetting a line.
  const { seq, idx } = useArtifactSeq(
    job,
    url,
    name,
    open,
  );

  /**
   * `stepArtifact` (app.js:1759) — the ONE navigation command, shared by the
   * ‹ › buttons and the arrow keys so the two can't drift. ±1, clamped at both
   * ends: a step past either end is a no-op, never a wrap.
   *
   * It re-points the viewer through the same prop the chips do rather than
   * swapping the body in place, which is what makes `raw`, the fetched text
   * and the open feedback form reset per artifact (they are state of the
   * remounted body, keyed on url).
   */
  const onStep = useCallback(
    (delta: -1 | 1) => {
      const i = idx + delta;
      const next: SeqEntry | undefined = seq[i];
      if (!onNavigate || i < 0 || i >= seq.length || !next) return;
      onNavigate(next.url, next.name);
    },
    [idx, onNavigate, seq],
  );

  return (
    <Overlay
      id="overlay"
      open={open}
      priority={ESC_PRIORITY.artifact}
      onClose={props.onClose}
    >
      {/* deliberately NOT keyed on url — see ArtifactBody's reset block */}
      <ArtifactBody
        {...props}
        drafts={drafts}
        seq={seq}
        idx={idx}
        onStep={onStep}
      />
    </Overlay>
  );
}

interface ArtifactBodyProps extends ArtifactOverlayProps {
  drafts: Map<string, ComposerDraft>;
  seq: SeqEntry[];
  idx: number;
  onStep: (delta: -1 | 1) => void;
}

function ArtifactBody({
  url,
  name,
  job,
  mutate,
  drafts,
  seq,
  idx,
  onStep,
  onNavigate,
  onAskBranched,
  library,
  onSaveArtifact,
  onRemoveSaved,
}: ArtifactBodyProps) {
  const [raw, setRaw] = useState(false);
  // the compiled-3D toggle; seeded from (and feeding) the session-sticky
  // isoDefault — see its comment at module scope
  const [iso, setIso] = useState(isoDefault);
  // undefined = not fetched yet; a page/image is never fetched up front (the
  // iframe/img does its own request), so `raw` on one triggers the fetch late
  const [text, setText] = useState<string | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  // `restoreFeedbackDraft` (app.js:1695): an artifact you left a note
  // half-written on re-opens with the composer showing and the text back in it
  // — in the mode it was written in.
  const [mode, setMode] = useState<ComposerMode | null>(
    () => drafts.get(url)?.mode ?? null,
  );
  const [composerText, setComposerText] = useState(
    () => drafts.get(url)?.text ?? "",
  );
  const [note, setNote] = useState("");
  const [sending, setSending] = useState(false);
  // which composer last succeeded, for the head button's transient ✓ — the
  // composer closes on success, and its note region closes with it
  const [sentMode, setSentMode] = useState<ComposerMode | null>(null);
  // one save/remove in flight — the button's LOOK never comes from here (that
  // is `saved`, i.e. the server's list); this only stops a double press from
  // posting twice while the first round trip is out
  const [savingLib, setSavingLib] = useState(false);
  // which artifact the state above describes — the whole reset block below
  const [shown, setShown] = useState(url);
  const composerInput = useRef<HTMLTextAreaElement | null>(null);

  /**
   * An artifact ref resolved in the rendered body: navigate the viewer to that
   * artifact through the SAME prop the pager steps with, preferring the `seq`
   * entry so the name arrives in the sequence's own "<label>/<artifact>" form.
   * A hit that is in the job's list but not in `seq` — the held-sequence and
   * singleton cases of useArtifactSeq, where the two deliberately diverge —
   * still navigates, with the name composed by the SAME expression
   * `artifactSeqFor` uses, so the title cannot come out different. The jobKey
   * the viewer opened with is kept either way (App re-points url/name only).
   */
  const onOpenRef = useCallback(
    (hit: ArtifactRefHit) => {
      const entry = seq.find((s) => s.url === hit.url);
      if (entry) onNavigate(entry.url, entry.name);
      else if (job) {
        onNavigate(hit.url, `${job.display_slug || job.slug}/${hit.name}`);
      }
    },
    [seq, onNavigate, job],
  );

  /**
   * What is on screen right now, for the draft stash — kept on a ref because
   * the three places that bank a note (a step, an unmount, a send that
   * unmounts this overlay) all need the CURRENT values against the CURRENT
   * url and none of them is a render.
   */
  const latest = useRef<ComposerSnapshot>({ url, text: composerText, mode });

  /**
   * Whether this body is still mounted, read AFTER an await.
   *
   * The url check below catches a pager step, because the reset block re-points
   * `latest.current`. It cannot catch a CLOSE: the overlay unmounts, but the
   * callback lives on in the promise and the ref it captured still names the
   * artifact it was fired from. A branch takes seconds — it spawns a claude in
   * a new tmux window — so "ask, change your mind, press Escape" is an ordinary
   * sequence, and without this the thread overlay would open over the board
   * two seconds after the reader walked away from it.
   */
  const alive = useRef(true);
  useEffect(() => {
    // the `= true` is not belt-and-braces next to `useRef(true)`: main.tsx
    // mounts the board inside <StrictMode>, which runs mount → cleanup → mount,
    // and without it the cleanup's `false` is the value that sticks for the
    // life of a perfectly live component — every send and every ask would then
    // take the early return, silently, in dev only. `usePoll` writes its own
    // aliveRef the same way.
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  /**
   * `stashFeedbackDraft` (app.js:1682) — bank the note against the artifact it
   * was typed about. An emptied or explicitly cancelled composer drops its
   * stash rather than keeping a stale one.
   */
  const stash = useCallback(
    (of: ComposerSnapshot) => {
      if (of.mode && of.text.trim()) {
        drafts.set(of.url, { mode: of.mode, text: of.text });
      } else drafts.delete(of.url);
    },
    [drafts],
  );

  /**
   * `openArtifact`'s per-artifact reset — raw off, the cached text dropped, the
   * composer closed and re-seeded from this artifact's own draft.
   *
   * This is React's "adjust state when a prop changes" idiom rather than the
   * `key={url}` remount the port's other overlays use, and the difference is
   * NOT stylistic. A remount tears down and rebuilds the whole `.panel-head`,
   * and `#panel-pos` lives in it: a live region that is REPLACED rather than
   * mutated is not reliably announced at all, which is precisely the failure
   * `TestPagerReadoutDoesNotSpamItsLiveRegion`'s second assertion names ("an
   * arrow-key navigation then announces nothing"). It also drops focus off the
   * `›` button on every activation, so a keyboard user has to re-aim after each
   * step. Both are silent, and both are regressions a step-by-remount buys for
   * nothing — this block is the same reset in one place.
   *
   * Ordered so it runs BEFORE the children render: React re-renders
   * immediately and discards this pass, so no frame ever paints the previous
   * artifact's text under the new artifact's title.
   */
  if (shown !== url) {
    stash(latest.current); // the artifact we are LEAVING, with its own values
    const draft = drafts.get(url);
    setShown(url);
    setRaw(false);
    setIso(isoDefault);
    setText(undefined);
    setError(null);
    setNote("");
    setSending(false);
    setSavingLib(false);
    setSentMode(null);
    setMode(draft?.mode ?? null);
    setComposerText(draft?.text ?? "");
    latest.current = {
      url,
      text: draft?.text ?? "",
      mode: draft?.mode ?? null,
    };
  } else {
    latest.current = { url, text: composerText, mode };
  }

  const page = isPage(url);
  const image = isImage(url);
  // .html pages fetch their text up front too: cardCompilable needs the bytes
  // to decide whether the 3D toggle is offered at all, and a bespoke card must
  // show NO affordance rather than a button that fails on press. One extra
  // same-origin read of a file the iframe fetches anyway.
  const htmlPage = page && isHtmlPage(url);
  const needsText = raw || (!page && !image) || htmlPage;
  const compilable = useMemo(
    () => (htmlPage && text !== undefined ? cardCompilable(text) : false),
    [htmlPage, text],
  );

  /**
   * The library row this artifact IS, if any — the ☆/★ button's entire state.
   *
   * It matches BOTH url shapes (lib/library.ts), which is what makes the button
   * work in the one place a local latch could not: a snapshot opened FROM the
   * library carries a `/api/library/artifact/<id>/…` url, has no job, and is by
   * definition already saved — pressing ★ there removes it.
   */
  const saved = useMemo(() => libraryMatch(library, url), [library, url]);
  // What a SAVE would address. A url that yields no parts and matches no row
  // is one nothing can be done with — the button hides rather than offering a
  // press that can only fail.
  // memoised so its identity is stable across the 3s board re-render — it is a
  // dependency of the handler below
  const saveParts = useMemo(() => artifactParts(url), [url]);
  // Another node's job: its artifact is served through this node's proxy, and
  // every write this head offers (feedback, ask, the library) is a write into
  // THIS machine's stores about a job that lives elsewhere — so none is
  // offered. The url test covers an owned card that has since left the board.
  const remote = !!job?.owner || url.startsWith("/api/nodes/");
  const savable = !remote && (saved !== null || saveParts !== null);
  // No 3D for another node's card: the compiled scene is inserted into THIS
  // board's document as live markup, the one place a sibling's content would
  // stop being confined to a sandboxed frame.
  const offer3D = compilable && !remote;

  const toggleSaved = useCallback(async () => {
    if (savingLib) return;
    setSavingLib(true);
    // The failure path is deliberately silent HERE: both callbacks go through
    // the shared `mutate`, which parks the server's own sentence in the clock
    // corner, and the button's state comes back from the refetched list — so a
    // refused save simply leaves it reading ☆ save.
    if (saved) await onRemoveSaved(saved.id);
    else if (saveParts) {
      await onSaveArtifact(saveParts.slug, saveParts.home, saveParts.name);
    }
    // Same identity question `send` and `ask` ask: a step or a close mid-flight
    // means this body is no longer the one that pressed the button.
    if (alive.current && latest.current.url === url) setSavingLib(false);
  }, [savingLib, saved, saveParts, onRemoveSaved, onSaveArtifact, url]);

  /**
   * The sha256 (hex) of the artifact bytes this viewer loaded — what a ◆ mind
   * capture's `artifact` source records, so the server can later say whether
   * the artifact has `changed` since. Hashed from the fetched TEXT re-encoded
   * as UTF-8, which equals the file's bytes for any valid UTF-8 file; a page
   * or image the viewer never fetches as text (.svg/.pdf/.png …) has no sha,
   * and the attribute is simply absent.
   */
  const sha = useArtifactSHA(text);

  /**
   * What the rendered body stamps for the ◆ mind button (lib/mind.ts): every
   * body mode — markdown, raw, the iframe wrapper, an image — is an `artifact`
   * source. A library snapshot (no {slug, home, name} in its url) still
   * names the artifact, with no slug: the capture then has no job to fork and
   * the server says so.
   */
  const mindAttrs = useMemo<MindAttrs | Record<string, never>>(
    () =>
      // the mental model is this machine's: another node's artifact is never a
      // capture source, so it carries no `data-mind-*` at all
      remote
        ? ({} as Record<string, never>)
        : {
            "data-mind-kind": "artifact",
            "data-mind-slug": saveParts?.slug || undefined,
            "data-mind-home": saveParts?.home || undefined,
            "data-mind-name": saveParts?.name || name.split("/").slice(1).join("/") || name,
            "data-mind-sha": sha || undefined,
          },
    [remote, saveParts, name, sha],
  );

  const onKey = useArtifactKeys(onStep);

  // the other way of leaving an artifact: the viewer closing. Escape and the
  // backdrop click both unmount this, so there is no "leaving banks the note,
  // except by Escape" split to get wrong. Empty deps — this must fire on
  // unmount ALONE, never on a url change, which the block above already owns.
  useEffect(() => {
    return () => stash(latest.current);
  }, [stash]);

  useEffect(() => {
    if (!needsText || text !== undefined) return;
    const ac = new AbortController();
    fetch(url, { signal: ac.signal })
      .then((r) => {
        if (!r.ok) throw new Error(String(r.status));
        return r.text();
      })
      .then((t) => {
        if (ac.signal.aborted) return;
        setText(t);
        setError(null);
      })
      .catch((e: unknown) => {
        if (ac.signal.aborted) return;
        setError("failed to load: " + (e instanceof Error ? e.message : e));
      });
    return () => ac.abort();
  }, [needsText, text, url]);

  // a lingering "saved ✓" must never carry over to another artifact or stack
  // across rapid sends — the timer is owned by this mount and dies with it
  useEffect(() => {
    if (!sentMode) return;
    const id = window.setTimeout(() => setSentMode(null), 2000);
    return () => window.clearTimeout(id);
  }, [sentMode]);

  const send = useCallback(async () => {
    const parts = artifactParts(url);
    const feedback = composerText.trim();
    if (!parts) {
      setNote("can't identify this artifact");
      return;
    }
    if (!feedback) {
      setNote("feedback is empty");
      return;
    }
    setSending(true);
    const ok = await mutate(() =>
      createFeedback(parts.slug, parts.home, parts.name, feedback),
    );
    // "is the composer still showing the note I sent?" — an IDENTITY question
    // (app.js:4104), asked AFTER the await because a step across it must be
    // seen. The post itself is right either way, but the panel belongs to
    // whatever is on screen: page onto another artifact mid-send and clearing
    // the composer here would wipe THAT artifact's restored draft, and
    // "saved ✓" would appear over a note nobody has sent.
    const onSentArtifact = alive.current && latest.current.url === url;
    if (ok) drafts.delete(url); // it's sent; it is no longer a draft
    if (!onSentArtifact) return;
    setSending(false);
    if (ok) {
      setMode(null);
      setComposerText("");
      setNote("");
      setSentMode("feedback");
    } else {
      setNote("failed — see status bar");
    }
  }, [composerText, drafts, mutate, url]);

  /**
   * The ask composer's send: branch a side thread seeded with the question.
   *
   * Nothing here is new on the wire — it is the SAME `/api/threads` branch
   * ThreadOverlay's own branch dialog posts, with the title and seed filled in
   * from the artifact you are looking at instead of typed by hand.
   *
   * `job.slug` addresses the op, not `parts.slug`: the composer is only offered
   * for a live `job`, and that row is what the server resolves a branch
   * against — with the guard below covering the restored-draft case where the
   * composer is on screen and the job is not. The artifact NAME still comes
   * from the url, which is the overlay's own source of truth for what is open.
   */
  const ask = useCallback(async () => {
    const parts = artifactParts(url);
    const question = composerText.trim();
    if (!parts) {
      setNote("can't identify this artifact");
      return;
    }
    // Reachable without the head button: an ask draft restored onto an artifact
    // whose job has since left the board comes back with the composer open
    // while `⑂ ask` itself is hidden. It is a different refusal from an
    // unparseable url and says so.
    if (!job) {
      setNote("this job is no longer on the board");
      return;
    }
    if (!question) {
      setNote("question is empty");
      return;
    }
    setSending(true);
    const res = await mutate(() =>
      threadOp(job.slug, "branch", {
        title: askThreadTitle(parts.name, question),
        text: askThreadSeed(parts.name, question),
      }),
    );
    // Same identity question as `send`, and the same answer: page onto another
    // artifact — or close the viewer — mid-branch and this panel is no longer
    // the one that asked. The thread exists either way and the board lists it,
    // so a branch that lands after you have moved on stays branched and simply
    // isn't opened. That is ThreadOverlay's cancelled-branch rule, which it
    // gets for free from its own unmount; here the callback lives one layer up
    // in App, so `alive` is what re-states it.
    const onAskedArtifact = alive.current && latest.current.url === url;
    if (res) drafts.delete(url); // it's asked; it is no longer a draft
    if (!onAskedArtifact) return;
    setSending(false);
    if (!res) {
      setNote("failed — see status bar");
      return;
    }
    setMode(null);
    setComposerText("");
    setNote("");
    setSentMode("ask");
    // Clear the ref BEFORE handing off: onAskBranched unmounts this body (the
    // integrator closes the artifact overlay as it opens the thread one), and
    // the unmount stash reads `latest.current` — not the state we just set,
    // which no render will ever apply. Without this the question you just sent
    // comes back as a draft the next time you open the artifact.
    latest.current = { url, text: "", mode: null };
    // No tid came back (defensive — the branch op always returns one): the
    // thread exists and the board lists it, so say so out of band. The note
    // region is not an option here; it lives inside the composer this very
    // success just closed.
    if (res.tid) onAskBranched(job.slug, res.tid);
    else showToast("branched — the thread is listed on the job");
  }, [composerText, drafts, job, mutate, onAskBranched, url]);

  /**
   * The head's two mode buttons, which drive ONE box.
   *
   * Clicking the mode that is already open closes it (the feedback button's
   * original toggle). Clicking the OTHER one switches the box over and KEEPS
   * what you have typed: those are still your words, and "actually this is a
   * question, not a complaint" is a real thing to notice halfway through a
   * paragraph. Clearing on a mode switch would be the same silent destruction
   * the per-artifact drafts exist to prevent, with a single click and no undo.
   */
  const toggleComposer = useCallback(
    (next: ComposerMode) => {
      if (mode === next) {
        setMode(null);
        return;
      }
      setMode(next);
      setNote("");
      // on the OPEN transition the box is `hidden` until this render lands, so
      // the focus waits for it. On a mode switch it is already on screen and
      // already focused, and this is a harmless no-op.
      requestAnimationFrame(() => composerInput.current?.focus());
    },
    [mode],
  );

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="panel-title">
          {name}
        </span>
        {/* between the title and the spacer, so ↗ tab / raw / feedback keep
            their right-hand positions (index.html:117) */}
        <ArtifactPager idx={idx} count={seq.length} onStep={onStep} />
        <span className="spacer"></span>
        <button
          className="btn ghost"
          id="panel-open"
          onClick={() => {
            if (url) window.open(url, "_blank");
          }}
        >
          ↗ tab
        </button>
        {/* a PDF has no useful raw text and an image has none at all */}
        <button
          className="btn ghost"
          id="panel-raw"
          hidden={image || /\.pdf$/i.test(url)}
          onClick={() => setRaw((v) => !v)}
        >
          raw
        </button>
        {/* the compiled isometric view — offered only when the whole card
            parses (cardCompilable); a bespoke concept diagram shows no 3D
            affordance at all and displays as-is */}
        <button
          className="btn ghost"
          id="panel-iso"
          hidden={!offer3D}
          aria-pressed={iso}
          title="compile this card into an animated isometric scene"
          onClick={() => {
            // the sticky write lives in the handler, not the updater —
            // StrictMode double-invokes updaters and they are meant to be pure
            isoDefault = !iso;
            setIso(!iso);
          }}
        >
          3D
        </button>
        {/* Freeze this artifact into the library — a SNAPSHOT of its current
            bytes, which is why it survives the job's teardown, its archive and
            even a hard delete. `aria-pressed` is the #panel-iso idiom directly
            above, and the pressed state is the SERVER's (`saved`), so this
            button cannot claim a save that did not land.

            Hidden when there is nothing to address: a url that parses to no
            {slug, home, name} and matches no saved row can neither be saved nor
            removed, and a button whose only outcome is a refusal is worse than
            no button. */}
        <button
          className="btn ghost"
          id="panel-save"
          hidden={!savable}
          disabled={savingLib}
          aria-pressed={saved !== null}
          title={
            saved
              ? "remove this snapshot from the library"
              : "save a frozen copy of this artifact to the library"
          }
          onClick={() => void toggleSaved()}
        >
          {saved ? "★ saved" : "☆ save"}
        </button>
        {/* Branching forks the cell's zeus session, so it needs a live cell —
            the same `job.live` gate ThreadChips.tsx puts on the ⑂ it renders in
            the steer modal. Without a job resolved (a singleton open) there is
            nothing to fork either.

            `disabled={sending}` on BOTH toggles — and on cancel below, which
            is the third door to the same room. A branch is seconds long, and
            closing OR switching the composer mid-flight would route its own
            outcome — "failed — see status bar" — into a region that is no
            longer on screen, or worse, into the other mode's. */}
        {/* neither composer is offered on another node's job: feedback rows
            and side threads are both the OWNER's stores (see `remote`) */}
        {remote ? null : (
          <button
            className="btn ghost"
            id="panel-ask"
            hidden={!job?.live}
            disabled={sending}
            title="ask about this artifact in a side thread — forks zeus, doesn't interrupt it"
            onClick={() => toggleComposer("ask")}
          >
            {sentMode === "ask" ? "asked ✓" : "⑂ ask"}
          </button>
        )}
        {remote ? null : (
          <button
            className="btn ghost"
            id="panel-feedback"
            disabled={sending}
            title="leave feedback on this artifact for the job to read"
            onClick={() => toggleComposer("feedback")}
          >
            {sentMode === "feedback" ? "saved ✓" : "feedback"}
          </button>
        )}
      </div>
      <div
        className={"feedback-form" + (mode === "ask" ? " ask" : "")}
        id={mode === "ask" ? "ask-form" : "feedback-form"}
        hidden={!mode || remote}
      >
        {/* the ask box is one gesture away from the feedback box and does
            something quite different with what you type — this line is what
            keeps them apart at a glance */}
        <div className="composer-hint" hidden={mode !== "ask"}>
          ⑂ forks zeus' session into a side thread and asks it there — zeus
          itself is not interrupted. Threads are for asking, not for doing.
        </div>
        <textarea
          id={mode === "ask" ? "ask-text" : "feedback-text"}
          ref={composerInput}
          rows={3}
          placeholder={
            mode === "ask"
              ? "what do you want elaborated? — answered in a side thread"
              : "what's wrong with this doc/diagram?"
          }
          value={composerText}
          onChange={(e) => setComposerText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey)) return;
            // a CJK candidate's commit Enter also fires key:"Enter". Behind a
            // modifier that is near unreachable, unlike the plain-Enter submits
            // ThreadOverlay and DocQuestions guard — it is here because it is
            // the pair of checks every Enter-submit in this app makes, and a
            // divergent one would be the odd one out.
            if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) {
              return;
            }
            e.preventDefault();
            if (sending) return;
            void (mode === "ask" ? ask() : send());
          }}
        />
        <div className="feedback-actions">
          <span
            className="feedback-note"
            id={mode === "ask" ? "ask-note" : "feedback-note"}
          >
            {note}
          </span>
          <button
            className="btn ghost"
            id={mode === "ask" ? "ask-cancel" : "feedback-cancel"}
            // the third composer-closing control, locked for the same reason as
            // the two head toggles — and for one of its own: the request is
            // already out and cannot be recalled, so a live cancel would only
            // let the reader express an intention the code cannot honour. It
            // would close the composer and the thread would still open.
            disabled={sending}
            onClick={() => {
              // the one gesture that means "throw this away": it must also drop
              // the stash, or the note reappears the next time you page onto
              // this artifact
              setMode(null);
              setComposerText("");
              setNote("");
            }}
          >
            cancel
          </button>
          {/* "⑂ branch", not a second "⑂ ask": the head toggle already carries
              that label, and two buttons with one accessible name in one panel
              is a screen reader announcing the same control twice. */}
          <button
            className="btn"
            id={mode === "ask" ? "ask-send" : "feedback-send"}
            disabled={sending}
            title="⌘⏎"
            onClick={() => void (mode === "ask" ? ask() : send())}
          >
            {mode === "ask" ? "⑂ branch" : "send"}
          </button>
        </div>
      </div>
      <ArtifactBodyContent
        url={url}
        raw={raw}
        iso={iso && offer3D}
        page={page}
        image={image}
        text={text}
        error={error}
        onKey={onKey}
        artifacts={job?.artifacts ?? []}
        onOpenRef={onOpenRef}
        mindAttrs={mindAttrs}
        remote={remote}
      />
    </>
  );
}

/**
 * `hookIframeKeys` (app.js:1864) — the artifact iframe's OWN document gets the
 * arrow handler too.
 *
 * Once focus lands inside the frame — one click on a diagram card does it —
 * keydown fires on contentDocument and never bubbles out to the board, so
 * without this the arrows die on exactly the artifacts you page through most.
 * Attached on load, because contentDocument before then is the blank
 * placeholder document the real one replaces.
 *
 * try/catch and a null-tolerant read: a document that isn't reachable must
 * degrade to buttons-only, never throw. A PDF is the accepted case of that —
 * the browser's built-in viewer is its own plugin document, unreachable from
 * here, so on a .pdf the arrows stay with the viewer and ‹ › are the pager.
 *
 * The listener is removed on unmount and on a src change. Both happen: a step
 * remounts the whole body, and the raw toggle coming back OFF builds a brand
 * new frame with a brand new document to route arrows out of.
 *
 * The SAME attach point also wires the frame's code refs (lib/codeRefs.ts) —
 * a hermes diagram's `data-code` on a component `<g>` is only clickable once
 * something inside that document is listening. It rides the identical
 * discipline for the identical reason: every rebuild of the frame is a new
 * document, and a wiring left pointing at the old one is a card whose boxes
 * quietly stop opening anything. `hooked.current` is the one dedupe for both,
 * so a re-fired `load` on an unchanged document adds neither a second keydown
 * listener nor a second click one.
 */
function ArtifactFrame({
  url,
  onKey,
  sandboxed = false,
}: {
  url: string;
  onKey: (ev: KeyboardEvent) => void;
  /**
   * Another node's artifact: framed with `sandbox=""` (see the
   * header), which makes it cross-origin, so nothing is wired inside it.
   */
  sandboxed?: boolean;
}) {
  const frame = useRef<HTMLIFrameElement | null>(null);
  const hooked = useRef<Document | null>(null);
  const unwire = useRef<(() => void) | null>(null);
  // render-phase ref write, deliberately: a latest-value mirror, read only
  // from the iframe document's keydown listener, never during this render
  const listener = useRef(onKey);
  listener.current = onKey;

  // The frame's own url is the source of truth for which job's checkout a ref
  // resolves against — the same derivation the feedback composer makes, and for
  // the same reason (artifactParts). openCodeRef takes the url rather than a
  // slug because the `home` segment beside it is what says archive-or-live; a
  // url no parts come out of simply opens nothing.
  const opener = useRef<(t: CodeRefTarget) => void>(() => {});
  opener.current = (t: CodeRefTarget) => {
    void openCodeRef(url, t);
  };

  // stable across renders — the same function object has to reach
  // removeEventListener that reached addEventListener
  const handle = useCallback((ev: Event) => {
    listener.current(ev as KeyboardEvent);
  }, []);

  const attach = useCallback(() => {
    // a sandboxed frame's document is cross-origin by construction — skip
    // rather than lean on the catch below
    if (sandboxed) return;
    try {
      const doc = frame.current?.contentDocument ?? null;
      if (!doc || doc === hooked.current) return;
      hooked.current?.removeEventListener("keydown", handle);
      unwire.current?.();
      doc.addEventListener("keydown", handle);
      // through the ref, so `attach` itself stays stable across url changes —
      // the wiring is hung once per document and must not be re-hung per render
      unwire.current = wireCodeRefs(doc, (t) => opener.current(t));
      hooked.current = doc;
    } catch {
      /* cross-origin or torn down: buttons still work */
    }
  }, [handle, sandboxed]);

  useEffect(() => {
    return () => {
      try {
        hooked.current?.removeEventListener("keydown", handle);
        unwire.current?.();
      } catch {
        /* the document went away with the frame */
      }
      unwire.current = null;
      hooked.current = null;
    };
  }, [handle, url]);

  return (
    <iframe
      ref={frame}
      src={url}
      title="artifact"
      // "" is the strictest sandbox (no scripts, opaque origin) — for a remote
      // artifact; a local one carries no attribute at all
      sandbox={sandboxed ? "" : undefined}
      onLoad={attach}
    />
  );
}

function ArtifactBodyContent({
  url,
  raw,
  iso,
  page,
  image,
  text,
  error,
  onKey,
  artifacts,
  onOpenRef,
  mindAttrs,
  remote,
}: {
  /** another node's artifact — framed sandboxed (see the header) */
  remote: boolean;
  url: string;
  raw: boolean;
  /** true only when the 3D toggle is on AND the card compiles (the caller
   * ANDs the two, so a stale sticky preference can never blank a card) */
  iso: boolean;
  page: boolean;
  image: boolean;
  text: string | undefined;
  error: string | null;
  onKey: (ev: KeyboardEvent) => void;
  /** The job's artifact list — the membership set artifact refs resolve against. */
  artifacts: WebArtifact[];
  /** Open a resolved artifact ref in this viewer (ArtifactBody's seq lookup). */
  onOpenRef: (hit: ArtifactRefHit) => void;
  /** The ◆ mind source every body mode carries — empty for another node's. */
  mindAttrs: MindAttrs | Record<string, never>;
}) {
  // Above the branches because it is a hook, and memoised because this is the
  // component the 3s board poll used to rebuild wholesale — see useRenderedMD.
  //
  // Fed `undefined` whenever the markdown branch is unreachable, which is
  // exactly `raw`: the raw branch has real `text` (needsText fetches it FOR
  // raw) and renders it verbatim, so parsing it as markdown here would burn a
  // whole renderMD over a body no branch will mount — on a raw-opened page
  // artifact, potentially hundreds of KB of it. Pages need the same guard now
  // that the 3D probe fetches every .html page's text up front: without
  // `page`/`image` here, opening any diagram card would markdown-parse 30-40KB
  // of HTML source for a branch `mdMounted` below can never take.
  const md = useRenderedMD(raw || page || image ? undefined : text);

  // ONE delegated handler on the body rather than per-`<code>` wiring, for the
  // reason the body is innerHTML in the first place: there are no React
  // elements down there to hang a prop on. Artifact refs are checked FIRST —
  // a span naming a sibling artifact opens it in this viewer, and only a span
  // that names nothing in the job's own list falls through to the editor path.
  // codeRefTarget answers null for a plain `<code>` and for every other click
  // in the doc, so the whole handler is inert everywhere except on a ref.
  const onBodyClick = useCallback(
    (ev: ReactMouseEvent<HTMLDivElement>) => {
      const el = ev.target;
      if (!(el instanceof Element)) return;
      const code = el.closest("code");
      if (code && !code.closest("pre")) {
        const hit = matchArtifactRef(code.textContent ?? "", artifacts);
        if (hit) {
          onOpenRef(hit);
          return;
        }
      }
      const target = codeRefTarget(el);
      if (!target) return;
      void openCodeRef(url, target);
    },
    [url, artifacts, onOpenRef],
  );

  /**
   * Dress artifact refs whenever the rendered body or the artifact list
   * changes: the renderer is pure and cannot know this job's artifact list,
   * so membership is decided here.
   */
  const bodyEl = useRef<HTMLDivElement | null>(null);
  const lastSweep = useRef<{
    md: { __html: string } | null;
    key: string;
  }>({
    md: null,
    key: "",
  });
  const mdMounted = !raw && !image && !page && !error && text !== undefined;
  useEffect(() => {
    // A branch switch (raw toggle, page/image, error) replaces the body's
    // content wholesale, and the dressing goes with it. Every one of those
    // transitions also changes `md` (useRenderedMD is keyed on the source
    // string, which each of them moves), so the sweep would re-run anyway —
    // this reset is the insurance that keeps that true if the branch
    // conditions ever stop moving `text`.
    if (!mdMounted || !bodyEl.current) {
      lastSweep.current = { md: null, key: "" };
      return;
    }
    const key = artifacts.map((a) => a.url).join("\n");
    // `artifacts` is a fresh array every 3s poll; `md` is memoised per source
    // text. Re-sweeping the DOM each tick for unchanged inputs is exactly the
    // poll-path work this file avoids elsewhere, so the sweep runs only when
    // an input actually changed.
    if (lastSweep.current.md === md && lastSweep.current.key === key) {
      return;
    }
    decorateArtifactRefs(bodyEl.current, artifacts);
    lastSweep.current = { md, key };
  }, [md, artifacts, mdMounted]);

  // Every branch's `#panel-body` carries the ◆ mind button's artifact source
  // (see ArtifactBody's `mindAttrs`) — the iframe WRAPPER included, since a
  // selection inside the frame's own document resolves up through the frame
  // element into it (lib/mind.ts crosses that boundary).
  if (raw) {
    return (
      <div className="panel-body raw" id="panel-body" {...mindAttrs}>
        {error ?? text ?? "loading…"}
      </div>
    );
  }
  if (image) {
    return (
      <div className="panel-body page" id="panel-body" {...mindAttrs}>
        <img src={url} alt="artifact" />
      </div>
    );
  }
  if (page && iso && text !== undefined) {
    // the compiled isometric scene, in the board's own document — a compile
    // failure inside renders its reason, and the flat iframe below stays one
    // toggle away; the card is never lost. The wrapper is `display: contents`
    // (.mind-src), so it carries the source without moving the scene's box.
    return (
      <div className="mind-src" {...mindAttrs}>
        <IsoSceneView html={text} onOpenRef={(t) => void openCodeRef(url, t)} />
      </div>
    );
  }
  if (page) {
    // rendered pages live in a same-origin iframe: full fidelity, and their
    // styles/scripts can't leak into the board
    return (
      <div className="panel-body page" id="panel-body" {...mindAttrs}>
        <ArtifactFrame
          url={url}
          onKey={onKey}
          // the scriptable types only — see the header for why not a PDF
          sandboxed={remote && isScriptablePage(url)}
        />
      </div>
    );
  }
  if (error) {
    return (
      <div className="panel-body" id="panel-body" {...mindAttrs}>
        {error}
      </div>
    );
  }
  if (text === undefined) {
    return (
      <div className="panel-body" id="panel-body" {...mindAttrs}>
        loading…
      </div>
    );
  }
  return (
    <div
      ref={bodyEl}
      className="panel-body md"
      id="panel-body"
      {...mindAttrs}
      onClick={onBodyClick}
      dangerouslySetInnerHTML={md}
    />
  );
}

/** The `data-mind-*` attributes an artifact body carries (lib/mind.ts). */
type MindAttrs = {
  "data-mind-kind": "artifact";
  "data-mind-slug"?: string;
  "data-mind-home"?: string;
  "data-mind-name"?: string;
  "data-mind-sha"?: string;
};

/**
 * sha256 hex of `text` encoded as UTF-8, or "" until it is computed (and
 * whenever `text` is undefined or the platform has no WebCrypto — the sha is
 * an optional part of a capture's source, never a reason to fail one).
 *
 * Known gap: `text` is the DECODED body, so an artifact carrying a UTF-8 BOM
 * or invalid bytes hashes differently from the server's raw-byte sha, and its
 * first verify pass raises a spurious `changed` flag, which carl's keep then
 * settles for good. Agent-written
 * markdown has neither; hashing the raw response would need the artifact
 * fetch to hand over its bytes as well as its text.
 */
function useArtifactSHA(text: string | undefined): string {
  const [sha, setSha] = useState<{ of: string; hex: string } | null>(null);
  useEffect(() => {
    if (text === undefined) return;
    const subtle = globalThis.crypto?.subtle;
    if (!subtle) return;
    let live = true;
    subtle
      .digest("SHA-256", new TextEncoder().encode(text))
      .then((buf) => {
        if (!live) return;
        const hex = Array.from(new Uint8Array(buf))
          .map((b) => b.toString(16).padStart(2, "0"))
          .join("");
        setSha({ of: text, hex });
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [text]);
  // only the hash OF the text on screen — a stale one from the previous
  // artifact must never ride a capture of this one
  return sha && sha.of === text ? sha.hex : "";
}
