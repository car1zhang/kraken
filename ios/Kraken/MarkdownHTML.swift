import Foundation

// MARK: - markdown → HTML, ported from web/app.js's esc()/inlineMD()/renderMD()
//
// The ArtifactView-only path (.md artifacts, full-screen) — inline body text
// (e.g. JobDetailView's DetailCard) renders natively via MarkdownText.swift
// instead; one WKWebView per inline block was defeating WebKit's viewport
// tiling and getting the app jetsam-killed.
//
// Block-level parsing lives in MarkdownParser.swift (`parseMarkdownBlocks`,
// shared with the native SwiftUI renderer in MarkdownText.swift); this file
// only renders those blocks to HTML via `inlineHTML`/`escapeHTML` (also in
// MarkdownParser.swift). Escaping happens FIRST inside inlineHTML, exactly
// like esc() does in app.js's inlineMD() — the inline-pattern regexes only
// ever run over already-entity-escaped text. Matching that ordering
// precisely matters: do it the other way around (format, then escape) and
// `<`/`&` inside e.g. a backtick span would either double-escape or —
// worse — not escape at all, opening an HTML-injection gap the web UI
// doesn't have.

/// Renders the parsed table. Ragged rows are padded with empty cells (fewer
/// than the header) or truncated (more) against the header's column count,
/// never propagating a raw index-out-of-bounds into a live layout. Wrapped
/// in an overflow-x:auto div so a wide table scrolls instead of squeezing
/// the phone layout.
private func renderTable(headerCells: [String], aligns: [String], bodyRows: [[String]]) -> String {
    let columnCount = headerCells.count
    func alignAttr(_ i: Int) -> String {
        let a = i < aligns.count ? aligns[i] : "left"
        return a == "left" ? "" : " style=\"text-align:\(a)\""
    }
    var html = "<div style=\"overflow-x:auto\"><table><thead><tr>"
    for (i, cell) in headerCells.enumerated() {
        html += "<th\(alignAttr(i))>\(inlineHTML(cell))</th>"
    }
    html += "</tr></thead><tbody>"
    for row in bodyRows {
        html += "<tr>"
        for i in 0..<columnCount {
            let raw = i < row.count ? row[i] : ""
            html += "<td\(alignAttr(i))>\(inlineHTML(raw))</td>"
        }
        html += "</tr>"
    }
    html += "</tbody></table></div>"
    return html
}

/// Renders `parseMarkdownBlocks`' output (MarkdownParser.swift) to HTML.
/// Byte-for-byte equivalent to the old inline block-level pass this replaced:
/// top-level emitted strings are joined with "\n", but a list's `<ul>`,
/// each `<li>`, and `</ul>` are separate emitted strings (so they end up
/// newline-separated), while a table renders as ONE string with no internal
/// newlines — match `renderTable`'s single-string return, don't split it.
private func markdownToHTML(_ src: String) -> String {
    var out: [String] = []
    for block in parseMarkdownBlocks(src) {
        switch block {
        case .heading(let level, let text):
            out.append("<h\(level)>\(inlineHTML(text))</h\(level)>")
        case .paragraph(let text):
            out.append("<p>\(inlineHTML(text))</p>")
        case .list(let ordered, let items):
            let tag = ordered ? "ol" : "ul"
            out.append("<\(tag)>")
            for item in items {
                out.append("<li>\(inlineHTML(item))</li>")
            }
            out.append("</\(tag)>")
        case .codeFence(let code):
            out.append("<pre><code>\(escapeHTML(code))</code></pre>")
        case .blockquote(let text):
            out.append("<blockquote>\(inlineHTML(text))</blockquote>")
        case .table(let header, let aligns, let rows):
            out.append(renderTable(headerCells: header, aligns: aligns, bodyRows: rows))
        }
    }
    return out.joined(separator: "\n")
}

// MARK: - the shell's palette, derived from the app's tint
//
// This page renders inside a WKWebView that sits directly on the tinted
// field, so a hardcoded cyber-dark palette here reads as a foreign near-black
// rectangle pasted onto a violet (or cyan, or amber…) screen — a seam. The
// custom properties below are therefore GENERATED from the same
// `(hue, saturation, brightness)` recipe `GlassTheme.shadeUI` uses, off the
// same persisted tint, so the artifact page is the same surface as the app
// around it whichever of the eight swatches is selected.
//
// The one deliberate exception is `--revealed` (inline code): state hues,
// job colors and skill hues are NOT tint-derived anywhere in this redesign,
// so inline code keeps KTheme.revealed's two literal hexes.

/// HSB → a CSS `hsl()` string, so the stylesheet can be written in the same
/// numbers `GlassTheme` is written in rather than in hand-picked hexes that
/// would drift from it. `saturation` is PRE-scale — the tint's
/// `saturationScale` is applied here, once, exactly as `shadeUI` does it.
private func themeCSSColor(
    hueDegrees: Double, saturationScale: Double, saturation: Double, brightness: Double
) -> String {
    let s = min(max(saturation * saturationScale, 0), 1)
    let v = min(max(brightness, 0), 1)
    let lightness = v * (1 - s / 2)
    let denominator = min(lightness, 1 - lightness)
    let hslSaturation = denominator <= 0 ? 0 : (v - lightness) / denominator
    return String(
        format: "hsl(%.0f, %.1f%%, %.1f%%)",
        hueDegrees, hslSaturation * 100, lightness * 100
    )
}

