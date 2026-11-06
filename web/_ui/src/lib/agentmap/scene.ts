// One card's cast and what passes between them: the lead, its crew, the
// bubbles flying between heads, and the arrows and threads drawn between
// cards. Ported from the trial page's actor/crew/message code.
//
// A card's scene works in CARD-LOCAL coordinates: the card's top-left corner
// is (0, 0) and every character stands on the card's top edge. The view
// translates a card's canvas so that frame lines up with the card, which is
// what lets each card draw its own characters inside its own stacking order.
// Times are SECONDS on the view's clock (performance.now() / 1000).
//
// Two halves with different budgets. `syncScene` runs when a job's payload
// changes and may allocate freely. Everything a frame calls — `drawScene`,
// `sceneBusy`, `drawCrossMsg`, `drawSpawnEdge`, `drawThread`, `leadHead` —
// allocates nothing: each actor owns its Pose and pose options, positions go
// through scratch numbers or a caller's tuple, and text comes from draw.ts'
// cached bitmaps.

import type { AgentEvent, WebActivity, WebAgent, WebJob, WebThread } from "../../api/types.ts";
import { blockedUrgency, crewPose, freshEvents, heldPapers, mapStatus } from "./activity.ts";
import {
  modelCoat,
  roleHat,
  shownThreads,
  threadBranch,
  threadCoat,
  type Branch,
  type Coat,
  type Hat,
} from "./avatar.ts";
import {
  clamp,
  dot,
  drawKraken,
  drawText,
  ease,
  fillPose,
  fr,
  hsh,
  hueOf,
  lerp,
  newPose,
  rr,
  textWidth,
  type Palette,
  type Pose,
  type PoseOpts,
} from "./draw.ts";
import type { MapNode } from "./layout.ts";

const { sin, cos, abs, max, min } = Math;
const PI = Math.PI;
const TAU = PI * 2;

type Ctx = CanvasRenderingContext2D;
export type XY = [number, number];

/** Lead scale on a full card and on a settled (done / draft) one. */
const LEAD_SCALE = 1.35;
const LEAD_SCALE_COMPACT = 0.95;
/** Crew scale; a subagent is smaller again. */
const CREW_SCALE = 0.8;
const SUBAGENT_SCALE = 0.85;
/** At most this many crew drawn on one card; the rest stay off the canvas. */
const CREW_MAX = 8;
/** The generic working loop a crew member with no call of its own cycles through. */
const WORK_LOOP = ["edit", "search", "read", "run", "review", "test"];
const ARROW_DASH = [5, 5];
const NO_DASH: number[] = [];

export interface Actor {
  name: string;
  kind: string;
  /** the hover name tag's text, built once when the name or kind is set */
  tag: string;
  mini: boolean;
  /** the body's colour, from the model the payload names for this character */
  coat: Coat;
  /** a crew member's role hat; the lead's is always "none" */
  hat: Hat;
  /** drawn as a girl (round glasses, ponytail); about one character in three, see `isGirl` */
  girl: boolean;
  /** the branch look of a side thread (avatar.ts `Branch`); "none" for the lead and the crew */
  branch: Branch;
  seed: number;
  /** offset along the card from its home slot */
  x: number;
  /** current home slot, walked toward `homeT` */
  home: number;
  homeT: number;
  flip: number;
  act: string;
  t0: number;
  k: number | null;
  /** sheets of paper held in the far hand: the card's new artifacts (lead only; the crew hold none) */
  papers: number;
  moving: boolean;
  walking: boolean;
  hopAt: number;
  sayUntil: number;
  working: boolean;
  born: number;
  leaving: number | null;
  /** the pose this actor is drawn with, refilled each draw */
  P: Pose;
  /** the options its pose is filled with, reused each draw */
  o: PoseOpts | null;
}

/** A bubble between two heads on one card, or dropped in from a tag above it. */
interface Msg {
  from: Actor | string;
  to: Actor;
  t0: number;
  dur: number;
  hue: string;
  done: boolean;
}

