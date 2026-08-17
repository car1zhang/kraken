import { backgroundURL } from "../api/client.ts";

// The board-background stamp: <html data-bg> plus the --bg-image custom
// property style.css's `:root[data-bg]` block reads. One function, one
// writer (App.tsx's effect), exactly two states — stamped with a url, or
// scrubbed clean. The scrub removes BOTH halves as hygiene, not because a
// half-stamped document breaks: the attribute alone paints the 80% ground
// tint over the opaque ground, which composites back to exactly the plain
// board — but that no-op is a coincidence of the current tint recipe, not a
// property to lean on, and a property left behind with no attribute is a
// stale url sitting one `data-bg` away from painting a removed image,
// waiting on any future writer that stamps the attribute alone.
export function stampBackground(updatedAt: number | undefined): void {
  const el = document.documentElement;
  if (updatedAt) {
    el.dataset.bg = "on";
    el.style.setProperty("--bg-image", `url("${backgroundURL(updatedAt)}")`);
  } else {
    delete el.dataset.bg;
    el.style.removeProperty("--bg-image");
  }
}
