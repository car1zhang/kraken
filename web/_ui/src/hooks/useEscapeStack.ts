import { useEffect } from "react";

/**
 * Escape closes exactly ONE layer per press.
 *
 * The vanilla board expressed this as a single handler with an explicit
 * if/else chain in a fixed order — artifact → thread → steer → editor →
 * usage (app.js:3422-3429) — which works but means every new overlay edits
 * one shared function and the ordering lives nowhere but that function's
 * line order.
 *
 * Here each overlay registers itself with a priority and the highest open one
 * wins. The priorities below reproduce the vanilla order exactly; they are
 * ranks, not z-indexes, though they deliberately agree with style.css's
 * stacking so "topmost visually" and "closes first" cannot disagree.
 *
 * One subtlety carried over: a rename input inside the thread overlay stops
 * propagation on Escape so it commits/cancels the rename without also closing
 * the overlay. That stays the input's own job — this hook only sees keydowns
 * that reach the document.
 */
export const ESC_PRIORITY = {
  artifact: 50,
  thread: 40,
  steer: 30,
  editor: 20,
  usage: 10,
} as const;

type Registered = { priority: number; close: () => void };

const layers = new Set<Registered>();
let listening = false;

function onKeyDown(e: KeyboardEvent) {
  if (e.key !== "Escape") return;
  let top: Registered | undefined;
  for (const l of layers) {
    if (!top || l.priority > top.priority) top = l;
  }
  if (!top) return;
  e.preventDefault();
  top.close();
}

/**
 * Register an open overlay. Pass `open: false` (or unmount) to deregister —
 * a closed overlay must not swallow the press meant for the one behind it.
 */
export function useEscapeLayer(
  open: boolean,
  priority: number,
  close: () => void,
): void {
  useEffect(() => {
    if (!open) return;
    const entry: Registered = { priority, close };
    layers.add(entry);
    if (!listening) {
      document.addEventListener("keydown", onKeyDown);
      listening = true;
    }
    return () => {
      layers.delete(entry);
      if (layers.size === 0 && listening) {
        document.removeEventListener("keydown", onKeyDown);
        listening = false;
      }
    };
  }, [open, priority, close]);
}