/** A bubble crossing between two cards' leads, drawn on the map's top layer. */
export interface CrossMsg {
  fromKey: string;
  toKey: string;
  t0: number;
  dur: number;
  /** set once it has landed and the receiver has hopped */
  landed: boolean;
}

export interface CardScene {
  key: string;
  w: number;
  compact: boolean;
  status: string;
  lead: Actor;
  crew: Actor[];
  msgs: Msg[];
  /** event ids already acted on; null until the first look */
  seen: Set<string> | null;
  /** talk peer slug → the `sent` count last seen */
  sent: Map<string, number> | null;
  /** the roster by name and the job's activity, taken at the last sync */
  roster: Map<string, WebAgent>;
  /** the side threads drawn as branches, by their actor's name (`threadActorName`), taken at the last sync */
  threads: Map<string, WebThread>;
  activity: WebActivity | undefined;
  hover: boolean;
  crewRR: number;
}

function seedOf(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return hsh(h % 100003);
}

/**
 * Whether the character with this name is drawn as a girl, with round
 * glasses and a ponytail: about one name in three, by the name's hash. The
 * pick is random across names and fixed for a name, so a character keeps its
 * look between frames, after a reload and on every browser. A lead is asked
 * by its job's slug, a crew member by its own name.
 */
export function isGirl(name: string): boolean {
  return seedOf(name) < 1 / 3;
}

function tagText(name: string, kind: string): string {
  return (kind === "subagent" ? "◌ " : "") + (name.length > 18 ? name.slice(0, 17) + "…" : name);
}

/**
 * A branch's actor name: its thread id under a prefix no roster name
 * carries, so a thread titled like a crew member is still its own character.
 */
export function threadActorName(t: Pick<WebThread, "tid">): string {
  return "branch:" + t.tid;
}

/** A branch's hover tag: the branch glyph and its title (its id when it has none), cut like any tag. */
function branchTag(t: Pick<WebThread, "tid" | "title">): string {
  const name = t.title || t.tid;
  return "⑂ " + (name.length > 18 ? name.slice(0, 17) + "…" : name);
}

/**
 * A branch's pose: `think` while a headless thread's question is being
 * answered (the one thing the wire says about what a branch is doing), and
 * `idle` otherwise, a thread no longer on the wire included.
 */
export function branchPose(t: Pick<WebThread, "answering"> | undefined): string {
  return t?.answering ? "think" : "idle";
}

/** A headless branch is drawn this see-through, and this far off the ground (in its own units, plus a slow bob). */
const GHOST_ALPHA = 0.62;
const GHOST_LIFT = 9;

function mkActor(name: string, kind: string, mini: boolean, now: number): Actor {
  return {
    name,
    kind,
    tag: tagText(name, kind),
    mini,
    coat: "fable",
    hat: "none",
    girl: false,
    branch: "none",
    seed: seedOf(name + "|" + kind),
    x: 0,
    home: 0,
    homeT: 0,
    flip: 1,
    act: "think",
    t0: now,
    k: null,
    papers: 0,
    moving: false,
    walking: false,
    hopAt: -9,
    sayUntil: 0,
    working: true,
    born: now,
    leaving: null,
    P: newPose(),
    o: null,
  };
}

export function newScene(key: string, now: number): CardScene {
  return {
    key,
    w: 300,
    compact: false,
    status: "working",
    lead: mkActor("lead:" + key, "lead", false, now - 10),
    crew: [],
    msgs: [],
    seen: null,
    sent: null,
    roster: new Map(),
    threads: new Map(),
    activity: undefined,
    hover: false,
    crewRR: 0,
  };
}

function setAct(a: Actor, act: string, now: number) {
  if (a.act !== act) {
    a.act = act;
    a.t0 = now;
  }
}

function liveCount(s: CardScene): number {
  let n = 0;
  for (let i = 0; i < s.crew.length; i++) if (s.crew[i]!.leaving === null) n++;
  return n;
}

