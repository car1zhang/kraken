// ✓ read — the board's one capture control.
//
// Mounted once by App over every view. It is lit whenever a non-empty text
// selection exists — in the board's own document or inside a same-origin
// artifact iframe (an html/svg card) — and a press posts that text, the job it
// belongs to and its SOURCE (read off the `data-mind-*` attributes the reading
// surfaces stamp, lib/mind.ts) to `POST /api/mm/mark`. The server stores it as
// a pending READ MARK that the job's next mind run (its Done run, else the
// daily catch-all) reads; this toasts the job's pending count and opens
// nothing.
//
// The selection is WATCHED, never read at click time: pressing anywhere
// collapses a selection, which is why the button's mousedown is prevented and
// why the text it posts is the text that was lit when the press began.

import { useCallback, useEffect, useRef, useState } from "react";

import { ApiError, markRead } from "../../api/client.ts";
import {
  resolveSelectionSource,
  selectionSlug,
  type CaptureSource,
} from "../../lib/mind.ts";
import { showToast } from "../overlays/Toast.tsx";

/** The selection the button would post, frozen at the last selectionchange. */
interface Pending {
  text: string;
  slug: string;
  source: CaptureSource;
  /** The document the selection lives in — the board's or an iframe's. */
  doc: Document;
}

/** A document still worth reading a selection from: attached and on screen. */
function docAlive(doc: Document): boolean {
  if (doc === document) return true;
  try {
    const frame = doc.defaultView?.frameElement ?? null;
    return !!frame && frame.isConnected;
  } catch {
    return false;
  }
}

/** Read `doc`'s current selection into a Pending, or null when there is none. */
function readSelection(doc: Document): Pending | null {
  let sel: Selection | null = null;
  try {
    sel = doc.getSelection ? doc.getSelection() : null;
  } catch {
    return null;
  }
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const text = sel.toString().trim();
  if (!text) return null;
  // The innermost source COMMON to both ends of the selection first, then the
  // anchor's own, then `manual`. A selection spanning two transcript blocks
  // therefore files under the block it STARTED in: the surface around the
  // blocks stamps only a slug, never a kind, so the common ancestor resolves
  // to nothing and the anchor's block is the nearest source that does.
  const common = sel.getRangeAt(0).commonAncestorContainer;
  const anchor = sel.anchorNode;
  const source =
    resolveSelectionSource(common) ??
    resolveSelectionSource(anchor) ?? { kind: "manual" as const };
  const slug = selectionSlug(common) || selectionSlug(anchor);
  return { text, slug, source, doc };
}

const isShortcut = (ev: KeyboardEvent) =>
  (ev.metaKey || ev.ctrlKey) && ev.shiftKey && ev.key.toLowerCase() === "m";

