import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { createFeedback, openInEditor, threadOp } from "../../api/client.ts";
import type { WebJob } from "../../api/types.ts";
import { useArtifactSeq, type SeqEntry } from "../../hooks/useArtifactSeq.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { useRenderedMD } from "../../hooks/useRenderedMD.ts";
import {
  codeRefTarget,
  wireCodeRefs,
  type CodeRefTarget,
} from "../../lib/codeRefs.ts";
import { ArtifactPager, useArtifactKeys } from "./ArtifactPager.tsx";
import { Overlay, type Mutate } from "./Overlay.tsx";
import { showToast } from "./Toast.tsx";

// The artifact viewer (app.js:1774 openArtifact). FOUR render modes, picked
// from the URL's extension alone:
//
//   png|jpe?g|gif|webp   → <img>
//   html|svg|pdf         → same-origin <iframe src>
//   anything else        → fetched text, rendered as markdown
//   …with `raw` on       → the same fetched text, verbatim in a <pre>-ish body
//
// The iframe is deliberately UNSANDBOXED. web/serve.go:253 justifies it as
// "same trust as opening the file" — these are artifacts this machine's own
// agents wrote into this machine's own cell directory, served from the same
// origin, and a sandbox would break exactly the diagram cards the viewer
// exists for. Adding `sandbox` is a separate decision with its own
// consequences, not a porting cleanup.
//
// Reading an artifact set is a SEQUENCE, so the viewer also pages: `job` is
// the job the open artifact belongs to and its artifact list is the ordered
// set ‹ › and ←/→ walk. See useArtifactSeq for what that derivation has to
// guarantee.

const isPage = (url: string) => /\.(html|svg|pdf)$/i.test(url);
const isImage = (url: string) => /\.(png|jpe?g|gif|webp)$/i.test(url);

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

/**
 * A cell-home artifact name may be a subpath — the title wants its tail. An
 * empty tail (a name that ends in the separator) falls back to the whole name
 * rather than to nothing, which would title the thread with a bare em-dash.
 */
const baseName = (name: string) =>
  name.slice(name.lastIndexOf("/") + 1) || name;

/**
 * Cut to at most `n` UTF-16 units WITHOUT splitting a surrogate pair.
 *
 * `slice` counts code units, so a cut landing between the halves of an astral
 * character (emoji, and everything above the BMP) leaves a lone high surrogate.
 * `JSON.stringify` will happily escape it, Go's `encoding/json` decodes an
 * unpaired escape to U+FFFD, and the thread's stored title then carries a
 * replacement glyph nothing can repair but a rename.
 */
function cutUnits(s: string, n: number): string {
  const cut = s.slice(0, n);
  return /[\uD800-\uDBFF]$/.test(cut) ? cut.slice(0, -1) : cut;
}

/** The `maxLength` ThreadOverlay's rename input enforces, in UTF-16 units. */
const TITLE_MAX = 80;
/** How much of that a long artifact name may take before it is itself cut. */
const TITLE_BASE_MAX = 40;

/**
 * The thread name a question about an artifact branches under.
 *
 * A thread's name is what its chip says wherever threads are listed, so "which
 * artifact" alone is not enough: ask two things about `00-overview.md` and you
 * would get two chips nobody can tell apart. The first line of the question is
 * what separates them.
 *
 * Which is why the artifact half is budgeted rather than simply written first.
 * A single cut at the end would let a long section-doc name eat the whole 80
 * and truncate the question away entirely — every chip identical again, the
 * exact failure this function exists to prevent.
 *
 * Capped at 80 to match the rename input's own `maxLength` — a title longer
 * than that could be branched but never edited back to in place.
 */
export function askThreadTitle(artifact: string, question: string): string {
  let base = baseName(artifact);
  if (base.length > TITLE_BASE_MAX) base = cutUnits(base, TITLE_BASE_MAX - 1) + "…";
  const first = (question.trim().split("\n")[0] ?? "").trim();
  const title = first ? `${base} — ${first}` : base;
  if (title.length <= TITLE_MAX) return title;
  return cutUnits(title, TITLE_MAX - 1).trimEnd() + "…";
}

/**
 * The seed prompt the branched thread comes up with.
 *
 * It names the artifact rather than its URL or its on-disk path: the thread is
 * a fork of zeus in the cell's own worktree, so the artifact set is already the
 * thing it knows by name, and a path derived from an /api/artifact URL would be
 * a second, weaker way of saying so.
 *
 * The last line restates the thread contract (ThreadOverlay's own banner:
 * "Threads are for asking, not for doing") — this composer sits on a doc, and
 * without it "elaborate on this section" reads as a licence to go edit it.
 */