function crewSlots(s: CardScene) {
  const n = liveCount(s);
  const edge = s.w / 2 - 12;
  const step = min((27 * CREW_SCALE) / 0.8, (edge - 30) / max(1, Math.ceil(n / 2)));
  let i = 0;
  for (const m of s.crew) {
    if (m.leaving !== null) continue;
    m.homeT = (i % 2 ? -1 : 1) * (edge - Math.floor(i / 2) * step);
    i++;
  }
}

function leadScale(s: CardScene) {
  return s.compact ? LEAD_SCALE_COMPACT : LEAD_SCALE;
}
function actorScale(s: CardScene, a: Actor) {
  if (!a.mini) return leadScale(s);
  return CREW_SCALE * (a.kind === "subagent" ? SUBAGENT_SCALE : 1);
}

/** An actor's head, card-local. */
const headX = (s: CardScene, a: Actor) => s.w / 2 + a.home + a.x;
const headY = (s: CardScene, a: Actor) => 1 - 23 * actorScale(s, a);

/** The lead's head in WORLD coordinates, written into `out` (for a bubble crossing between cards). */
export function leadHead(s: CardScene, node: MapNode, out: XY): XY {
  out[0] = node.x + headX(s, s.lead);
  out[1] = node.y + headY(s, s.lead);
  return out;
}

/**
 * Bring the scene in line with the job: status, size, the crew on the
 * roster, and the bubbles for every event that arrived since the last look.
 * Returns the cross-card bubbles this job SENT (its `talks` sent counts went
 * up), which the view draws on its top layer.
 *
 * The first look at a job animates nothing: everything already in the
 * transcript tail happened before the map was open.
 */
export function syncScene(
  s: CardScene,
  job: WebJob,
  now: number,
  peerKey: (slug: string) => string,
  onMap: (key: string) => boolean,
): CrossMsg[] {
  s.status = mapStatus(job);
  s.compact = s.status === "done" || s.status === "draft";
  s.lead.k = s.status === "blocked" ? blockedUrgency(job) : null;
  s.lead.papers = heldPapers(job);
  s.activity = job.activity;
  // The lead's coat. A codex cell's lead (`agent` is "codex") is slate
  // whatever its model string says, since a codex `model:` need not name
  // gpt, codex or astra. Any other lead takes its coat from the model id
  // `activity.model` carries for a live cell; a card without it (no activity
  // yet, or a node on an older build) falls back to `agent`, which then says
  // nothing, so that lead is yellow, the coat the map shipped with.
  s.lead.coat = job.agent === "codex" ? "codex" : modelCoat(job.activity?.model || job.agent);
  s.lead.girl = isGirl(job.slug);

  // ---- crew from the roster. Shells are agent-free panes, not characters.
  const want = new Map<string, WebAgent>();
  for (const a of job.roster ?? []) {
    if (a.kind === "shell") continue;
    want.set(a.name || a.role || a.kind || "crew", a);
  }
  s.roster = want;
  for (const m of s.crew) {
    // a branch is not on the roster; the branch loop below decides when it leaves
    if (m.leaving === null && m.branch === "none" && !want.has(m.name)) m.leaving = now;
  }
  for (const [name, a] of want) {
    let m = s.crew.find((c) => c.name === name && c.leaving === null);
    if (!m) {
      if (liveCount(s) >= CREW_MAX) continue;
      m = mkActor(name, a.kind ?? "teammate", true, now);
      m.girl = isGirl(name);
      m.home = s.lead.home + s.lead.x;
      s.crew.push(m);
    }
    const kind = a.kind ?? "teammate";
    if (m.kind !== kind) {
      m.kind = kind;
      m.tag = tagText(m.name, kind);
    }
    m.working = a.working;
    m.coat = modelCoat(a.model);
    m.hat = roleHat(a);
  }

  // ---- branches: the job's live side threads stand with the crew, each in
  // its own model's coat with a twig on its head; a headless one is a ghost.
  // They are added after the crew, so on a card with no room left it is a
  // branch that goes undrawn, and a branch never takes a crew member's name
  // (its actor is named by its thread id).
  const branches = new Map<string, WebThread>();
  for (const t of shownThreads(job.threads)) branches.set(threadActorName(t), t);
  s.threads = branches;
  for (const m of s.crew) {
    if (m.leaving === null && m.branch !== "none" && !branches.has(m.name)) m.leaving = now;
  }
  for (const [name, t] of branches) {
    let m = s.crew.find((c) => c.name === name && c.leaving === null);
    if (!m) {
      if (liveCount(s) >= CREW_MAX) continue;
      m = mkActor(name, "branch", true, now);
      m.girl = isGirl(name);
      m.home = s.lead.home + s.lead.x;
      s.crew.push(m);
    }
    m.branch = threadBranch(t);
    m.coat = threadCoat(t);
    m.tag = branchTag(t);
    m.working = !!t.answering;
  }
  crewSlots(s);

  // ---- events: bubbles inside the card
  const events = job.activity?.events ?? [];
  if (s.seen === null) {
    s.seen = new Set(events.map((e) => e.id));
  } else {
    const fresh = freshEvents(s.seen, events).slice(-4);
    fresh
      // a message from a cell whose card is on the map is drawn as the bubble
      // crossing from that card (the sender's talks count), not a second time
      // as a tag dropping in here
      .filter((e) => !(e.act === "hear" && e.from === "cell" && e.who && onMap(peerKey(e.who))))
      .forEach((e, i) => eventBubble(s, e, now + i * 0.9));
    s.seen = new Set(events.map((e) => e.id));
  }

  // ---- talks: a bubble to each peer this job sent to since the last look
  const out: CrossMsg[] = [];
  const talks = job.talks ?? [];
  if (s.sent === null) {
    s.sent = new Map(talks.map((t) => [t.slug, t.sent]));
  } else {
    for (const t of talks) {
      const before = s.sent.get(t.slug) ?? 0;
      if (t.sent > before) out.push({ fromKey: s.key, toKey: peerKey(t.slug), t0: now, dur: 1.6, landed: false });
      s.sent.set(t.slug, t.sent);
    }
  }
  return out;
}

