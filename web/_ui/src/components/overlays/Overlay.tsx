import {
  useCallback,
  useEffect,
  type DragEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { openTmux } from "../../api/client.ts";
import { useEscapeLayer } from "../../hooks/useEscapeStack.ts";
import { showToast } from "./Toast.tsx";

/**
 * The shell every overlay sits in.
 *
 * The vanilla board's five overlays are five copies of the same three lines of
 * markup (`.overlay > .panel`, toggled with the `hidden` attribute) plus three
 * copies of the same two behaviours: a backdrop click that closes only when
 * the click landed on the backdrop ITSELF (`ev.target === overlay`, so a click
 * inside the panel never closes it), and a registration in the document-level
 * Escape chain. Here that is one component.
 *
 * `hidden` rather than unmounting the wrapper: style.css targets these ids for
 * z-index (`#overlay` 30, `#thread-overlay` 25, the rest 20) and the attribute
 * is how the vanilla board expresses open/closed, so a screenshot diff sees
 * the same tree. The CHILDREN are unmounted while closed, which is what makes
 * "open resets everything" (openSteer clearing text/chips/note, openThread
 * clearing the previous thread's block count) fall out of mounting rather than
 * out of a dozen imperative resets — and what makes an overlay's polling stop
 * dead when it closes.
 */
export interface OverlayProps {
  /** The element id, e.g. "steer-overlay". Load-bearing: style.css keys off it. */
  id: string;
  open: boolean;
  /** ESC_PRIORITY.<layer>. Escape closes the highest open layer only. */
  priority: number;
  onClose: () => void;
  children: ReactNode;
  /** Extra classes on the inner `.panel` (none of the five need this today). */
  panelClassName?: string;
  /** Files-drop support on the panel (steer + editor). */
  onPanelDragOver?: (e: DragEvent<HTMLDivElement>) => void;
  onPanelDrop?: (e: DragEvent<HTMLDivElement>) => void;
}

export function Overlay({
  id,
  open,
  priority,
  onClose,
  children,
  panelClassName,
  onPanelDragOver,
  onPanelDrop,
}: OverlayProps) {
  useEscapeLayer(open, priority, onClose);
  return (
    <div
      className="overlay"
      id={id}
      hidden={!open}
      onClick={(e) => {
        // only the backdrop itself — a click inside the panel must not close
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className={panelClassName ? `panel ${panelClassName}` : "panel"}
        onDragOver={onPanelDragOver}
        onDrop={onPanelDrop}
      >
        {open ? children : null}
      </div>
    </div>
  );
}

/**
 * How an overlay performs a write.
 *
 * This is the vanilla `post()` (app.js:1137) as one injected function, and it
 * carries the same three-part contract, which is why it is a prop rather than
 * something each overlay hand-rolls:
 *
 *   - it AWAITS the write, then invalidates and refetches the board — on BOTH
 *     the success and the failure path, because a failed write still means the
 *     DOM may be showing controls disabled against a state that never changed;
 *   - a failure puts the SERVER'S OWN sentence in the tick-stat corner (the
 *     board never invents error copy for a refused op — it only points at the
 *     status bar);
 *   - it resolves to the parsed body on success and `null` on failure, so a
 *     bare truthiness check works at every call site while `threadOp`'s
 *     `{tid}` is still readable.
 *
 * Overlays therefore call `mutate(() => threadOp(slug, "kill", …))` rather
 * than calling the client directly: the typing stays on the client function
 * and the board-refresh policy stays in one place the integrator owns.
 */
export type Mutate = <T>(op: () => Promise<T>) => Promise<T | null>;

// ---------------------------------------------------------------- paste layer
//
// ⌘V image paste is a DOCUMENT-level listener in the vanilla board (app.js:
// 3326) rather than an overlay-level one, so it works even when focus sits on
// a non-input element inside the panel. That single handler then hard-codes
// its own precedence — steer is checked, and wins, before editor — so a paste
// can never land in both if they somehow ended up open at once.
//
// Same shape as useEscapeStack: highest-priority open layer takes the event.
// Deliberately a separate registry from the Escape one: Escape closes exactly
// one layer and every overlay registers, whereas only the two composer
// overlays care about paste, and the editor additionally opts out while its
// form is read-only.

type PasteLayer = { priority: number; handle: (e: ClipboardEvent) => void };

const pasteLayers = new Set<PasteLayer>();
let pasteListening = false;

function onPaste(e: ClipboardEvent) {
  let top: PasteLayer | undefined;
  for (const l of pasteLayers) if (!top || l.priority > top.priority) top = l;
  top?.handle(e);
}

/**
 * Register a paste handler while `enabled`. Pass the same ESC_PRIORITY rank so
 * "closes first" and "receives the paste first" cannot disagree.
 */
export function usePasteLayer(
  enabled: boolean,
  priority: number,
  handle: (e: ClipboardEvent) => void,
): void {
  useEffect(() => {
    if (!enabled) return;
    const entry: PasteLayer = { priority, handle };
    pasteLayers.add(entry);
    if (!pasteListening) {
      document.addEventListener("paste", onPaste);
      pasteListening = true;
    }
    return () => {
      pasteLayers.delete(entry);
      if (pasteLayers.size === 0 && pasteListening) {
        document.removeEventListener("paste", onPaste);
        pasteListening = false;
      }
    };
  }, [enabled, priority, handle]);
}

/**
 * `pastedFiles` from app.js:1736 — the raw File objects on a paste event.
 *
 * Deliberately UNFILTERED by mime type (unlike pickImageFiles) so a caller can
 * tell "nothing was pasted, let the text land normally" apart from "files were
 * pasted but none were images", which earns a note rather than silence.
 */
export function pastedFiles(e: ClipboardEvent): File[] {
  const items = [...(e.clipboardData?.items || [])].filter(
    (it) => it.kind === "file",
  );
  return items.map((it) => it.getAsFile()).filter((f): f is File => !!f);
}

/** app.js:1701 — the four types /api/upload accepts. */
export const IMAGE_TYPE_RE = /^image\/(png|jpeg|gif|webp)$/;

/** app.js:1727 */
export function pickImageFiles(files: FileList | File[]): File[] {
  return [...files].filter((f) => IMAGE_TYPE_RE.test(f.type));
}

/**
 * A sink for files dropped anywhere on a composer overlay's panel.
 *
 * The vanilla board binds dragover/drop to `#steer-overlay .panel` and
 * `#editor-overlay .panel` — the WHOLE panel, head included, not just the
 * attach zone — so a drop lands wherever you aim it. That element belongs to
 * <Overlay>, one level above the body component that owns the upload state, so
 * the body publishes its handler into this ref and Overlay reads it.
 *
 * An unset ref (the editor while read-only, or a closed overlay) means the
 * dragover is never accepted, and the browser's own "navigate to the dropped
 * file" default is blocked globally elsewhere.
 */
export type FileSink = RefObject<((files: FileList | File[]) => void) | null>;

/** The two handlers <Overlay> needs, derived from a FileSink. */
export function panelDropHandlers(sink: FileSink) {
  return {
    onPanelDragOver: (e: DragEvent<HTMLDivElement>) => {
      if (!sink.current) return;
      if (e.dataTransfer?.types?.includes("Files")) e.preventDefault();
    },
    onPanelDrop: (e: DragEvent<HTMLDivElement>) => {
      if (!sink.current || !e.dataTransfer?.files?.length) return;
      e.preventDefault();
      sink.current(e.dataTransfer.files);
    },
  };
}

/**
 * `.open-tmux` (app.js:1594), shared by the steer head and the thread head.
 *
 * A PANE on the target wins over a slug, and the difference is not cosmetic:
 * the thread head's button raises the THREAD's own tmux window, while a
 * slug-only post resolves to zeus' pane — a different window, and the one
 * window a side thread exists in order not to disturb.
 *
 * `focus_error` is additive on the response rather than an error status (the
 * pane was raised; only the AppleScript focus failed), so it toasts instead of
 * going down the failure path.
 */
export function useOpenTmux(mutate: Mutate) {
  return useCallback(
    async (target: { pane?: string; slug?: string }) => {
      const data = await mutate(() => openTmux(target));
      if (data && data.focus_error) {
        showToast(`couldn't focus the window: ${data.focus_error}`);
      }
    },
    [mutate],
  );
}

/** One uploaded attachment, as /api/upload returns it. */
export interface AttChip {
  name: string;
  path: string;
  url: string;
}

/**
 * The attachment chip row (app.js:1703/1711). Shared by steer and editor, but
 * note that the two mean DIFFERENT things by it — see SteerOverlay's and
 * EditorOverlay's own comments. Here it is only markup.
 */
export function AttChips({
  id,
  chips,
  onRemove,
}: {
  id: string;
  chips: AttChip[];
  onRemove: (path: string) => void;
}) {
  return (
    <div className="att-chips" id={id} hidden={!chips.length}>
      {chips.map((c) => (
        <div className="att-chip" data-path={c.path} key={c.path}>
          <img src={c.url} alt="" />
          <span className="att-name">{c.name}</span>
          <button
            className="att-rm"
            type="button"
            title="remove"
            onClick={() => onRemove(c.path)}
          >
            ✕
          </button>
        </div>
      ))}
    </div>
  );
}