export function MindButton() {
  const [pending, setPending] = useState<Pending | null>(null);
  const [busy, setBusy] = useState(false);
  const btn = useRef<HTMLButtonElement | null>(null);
  // Latest values for the document-level listeners, which are hung once.
  const pendingRef = useRef<Pending | null>(null);
  pendingRef.current = pending;
  const captureRef = useRef<() => void>(() => {});

  const capture = useCallback(async () => {
    const p = pendingRef.current;
    if (!p || busy) return;
    setBusy(true);
    try {
      const res = await markRead(p.slug, p.text, p.source);
      try {
        p.doc.getSelection?.()?.removeAllRanges();
      } catch {
        /* the frame went away mid-flight */
      }
      setPending(null);
      showToast(`✓ marked for the next mind run · ${res.pending} pending`);
    } catch (e) {
      // 404 (no such job), 400 (empty, too long, or a source the server
      // refused): the server's own sentence, as a toast
      showToast(
        e instanceof ApiError
          ? e.message
          : "✓ read failed: " + (e instanceof Error ? e.message : String(e)),
      );
    } finally {
      setBusy(false);
    }
  }, [busy]);
  captureRef.current = () => void capture();

  useEffect(() => {
    // Every document hooked so far — the board's own plus each artifact
    // iframe's — so a reload of a frame (a new document) is hooked afresh and
    // a re-fired load on the same one is not hooked twice.
    const hooked = new Map<Document, () => void>();

    const onSelect = (doc: Document) => {
      const next = readSelection(doc);
      if (next) {
        setPending(next);
        return;
      }
      // An empty selection in one document says nothing about another: the
      // board's own selection collapses the moment focus moves into a frame,
      // which must not blank a selection the reader is making IN the frame.
      setPending((cur) => (cur && cur.doc !== doc && docAlive(cur.doc) ? cur : null));
    };

    const onKey = (ev: KeyboardEvent) => {
      if (!isShortcut(ev)) return;
      if (!pendingRef.current) return;
      ev.preventDefault();
      captureRef.current();
    };

    const hook = (doc: Document) => {
      if (hooked.has(doc)) return;
      const sel = () => onSelect(doc);
      const unhook = () => {
        doc.removeEventListener("selectionchange", sel);
        doc.removeEventListener("keydown", onKey);
        doc.defaultView?.removeEventListener("pagehide", unhook);
        hooked.delete(doc);
      };
      doc.addEventListener("selectionchange", sel);
      doc.addEventListener("keydown", onKey);
      // A frame's document goes away when the frame navigates or unmounts;
      // this button lives for the whole page, so without the unhook on
      // `pagehide` the map would retain every artifact document ever viewed.
      // Frames only: the board's own document also fires `pagehide` on a
      // back/forward-cache navigation and comes BACK, with this effect never
      // re-run — unhooking it there would leave the button deaf until a reload.
      if (doc !== document) doc.defaultView?.addEventListener("pagehide", unhook);
      hooked.set(doc, unhook);
    };

    const hookFrame = (frame: HTMLIFrameElement) => {
      try {
        const doc = frame.contentDocument;
        if (doc) hook(doc);
      } catch {
        /* cross-origin: its selection is not ours to read */
      }
    };

    // `load` does not bubble, but a CAPTURE listener on the document still
    // sees every iframe's load on its way down — which is how a frame the
    // artifact viewer mounts later gets hooked without the viewer knowing this
    // button exists.
    const onLoad = (ev: Event) => {
      if (ev.target instanceof HTMLIFrameElement) hookFrame(ev.target);
    };

    // A press anywhere in the board (but on this button) moves the reader's
    // attention back to the board: a selection still standing inside an
    // artifact frame is no longer the one they mean.
    const onDown = (ev: MouseEvent) => {
      if (btn.current && ev.target instanceof Node && btn.current.contains(ev.target)) {
        return;
      }
      setPending((cur) => (cur && cur.doc !== document ? null : cur));
    };

    hook(document);
    document.addEventListener("load", onLoad, true);
    document.addEventListener("mousedown", onDown, true);
    for (const f of Array.from(document.querySelectorAll("iframe"))) hookFrame(f);
    return () => {
      document.removeEventListener("load", onLoad, true);
      document.removeEventListener("mousedown", onDown, true);
      for (const un of hooked.values()) {
        try {
          un();
        } catch {
          /* a frame document already torn down */
        }
      }
      hooked.clear();
    };
  }, []);

  const enabled = !!pending && !busy;
  return (
    <button
      ref={btn}
      type="button"
      className={"mind-btn" + (enabled ? " on" : "")}
      id="mind-button"
      disabled={!enabled}
      aria-keyshortcuts="Meta+Shift+M"
      title={
        pending
          ? `mark the selection as read${pending.slug ? ` on ${pending.slug}` : ""} — the next mind run reads it (⌘⇧M)`
          : "highlight text anywhere, then press to mark it as read for the next mind run (⌘⇧M)"
      }
      // The press must not collapse the selection it is about to post.
      onMouseDown={(e) => e.preventDefault()}
      onClick={() => void capture()}
    >
      ✓ read <span className="mind-key">⌘⇧M</span>
    </button>
  );
}

export default MindButton;