function findCrew(s: CardScene, who: string | undefined): Actor | undefined {
  if (!who) return undefined;
  return s.crew.find((m) => m.leaving === null && m.name === who);
}

/** A teammate to stand in for one the event cannot name. */
function anyTeammate(s: CardScene): Actor | undefined {
  const tm = s.crew.filter((m) => m.leaving === null && m.kind === "teammate");
  if (!tm.length) return undefined;
  s.crewRR += 1;
  return tm[s.crewRR % tm.length];
}

const TAGX: Readonly<Record<string, number>> = { you: -64, kraken: 64, task: 24, cell: 0, crew: 40 };

function eventBubble(s: CardScene, e: AgentEvent, at: number) {
  const msg = (from: Actor | string, to: Actor, hue: string, dur = 0.9) => {
    s.msgs.push({ from, to, t0: at, dur, hue, done: false });
    if (s.msgs.length > 40) s.msgs.shift();
  };
  if (e.act === "talk") {
    const m = findCrew(s, e.who);
    if (!m) return;
    s.lead.sayUntil = at + 1.3;
    msg(s.lead, m, "talk");
    return;
  }
  if (e.act !== "hear") return;
  switch (e.from) {
    case "teammate": {
      const m = findCrew(s, e.who) ?? anyTeammate(s);
      if (m) {
        m.sayUntil = at + 1.3;
        msg(m, s.lead, "talk");
      } else msg("crew", s.lead, "talk");
      return;
    }
    case "task":
      msg("task", s.lead, "steel");
      return;
    case "kraken":
      msg("kraken", s.lead, "ac", 1.1);
      return;
    case "cell":
      msg("cell", s.lead, "ac2", 1.1);
      return;
    default:
      msg("you", s.lead, "fg", 1.1);
  }
}

/** A message hue is a palette NAME, resolved at draw time so a theme flip recolours bubbles in flight. */
function msgHue(C: Palette, h: string): string {
  switch (h) {
    case "talk":
      return hueOf(C, "talk");
    case "steel":
      return C.steel;
    case "ac":
      return C.ac;
    case "ac2":
      return C.ac2;
    case "fg":
      return C.fg;
    default:
      return h;
  }
}

