import { useEffect, type Dispatch, type RefObject, type SetStateAction } from "react";

/**
 * The two dismissal behaviours every inline dropdown menu needs, extracted
 * from SwatchStrip when the repo selector became a second such dropdown —
 * one implementation, so the two cannot drift in how they close.
 *
 * `setOpen` is a React state setter on purpose: its identity is stable, so
 * the listeners are not torn down and re-added on every render while open.
 */
export function useMenuDismiss(
  open: boolean,
  setOpen: Dispatch<SetStateAction<boolean>>,
  rootRef: RefObject<HTMLElement | null>,
) {
  // Escape closes the MENU and nothing else. It is captured on the document so
  // the press is stopped before useEscapeStack's own document listener sees it
  // (that one is registered first, when the overlay opened, so a bubble-phase
  // listener here could never get in front of it) — otherwise one Escape would
  // close the menu AND the overlay underneath it.
  useEffect(() => {
    if (!open) return;
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key !== "Escape") return;
      ev.stopPropagation();
      setOpen(false);
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [open, setOpen]);

  // Outside-click closes it, on mousedown rather than click: the click that
  // follows may well land on a control that is about to be re-rendered away,
  // and a menu left open over the next overlay is the failure people notice.
  // Presses INSIDE the component are ignored here — the button toggles and a
  // row picks, both of which own their own close.
  useEffect(() => {
    if (!open) return;
    const onDown = (ev: MouseEvent) => {
      const root = rootRef.current;
      if (root && ev.target instanceof Node && root.contains(ev.target)) return;
      setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open, setOpen, rootRef]);
}
