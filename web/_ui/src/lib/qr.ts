/**
 * A minimal QR encoder for ONE job: the Devices overlay's pairing code, a
 * `http://<dns-name>:<port>` URL of ~50 bytes. Stdlib-only by the repo's rule
 * (no third-party deps), so it is written out rather than imported.
 *
 * Deliberately narrow: byte mode, error-correction level L, versions 1–6
 * (up to 134 bytes — far past any tailnet DNS name), and a FIXED mask 0.
 * Every mask is a valid QR code; the eight-way penalty search only picks the
 * one that scans most easily on marginal prints, which a screen-to-camera hop
 * does not need. Anything longer than v6 returns null and the caller shows the
 * URL as text instead.
 */

// GF(256), primitive polynomial 0x11d.
const EXP = new Uint8Array(512);
const LOG = new Uint8Array(256);
{
  let x = 1;
  for (let i = 0; i < 255; i++) {
    EXP[i] = x;
    LOG[x] = i;
    x <<= 1;
    if (x & 0x100) x ^= 0x11d;
  }
  for (let i = 255; i < 512; i++) EXP[i] = EXP[i - 255]!;
}
const mul = (a: number, b: number) => (a && b ? EXP[LOG[a]! + LOG[b]!]! : 0);

/** Reed–Solomon EC codewords for `data`, `deg` of them. */
function rsEncode(data: number[], deg: number): number[] {
  let gen = [1];
  for (let i = 0; i < deg; i++) {
    const next = new Array<number>(gen.length + 1).fill(0);
    for (let j = 0; j < gen.length; j++) {
      next[j]! ^= gen[j]!;
      next[j + 1]! ^= mul(gen[j]!, EXP[i]!);
    }
    gen = next;
  }
  const res = [...data, ...new Array<number>(deg).fill(0)];
  for (let i = 0; i < data.length; i++) {
    const c = res[i]!;
    if (c) for (let j = 0; j < gen.length; j++) res[i + j]! ^= mul(gen[j]!, c);
  }
  return res.slice(data.length);
}

// Level L, versions 1..6: [blocks, data codewords per block, EC per block].
const BLOCKS: readonly (readonly [number, number, number])[] = [
  [1, 19, 7],
  [1, 34, 10],
  [1, 55, 15],
  [1, 80, 20],
  [1, 108, 26],
  [2, 68, 18],
];

/** The module matrix (true = dark), or null when the text exceeds v6-L. */
export function qrMatrix(text: string): boolean[][] | null {
  const bytes = [...new TextEncoder().encode(text)];
  // byte mode: 4-bit mode + 8-bit count (v1–9) + the bytes
  const vi = BLOCKS.findIndex(([n, d]) => n * d * 8 >= 12 + bytes.length * 8);
  if (vi < 0) return null;
  const version = vi + 1;
  const [nBlocks, perBlock, ecLen] = BLOCKS[vi]!;
  const capacity = nBlocks * perBlock;

  // ---- data codewords
  const bits: number[] = [];
  const put = (v: number, n: number) => {
    for (let i = n - 1; i >= 0; i--) bits.push((v >>> i) & 1);
  };
  put(0b0100, 4);
  put(bytes.length, 8);
  for (const b of bytes) put(b, 8);
  put(0, Math.min(4, capacity * 8 - bits.length));
  while (bits.length % 8) bits.push(0);
  const data: number[] = [];
  for (let i = 0; i < bits.length; i += 8) {
    data.push(bits.slice(i, i + 8).reduce((a, b) => (a << 1) | b, 0));
  }
  for (let pad = 0xec; data.length < capacity; pad ^= 0xec ^ 0x11) data.push(pad);

  // ---- blocks, EC, interleave
  const blocks: number[][] = [];
  const ecs: number[][] = [];
  for (let b = 0; b < nBlocks; b++) {
    const blk = data.slice(b * perBlock, (b + 1) * perBlock);
    blocks.push(blk);
    ecs.push(rsEncode(blk, ecLen));
  }
  const stream: number[] = [];
  for (let i = 0; i < perBlock; i++) for (const blk of blocks) stream.push(blk[i]!);
  for (let i = 0; i < ecLen; i++) for (const ec of ecs) stream.push(ec[i]!);

  // ---- matrix and function patterns
  const size = 17 + 4 * version;
  const mod = Array.from({ length: size }, () => new Array<boolean>(size).fill(false));
  const fn = Array.from({ length: size }, () => new Array<boolean>(size).fill(false));
  const set = (x: number, y: number, dark: boolean) => {
    mod[y]![x] = dark;
    fn[y]![x] = true;
  };
  for (let i = 0; i < size; i++) {
    set(6, i, i % 2 === 0);
    set(i, 6, i % 2 === 0);
  }
  const finder = (cx: number, cy: number) => {
    for (let dy = -4; dy <= 4; dy++) {
      for (let dx = -4; dx <= 4; dx++) {
        const x = cx + dx;
        const y = cy + dy;
        if (x < 0 || y < 0 || x >= size || y >= size) continue;
        const d = Math.max(Math.abs(dx), Math.abs(dy));
        set(x, y, d !== 2 && d !== 4);
      }
    }
  };
  finder(3, 3);
  finder(size - 4, 3);
  finder(3, size - 4);
  if (version >= 2) {
    // v2–6 have exactly one alignment pattern that clears the finders
    const c = size - 7;
    for (let dy = -2; dy <= 2; dy++) {
      for (let dx = -2; dx <= 2; dx++) {
        set(c + dx, c + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
      }
    }
  }

  // ---- format info: level L (01), mask 0, BCH(15,5), XOR 0x5412
  const fmtData = 0b01 << 3;
  let rem = fmtData;
  for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
  const fmt = ((fmtData << 10) | rem) ^ 0x5412;
  const fbit = (i: number) => ((fmt >>> i) & 1) === 1;
  for (let i = 0; i <= 5; i++) set(8, i, fbit(i));
  set(8, 7, fbit(6));
  set(8, 8, fbit(7));
  set(7, 8, fbit(8));
  for (let i = 9; i < 15; i++) set(14 - i, 8, fbit(i));
  for (let i = 0; i < 8; i++) set(size - 1 - i, 8, fbit(i));
  for (let i = 8; i < 15; i++) set(8, size - 15 + i, fbit(i));
  set(8, size - 8, true); // the dark module

  // ---- zigzag placement, then mask 0 ((x + y) % 2 === 0) on data modules
  let k = 0;
  const total = stream.length * 8;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let vert = 0; vert < size; vert++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const upward = ((right + 1) & 2) === 0;
        const y = upward ? size - 1 - vert : vert;
        if (fn[y]![x]) continue;
        const bit = k < total ? ((stream[k >>> 3]! >>> (7 - (k & 7))) & 1) === 1 : false;
        k++;
        mod[y]![x] = bit !== ((x + y) % 2 === 0);
      }
    }
  }
  return mod;
}

/**
 * The code as an SVG data URL (4-module quiet zone, dark on light — scanners
 * want that polarity whatever the board's theme), or null past v6-L.
 */
export function qrDataURL(text: string): string | null {
  const m = qrMatrix(text);
  if (!m) return null;
  const n = m.length + 8;
  let d = "";
  m.forEach((row, y) =>
    row.forEach((dark, x) => {
      if (dark) d += `M${x + 4} ${y + 4}h1v1h-1z`;
    }),
  );
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${n} ${n}" shape-rendering="crispEdges">` +
    `<rect width="${n}" height="${n}" fill="#fff"/><path d="${d}" fill="#000"/></svg>`;
  return "data:image/svg+xml;utf8," + encodeURIComponent(svg);
}