function stepActor(a: Actor, now: number, dt: number, R: number, C: Palette) {
  const act = now < a.sayUntil ? "talk" : a.act;
  const t = now - a.t0 + a.seed * 10;
  let o = a.o;
  if (!o) {
    o = { seed: a.seed, moving: false, k: null, hue: "", C, papers: 0 };
    a.o = o;
  }
  o.moving = a.moving;
  o.k = a.k;
  o.papers = a.papers;
  o.hue = hueOf(C, act);
  o.C = C;
  const P = a.P;
  fillPose(P, act, t, o);
  const hp = now - a.hopAt;
  if (hp >= 0 && hp < 0.45) P.bob -= sin((hp / 0.45) * PI) * 7;
  if (!Number.isNaN(P.tx)) {
    const target = clamp(P.tx, -1, 1) * R;
    const d = target - a.x;
    if (abs(d) > 1.5) {
      a.moving = true;
      a.flip = d > 0 ? 1 : -1;
      a.x += Math.sign(d) * min(abs(d), P.speed * dt);
    } else a.moving = false;
  } else {
    a.moving = false;
    if (abs(a.x) > R) a.x = Math.sign(a.x) * R;
    if (P.face === 0) a.flip = 1;
    // props reach ~35px forward: turn inward rather than hang off the card end
    else if (now >= a.sayUntil && !a.mini && a.x * a.flip > R - 18) a.flip = -a.flip;
  }
  if (a.walking) a.moving = true;
}

/**
 * Every character wears its model's coat; a lead that hit a usage limit
 * keeps the greyed-out spent coat it always had, since that is its status.
 * Crew wear their role hats; the lead's hat is always none. A girl keeps
 * her glasses and ponytail under any coat and any hat. A branch wears its
 * twig; a headless one is a ghost, drawn see-through and lifted off the
 * ground on a slow bob of its own.
 */
function drawActor(c: Ctx, s: CardScene, a: Actor, C: Palette) {
  const col = !a.mini && a.act === "limit" ? C.dino.spent : C.coats[a.coat];
  const sc = actorScale(s, a);
  if (a.branch !== "headless") {
    drawKraken(c, headX(s, a), 1, sc, a.flip, a.P, a.o!, col, a.moving, a.hat, a.girl, a.branch);
    return;
  }
  const was = c.globalAlpha;
  c.globalAlpha = was * GHOST_ALPHA;
  const y = 1 - (GHOST_LIFT + Math.sin(a.P.t * 2.1 + a.seed * 7) * 1.6) * sc;
  drawKraken(c, headX(s, a), y, sc, a.flip, a.P, a.o!, col, a.moving, a.hat, a.girl, a.branch);
  c.globalAlpha = was;
}

/**
 * Advance and draw one card's cast on its own canvas: crew first (behind),
 * then the lead, then the bubbles between them, then — while the card is
 * hovered — a name tag over each crew member.
 *
 * `leadAct` is the lead's pose (leadPose, or the status pose); crew poses are
 * recomputed here from the roster and activity the last sync took.
 */
export function drawScene(
  c: Ctx,
  s: CardScene,
  leadAct: string,
  C: Palette,
  now: number,
  nowMs: number,
  dt: number,
): void {
  for (let i = s.crew.length - 1; i >= 0; i--) {
    const m = s.crew[i]!;
    if (m.leaving !== null && now - m.leaving > 0.6) {
      s.crew.splice(i, 1);
      continue;
    }
    const dh = m.homeT - m.home;
    m.walking = abs(dh) > 2;
    if (m.walking) {
      m.home += Math.sign(dh) * min(abs(dh), 70 * dt);
      m.flip = dh > 0 ? 1 : -1;
    }
    const agent = s.roster.get(m.name);
    let pose = agent ? crewPose(agent, s.activity, nowMs) : "idle";
    if (pose === "work") pose = WORK_LOOP[Math.floor(now / 7 + m.seed * 40) % WORK_LOOP.length] ?? "think";
    // a branch has no calls on the wire: a headless one thinks while its
    // question is being answered, and any branch otherwise stands idle
    if (m.branch !== "none") pose = branchPose(s.threads.get(m.name));
    setAct(m, pose, now);
    stepActor(m, now, dt, 4, C);
    // a teammate and a branch are drawn solid (a headless branch's ghost
    // fade is drawActor's); a subagent or monitor is fainter
    let al = m.kind === "teammate" || m.branch !== "none" ? 1 : 0.55;
    if (m.leaving !== null) al *= 1 - (now - m.leaving) / 0.6;
    al *= min(1, (now - m.born) / 0.3);
    c.globalAlpha = max(0, al);
    drawActor(c, s, m, C);
  }
  c.globalAlpha = 1;
  setAct(s.lead, leadAct, now);
  const R = s.w / 2 - (s.compact ? 34 : 30) - (liveCount(s) ? 16 : 0);
  stepActor(s.lead, now, dt, R, C);
  drawActor(c, s, s.lead, C);
  drawMsgs(c, s, C, now);
  if (s.hover) drawCrewTags(c, s, C);
}

