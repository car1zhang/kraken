// index.ts — the isoscene compiler's one public door: HTML in, compiled SVG
// out, with the reader's own refusals passed through untouched.
//
// The pass-through is the load-bearing half. `not-diagram` and `not-parseable`
// are not failures of this module to route around: a bespoke card must display
// as-is, and its `reason` is the only channel that says WHICH element left the
// grammar. Rewriting either — a friendlier reason, a partial scene from the
// elements that did parse — would turn a card the board can still show
// correctly into a card it shows wrong (R11).

import type { CompileResult, RenderOptions } from "./types.ts";
import { readCard } from "./reader.ts";
import { projectScene } from "./project.ts";
import { renderSceneMarkup } from "./render.ts";

/** `opts` defaults to full motion so a caller with no accessibility signal to
 *  pass still gets a valid scene; the board passes the prefers-reduced-motion
 *  media query's answer explicitly. */
export function compileCard(
  html: string,
  opts: RenderOptions = { reducedMotion: false },
): CompileResult {
  const read = readCard(html);
  // R11: same kind, same reason, no repackaging.
  if (read.kind !== "scene") return read;
  return { kind: "scene", svg: renderSceneMarkup(projectScene(read.scene), opts) };
}

/** Whether the 3D toggle may be offered for this card at all. Deliberately the
 *  reader's verdict rather than an independent sniff: a probe that could
 *  disagree with `compileCard` would offer a toggle that then fails, which is
 *  the one outcome the fallback rules out. */
export function cardCompilable(html: string): boolean {
  return readCard(html).kind === "scene";
}
