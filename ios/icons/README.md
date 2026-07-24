# Icon archive

App-icon glyph sources (active and retired), kept so directions stay recoverable
without digging through git history. The active icon lives in
`ios/Kraken/AppIcon.icon/` (Liquid Glass) with its iOS 17/18 fallback in
`Assets.xcassets/AppIcon.appiconset/`.

- `zigzag-kraken.svg` — the faceted zigzag mark (shipped 2026-07-21,
  retired same day for the original circular silhouette in saturated purple).
- `og4-kraken.svg` — the ACTIVE glyph (shipped 2026-07-21, geometry refined
  2026-07-22: outer teeth +6px out, inner pair +2px out): OG circular dome
  rebuilt as 5 primitives (1 ellipse + 4 tentacle petals), saturated purple;
  hollowed 2026-07-23: `<mask>` alpha hole `cx=512 cy=452 r=150` punched
  through all five primitives (never a background-colored disc).

Hollow-glyph rasters, regenerated 2026-07-23 from the masked SVG:
`icon-32`/`icon-192` crop `viewBox="149.3 172 727 727"`; `kraken-glyph` is a
plain full-canvas 1024 render. The two opaque assets composite the holed
glyph over their background gradient: `apple-touch-icon` 180² over vertical
`#1B2133`→`#0E131C`; `AppIcon-1024` at 0.88 scale about (512,512) over
vertical `#181D2D`→`#0C1119`.