export function askThreadSeed(artifact: string, question: string): string {
  return (
    "I'm reading the artifact `" +
    artifact +
    "` from this job's artifact set. A question about it:\n\n" +
    question.trim() +
    "\n\nAnswer here in the thread — this is a question about the work, " +
    "not a request to change it. Don't edit the artifact."
  );
}

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
  onAskBranched,
}: ArtifactBodyProps) {
  const [raw, setRaw] = useState(false);
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
  // which artifact the state above describes — the whole reset block below
  const [shown, setShown] = useState(url);
  const composerInput = useRef<HTMLTextAreaElement | null>(null);

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
    setText(undefined);
    setError(null);
    setNote("");
    setSending(false);
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
  const needsText = raw || (!page && !image);

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
        {/* Branching forks the cell's zeus session, so it needs a live cell —
            the same `job.live` gate ThreadChips.tsx puts on the ⑂ it renders in
            the steer modal. Without a job resolved (a singleton open) there is
            nothing to fork either.

            `disabled={sending}` on BOTH toggles — and on cancel below, which
            is the third door to the same room. A branch is seconds long, and
            closing OR switching the composer mid-flight would route its own
            outcome — "failed — see status bar" — into a region that is no
            longer on screen, or worse, into the other mode's. */}
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
        <button
          className="btn ghost"
          id="panel-feedback"
          disabled={sending}
          title="leave feedback on this artifact for the job to read"
          onClick={() => toggleComposer("feedback")}
        >
          {sentMode === "feedback" ? "saved ✓" : "feedback"}
        </button>
      </div>
      <div
        className={"feedback-form" + (mode === "ask" ? " ask" : "")}
        id={mode === "ask" ? "ask-form" : "feedback-form"}
        hidden={!mode}
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
        page={page}
        image={image}
        text={text}
        error={error}
        onKey={onKey}
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
}: {
  url: string;
  onKey: (ev: KeyboardEvent) => void;
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
  }, [handle]);

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

  return <iframe ref={frame} src={url} title="artifact" onLoad={attach} />;
}

function ArtifactBodyContent({
  url,
  raw,
  page,
  image,
  text,
  error,
  onKey,
}: {
  url: string;
  raw: boolean;
  page: boolean;
  image: boolean;
  text: string | undefined;
  error: string | null;
  onKey: (ev: KeyboardEvent) => void;
}) {
  // Above the branches because it is a hook, and memoised because this is the
  // component the 3s board poll used to rebuild wholesale — see useRenderedMD.
  //
  // Fed `undefined` whenever the markdown branch is unreachable, which is
  // exactly `raw`: the raw branch has real `text` (needsText fetches it FOR
  // raw) and renders it verbatim, so parsing it as markdown here would burn a
  // whole renderMD over a body no branch will mount — on a raw-opened page
  // artifact, potentially hundreds of KB of it. The other non-markdown
  // branches already have `text === undefined` on their own.
  const md = useRenderedMD(raw ? undefined : text);

  // ONE delegated handler on the body rather than per-`<code>` wiring, for the
  // reason the body is innerHTML in the first place: there are no React
  // elements down there to hang a prop on. codeRefTarget answers null for a
  // plain `<code>` and for every other click in the doc, so this is inert
  // everywhere except on a ref.
  const onBodyClick = useCallback(
    (ev: ReactMouseEvent<HTMLDivElement>) => {
      const el = ev.target;
      if (!(el instanceof Element)) return;
      const target = codeRefTarget(el);
      if (!target) return;
      void openCodeRef(url, target);
    },
    [url],
  );

  if (raw) {
    return (
      <div className="panel-body raw" id="panel-body">
        {error ?? text ?? "loading…"}
      </div>
    );
  }
  if (image) {
    return (
      <div className="panel-body page" id="panel-body">
        <img src={url} alt="artifact" />
      </div>
    );
  }
  if (page) {
    // rendered pages live in a same-origin iframe: full fidelity, and their
    // styles/scripts can't leak into the board
    return (
      <div className="panel-body page" id="panel-body">
        <ArtifactFrame url={url} onKey={onKey} />
      </div>
    );
  }
  if (error) {
    return (
      <div className="panel-body" id="panel-body">
        {error}
      </div>
    );
  }
  if (text === undefined) {
    return (
      <div className="panel-body" id="panel-body">
        loading…
      </div>
    );
  }
  return (
    <div
      className="panel-body md"
      id="panel-body"
      onClick={onBodyClick}
      dangerouslySetInnerHTML={md}
    />
  );
}