/**
 * Whether the card has fast motion going on right now: a bubble in flight or
 * waiting to fly, a crew member walking to its slot or fading out, a lead
 * mid-hop or mid-sentence, or name tags shown under the pointer. The view
 * draws a busy card at its full rate whatever its status.
 */
export function sceneBusy(s: CardScene, now: number): boolean {
  if (s.msgs.length || s.hover) return true;
  if (now < s.lead.sayUntil || now - s.lead.hopAt < 0.5) return true;
  for (let i = 0; i < s.crew.length; i++) {
    const m = s.crew[i]!;
    if (m.leaving !== null || m.walking || abs(m.homeT - m.home) > 2) return true;
    if (now < m.sayUntil || now - m.hopAt < 0.5 || now - m.born < 0.4) return true;
  }
  return false;
}

function drawCrewTags(c: Ctx, s: CardScene, C: Palette) {
  const k = leadScale(s) / 1.35;
  const px = 7.5 * k;
  let i = 0;
  for (let j = 0; j < s.crew.length; j++) {
    const m = s.crew[j]!;
    if (m.leaving !== null) continue;
    const hx = headX(s, m);
    const y = headY(s, m) - (16 + (i % 3) * 10) * k;
    const w = textWidth(m.tag, px, 600) + 6 * k;
    c.fillStyle = C.b0;
    c.globalAlpha = 0.85;
    rr(c, hx - w / 2, y - 5.5 * k, w, 11 * k, 2.5 * k);
    c.fill();
    c.globalAlpha = 1;
    drawText(c, m.tag, hx, y + 0.5 * k, px, m.kind === "teammate" || m.branch !== "none" ? C.fg : C.steel, 600);
    i++;
  }
}

/** One bubble's flight from (px, py) to (qx, qy): a dotted trail behind it, a ring where it lands. */
function drawBubble(
  c: Ctx,
  C: Palette,
  px: number,
  py: number,
  qx: number,
  qy: number,
  u: number,
  lift: number,
  hue: string,
  k: number,
) {
  const was = c.globalAlpha;
  if (u <= 1) {
    const e = ease(u);
    c.fillStyle = hue;
    for (let j = 3; j >= 1; j--) {
      const v = max(0, e - j * 0.07);
      c.globalAlpha = was * (0.5 - j * 0.13);
      dot(c, lerp(px, qx, v), lerp(py, qy, v) - sin(v * PI) * lift, 2.2 * k);
    }
    c.globalAlpha = was;
    const bx = lerp(px, qx, e);
    const by = lerp(py, qy, e) - sin(e * PI) * lift;
    // the bubble itself, scaled about its centre by `k`, drawn by hand rather
    // than under a save/translate/scale/restore
    c.fillStyle = hue;
    c.strokeStyle = C.b0;
    c.lineWidth = k;
    rr(c, bx - 6 * k, by - 5 * k, 12 * k, 9 * k, 3 * k);
    c.fill();
    c.stroke();
    c.beginPath();
    c.moveTo(bx - 2 * k, by + 3.5 * k);
    c.lineTo(bx - 4.5 * k, by + 7.5 * k);
    c.lineTo(bx + 1.5 * k, by + 3.5 * k);
    c.fill();
    c.fillStyle = C.b0;
    for (let j = 0; j < 3; j++) dot(c, bx + (-3 + j * 3) * k, by - 0.6 * k, 0.9 * k);
  } else {
    const r = (u - 1) / 0.5;
    c.globalAlpha = was * (1 - r);
    c.strokeStyle = hue;
    c.lineWidth = 1.4;
    c.beginPath();
    c.arc(qx, qy, (5 + r * 12) * k, 0, TAU);
    c.stroke();
    c.globalAlpha = was;
  }
}

