import { useCallback, useEffect, useRef, useState } from "react";
import { createFeedback } from "../../api/client.ts";
import type { WebJob } from "../../api/types.ts";
import { useArtifactSeq, type SeqEntry } from "../../hooks/useArtifactSeq.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { renderMD } from "../../lib/markdown.ts";
import { ArtifactPager, useArtifactKeys } from "./ArtifactPager.tsx";
import { Overlay, type Mutate } from "./Overlay.tsx";

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
  /** See Overlay.tsx. Feedback rides the board payload, so this invalidates. */
  mutate: Mutate;
}

export function ArtifactOverlay(props: ArtifactOverlayProps) {
  const { job, url, name, open, onNavigate } = props;

  /**
   * `feedbackDrafts` (app.js:1676) — an unsent feedback note per artifact url.
   *
   * `‹ ›` now sit a few pixels from the text you are writing about, and every
   * open resets the composer, so without this one click on `›` silently
   * destroys a half-typed note with no undo. Refusing to step instead would
   * make the pager look broken; carrying the draft with its artifact is what
   * the rest of the board does.
   *
   * A Map on a ref, deliberately, not localStorage: surviving the tab is a
   * bigger promise than a scratch composer should make. It lives HERE rather
   * than in the body because the body remounts on every step — which is the
   * whole point of the remount — and it survives a close for the same reason
   * the vanilla module-level Map does.
   */
  const drafts = useRef<Map<string, string>>(new Map()).current;

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
  drafts: Map<string, string>;
  seq: SeqEntry[];
  idx: number;
  onStep: (delta: -1 | 1) => void;
}

function ArtifactBody({
  url,
  name,
  mutate,
  drafts,
  seq,
  idx,
  onStep,
}: ArtifactBodyProps) {
  const [raw, setRaw] = useState(false);
  // undefined = not fetched yet; a page/image is never fetched up front (the
  // iframe/img does its own request), so `raw` on one triggers the fetch late
  const [text, setText] = useState<string | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  // `restoreFeedbackDraft` (app.js:1695): an artifact you left a note
  // half-written on re-opens with the composer showing and the text back in it
  const [showForm, setShowForm] = useState(() => drafts.has(url));
  const [feedback, setFeedback] = useState(() => drafts.get(url) ?? "");
  const [note, setNote] = useState("");
  const [sending, setSending] = useState(false);
  const [saved, setSaved] = useState(false);
  // which artifact the state above describes — the whole reset block below
  const [shown, setShown] = useState(url);
  const feedbackInput = useRef<HTMLTextAreaElement | null>(null);

  /**
   * What is on screen right now, for the draft stash — kept on a ref because
   * the two places that bank a note (a step, an unmount) both need the CURRENT
   * values against the CURRENT url and neither is a render.
   */
  const latest = useRef({ url, feedback, showForm });

  /**
   * `stashFeedbackDraft` (app.js:1682) — bank the note against the artifact it
   * was typed about. An emptied or explicitly cancelled composer drops its
   * stash rather than keeping a stale one.
   */
  const stash = useCallback(
    (of: { url: string; feedback: string; showForm: boolean }) => {
      if (of.showForm && of.feedback.trim()) drafts.set(of.url, of.feedback);
      else drafts.delete(of.url);
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
    setSaved(false);
    setShowForm(draft !== undefined);
    setFeedback(draft ?? "");
    latest.current = {
      url,
      feedback: draft ?? "",
      showForm: draft !== undefined,
    };
  } else {
    latest.current = { url, feedback, showForm };
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
    if (!saved) return;
    const id = window.setTimeout(() => setSaved(false), 2000);
    return () => window.clearTimeout(id);
  }, [saved]);

  const send = useCallback(async () => {
    const parts = artifactParts(url);
    const body = feedback.trim();
    if (!parts) {
      setNote("can't identify this artifact");
      return;
    }
    if (!body) {
      setNote("feedback is empty");
      return;
    }
    setSending(true);
    const ok = await mutate(() =>
      createFeedback(parts.slug, parts.home, parts.name, body),
    );
    // "is the composer still showing the note I sent?" — an IDENTITY question
    // (app.js:4104), asked AFTER the await because a step across it must be
    // seen. The post itself is right either way, but the panel belongs to
    // whatever is on screen: page onto another artifact mid-send and clearing
    // the composer here would wipe THAT artifact's restored draft, and
    // "saved ✓" would appear over a note nobody has sent.
    const onSentArtifact = latest.current.url === url;
    if (ok) drafts.delete(url); // it's sent; it is no longer a draft
    if (!onSentArtifact) return;
    setSending(false);
    if (ok) {
      setShowForm(false);
      setFeedback("");
      setNote("");
      setSaved(true);
    } else {
      setNote("failed — see status bar");
    }
  }, [drafts, feedback, mutate, url]);

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
        <button
          className="btn ghost"
          id="panel-feedback"
          onClick={() => {
            setShowForm((v) => {
              const next = !v;
              if (next) {
                setNote("");
                requestAnimationFrame(() => feedbackInput.current?.focus());
              }
              return next;
            });
          }}
        >
          {saved ? "saved ✓" : "feedback"}
        </button>
      </div>
      <div className="feedback-form" id="feedback-form" hidden={!showForm}>
        <textarea
          id="feedback-text"
          ref={feedbackInput}
          rows={3}
          placeholder="what's wrong with this doc/diagram?"
          value={feedback}
          onChange={(e) => setFeedback(e.target.value)}
        />
        <div className="feedback-actions">
          <span className="feedback-note" id="feedback-note">
            {note}
          </span>
          <button
            className="btn ghost"
            id="feedback-cancel"
            onClick={() => {
              // the one gesture that means "throw this away": it must also drop
              // the stash, or the note reappears the next time you page onto
              // this artifact
              setShowForm(false);
              setFeedback("");
              setNote("");
            }}
          >
            cancel
          </button>
          <button
            className="btn"
            id="feedback-send"
            disabled={sending}
            onClick={() => void send()}
          >
            send
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
  // render-phase ref write, deliberately: a latest-value mirror, read only
  // from the iframe document's keydown listener, never during this render
  const listener = useRef(onKey);
  listener.current = onKey;

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
      doc.addEventListener("keydown", handle);
      hooked.current = doc;
    } catch {
      /* cross-origin or torn down: buttons still work */
    }
  }, [handle]);

  useEffect(() => {
    return () => {
      try {
        hooked.current?.removeEventListener("keydown", handle);
      } catch {
        /* the document went away with the frame */
      }
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
      dangerouslySetInnerHTML={{ __html: renderMD(text) }}
    />
  );
}
