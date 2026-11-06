// The agent map's characters: the dino in its model's coat (avatar.ts), the
// role hats the crew wear, every pose it can hold, and the small props and
// effects each pose carries. Ported from the trial page
// (artifacts/10-agent-map-trial.html), which is the visual reference; the
// numbers are the trial's own, so the two read the same side by side.
//
// Every drawing function takes the 2D context it draws on and works in the
// character's LOCAL frame: origin at the feet on the ground line, +x the way
// it faces, y negative upward. A pose is a pure function of time, so a
// character's motion never depends on frame rate.
//
// Nothing on the drawing path allocates. A pose FILLS a Pose object its actor
// owns (`fillPose`) instead of returning a fresh one with closures, its props
// and effects are static functions reading the numbers the fill left on it,
// derived colours are computed once per palette, and text is drawn from
// cached bitmaps (`drawText`) rather than with fillText. On an idle 20-card
// map fillText was the largest script cost, with garbage collection close
// behind (the lead's profile, 2026-11-05); those two are what these choices
// remove.

import { COAT_HUE, type Coat, type Hat } from "./avatar.ts";

const PI = Math.PI;
const TAU = PI * 2;
const { sin, cos, abs, max, min } = Math;

export const fr = (x: number) => x - Math.floor(x);
/** A deterministic hash of a number, 0..1 — the trial's per-character seed. */
export const hsh = (n: number) => fr(Math.sin(n * 127.1 + 311.7) * 43758.5453);
export const lerp = (a: number, b: number, u: number) => a + (b - a) * u;
export const clamp = (x: number, a = 0, b = 1) => min(b, max(a, x));
export const ease = (u: number) => {
  const v = clamp(u);
  return v * v * (3 - 2 * v);
};

type Ctx = CanvasRenderingContext2D;

// ------------------------------------------------------------------ colour

function hexRGB(c: string): [number, number, number] {
  let s = c.trim();
  if (s.startsWith("#")) {
    if (s.length === 4) s = "#" + s[1] + s[1] + s[2] + s[2] + s[3] + s[3];
    return [
      parseInt(s.slice(1, 3), 16) || 0,
      parseInt(s.slice(3, 5), 16) || 0,
      parseInt(s.slice(5, 7), 16) || 0,
    ];
  }
  const m = s.match(/[\d.]+/g);
  return m && m.length >= 3 ? [+m[0]!, +m[1]!, +m[2]!] : [160, 160, 160];
}

/** `a` moved `u` of the way to `b`, as an rgb() string. Accepts hex or rgb(). */
export function mix(a: string, b: string, u: number): string {
  const x = hexRGB(a);
  const y = hexRGB(b);
  return "rgb(" + x.map((v, i) => Math.round(lerp(v, y[i] ?? v, u))).join(",") + ")";
}

/**
 * One hue per activity, the trial's legend. style.css carries the same table
 * as `[data-act="…"] { --act: … }` for the card's ribbon and the steer list;
 * `maprepair.test.ts` holds the two in step.
 */
export const ACT_HUE: Readonly<Record<string, string>> = {
  think: "#9aa0b4",
  edit: "#f5d34d",
  search: "#5ea8ff",
  read: "#c9b2ff",
  run: "#fb923c",
  home: "#2dd4bf",
  delegate: "#ff4fd8",
  talk: "#f0abfc",
  review: "#a3e635",
  test: "#7fd44a",
  web: "#00e5ff",
  wait: "#b0b3c4",
  check: "#c084fc",
  ship: "#ffffff",
  data: "#60a5fa",
  build: "#fbbf24",
};
/** `ship`'s white would vanish on a light surface; this is its light-theme hue. */
export const SHIP_LIGHT_HUE = "#55506b";

export interface DinoColors {
  body: string;
  dark: string;
  lite: string;
  belly: string;
  claw: string;
  iris: string;
}

/**
 * The colours the canvases draw with. The theme's own tokens are read off
 * `:root` (style.css defines them there, per theme and accent); the rest are
 * the trial's fixed prop colours, one set per theme, and the colours derived
 * from both, mixed once here so no frame mixes a colour.
 */
export interface Palette {
  light: boolean;
  ac: string;
  ac2: string;
  ok: string;
  warn: string;
  bad: string;
  fg: string;
  b0: string;
  b1: string;
  g1: string;
  muted: string;
  /** a parked cell: ac2 45% of the way to g1, as style.css's `color-mix(ac2 55%, g1)` */
  parked: string;
  ink: string;
  paper: string;
  steel: string;
  wood: string;
  panel: string;
  steam: string;
  glass: string;
  bubble: string;
  pos: string;
  neg: string;
  edge: string;
  /** the data pose's disc side and the build pose's two block fills */
  discSide: string;
  blockLight: string;
  blockDark: string;
  /** a lead spent on a usage limit; every other body is its coat (`coats`) */
  dino: { spent: DinoColors };
  /** one body per model's coat (avatar.ts), lead and crew alike */
  coats: Readonly<Record<Coat, DinoColors>>;
}

/**
 * Each coat's trim, candidate set A's (artifacts/12-model-avatars.html): the
 * outline colour the darker shade is mixed toward, the belly, the claws and
 * the iris. The slate (codex) coat is dark, so on a dark theme its outline
 * and claws take light stand-ins, or they would vanish into it.
 */
const COAT_TRIM: Readonly<
  Record<Coat, { edge: string; edgeDark?: string; belly: string; claw: string; clawDark?: string; iris: string }>
> = {
  fable: { edge: "#6b4300", belly: "#fff3c4", claw: "#7a4a21", iris: "#1f8a4c" },
  opus: { edge: "#123a73", belly: "#e6f2ff", claw: "#1d3f75", iris: "#123a73" },
  sonnet: { edge: "#6b5a3a", belly: "#e2d3b3", claw: "#6b5a3a", iris: "#6b4a26" },
  haiku: { edge: "#0f4a44", belly: "#e0f7f3", claw: "#0f4a44", iris: "#0f4a44" },
  codex: { edge: "#15181f", edgeDark: "#9aa3b6", belly: "#767f92", claw: "#1b1e26", clawDark: "#c3cad8", iris: "#2b2f38" },
};

/** One coat's colours for a theme, the reference page's `coatColors`. */
function coatColors(light: boolean, coat: Coat): DinoColors {
  const k = COAT_TRIM[coat];
  const body = COAT_HUE[coat];
  return {
    body,
    dark: k.edgeDark && !light ? k.edgeDark : mix(body, k.edge, light ? 0.5 : 0.42),
    lite: mix(body, "#ffffff", 0.6),
    belly: k.belly,
    claw: (!light && k.clawDark) || k.claw,
    iris: k.iris,
  };
}

/** The greyed-out coat a lead wears once it hit a usage limit, whatever its model. */
function spentColors(light: boolean): DinoColors {
  const Y = "#bdb7a4";
  return {
    body: Y,
    dark: mix(Y, "#6b4300", light ? 0.5 : 0.42),
    lite: mix(Y, "#ffffff", 0.6),
    belly: "#e6e1d2",
    claw: "#7d766a",
    iris: "#8a8f86",
  };
}

/**
 * Read the palette from the document. Only the PRIMITIVE tokens are read
 * (`--ac`, `--b1`, …): the derived ones are `color-mix()` expressions, and a
 * custom property's computed value is its specified text, which a canvas
 * cannot parse — so `muted` and `parked` are re-derived here with the
 * stylesheet's own recipes.
 */
