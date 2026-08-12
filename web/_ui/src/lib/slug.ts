/**
 * `util.Slugify` (internal/util/util.go), ported.
 *
 * This is a PREVIEW, never a decision: the server slugifies the `to` it is
 * given and the response's `slug` is what the client re-points to. The port
 * exists so the rename control can show what a typed name will become BEFORE
 * the POST — "My Job!" surprising nobody is the whole point — and a drift
 * between the two spellings costs a wrong preview, never a wrong rename.
 *
 * The Go loop is: lowercase, keep [a-z0-9], collapse every other run into one
 * "-", and trim. `lastDash` starts TRUE, which is why a leading separator is
 * never written at all (the trim below only ever has a trailing one to take).
 * `for…of` iterates CODE POINTS, matching Go's range-over-string: an astral
 * character arrives as one two-unit string, fails both range tests, and
 * becomes a dash exactly as its rune does.
 *
 * The LOOP is therefore rune-for-rune; the whole function is not, and the two
 * places it disagrees with Go are pinned in slug.test.ts rather than left to
 * be rediscovered:
 *
 *   - `toLowerCase` applies FULL case mapping where Go's `strings.ToLower`
 *     applies SIMPLE mapping, so U+0130 (İ) expands here to "i" + U+0307 and
 *     that combining mark becomes a separator: "İstanbul" previews as
 *     "i-stanbul" where the server stores "istanbul".
 *   - the sibling Swift port (`JobSlug.slugify`, ios/Kraken/JobFormView.swift)
 *     iterates GRAPHEME CLUSTERS and so diverges from BOTH of us on a
 *     decomposed combining mark; this port agrees with Go there, which is why
 *     slug.test.ts pins the agreeing side too.
 */
export function slugify(s: string): string {
  let out = "";
  let lastDash = true;
  for (const ch of s.toLowerCase()) {
    if ((ch >= "a" && ch <= "z") || (ch >= "0" && ch <= "9")) {
      out += ch;
      lastDash = false;
    } else if (!lastDash) {
      out += "-";
      lastDash = true;
    }
  }
  return out.replace(/^-+|-+$/g, "");
}