/// The `:root` / light-override custom properties for the currently stored
/// tint. Read through `ThemeTint.resolve`, so an unrecognized or absent
/// persisted value degrades to violet rather than producing a broken page.
private func themeCSSVariables() -> String {
    let tint = ThemeTint.resolve(UserDefaults.standard.string(forKey: KTheme.themeTintKey))
    let hue = tint.hueDegrees
    let scale = tint.saturationScale
    func color(_ saturation: Double, _ brightness: Double) -> String {
        themeCSSColor(
            hueDegrees: hue, saturationScale: scale,
            saturation: saturation, brightness: brightness
        )
    }

    // Dark: the field's own near-black stops, lifted step by step for the
    // surfaces above it. Light: the pale siblings. The accent pair is
    // literally `GlassTheme.accentUI`'s numbers (0.45/1.0 dark;
    // 0.72/`tint.lightBrightness` light — per-tint, so the artifact page's
    // links clear the same 4.5:1 the app's accent was re-solved to), so a
    // link in an artifact is the same colour as a link in the app.
    return """
      :root {
        color-scheme: light dark;
        --field-1: \(color(0.55, 0.075));
        --field-2: \(color(0.62, 0.170));
        --ground: \(color(0.55, 0.075));
        --raised: \(color(0.50, 0.140));
        --hairline: \(color(0.40, 0.220));
        --hairline2: \(color(0.32, 0.340));
        --text: \(color(0.10, 0.930));
        --text2: \(color(0.12, 0.620));
        --accent: \(color(0.45, 1.000));
        --revealed: #F5B84D;
        --zebra: rgba(255, 255, 255, 0.02);
      }
      @media (prefers-color-scheme: light) {
        :root {
          --field-1: \(color(0.06, 0.990));
          --field-2: \(color(0.12, 0.940));
          --ground: \(color(0.06, 0.990));
          --raised: \(color(0.10, 0.950));
          --hairline: \(color(0.14, 0.880));
          --hairline2: \(color(0.18, 0.740));
          --text: \(color(0.30, 0.160));
          --text2: \(color(0.18, 0.450));
          --accent: \(color(0.72, tint.lightBrightness));
          --revealed: #8A5106;
          --zebra: rgba(0, 0, 0, 0.025);
        }
      }
    """
}

/// Common HTML shell matching web/style.css's .md rules (accent h1,
/// hairline-underlined h2, raised-bg inline code, bordered pre blocks,
/// accent links) plus a viewport meta tag and a touch-friendly base font
/// size. Always fills its container — full-screen ArtifactView is the only
/// caller now, so there's no transparent/inline variant to switch between.
///
/// The body paints the theme's own three-stop gradient (same hues, same
/// top-leading→bottom-trailing direction as `ThemeBackgroundView`), fixed so
/// scrolling doesn't drag it, and every block surface picked up a continuous
/// corner radius — the sharp-rect idiom is gone from this page too.
private func documentShell(body: String) -> String {
    return """
    <!doctype html>
    <html>
    <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
    \(themeCSSVariables())
      * { box-sizing: border-box; -webkit-text-size-adjust: 100%; }
      html, body { background: var(--field-1); }
      body {
        margin: 0; padding: 16px;
        min-height: 100%;
        color: var(--text);
        font: 15px/1.55 -apple-system, "Avenir Next", "Helvetica Neue", sans-serif;
        background:
          linear-gradient(135deg, var(--field-1) 0%, var(--field-2) 50%, var(--field-1) 100%)
          fixed;
      }
      h1, h2, h3, h4 { margin: 1.1em 0 0.4em; font-weight: 600; }
      h1:first-child, h2:first-child, h3:first-child, h4:first-child, p:first-child { margin-top: 0; }
      h1 { font-size: 20px; color: var(--accent); }
      h2 { font-size: 17px; border-bottom: 1px solid var(--hairline); padding-bottom: 3px; }
      h3 { font-size: 16px; }
      h4 { font-size: 15px; }
      p, ul, ol { margin: 0.55em 0; }
      ul, ol { padding-left: 20px; }
      li { margin: 0.15em 0; }
      code {
        font: 13px "SF Mono", Menlo, monospace; background: var(--raised);
        color: var(--revealed); padding: 1px 5px; border-radius: 5px;
      }
      pre {
        background: var(--raised); border: 1px solid var(--hairline); padding: 10px 12px;
        border-radius: 12px;
        overflow-x: auto; margin: 0.7em 0; white-space: pre-wrap; word-break: break-word;
      }
      pre code { background: none; padding: 0; color: var(--text); }
      a { color: var(--accent); }
      strong { color: var(--text); font-weight: 600; }
      blockquote {
        border-left: 3px solid var(--hairline2); border-radius: 2px;
        padding-left: 10px; color: var(--text2); margin: 0.55em 0;
      }
      table {
        border-collapse: separate; border-spacing: 0; width: 100%; margin: 0.7em 0;
        font-size: 14px; border: 1px solid var(--hairline); border-radius: 12px;
        overflow: hidden;
      }
      th, td { border-bottom: 1px solid var(--hairline); border-right: 1px solid var(--hairline); padding: 6px 10px; }
      th:last-child, td:last-child { border-right: none; }
      tbody tr:last-child td { border-bottom: none; }
      th { background: var(--raised); color: var(--text); font-weight: 600; text-align: left; }
      td { color: var(--text); }
      tr:nth-child(even) td { background: var(--zebra); }
    </style>
    </head>
    <body>
    \(body)
    </body>
    </html>
    """
}

/// Full-page dark-shell HTML for a markdown source — used by ArtifactView
/// for .md artifacts (and anywhere else a markdown document should fill the
/// screen the same way it does in the browser UI).
func markdownHTML(_ md: String) -> String {
    documentShell(body: markdownToHTML(md))
}

/// Preformatted (non-markdown) text in the same dark shell — .log/.txt
/// artifacts.
func plainTextHTML(_ text: String) -> String {
    documentShell(body: "<pre>\(escapeHTML(text))</pre>")
}