export function readPalette(root: HTMLElement = document.documentElement): Palette {
  const light = root.dataset.theme === "light";
  let cs: CSSStyleDeclaration | null = null;
  try {
    cs = getComputedStyle(root);
  } catch {
    cs = null;
  }
  const v = (name: string, dark: string, lite: string) => {
    const got = cs?.getPropertyValue(name).trim();
    return got && /^(#|rgb)/.test(got) ? got : light ? lite : dark;
  };
  const ac = v("--ac", "#a86aff", "#6d3fd4");
  const ac2 = v("--ac2", "#ff4fd8", "#c41d9e");
  const g1 = v("--g1", "#90909a", "#6b6a78");
  const b1 = v("--b1", "#0d0c12", "#ecebf3");
  return {
    light,
    ac,
    ac2,
    ok: v("--ok", "#7fd44a", "#3e8f1e"),
    warn: v("--warn", "#ffb000", "#a36f00"),
    bad: v("--stopped", "#ff5c5c", "#c22a2a"),
    fg: v("--fg", "#f2edff", "#1a1724"),
    b0: v("--b0", "#0a090d", "#f6f4fa"),
    b1,
    g1,
    muted: mix(g1, ac, 0.28),
    parked: mix(ac2, g1, 0.45),
    ink: light ? "#1a1724" : "#0d0c12",
    paper: light ? "#ffffff" : "#f2edff",
    steel: light ? "#7c7b8c" : "#b9b6c9",
    wood: light ? "#8a5a2b" : "#c9935a",
    panel: light ? "#dcd9ea" : "#231f33",
    steam: light ? "#9a98ab" : "#cfcbe0",
    glass: light ? "rgba(120,160,255,.25)" : "rgba(160,200,255,.22)",
    bubble: light ? "#ffffff" : "#f2edff",
    pos: light ? "#3e8f1e" : "#7fd44a",
    neg: light ? "#c22a2a" : "#ff5c5c",
    edge: light ? "#8f86b3" : "#5d5285",
    discSide: mix(ACT_HUE.data!, b1, 0.35),
    blockLight: mix(ACT_HUE.build!, b1, 0.15),
    blockDark: mix(ACT_HUE.build!, b1, 0.4),
    dino: { spent: spentColors(light) },
    coats: {
      fable: coatColors(light, "fable"),
      opus: coatColors(light, "opus"),
      sonnet: coatColors(light, "sonnet"),
      haiku: coatColors(light, "haiku"),
      codex: coatColors(light, "codex"),
    },
  };
}

/** The colour of an activity or status pose; `hear` borrows `talk`'s. */
export function hueOf(C: Palette, id: string): string {
  if (id === "hear") return ACT_HUE.talk!;
  const a = ACT_HUE[id];
  if (a) return C.light && id === "ship" ? SHIP_LIGHT_HUE : a;
  switch (id) {
    case "blocked":
      return C.ac2;
    case "question":
      return C.warn;
    case "parked":
      return C.parked;
    case "limit":
      return C.bad;
    case "done":
      return C.ok;
    case "working":
      return C.ac;
    default:
      return C.muted;
  }
}

// ------------------------------------------------------------------ text, from cached bitmaps

/** Text bitmaps are rendered at this font size and this scale, then drawn at any size. */
const SPRITE_PX = 16;
const SPRITE_SCALE = 4;
/** At most this many distinct bitmaps are kept; past it the cache starts over. */
const SPRITE_MAX = 384;

interface Sprite {
  cv: HTMLCanvasElement;
  /** size in CSS px at SPRITE_PX */
  w: number;
  h: number;
  /** the text's own advance width at SPRITE_PX */
  tw: number;
}

/** weight → colour → text → bitmap (null: no 2D context, so no text at all). Nested so a lookup builds no key string. */
const sprites = new Map<number, Map<string, Map<string, Sprite | null>>>();
let spriteCount = 0;

function spriteFor(text: string, weight: number, color: string): Sprite | null {
  let byColor = sprites.get(weight);
  if (!byColor) {
    byColor = new Map();
    sprites.set(weight, byColor);
  }
  let byText = byColor.get(color);
  if (!byText) {
    byText = new Map();
    byColor.set(color, byText);
  }
  const hit = byText.get(text);
  if (hit !== undefined) return hit;
  if (spriteCount >= SPRITE_MAX) {
    sprites.clear();
    spriteCount = 0;
    return spriteFor(text, weight, color);
  }
  let sp: Sprite | null = null;
  try {
    const cv = document.createElement("canvas");
    const c = cv.getContext("2d");
    if (c) {
      const font = weight + " " + SPRITE_PX + "px ui-monospace,Menlo,monospace";
      c.font = font;
      const tw = c.measureText(text).width;
      const w = tw + 4;
      const h = SPRITE_PX * 1.4;
      cv.width = Math.ceil(w * SPRITE_SCALE);
      cv.height = Math.ceil(h * SPRITE_SCALE);
      c.scale(SPRITE_SCALE, SPRITE_SCALE);
      c.font = font;
      c.textAlign = "center";
      c.textBaseline = "middle";
      c.fillStyle = color;
      c.fillText(text, w / 2, h / 2);
      sp = { cv, w, h, tw };
    }
  } catch {
    sp = null;
  }
  byText.set(text, sp);
  spriteCount++;
  return sp;
}

/**
 * Text centred on (x, y) at `px`, drawn from a cached bitmap: an unchanged
 * label costs one drawImage, never a fillText.
 */
export function drawText(c: Ctx, text: string, x: number, y: number, px: number, color: string, weight = 700) {
  const sp = spriteFor(text, weight, color);
  if (!sp) return;
  const k = px / SPRITE_PX;
  const w = sp.w * k;
  const h = sp.h * k;
  c.drawImage(sp.cv, x - w / 2, y - h / 2, w, h);
}

/** The advance width `text` would have at `px` (for sizing a tag's pill). */
export function textWidth(text: string, px: number, weight = 700): number {
  const sp = spriteFor(text, weight, "#000");
  return sp ? (sp.tw * px) / SPRITE_PX : text.length * px * 0.6;
}

// ------------------------------------------------------------------ primitives

let FLIP = 1;

export function dot(c: Ctx, x: number, y: number, r: number, col?: string) {
  if (col) c.fillStyle = col;
  c.beginPath();
  c.arc(x, y, r, 0, TAU);
  c.fill();
}
export function line(c: Ctx, x1: number, y1: number, x2: number, y2: number, col: string, w: number) {
  c.strokeStyle = col;
  c.lineWidth = w;
  c.lineCap = "round";
  c.beginPath();
  c.moveTo(x1, y1);
  c.lineTo(x2, y2);
  c.stroke();
}
export function rr(c: Ctx, x: number, y: number, w: number, h: number, r: number) {
  c.beginPath();
  c.moveTo(x + r, y);
  c.lineTo(x + w - r, y);
  c.quadraticCurveTo(x + w, y, x + w, y + r);
  c.lineTo(x + w, y + h - r);
  c.quadraticCurveTo(x + w, y + h, x + w - r, y + h);
  c.lineTo(x + r, y + h);
  c.quadraticCurveTo(x, y + h, x, y + h - r);
  c.lineTo(x, y + r);
  c.quadraticCurveTo(x, y, x + r, y);
  c.closePath();
}
/** A glyph that stays readable when the character is mirrored. */
function T(c: Ctx, s: string, x: number, y: number, px: number, col: string) {
  if (FLIP < 0) {
    // mirror the glyph back about its own centre
    c.scale(-1, 1);
    drawText(c, s, -x, y, px, col);
    c.scale(-1, 1);
  } else drawText(c, s, x, y, px, col);
}
/** Multiply the context's alpha by `a`; returns the alpha to put back. */
function fade(c: Ctx, a: number): number {
  const was = c.globalAlpha;
  c.globalAlpha = was * a;
  return was;
}
function gear(c: Ctx, C: Palette, x: number, y: number, r: number, a: number, col: string) {
  c.fillStyle = col;
  c.beginPath();
  for (let i = 0; i < 16; i++) {
    const rr_ = i % 2 ? r : r * 0.7;
    const an = (i / 16) * TAU + a;
    c.lineTo(x + cos(an) * rr_, y + sin(an) * rr_);
  }
  c.closePath();
  c.fill();
  c.fillStyle = C.panel;
  c.beginPath();
  c.arc(x, y, r * 0.3, 0, TAU);
  c.fill();
}
function star(c: Ctx, x: number, y: number, r: number, col: string, a: number) {
  const was = fade(c, a);
  c.strokeStyle = col;
  c.lineWidth = 1.1;
  c.lineCap = "round";
  c.beginPath();
  c.moveTo(x - r, y);
  c.lineTo(x + r, y);
  c.moveTo(x, y - r);
  c.lineTo(x, y + r);
  c.stroke();
  c.globalAlpha = was;
}
/** A paper plane at (x, y) pointing `a` radians: its four corners rotated by hand, no transform. */
function plane(c: Ctx, C: Palette, x: number, y: number, a: number, col: string) {
  const ca = cos(a);
  const sa = sin(a);
  c.fillStyle = C.paper;
  c.strokeStyle = col === "#ffffff" ? C.steel : col;
  c.lineWidth = 0.9;
  c.lineJoin = "round";
  c.beginPath();
  c.moveTo(x + 7 * ca, y + 7 * sa);
  c.lineTo(x - 5 * ca + 3.6 * sa, y - 5 * sa - 3.6 * ca);
  c.lineTo(x - 2.4 * ca, y - 2.4 * sa);
  c.lineTo(x - 5 * ca - 3.2 * sa, y - 5 * sa + 3.2 * ca);
  c.closePath();
  c.fill();
  c.stroke();
}
function sheet(c: Ctx, C: Palette, x: number, y: number, col: string) {
  c.fillStyle = C.paper;
  c.strokeStyle = col;
  c.lineWidth = 1.1;
  c.beginPath();
  c.rect(x, y, 9, 12);
  c.fill();
  c.stroke();
  for (let i = 0; i < 3; i++) line(c, x + 2, y + 3 + i * 3, x + 7 - (i === 1 ? 2 : 0), y + 3 + i * 3, col, 1);
}
function disc(c: Ctx, C: Palette, x: number, y: number, col: string) {
  c.fillStyle = C.discSide;
  c.fillRect(x - 4.5, y - 1.5, 9, 3);
  c.beginPath();
  c.ellipse(x, y + 1.5, 4.5, 1.5, 0, 0, TAU);
  c.fill();
  c.fillStyle = col;
  c.beginPath();
  c.ellipse(x, y - 1.5, 4.5, 1.5, 0, 0, TAU);
  c.fill();
}
function block(c: Ctx, C: Palette, x: number, y: number, col: string, i: number) {
  c.fillStyle = i % 2 ? C.blockLight : C.blockDark;
  c.strokeStyle = col;
  c.lineWidth = 1;
  c.beginPath();
  c.rect(x - 4.5, y - 3, 9, 6);
  c.fill();
  c.stroke();
}
function hourglass(c: Ctx, C: Palette, x: number, y: number, rot: number, sand: number, col: string) {
  c.translate(x, y - 8);
  c.rotate(rot);
  line(c, -4.5, -7, 4.5, -7, col, 1.6);
  line(c, -4.5, 7, 4.5, 7, col, 1.6);
  c.strokeStyle = col;
  c.lineWidth = 1;
  c.beginPath();
  c.moveTo(-3.5, -7);
  c.lineTo(0, 0);
  c.lineTo(3.5, -7);
  c.moveTo(-3.5, 7);
  c.lineTo(0, 0);
  c.lineTo(3.5, 7);
  c.stroke();
  const a = 1 - sand;
  c.fillStyle = C.warn;
  if (a > 0.03) {
    c.beginPath();
    c.moveTo(-3.2 * a, -6.4 * a);
    c.lineTo(0, -0.6);
    c.lineTo(3.2 * a, -6.4 * a);
    c.closePath();
    c.fill();
  }
  if (sand > 0.03) {
    c.beginPath();
    c.moveTo(-3.2, 6.4);
    c.lineTo(3.2, 6.4);
    c.lineTo(3.2 * (1 - sand), 6.4 - 6 * sand);
    c.lineTo(-3.2 * (1 - sand), 6.4 - 6 * sand);
    c.closePath();
    c.fill();
  }
  if (a > 0.03 && rot === 0) line(c, 0, -0.5, 0, 5.5, C.warn, 0.7);
  // undo exactly what was applied, in reverse: no save/restore pair
  c.rotate(-rot);
  c.translate(-x, -(y - 8));
}
/** A speech flag over the head: the rounded box, its tail, and the glyph. */
function flag(c: Ctx, C: Palette, x: number, y: number, s: number, hue: string, glyph: string) {
  c.translate(x, y);
  c.scale(s, s);
  c.fillStyle = hue;
  rr(c, -6.5, -8.5, 13, 15, 3);
  c.fill();
  c.beginPath();
  c.moveTo(-3.5, 6);
  c.lineTo(-7.5, 11.5);
  c.lineTo(1.5, 6);
  c.fill();
  T(c, glyph, 0, -0.5, 12, C.light ? "#ffffff" : "#15121f");
  c.scale(1 / s, 1 / s);
  c.translate(-x, -y);
}

// ------------------------------------------------------------------ poses

export interface Eyes {
  x: number;
  y: number;
  lid: number;
  wide: boolean;
  wink: boolean;
  dead: boolean;
}

/**
 * One frame of a pose, owned by its actor and refilled every draw. The
 * numbered scratch fields (`a`…`f`) carry whatever a pose's props and effects
 * need from the fill, so the drawers can be static functions.
 */
export interface Pose {
  /** which pose filled it, which picks the drawers */
  kind: string;
  t: number;
  /** walk target along the card, -1..1 of the walkable half-width; NaN = stand */
  tx: number;
  speed: number;
  /** hand positions: [x0, y0, x1, y1] (0 = near hand, 1 = far hand) */
  hands: [number, number, number, number];
  eyes: Eyes;
  /** 0 faces the viewer; anything else is side-on */
  face: number;
  bob: number;
  lean: number;
  sit: number;
  tap: number;
  egg: number;
  seed: number;
  a: number;
  b: number;
  cc: number;
  d: number;
  e: number;
  f: number;
}

export interface PoseOpts {
  seed: number;
  moving: boolean;
  /** blocked urgency 0..1, or null for the default */
  k: number | null;
  hue: string;
  C: Palette;
}

export function newPose(): Pose {
  return {
    kind: "think",
    t: 0,
    tx: NaN,
    speed: 16,
    hands: [11, -8, -11, -8],
    eyes: { x: 0, y: 0, lid: 0, wide: false, wink: false, dead: false },
    face: 1,
    bob: 0,
    lean: 0,
    sit: 0,
    tap: 0,
    egg: 0,
    seed: 0,
    a: 0,
    b: 0,
    cc: 0,
    d: 0,
    e: 0,
    f: 0,
  };
}

function reset(P: Pose, kind: string, t: number) {
  P.kind = kind;
  P.t = t;
  P.tx = NaN;
  P.speed = 16;
  P.face = 1;
  P.bob = 0;
  P.lean = 0;
  P.sit = 0;
  P.tap = 0;
  P.egg = 0;
  P.a = P.b = P.cc = P.d = P.e = P.f = 0;
  const e = P.eyes;
  e.x = e.y = e.lid = 0;
  e.wide = e.wink = e.dead = false;
}
function hands(P: Pose, x0: number, y0: number, x1: number, y1: number) {
  const h = P.hands;
  h[0] = x0;
  h[1] = y0;
  h[2] = x1;
  h[3] = y1;
}
function eyes(P: Pose, x: number, y: number) {
  P.eyes.x = x;
  P.eyes.y = y;
}

type Fill = (P: Pose, t: number, o: PoseOpts) => void;
type Draw = (c: Ctx, P: Pose, o: PoseOpts) => void;

/** The head centre's height in the local frame. */
const HY = -19;

const fillSleep: Fill = (P, t) => {
  P.sit = 1;
  P.face = 0;
  P.bob = sin(t * 1.1) * 0.9;
  hands(P, 6, -6, -6, -6);
  P.eyes.lid = 1;
};
const fxSleep: Draw = (c, P, o) => {
  const t = P.t;
  for (let i = 0; i < 3; i++) {
    const p = fr(t * 0.33 + i / 3);
    const was = fade(c, sin(p * PI));
    T(c, "z", 15 + p * 13 + sin(p * 7) * 1.5, -33 - p * 21, 6 + p * 5, o.hue);
    c.globalAlpha = was;
  }
};

/**
 * Every pose by name: the sixteen activities (plus `think`), the crew's
 * `idle`, and the status poses a card holds when it is not working. Each
 * fills the actor's Pose; PRE draws behind the body, PROP in the body's frame
 * after the arms, FX last and outside the lean.
 */
const FILL: Readonly<Record<string, Fill>> = {
  think: (P, t, o) => {
    P.tx = sin(t * 0.45 + o.seed * 9) * 0.55;
    P.speed = 16;
    hands(P, 6, -13, -10, -8);
    eyes(P, 0.3, -0.7);
  },
  edit: (P, t) => {
    const p = fr(t * 2.4);
    const r = p < 0.55 ? ease(p / 0.55) : p < 0.7 ? 1 - (p - 0.55) / 0.15 : 0;
    const hit = p >= 0.7 ? 1 - (p - 0.7) / 0.3 : 0;
    const hx = lerp(14, 4, r);
    const hy = lerp(-8, -31, r);
    P.face = 1;
    P.bob = hit * 1.6;
    hands(P, hx, hy, 9, -4);
    eyes(P, 0.7, 0.6);
    P.a = hx;
    P.b = hy;
    P.cc = lerp(0.55, -1.25, r);
    P.d = hit;
  },
  search: (P, t, o) => {
    const seg = Math.floor(t / 1.25);
    const mv = o.moving;
    const hy = mv ? -17 + sin(t * 10) * 1.2 : -9;
    P.tx = hsh(seg + o.seed * 7) * 1.9 - 0.95;
    P.speed = 84;
    P.lean = mv ? 0.12 : 0.22;
    hands(P, 14, hy, -9, -9);
    eyes(P, 0.8, mv ? 0.2 : 0.8);
    P.eyes.wide = true;
    P.a = 14;
    P.b = hy;
  },
  read: (P, t) => {
    const sc = fr(t * 0.8);
    P.sit = 1;
    P.face = 0;
    P.bob = sin(t * 1.5) * 0.5;
    hands(P, 8.5, -8.5, -8.5, -8.5);
    eyes(P, sc < 0.85 ? lerp(-0.8, 0.8, sc / 0.85) : lerp(0.8, -0.8, (sc - 0.85) / 0.15), 0.9);
    P.a = fr(t / 2.4);
  },
  run: (P, t) => {
    const a = t * 5.5;
    const hx = 15 + 4.5 * cos(a);
    const hy = -11 + 4.5 * sin(a);
    P.face = 1;
    P.bob = sin(a) * 0.7;
    hands(P, hx, hy, 8, -5);
    eyes(P, 0.8, 0.3);
    P.a = hx;
    P.b = hy;
  },
  home: (P, t) => {
    P.face = 0;
    P.bob = sin(t * 2) * 0.5;
    hands(P, 16, -29, -11, -7);
    eyes(P, 0.6, -0.7);
  },
  delegate: (P, t) => {
    P.face = 1;
    P.lean = -0.05;
    hands(P, 16.5, -17 + sin(t * 8) * 0.7, -9, -8);
    eyes(P, 0.9, 0);
  },
  talk: (P, t) => {
    const b = sin(t * 9);
    P.face = 1;
    P.bob = abs(b) * 0.5;
    hands(P, 9.5, -16.5 + b * 0.6, -9, -8);
    eyes(P, 0.8, -0.1);
  },
  idle: (P, t) => {
    P.sit = 1;
    P.face = 0;
    P.bob = sin(t * 1.3) * 0.4;
    hands(P, 6, -6, -6, -6);
    eyes(P, sin(t * 0.5) * 0.6, 0.1);
  },
  review: (P, t) => {
    P.face = 0;
    hands(P, 15, -21, -15, -21);
    eyes(P, clamp(sin(t * 2.2) * 3, -1, 1) * 0.9, -0.3);
  },
  test: (P, t) => {
    const w = t * 4.4;
    P.face = 0;
    P.bob = abs(sin(w)) * 0.6;
    hands(P, 14.5, -19 + sin(w) * 2.6, -14.5, -19 - sin(w) * 2.6);
    eyes(P, cos(w * 0.5) * 0.7, -0.9);
    P.a = w;
  },
  web: (P, t) => {
    const ang = -0.6 + sin(t * 0.7) * 0.22;
    P.face = 1;
    P.lean = -0.07;
    hands(P, 6 + cos(ang) * 8, -19.5 + sin(ang) * 8 + 2, 4, -17);
    eyes(P, 0.8, -0.5);
    P.eyes.wink = true;
    P.a = ang;
  },
  wait: (P, t) => {
    const cyc = fr(t / 4);
    P.sit = 1;
    P.face = 0;
    P.tap = 1;
    P.bob = sin(t * 1.2) * 0.4;
    hands(P, 5, -7, -5, -7);
    eyes(P, fr(t / 5) > 0.7 ? 0.9 : 0, 0.2);
    P.eyes.lid = 0.45;
    P.a = cyc > 0.88 ? ease((cyc - 0.88) / 0.12) * PI : 0;
    P.b = clamp(cyc / 0.88);
  },
  check: (P, t, o) => {
    P.tx = sin(t * 0.7 + o.seed * 5) * 0.8;
    P.speed = 26;
    P.lean = 0.08;
    hands(P, 7, -17, 11, -9);
    eyes(P, 0.6, 0.8);
    P.a = 18 + sin(t * 7) * 3.5;
  },
  ship: (P, t) => {
    const p = fr(t / 2.2);
    let hx: number;
    let hy: number;
    if (p < 0.3) {
      const u = ease(p / 0.3);
      hx = lerp(10, -7, u);
      hy = lerp(-12, -25, u);
    } else if (p < 0.42) {
      const u = (p - 0.3) / 0.12;
      hx = lerp(-7, 17, u);
      hy = lerp(-25, -23, u);
    } else {
      const u = (p - 0.42) / 0.58;
      hx = 17 - u * 4;
      hy = -23 + u * 10 + (u < 0.6 ? sin(u * 20) * 1.2 : 0);
    }
    P.face = 1;
    P.lean = p < 0.3 ? -0.12 * (p / 0.3) : p < 0.42 ? 0.12 : 0.04;
    hands(P, hx, hy, -9, -8);
    eyes(P, 0.8, -0.5);
    P.a = hx;
    P.b = hy;
    P.cc = p >= 0.4 ? (p - 0.4) / 0.6 : -1;
  },
  data: (P, t) => {
    const p = fr(t / 1.3);
    const dig = p < 0.4 ? ease(p / 0.4) : 1 - ease((p - 0.4) / 0.6);
    P.face = 1;
    P.lean = 0.1 * dig;
    P.bob = dig * 1.2;
    hands(P, 7, -15 + dig * 3, 12, -9 + dig * 3);
    eyes(P, 0.7, 0.7);
    P.a = p;
    P.b = dig;
    P.cc = Math.floor(t / 1.3) % 4;
  },
  build: (P, t) => {
    const per = 1.1;
    const n = Math.floor(t / per) % 5;
    const p = fr(t / per);
    const u = ease(p / 0.75);
    const bx = lerp(9, 24, u);
    const by = lerp(-3, -3 - n * 6, u) - sin(u * PI) * 10;
    P.face = 1;
    P.bob = p < 0.2 ? 1.2 * (1 - p / 0.2) : 0;
    hands(P, bx - 3, by + 2, max(4, bx - 8), by + 3);
    eyes(P, 0.7, by < -20 ? -0.5 : 0.4);
    P.a = bx;
    P.b = by;
    P.cc = n;
  },
  blocked: (P, t, o) => {
    // urgency `k` speeds the wave and the hops; past 0.6 both arms go up
    const k = o.k ?? 0.4;
    const w = 5 + k * 7;
    const hc = fr(t / (2.7 - k * 1.5));
    const hop = hc < 0.22 ? sin((hc / 0.22) * PI) * (4 + k * 6) : 0;
    const wv = sin(t * w);
    const w2 = sin(t * w + 1.2);
    P.face = 0;
    P.bob = -hop;
    if (k > 0.6) hands(P, 15 + wv * 4.5, -31 + abs(wv) * 2, -15 - w2 * 4.5, -31 + abs(w2) * 2);
    else hands(P, 15 + wv * 4.5, -31 + abs(wv) * 2, -11, -7);
    P.eyes.wide = true;
    P.a = hop;
  },
  question: (P, t) => {
    const up = sin(t * 1.5) * 1.6;
    P.face = 0;
    P.lean = sin(t * 1.2) * 0.11;
    hands(P, 16.5, -15 + up, -16.5, -15 + up);
    eyes(P, 0.5, -0.7);
  },
  parked: fillSleep,
  done: fillSleep,
  limit: (P) => {
    P.sit = 1;
    P.face = 0;
    P.lean = 0.3;
    hands(P, 12, -2, -11, -3);
    P.eyes.dead = true;
  },
  draft: (P) => {
    P.egg = 1;
  },
};

const PRE: Readonly<Record<string, Draw>> = {
  run: (c, P, o) => {
    const t = P.t;
    c.fillStyle = o.C.steel;
    c.fillRect(28.5, -22, 3.2, 5);
    c.fillStyle = o.C.panel;
    rr(c, 17, -17, 18, 17, 2);
    c.fill();
    c.strokeStyle = o.hue;
    c.lineWidth = 1.3;
    c.stroke();
    gear(c, o.C, 26.5, -8.5, 5.4, t * 3, o.hue);
    dot(c, 20, -14, 1.1, fr(t * 2) < 0.5 ? o.C.ok : o.C.steel);
    for (let i = 0; i < 3; i++) {
      const p = fr(t * 0.9 + i / 3);
      const was = fade(c, (1 - p) * 0.7);
      dot(c, 30 + sin(p * 6 + i) * 2.2, -23 - p * 13, 1.5 + p * 2.4, o.C.steam);
      c.globalAlpha = was;
    }
  },
  wait: (c, P, o) => hourglass(c, o.C, 20, -1, P.a, P.b, o.hue),
};

const PROP: Readonly<Record<string, Draw>> = {
  edit: (c, P, o) => {
    const hx = P.a;
    const hy = P.b;
    const ang = P.cc;
    const dx = cos(ang);
    const dy = sin(ang);
    const tx = hx + dx * 9;
    const ty = hy + dy * 9;
    line(c, hx - dx * 2, hy - dy * 2, tx, ty, o.C.wood, 2);
    // the hammer head: a 5.2×10 box rotated by `ang`, its corners by hand
    const ux = cos(ang);
    const uy = sin(ang);
    c.fillStyle = o.C.steel;
    c.beginPath();
    c.moveTo(tx + -2.6 * ux - -5 * uy, ty + -2.6 * uy + -5 * ux);
    c.lineTo(tx + 2.6 * ux - -5 * uy, ty + 2.6 * uy + -5 * ux);
    c.lineTo(tx + 2.6 * ux - 5 * uy, ty + 2.6 * uy + 5 * ux);
    c.lineTo(tx + -2.6 * ux - 5 * uy, ty + -2.6 * uy + 5 * ux);
    c.closePath();
    c.fill();
  },
  search: (c, P, o) => {
    const gx = P.a + 5;
    const gy = P.b - 4;
    line(c, P.a, P.b, gx - 3, gy + 3, o.C.wood, 2.2);
    c.beginPath();
    c.arc(gx, gy, 5.2, 0, TAU);
    c.fillStyle = o.C.glass;
    c.fill();
    c.strokeStyle = o.hue;
    c.lineWidth = 1.8;
    c.stroke();
    c.strokeStyle = "#ffffff";
    c.lineWidth = 1;
    c.beginPath();
    c.arc(gx, gy, 3.1, PI * 1.1, PI * 1.5);
    c.stroke();
  },
  read: (c, P, o) => {
    const fl = P.a;
    c.fillStyle = o.C.paper;
    c.strokeStyle = o.hue;
    c.lineWidth = 1.1;
    c.beginPath();
    c.moveTo(-9, -12);
    c.lineTo(0, -10);
    c.lineTo(0, -3.5);
    c.lineTo(-9, -5.5);
    c.closePath();
    c.fill();
    c.stroke();
    c.beginPath();
    c.moveTo(9, -12);
    c.lineTo(0, -10);
    c.lineTo(0, -3.5);
    c.lineTo(9, -5.5);
    c.closePath();
    c.fill();
    c.stroke();
    for (let i = 0; i < 2; i++) {
      line(c, -7, -9.3 + i * 2.3, -2, -8.2 + i * 2.3, o.C.steel, 0.7);
      line(c, 7, -9.3 + i * 2.3, 2, -8.2 + i * 2.3, o.C.steel, 0.7);
    }
    if (fl < 0.16) {
      const u = fl / 0.16;
      const px = 9 * cos(u * PI);
      const py = -12 - sin(u * PI) * 3.5;
      c.fillStyle = o.C.paper;
      c.strokeStyle = o.hue;
      c.beginPath();
      c.moveTo(0, -10);
      c.lineTo(px, py);
      c.lineTo(px, py + 6.5);
      c.lineTo(0, -3.5);
      c.closePath();
      c.fill();
      c.stroke();
    }
  },
  run: (c, P, o) => {
    line(c, 15, -11, P.a, P.b, o.C.steel, 1.7);
    dot(c, 15, -11, 1.7, o.C.steel);
  },
  home: (c, P, o) => {
    const t = P.t;
    c.fillStyle = o.C.panel;
    rr(c, 13.3, -38, 5.6, 9.5, 1.2);
    c.fill();
    c.strokeStyle = o.hue;
    c.lineWidth = 1;
    c.stroke();
    line(c, 17.3, -38, 19.2, -46, o.hue, 1.1);
    dot(c, 19.2, -46, 1.2, o.hue);
    c.strokeStyle = o.hue;
    c.lineWidth = 1.4;
    for (let i = 0; i < 3; i++) {
      const p = fr(t * 1.1 + i / 3);
      const was = fade(c, 1 - p);
      c.beginPath();
      c.arc(19.2, -46, 3 + p * 12, -PI * 0.85, -PI * 0.15);
      c.stroke();
      c.globalAlpha = was;
    }
  },
  review: (c, _P, o) => {
    sheet(c, o.C, -21.5, -33, o.C.neg);
    sheet(c, o.C, 12.5, -33, o.C.pos);
  },
  web: (c, P, o) => {
    // the telescope: a tapered tube along `ang` from (5, -19.5), by hand
    c.translate(5, -19.5);
    c.rotate(P.a);
    c.fillStyle = o.C.steel;
    c.strokeStyle = o.hue;
    c.lineWidth = 1.1;
    c.beginPath();
    c.moveTo(0, -1.7);
    c.lineTo(18, -3.3);
    c.lineTo(18, 3.3);
    c.lineTo(0, 1.7);
    c.closePath();
    c.fill();
    c.stroke();
    c.fillStyle = o.hue;
    c.fillRect(17, -3.8, 2.2, 7.6);
    // undo in reverse: no save/restore pair
    c.rotate(-P.a);
    c.translate(-5, 19.5);
  },
  check: (c, P, o) => {
    const bx = P.a;
    line(c, 5, -21, bx, -3, o.C.wood, 2);
    c.fillStyle = o.hue;
    c.beginPath();
    c.moveTo(bx - 3, -4);
    c.lineTo(bx + 3.5, -4);
    c.lineTo(bx + 5.5, 1);
    c.lineTo(bx - 4.5, 1);
    c.closePath();
    c.fill();
  },
  ship: (c, P, o) => {
    if (P.cc < 0) plane(c, o.C, P.a + 2, P.b - 2, -0.25, o.hue);
  },
  data: (c, P, o) => {
    const dig = P.b;
    const ty = lerp(-12, 0, dig);
    line(c, 5, -18 + dig * 3, 17, ty - 3, o.C.wood, 2);
    c.fillStyle = o.C.steel;
    c.beginPath();
    c.moveTo(15, ty - 5);
    c.lineTo(20.5, ty - 3);
    c.lineTo(19.5, ty + 2);
    c.lineTo(14, ty - 1);
    c.closePath();
    c.fill();
  },
};

/** Fixed points the web pose's stars twinkle at; module-level so no frame builds them. */
const STARS = [31, -45, 41, -37, 37, -53, 47, -48];
const SHIP_DASH = [2, 3];
const NO_DASH: number[] = [];

const FX: Readonly<Record<string, Draw>> = {
  think: (c, P, o) => {
    const t = P.t;
    c.fillStyle = o.C.bubble;
    dot(c, 11, HY - 16, 1.3);
    dot(c, 14, HY - 20, 2);
    rr(c, 8, HY - 35.5, 21, 12, 5.5);
    c.fill();
    c.fillStyle = o.C.ink;
    for (let i = 0; i < 3; i++) {
      const a = fr(t * 1.1 - i * 0.2);
      dot(c, 13 + i * 5.2, HY - 29.5 - (a < 0.3 ? sin((a / 0.3) * PI) * 2.2 : 0), 1.4);
    }
  },
  edit: (c, P, o) => {
    const hit = P.d;
    c.fillStyle = o.hue;
    c.fillRect(17.5, -3, 9, 3);
    if (hit > 0) {
      const was = fade(c, hit);
      for (let i = 0; i < 5; i++) {
        const a = -PI * (0.12 + i * 0.19);
        const r0 = 4 + (1 - hit) * 7;
        const r1 = r0 + 3.2;
        line(c, 22 + cos(a) * r0, -3 + sin(a) * r0, 22 + cos(a) * r1, -3 + sin(a) * r1, o.hue, 1.3);
      }
      c.globalAlpha = was;
    }
  },
  delegate: (c, P, o) => {
    const t = P.t;
    c.strokeStyle = o.hue;
    c.lineWidth = 1.3;
    for (let i = 0; i < 3; i++) {
      const p = fr(t * 1.6 + i / 3);
      const was = fade(c, sin(p * PI) * 0.8);
      const x = 21 + p * 10;
      c.beginPath();
      c.moveTo(x, -20);
      c.lineTo(x + 2.5, -17);
      c.lineTo(x, -14);
      c.stroke();
      c.globalAlpha = was;
    }
  },
  talk: (c, P, o) => {
    const t = P.t;
    const ox = 17;
    const oy = HY - 21;
    c.fillStyle = o.hue;
    rr(c, ox - 2, oy - 10, 18, 12.5, 4.5);
    c.fill();
    c.beginPath();
    c.moveTo(ox, oy + 1.5);
    c.lineTo(ox - 4.5, oy + 7);
    c.lineTo(ox + 5, oy + 2);
    c.fill();
    for (let i = 0; i < 3; i++) {
      const q = fr(t * 1.5 - i * 0.22);
      const y = oy - 6.5 + i * 3.2;
      line(c, ox + 1.5, y, ox + 1.5 + (5 + 6.5 * abs(sin(q * PI))) * (i === 2 ? 0.6 : 1), y, "#15121f", 1.2);
    }
  },
  review: (c, P, o) => {
    const t = P.t;
    for (let i = 0; i < 4; i++) {
      const p = fr(t * 0.7 + i * 0.25);
      const L = i % 2 === 0;
      const was = fade(c, (1 - p) * 0.95);
      T(c, L ? "−" : "+", (L ? -17 : 17) + sin(p * 5 + i) * 2, -39 - p * 15, 9, L ? o.C.neg : o.C.pos);
      c.globalAlpha = was;
    }
  },
  test: (c, P, o) => {
    const w = P.a;
    for (let i = 0; i < 3; i++) {
      const a = w * 0.5 + (i * TAU) / 3;
      dot(c, cos(a) * 13, -37 - sin(a) * 9, 2.7, i === 0 ? o.C.ok : i === 1 ? o.C.warn : o.C.neg);
    }
  },
  web: (c, P, o) => {
    const t = P.t;
    for (let i = 0; i < 4; i++) {
      const tw = 0.35 + 0.65 * abs(sin(t * 2.3 + i * 1.7));
      star(c, STARS[i * 2]!, STARS[i * 2 + 1]!, 1.2 + tw * 1.8, o.hue, tw);
    }
  },
  check: (c, P, o) => {
    const t = P.t;
    for (let i = 0; i < 3; i++) {
      const p = fr(t * 1.6 + i / 3);
      const was = fade(c, (1 - p) * 0.6);
      dot(c, 24 + p * 9, -1 - p * 5 - i, 1 + p * 1.8, o.C.steam);
      c.globalAlpha = was;
    }
  },
  ship: (c, P, o) => {
    const fly = P.cc;
    if (fly < 0) return;
    const was = fade(c, 1 - clamp((fly - 0.75) / 0.25));
    c.setLineDash(SHIP_DASH);
    c.strokeStyle = o.hue === "#ffffff" ? o.C.steel : o.hue;
    c.lineWidth = 1;
    c.beginPath();
    for (let j = 0; j <= 8; j++) {
      const v = (fly * j) / 8;
      const x = lerp(19, 80, v);
      const y = -25 - sin(v * PI * 0.55) * 48;
      if (j) c.lineTo(x, y);
      else c.moveTo(x, y);
    }
    c.stroke();
    c.setLineDash(NO_DASH);
    plane(c, o.C, lerp(19, 80, fly), -25 - sin(fly * PI * 0.55) * 48, -0.6 + fly * 0.45, o.hue);
    c.globalAlpha = was;
  },
  data: (c, P, o) => {
    const p = P.a;
    const n = P.cc;
    for (let i = 0; i < n; i++) disc(c, o.C, 33, -2 - i * 4.6, o.hue);
    if (p > 0.4) {
      const u = (p - 0.4) / 0.6;
      disc(c, o.C, lerp(20, 33, u), lerp(-3, -2 - n * 4.6, u) - sin(u * PI) * 17, o.hue);
    }
  },
  build: (c, P, o) => {
    const n = P.cc;
    for (let i = 0; i < n; i++) block(c, o.C, 24, -3 - i * 6, o.hue, i);
    block(c, o.C, P.a, P.b, o.hue, n);
  },
  blocked: (c, P, o) => flag(c, o.C, 21, -49 - P.a, 1 + 0.13 * sin(P.t * 6), o.hue, "!"),
  question: (c, P, o) => flag(c, o.C, 19, -50 + sin(P.t * 2.4) * 1.5, 1, o.hue, "?"),
  parked: fxSleep,
  done: (c, P, o) => {
    fxSleep(c, P, o);
    line(c, -19, 0, -19, -24, o.C.steel, 1.4);
    c.fillStyle = o.hue;
    c.beginPath();
    c.moveTo(-19, -24);
    c.lineTo(-8.5, -20 + sin(P.t * 3) * 1.1);
    c.lineTo(-19, -16.5);
    c.fill();
  },
  limit: (c, P, o) => {
    const on = fr(P.t * 1.2) < 0.5;
    c.strokeStyle = o.C.bad;
    c.lineWidth = 1.3;
    rr(c, 7, -48, 15, 8.5, 1.5);
    c.stroke();
    c.fillStyle = o.C.bad;
    c.fillRect(22.6, -46, 1.7, 4.5);
    if (on) c.fillRect(9, -46, 2.6, 4.5);
  },
};

/** The names of every pose there is (activities, idle, statuses). */
export const POSE_NAMES: readonly string[] = Object.keys(FILL);

/** Fill `P` with pose `kind` at time `t`; an unknown kind is `think`. */
export function fillPose(P: Pose, kind: string, t: number, o: PoseOpts): void {
  const fill = FILL[kind] ? kind : "think";
  reset(P, fill, t);
  P.seed = o.seed;
  FILL[fill]!(P, t, o);
}

// ------------------------------------------------------------------ the dino

/** The egg's yellow spots: x, y, r. */
const EGG_SPOTS = [-3.5, -15, 2, 3.2, -9, 2.4, -2, -5, 1.5, 3.5, -17, 1.2];

function drawArm(c: Ctx, col: DinoColors, sx: number, hx: number, hy: number) {
  const sy = -13;
  const mx = (sx + hx) / 2;
  const my = (sy + hy) / 2;
  const dx = hx - sx;
  const dy = hy - sy;
  const len = Math.hypot(dx, dy) || 1;
  const sag = max(0, 6 - len * 0.4);
  const ux = dx / len;
  const uy = dy / len;
  const cx = mx - uy * sag * 0.4;
  const cy = my + sag;
  c.strokeStyle = col.dark;
  c.lineWidth = 5.6;
  c.beginPath();
  c.moveTo(sx, sy);
  c.quadraticCurveTo(cx, cy, hx, hy);
  c.stroke();
  c.strokeStyle = col.body;
  c.lineWidth = 3.9;
  c.beginPath();
  c.moveTo(sx, sy);
  c.quadraticCurveTo(cx, cy, hx, hy);
  c.stroke();
  c.fillStyle = col.claw;
  for (let k = -1; k <= 1; k++) {
    c.beginPath();
    c.arc(hx + ux * 1.9 - uy * k * 1.5, hy + uy * 1.9 + ux * k * 1.5, 0.8, 0, TAU);
    c.fill();
  }
}

// ------------------------------------------------------------------ the hats

/**
 * The hats' colours: steel, off-white, brown and oatmeal, none of them near
 * a status colour (violet working, magenta blocked, amber question, red
 * limit, green done), so a hat never reads as its card's state. The hard hat
 * is the exception by the operator's ask: construction yellow, with a dark
 * olive edge so it still shows on the yellow Fable coat.
 */
const HAT_INK = {
  shell: "#ffd83a",
  shellEdge: "#8f6b0a",
  steel: "#b4b8c2",
  steelEdge: "#555a66",
  crest: "#6b5644",
  wing: "#f6f4ee",
  wingEdge: "#8a8679",
  cap: "#6b5a48",
  capEdge: "#3e3226",
  knit: "#b8a98c",
  knitEdge: "#6e6350",
} as const;

function inked(c: Ctx, fill: string, stroke: string, w: number) {
  c.fillStyle = fill;
  c.fill();
  c.strokeStyle = stroke;
  c.lineWidth = w;
  c.stroke();
}

/**
 * A crew member's role hat, in the head's frame (`hx` the head's centre, the
 * crown at y = -34, +x the way the character faces), drawn after the eyes so
 * it sits over the brow. Every shape stays within 18 units of the centre and
 * below y = -50 (the highest control point), far inside the canvas' 150px
 * reach above the card at any character's scale. The shapes follow the
 * style of the reference page's ported hats (12-model-avatars.html, set D):
 * a filled path or two with a darker outline, at its line weights of 1 to
 * 1.2.
 */
const HAT: Readonly<Record<Exclude<Hat, "none">, (c: Ctx, hx: number, front: boolean) => void>> = {
  // hephaestus builds: a hard hat, dome, ridge and brim
  hardhat(c, hx) {
    c.beginPath();
    c.moveTo(hx - 11.5, -31);
    c.bezierCurveTo(hx - 11.5, -43, hx + 11.5, -43, hx + 11.5, -31);
    c.closePath();
    inked(c, HAT_INK.shell, HAT_INK.shellEdge, 1.2);
    line(c, hx, -39.6, hx, -32, HAT_INK.shellEdge, 1.1);
    c.beginPath();
    c.ellipse(hx, -31, 15, 2.3, 0, 0, TAU);
    inked(c, HAT_INK.shell, HAT_INK.shellEdge, 1.1);
  },
  // athena reviews: a crested helmet
  helmet(c, hx) {
    c.beginPath();
    c.moveTo(hx - 9, -37);
    c.quadraticCurveTo(hx - 1, -50, hx + 11, -38.5);
    c.quadraticCurveTo(hx + 1, -44, hx - 9, -37);
    c.closePath();
    inked(c, HAT_INK.crest, HAT_INK.steelEdge, 1);
    c.beginPath();
    c.moveTo(hx - 12.5, -27.5);
    c.bezierCurveTo(hx - 13, -42, hx + 13, -42, hx + 12.5, -27.5);
    c.closePath();
    inked(c, HAT_INK.steel, HAT_INK.steelEdge, 1.2);
    line(c, hx - 12, -29.5, hx + 12, -29.5, HAT_INK.steelEdge, 1);
  },
  // hermes renders: a round cap with a wing on each side
  wings(c, hx) {
    for (let s = -1; s <= 1; s += 2) {
      c.beginPath();
      c.moveTo(hx + s * 7.5, -34.5);
      c.quadraticCurveTo(hx + s * 12, -45, hx + s * 17.5, -44);
      c.quadraticCurveTo(hx + s * 15, -41.5, hx + s * 16, -39);
      c.quadraticCurveTo(hx + s * 12.5, -39.5, hx + s * 7.5, -34.5);
      c.closePath();
      inked(c, HAT_INK.wing, HAT_INK.wingEdge, 1);
    }
    c.beginPath();
    c.moveTo(hx - 9, -32);
    c.bezierCurveTo(hx - 9, -41, hx + 9, -41, hx + 9, -32);
    c.closePath();
    inked(c, HAT_INK.steel, HAT_INK.steelEdge, 1.1);
    c.beginPath();
    c.ellipse(hx, -32, 12, 2.2, 0, 0, TAU);
    inked(c, HAT_INK.steel, HAT_INK.steelEdge, 1.1);
  },
  // any other teammate: a cap, its peak forward (centred when facing out)
  cap(c, hx, front) {
    c.beginPath();
    if (front) c.ellipse(hx, -30.8, 10, 2.2, 0, 0, TAU);
    else c.ellipse(hx + 10, -31, 7.5, 1.9, 0, 0, TAU);
    inked(c, HAT_INK.cap, HAT_INK.capEdge, 1.1);
    c.beginPath();
    c.moveTo(hx - 11, -31);
    c.bezierCurveTo(hx - 11, -42, hx + 11, -42, hx + 11, -31);
    c.closePath();
    inked(c, HAT_INK.cap, HAT_INK.capEdge, 1.2);
    dot(c, hx, -39.3, 1.3, HAT_INK.capEdge);
  },
  // an in-process subagent: a knit beanie with a pom
  beanie(c, hx) {
    dot(c, hx, -42, 2.7, HAT_INK.knit);
    c.strokeStyle = HAT_INK.knitEdge;
    c.lineWidth = 1;
    c.stroke();
    c.beginPath();
    c.moveTo(hx - 12, -29.5);
    c.bezierCurveTo(hx - 12, -43, hx + 12, -43, hx + 12, -29.5);
    c.closePath();
    inked(c, HAT_INK.knit, HAT_INK.knitEdge, 1.2);
    rr(c, hx - 12.8, -32.4, 25.6, 4.4, 1.6);
    inked(c, HAT_INK.knit, HAT_INK.knitEdge, 1.1);
  },
};

/**
 * Draw one dino with pose `P` (already filled), feet at (x, y), scale `s`,
 * facing `flip`, wearing `hat` (none by default; a lead never wears one).
 * Uses one save/restore pair for the whole figure, since the scale, the lean
 * and the bob nest; every effect inside undoes its own state.
 */
export function drawKraken(
  c: Ctx,
  x: number,
  y: number,
  s: number,
  flip: number,
  P: Pose,
  o: PoseOpts,
  col: DinoColors,
  walking: boolean,
  hat: Hat = "none",
): void {
  const t = P.t;
  const keep = FLIP;
  FLIP = flip * keep;
  c.save();
  c.translate(x, y);
  c.scale(s * flip, s);
  c.lineCap = "round";
  c.lineJoin = "round";
  if (P.egg) {
    // a draft has not hatched: a spotted egg that wobbles now and then
    const w = fr(t / 3.2) < 0.18 ? sin((fr(t / 3.2) / 0.18) * TAU * 2) * 0.14 : 0;
    c.rotate(w);
    c.fillStyle = "#fff3c4";
    c.strokeStyle = "#b9a877";
    c.lineWidth = 1.4;
    c.beginPath();
    c.ellipse(0, -11, 8.5, 11, 0, 0, TAU);
    c.fill();
    c.stroke();
    c.fillStyle = "#ffd21f";
    for (let i = 0; i < EGG_SPOTS.length; i += 3) {
      c.beginPath();
      c.arc(EGG_SPOTS[i]!, EGG_SPOTS[i + 1]!, EGG_SPOTS[i + 2]!, 0, TAU);
      c.fill();
    }
    c.restore();
    FLIP = keep;
    return;
  }
  PRE[P.kind]?.(c, P, o);
  const sit = P.sit ? 1 : 0;
  const oy = P.bob + sit * 6;
  const front = P.face === 0;
  const fsh = front ? 0 : 2.4;
  const hx = front ? 0 : 1.2;
  const e = P.eyes;
  const lean = P.lean;
  c.rotate(lean);
  // feet: two stubs with toe claws, on the ground, or dangling over the card
  // edge when seated
  for (let sx = -1; sx <= 1; sx += 2) {
    let fx = sx * 5.4 + (front ? 0 : 1);
    let fy = 0;
    if (sit) {
      fy = oy + 1.8 + sin(t * 2.1 + sx) * 0.7;
      if (P.tap && sx === 1) fy -= max(0, sin(t * 9)) * 3.2;
    } else if (walking) {
      const ph = t * 15 + (sx > 0 ? 0 : PI);
      fx += sin(ph) * 3;
      fy = -max(0, cos(ph)) * 2.6;
    }
    c.fillStyle = col.body;
    c.strokeStyle = col.dark;
    c.lineWidth = 1.2;
    c.beginPath();
    c.ellipse(fx, fy - 2.3, 4.7, 2.9, 0, 0, TAU);
    c.fill();
    c.stroke();
    c.fillStyle = col.claw;
    for (let k = -1; k <= 1; k++) {
      c.beginPath();
      c.arc(fx + k * 2.7 + (front ? 0 : 0.8), fy - 0.5, 0.95, 0, TAU);
      c.fill();
    }
  }
  c.translate(0, oy);
  // a stub of a tail shows when it turns sideways
  if (!front) {
    const wag = sin(t * 3 + P.seed * 9) * 1.3;
    c.fillStyle = col.body;
    c.strokeStyle = col.dark;
    c.lineWidth = 1.3;
    c.beginPath();
    c.moveTo(-7, -9.5);
    c.quadraticCurveTo(-15, -9.5 + wag, -16.5, -3.5 + wag);
    c.quadraticCurveTo(-12, -1.5, -7, -2.5);
    c.closePath();
    c.fill();
    c.stroke();
  }
  // arms: a far arm reaching forward past the body is drawn first, so the
  // body hides its root
  const h = P.hands;
  const farFwd = !front && h[2] > -3;
  if (farFwd) drawArm(c, col, 5, h[2], h[3]);
  // body, narrower than the head, with the cream belly
  c.fillStyle = col.body;
  c.strokeStyle = col.dark;
  c.lineWidth = 1.4;
  c.beginPath();
  c.moveTo(-8.6, -1.2);
  c.bezierCurveTo(-12.8, -4, -11.6, -12, -9.2, -17);
  c.lineTo(9.2, -17);
  c.bezierCurveTo(11.6, -12, 12.8, -4, 8.6, -1.2);
  c.quadraticCurveTo(0, 0.9, -8.6, -1.2);
  c.closePath();
  c.fill();
  c.stroke();
  c.fillStyle = col.belly;
  c.beginPath();
  c.ellipse(fsh * 0.5, -7.4, 6.7, 6, 0, 0, TAU);
  c.fill();
  // head: one big rounded loaf, wider than the body
  c.fillStyle = col.body;
  c.strokeStyle = col.dark;
  c.lineWidth = 1.4;
  c.beginPath();
  c.moveTo(hx - 13.6, -20);
  c.bezierCurveTo(hx - 13.9, -29.5, hx - 8.5, -34, hx, -34);
  c.bezierCurveTo(hx + 8.5, -34, hx + 13.9, -29.5, hx + 13.6, -20);
  c.bezierCurveTo(hx + 13.9, -14.6, hx + 9.5, -12.4, hx, -12.4);
  c.bezierCurveTo(hx - 9.5, -12.4, hx - 13.9, -14.6, hx - 13.6, -20);
  c.closePath();
  c.fill();
  c.stroke();
  let was = fade(c, 0.5);
  c.fillStyle = col.lite;
  c.beginPath();
  c.ellipse(hx - 6, -30, 3.6, 1.6, -0.45, 0, TAU);
  c.fill();
  c.globalAlpha = was;
  // eyes: small, set wide apart, green iris. No mouth.
  const blink = fr(t / 3.7 + P.seed) < 0.035;
  const ey = -23.6;
  for (let sx = -1; sx <= 1; sx += 2) {
    const ex = hx + sx * 7.4 + fsh * 0.75;
    if (e.dead) {
      line(c, ex - 2, ey - 2, ex + 2, ey + 2, col.dark, 1.4);
      line(c, ex - 2, ey + 2, ex + 2, ey - 2, col.dark, 1.4);
      continue;
    }
    let lid = e.lid;
    if (blink && lid < 0.9) lid = 1;
    if (e.wink && sx === -1) lid = 1;
    if (lid >= 0.9) {
      c.strokeStyle = col.dark;
      c.lineWidth = 1.3;
      c.beginPath();
      c.arc(ex, ey - 0.6, 2.5, 0.15 * PI, 0.85 * PI);
      c.stroke();
      continue;
    }
    const rx = e.wide ? 3.3 : 2.9;
    const ry = (e.wide ? 3.7 : 3.2) * (1 - lid * 0.7);
    // the pupil kept inside the white: clamped to the eye's ellipse rather
    // than clipped, which would cost a save/clip/restore per eye
    let px = e.x * 0.85;
    let py = e.y * 0.95 * (1 - lid * 0.6);
    const lim = max(0, ry - 1.6);
    if (abs(py) > lim) py = Math.sign(py) * lim;
    if (abs(px) > rx - 1.4) px = Math.sign(px) * (rx - 1.4);
    px += ex;
    py += ey;
    c.fillStyle = "#ffffff";
    c.beginPath();
    c.ellipse(ex, ey, rx, ry, 0, 0, TAU);
    c.fill();
    c.fillStyle = col.iris;
    c.beginPath();
    c.arc(px, py, min(2.05, ry), 0, TAU);
    c.fill();
    c.fillStyle = "#111111";
    c.beginPath();
    c.arc(px, py, min(1.25, ry * 0.7), 0, TAU);
    c.fill();
    c.fillStyle = "#ffffff";
    c.beginPath();
    c.arc(px - 0.5, py - 0.6, 0.45, 0, TAU);
    c.fill();
  }
  if (hat !== "none") HAT[hat](c, hx, front);
  if (!farFwd) drawArm(c, col, -9.5, h[2], h[3]);
  drawArm(c, col, 9.5, h[0], h[1]);
  PROP[P.kind]?.(c, P, o);
  // back out of the bob and the lean: the effects draw outside them
  c.translate(0, -oy);
  c.rotate(-lean);
  was = c.globalAlpha;
  FX[P.kind]?.(c, P, o);
  c.globalAlpha = was;
  c.restore();
  FLIP = keep;
}
