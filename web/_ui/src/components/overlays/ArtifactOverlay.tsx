import { useCallback, useEffect, useRef, useState } from "react";
import { createFeedback } from "../../api/client.ts";
import { ESC_PRIORITY } from "../../hooks/useEscapeStack.ts";
import { renderMD } from "../../lib/markdown.ts";
import { Overlay, type Mutate } from "./Overlay.tsx";

// The artifact viewer (app.js:1304 openArtifact). FOUR render modes, picked
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

const isPage = (url: string) => /\.(html|svg|pdf)$/i.test(url);
const isImage = (url: string) => /\.(png|jpe?g|gif|webp)$/i.test(url);

export interface ArtifactParts {
  slug: string;
  home: string;
  name: string;
}

/**
 * Recover {slug, home, name} from an /api/artifact URL (app.js:1287).
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
  onClose: () => void;
  /** See Overlay.tsx. Feedback rides the board payload, so this invalidates. */
  mutate: Mutate;
}

export function ArtifactOverlay(props: ArtifactOverlayProps) {
  return (
    <Overlay
      id="overlay"
      open={props.open}
      priority={ESC_PRIORITY.artifact}
      onClose={props.onClose}
    >
      <ArtifactBody key={props.url} {...props} />
    </Overlay>
  );
}

function ArtifactBody({ url, name, mutate }: ArtifactOverlayProps) {
  const [raw, setRaw] = useState(false);
  // undefined = not fetched yet; a page/image is never fetched up front (the
  // iframe/img does its own request), so `raw` on one triggers the fetch late
  const [text, setText] = useState<string | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [note, setNote] = useState("");
  const [sending, setSending] = useState(false);
  const [saved, setSaved] = useState(false);
  const feedbackInput = useRef<HTMLTextAreaElement | null>(null);

  const page = isPage(url);
  const image = isImage(url);
  const needsText = raw || (!page && !image);

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
    setSending(false);
    if (ok) {
      setShowForm(false);
      setFeedback("");
      setNote("");
      setSaved(true);
    } else {
      setNote("failed — see status bar");
    }
  }, [feedback, mutate, url]);

  return (
    <>
      <div className="panel-head">
        <span className="panel-title" id="panel-title">
          {name}
        </span>
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
      />
    </>
  );
}

function ArtifactBodyContent({
  url,
  raw,
  page,
  image,
  text,
  error,
}: {
  url: string;
  raw: boolean;
  page: boolean;
  image: boolean;
  text: string | undefined;
  error: string | null;
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
        <iframe src={url} title="artifact" />
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