function drawMsgs(c: Ctx, s: CardScene, C: Palette, now: number) {
  const k = leadScale(s) / 1.35;
  for (let i = s.msgs.length - 1; i >= 0; i--) {
    const m = s.msgs[i]!;
    const u = (now - m.t0) / m.dur;
    if (u < 0) continue;
    if (u > 1.5) {
      s.msgs.splice(i, 1);
      continue;
    }
    const hue = msgHue(C, m.hue);
    const qx = headX(s, m.to);
    const qy = headY(s, m.to);
    const from = m.from;
    const tag = typeof from === "string";
    const px = tag ? qx + (TAGX[from] ?? 0) * k : headX(s, from);
    const py = tag ? qy - 92 * k : headY(s, from);
    const lift = tag ? 0 : min(60, 26 + Math.hypot(qx - px, qy - py) * 0.18);
    if (u >= 1 && !m.done) {
      m.done = true;
      m.to.hopAt = now;
    }
    if (tag) {
      // the sender's tag, faded in and out around the drop
      c.globalAlpha = u < 0.15 ? u / 0.15 : u > 1.1 ? max(0, (1.5 - u) / 0.4) : 1;
      const w = textWidth(from, 8 * k) + 8 * k;
      c.fillStyle = hue;
      rr(c, px - w / 2, py - 13 * k, w, 11 * k, 3 * k);
      c.fill();
      drawText(c, from, px, py - 7.2 * k, 8 * k, C.b0);
      c.globalAlpha = 1;
    }
    drawBubble(c, C, px, py, qx, qy, u, lift, hue, k);
  }
}

/**
 * A bubble crossing from one card's lead (p) to another's (q), in world
 * coordinates. Returns false once it has landed and faded, so the caller can
 * drop it.
 */
export function drawCrossMsg(c: Ctx, C: Palette, m: CrossMsg, p: XY, q: XY, now: number): boolean {
  const u = (now - m.t0) / m.dur;
  if (u > 1.5) return false;
  if (u < 0) return true;
  const d = Math.hypot(q[0] - p[0], q[1] - p[1]);
  drawBubble(c, C, p[0], p[1], q[0], q[1], u, min(90, 26 + d * 0.18), C.ac2, 1);
  return true;
}

/** Make the receiving lead hop when a crossing bubble lands. */
export function landCross(s: CardScene, now: number) {
  s.lead.hopAt = now;
}

/**
 * The point where a ray from a box's centre toward (tx, ty) leaves it, 6px
 * out, written into `out`.
 */
export function edgePoint(n: MapNode, tx: number, ty: number, out: XY = [0, 0]): XY {
  const cx = n.x + n.w / 2;
  const cy = n.y + n.h / 2;
  const dx = tx - cx;
  const dy = ty - cy;
  if (!dx && !dy) {
    out[0] = cx;
    out[1] = cy;
    return out;
  }
  const s = min((n.w / 2 + 6) / abs(dx || 1e-6), (n.h / 2 + 6) / abs(dy || 1e-6));
  out[0] = cx + dx * s;
  out[1] = cy + dy * s;
  return out;
}

/** Scratch end points for the arrow and thread drawers; never escapes this module. */
const EP: XY = [0, 0];
const EQ: XY = [0, 0];

/**
 * A spawn link: a dashed arrow from the parent card to the child. While the
 * parent's lead is delegating, the arrow brightens, its dashes run faster and
 * two pulses travel down it.
 */
