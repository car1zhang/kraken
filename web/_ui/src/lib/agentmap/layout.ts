// Card placement on the agent map: where a new card goes, how overlapping
// cards push each other apart, and which card draws in front.
//
// Coordinates are WORLD pixels (the map's own space, before pan and zoom), and
// a node's x/y is its card's top-left corner. The clearance MAP_GAP keeps
// between cards is the distance a settle or a drop pushes cards apart to, so
// a smaller gap is a shorter push. It is taller than it is wide to leave some
// room for the characters standing on a card's top edge. Their canvas reaches
// further above the card than the vertical gap (MapView's PAD_TOP), so with
// cards this close they may reach over the card above.

export interface MapNode {
  key: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

export const MAP_GAP = { x: 16, y: 64 };

/**
 * Extra separation a push adds beyond the exact overlap. Positions are saved
 * rounded to whole pixels, and without this margin the rounding could put two
 * just-separated cards a fraction of a pixel back into overlap — and the next
 * settle would move them, and save them, again.
 */
const PUSH_MARGIN = 2;

/** Share of a pair's overlap one settle step resolves: fast, but not a snap. */
const PUSH_RATE = 0.6;

const finite = (n: MapNode) => Number.isFinite(n.x) && Number.isFinite(n.y);

/** True when the two boxes, each grown by half of MAP_GAP on every side, intersect. */
export function overlaps(a: MapNode, b: MapNode): boolean {
  return (
    a.x < b.x + b.w + MAP_GAP.x &&
    b.x < a.x + a.w + MAP_GAP.x &&
    a.y < b.y + b.h + MAP_GAP.y &&
    b.y < a.y + a.h + MAP_GAP.y
  );
}

/** A small deterministic hash of a string, 0..1, for tie-breaking directions. */
function hash01(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return ((h >>> 0) % 10007) / 10007;
}

/**
 * Gives a position to every node without finite coordinates, leaving placed
 * nodes exactly where they are.
 *
 * A new node starts next to the placed node it is linked to (a spawn link), or
 * centred on `origin` when one is given, or else at the centre of everything
 * placed, and walks outward along a spiral until it finds a spot clear of
 * every placed node. Placing them one at a time against what is already
 * placed means a fresh map needs almost no settling: settle then only has to
 * fix what the spiral's step size could not.
 *
 * `origin` is a world point: the map passes the middle of the visible area
 * for a card that arrives after the first placement, so a job just added
 * shows up where the operator is looking rather than wherever the middle of
 * the whole layout happens to be. A non-finite origin is ignored.
 */
export function seedLayout(
  nodes: MapNode[],
  links: ReadonlyArray<readonly [string, string]>,
  origin?: readonly [number, number],
): void {
  const placed = nodes.filter(finite);
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  let cx = 0;
  let cy = 0;
  if (placed.length) {
    for (const n of placed) {
      cx += n.x + n.w / 2;
      cy += n.y + n.h / 2;
    }
    cx /= placed.length;
    cy /= placed.length;
  }
  if (origin && Number.isFinite(origin[0]) && Number.isFinite(origin[1])) {
    cx = origin[0];
    cy = origin[1];
  }
  for (const n of nodes) {
    if (finite(n)) continue;
    // the anchor: a placed partner across any link naming this node
    let ax = cx;
    let ay = cy;
    for (const [a, b] of links) {
      const other = a === n.key ? byKey.get(b) : b === n.key ? byKey.get(a) : undefined;
      if (other && other !== n && finite(other)) {
        ax = other.x + other.w / 2;
        ay = other.y + other.h + MAP_GAP.y + n.h / 2;
        break;
      }
    }
    const w = Number.isFinite(n.w) && n.w > 0 ? n.w : 0;
    const h = Number.isFinite(n.h) && n.h > 0 ? n.h : 0;
    const step = Math.max(40, (w + MAP_GAP.x) / 3);
    let found = false;
    for (let i = 0; i < 4000 && !found; i++) {
      // a golden-angle spiral, stretched wide since cards are wider than tall
      // once the vertical gap is counted
      const r = step * Math.sqrt(i);
      const a = i * 2.39996;
      n.x = Math.round(ax + Math.cos(a) * r * 1.4 - w / 2);
      n.y = Math.round(ay + Math.sin(a) * r * 0.9 - h / 2);
      found = !placed.some((p) => overlaps(n, p));
    }
    placed.push(n);
  }
}

/**
 * One physics step: every overlapping pair is pushed apart along the axis of
 * least overlap by a share of that overlap. A held node never moves, so its
 * partner takes the whole push — which is what makes a dropped card shove the
 * others out of its way. `held` is one key (the card under the pointer) or a
 * set of keys (the cards already showing when another arrives).
 *
 * Returns whether anything moved; false when no pair overlaps, and false too
 * when the only overlapping pairs are held on both ends, which are left as
 * they are and are not movement.
 */
export function settle(nodes: MapNode[], held?: string | ReadonlySet<string>): boolean {
  let moved = false;
  for (let i = 0; i < nodes.length; i++) {
    const a = nodes[i];
    if (!a || !finite(a)) continue;
    for (let k = i + 1; k < nodes.length; k++) {
      const b = nodes[k];
      if (!b || !finite(b) || !overlaps(a, b)) continue;
      const fa = isHeld(a.key, held) ? 0 : 1;
      const fb = isHeld(b.key, held) ? 0 : 1;
      if (fa + fb === 0) continue;
      const wa = fa / (fa + fb);
      const wb = fb / (fa + fb);
      // overlap along each axis, including the clearance, and the direction b
      // should go to leave a
      const dx = b.x + b.w / 2 - (a.x + a.w / 2);
      const dy = b.y + b.h / 2 - (a.y + a.h / 2);
      const ox = (a.w + b.w) / 2 + MAP_GAP.x - Math.abs(dx);
      const oy = (a.h + b.h) / 2 + MAP_GAP.y - Math.abs(dy);
      // two coincident centres have no direction of their own: pick one from
      // the pair's keys, so the same pair always splits the same way
      const sx = dx > 0 ? 1 : dx < 0 ? -1 : hash01(a.key + "|" + b.key) < 0.5 ? -1 : 1;
      const sy = dy > 0 ? 1 : dy < 0 ? -1 : hash01(b.key + "|" + a.key) < 0.5 ? -1 : 1;
      // compare relative overlaps: the vertical clearance is four times the
      // horizontal one, and an absolute comparison would push almost every
      // pair sideways
      const horizontal = ox / ((a.w + b.w) / 2 + MAP_GAP.x) <= oy / ((a.h + b.h) / 2 + MAP_GAP.y);
      const over = horizontal ? ox : oy;
      // small overlaps resolve in one go, so the step count stays bounded
      const q = (over < 8 ? over : over * PUSH_RATE) + PUSH_MARGIN;
      if (horizontal) {
        a.x -= sx * q * wa;
        b.x += sx * q * wb;
      } else {
        a.y -= sy * q * wa;
        b.y += sy * q * wb;
      }
      moved = true;
    }
  }
  return moved;
}

function isHeld(key: string, held: string | ReadonlySet<string> | undefined): boolean {
  if (held === undefined) return false;
  return typeof held === "string" ? key === held : held.has(key);
}

/**
 * Move `n` to the nearest spot overlapping none of `others`, searching
 * outward from where it is along a golden-angle spiral (the seed's walk). True
 * when it moved; false when it was already clear, or when the search found no
 * free spot, in which case `n` is left where it was. The arriving card's way
 * out when physics alone cannot free it from between cards that may not move.
 */
export function freeSpot(n: MapNode, others: readonly MapNode[]): boolean {
  const clear = () => {
    for (const o of others) if (o !== n && finite(o) && overlaps(n, o)) return false;
    return true;
  };
  if (!finite(n) || clear()) return false;
  const x0 = n.x;
  const y0 = n.y;
  const step = Math.max(24, (n.w + MAP_GAP.x) / 6);
  for (let i = 1; i < 20000; i++) {
    const r = step * Math.sqrt(i);
    const a = i * 2.39996;
    n.x = Math.round(x0 + Math.cos(a) * r * 1.4);
    n.y = Math.round(y0 + Math.sin(a) * r * 0.9);
    if (clear()) return true;
  }
  n.x = x0;
  n.y = y0;
  return false;
}

/**
 * The keys, in the order of `nodes`, of every node whose own box (no MAP_GAP)
 * intersects the rectangle with opposite corners `a` and `b`, given in either
 * order. Touching edges count. A node with a non-finite coordinate or size is
 * left out, and a non-finite corner selects nothing. Mutates nothing: this is
 * the shift-drag marquee's hit test.
 */
export function marqueeKeys(
  nodes: readonly MapNode[],
  a: { x: number; y: number },
  b: { x: number; y: number },
): string[] {
  if (![a.x, a.y, b.x, b.y].every(Number.isFinite)) return [];
  const x0 = Math.min(a.x, b.x);
  const x1 = Math.max(a.x, b.x);
  const y0 = Math.min(a.y, b.y);
  const y1 = Math.max(a.y, b.y);
  const out: string[] = [];
  for (const n of nodes) {
    if (![n.x, n.y, n.w, n.h].every(Number.isFinite)) continue;
    if (n.x <= x1 && x0 <= n.x + n.w && n.y <= y1 && y0 <= n.y + n.h) out.push(n.key);
  }
  return out;
}

/**
 * A z-index per key: a card lower on the map (greater y) draws over one higher
 * up, so the characters standing on a card's top edge are covered by the card
 * in front of them and never by the one behind. Equal heights break by key so
 * the order never flickers between two frames.
 */
export function stackOrder(nodes: readonly MapNode[]): Map<string, number> {
  const ys = (n: MapNode) => (Number.isFinite(n.y) ? n.y : -Infinity);
  const sorted = [...nodes].sort(
    (a, b) => ys(a) - ys(b) || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0),
  );
  const out = new Map<string, number>();
  sorted.forEach((n, i) => out.set(n.key, i + 1));
  return out;
}