export function drawSpawnEdge(c: Ctx, C: Palette, a: MapNode, b: MapNode, hot: boolean, now: number) {
  const p = edgePoint(a, b.x + b.w / 2, b.y + b.h / 2, EP);
  const q = edgePoint(b, a.x + a.w / 2, a.y + a.h / 2, EQ);
  const cx = (p[0] + q[0]) / 2 - (q[1] - p[1]) * 0.12;
  const cy = (p[1] + q[1]) / 2 + (q[0] - p[0]) * 0.12;
  const col = hot ? C.ac2 : C.edge;
  c.strokeStyle = col;
  c.lineWidth = hot ? 1.8 : 1.2;
  c.setLineDash(ARROW_DASH);
  c.lineDashOffset = -now * (hot ? 40 : 8);
  c.beginPath();
  c.moveTo(p[0], p[1]);
  c.quadraticCurveTo(cx, cy, q[0], q[1]);
  c.stroke();
  c.setLineDash(NO_DASH);
  const ang = Math.atan2(q[1] - cy, q[0] - cx);
  c.fillStyle = col;
  c.beginPath();
  c.moveTo(q[0], q[1]);
  c.lineTo(q[0] - cos(ang - 0.4) * 9, q[1] - sin(ang - 0.4) * 9);
  c.lineTo(q[0] - cos(ang + 0.4) * 9, q[1] - sin(ang + 0.4) * 9);
  c.closePath();
  c.fill();
  if (hot) {
    for (let i = 0; i < 2; i++) {
      const u = fr(now * 0.7 + i * 0.5);
      const x = (1 - u) * (1 - u) * p[0] + 2 * u * (1 - u) * cx + u * u * q[0];
      const y = (1 - u) * (1 - u) * p[1] + 2 * u * (1 - u) * cy + u * u * q[1];
      dot(c, x, y, 3, C.ac2);
    }
  }
}

/** Thread counts as strings, made once each so a redraw builds no string. */
const COUNTS: string[] = [];
function countText(n: number): string {
  const i = Math.max(0, Math.floor(n));
  if (i >= 10000) return String(i);
  return (COUNTS[i] ??= String(i));
}

/**
 * A message thread between two cards: a solid line, thicker the more messages
 * it carries and brightest just after the latest, with the count on it.
 * `ageSec` is how long ago the last message was.
 */
export function drawThread(c: Ctx, C: Palette, a: MapNode, b: MapNode, n: number, ageSec: number) {
  const p = edgePoint(a, b.x + b.w / 2, b.y + b.h / 2, EP);
  const q = edgePoint(b, a.x + a.w / 2, a.y + a.h / 2, EQ);
  const hot = max(0, 1 - ageSec / 2.5);
  const cx = (p[0] + q[0]) / 2 + (q[1] - p[1]) * 0.1;
  const cy = (p[1] + q[1]) / 2 - (q[0] - p[0]) * 0.1;
  const was = c.globalAlpha;
  c.globalAlpha = was * (0.5 + 0.5 * max(0, 1 - ageSec / 40));
  c.strokeStyle = C.ac2;
  c.lineWidth = 1.6 + hot * 1.6 + min(2, n * 0.15);
  c.lineCap = "round";
  c.beginPath();
  c.moveTo(p[0], p[1]);
  c.quadraticCurveTo(cx, cy, q[0], q[1]);
  c.stroke();
  dot(c, p[0], p[1], 3, C.ac2);
  dot(c, q[0], q[1], 3, C.ac2);
  const bx = (p[0] + 2 * cx + q[0]) / 4;
  const by = (p[1] + 2 * cy + q[1]) / 4;
  const txt = countText(n);
  const w = max(15, textWidth(txt, 9) + 9);
  c.globalAlpha = was;
  c.fillStyle = C.b0;
  c.strokeStyle = C.ac2;
  c.lineWidth = 1.2;
  rr(c, bx - w / 2, by - 7, w, 14, 7);
  c.fill();
  c.stroke();
  drawText(c, txt, bx, by + 0.5, 9, C.ac2);
}
