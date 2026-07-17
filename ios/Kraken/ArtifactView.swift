import SwiftUI
import WebKit
import UIKit

// MARK: - WKWebView wrapper

/// What the web view is showing: a live page loaded by URL (html/svg — the
/// server already sends renderable markup), or a locally-built HTML string
/// (the markdown/plain-text path below).
private enum ArtifactContent: Equatable {
    case url(URL)
    case html(String)
}

private struct ArtifactWebView: UIViewRepresentable {
    let content: ArtifactContent

    final class Coordinator {
        var lastLoaded: ArtifactContent?
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let webView = WKWebView()
        // Only the container/loading background moves to the theme's ground
        // color — a loaded .html/.svg artifact's own content is left exactly
        // as the server renders it; the markdown/plain path builds its own
        // dark-themed shell (artifactShell below) instead.
        webView.isOpaque = false
        webView.backgroundColor = UIColor(KTheme.ground)
        webView.scrollView.backgroundColor = UIColor(KTheme.ground)
        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        // SwiftUI calls updateUIView on unrelated state changes too; only
        // (re)load when the content actually changed, else every ambient
        // re-render would restart the page / re-inject the HTML string.
        guard context.coordinator.lastLoaded != content else { return }
        context.coordinator.lastLoaded = content
        switch content {
        case .url(let url):
            webView.load(URLRequest(url: url))
        case .html(let html):
            webView.loadHTMLString(html, baseURL: nil)
        }
    }
}

// MARK: - artifact view

/// Whether an artifact renders as a live page, preformatted text, or
/// markdown — chosen by filename extension, mirroring the web UI's isPage().
private enum ArtifactKind {
    case page      // .html/.htm/.svg — server already sends renderable markup
    case plain     // .log/.txt/etc — dark <pre> shell, no markdown transform
    case markdown  // everything else (.md, unknown) — full markdown render

    init(name: String) {
        let lower = name.lowercased()
        if lower.hasSuffix(".html") || lower.hasSuffix(".htm") || lower.hasSuffix(".svg") {
            self = .page
        } else if lower.hasSuffix(".log") || lower.hasSuffix(".txt") {
            self = .plain
        } else {
            self = .markdown
        }
    }
}

/// Artifact viewer reached from a job's Artifacts list. `path` is
/// webArtifact.url — already a full server-relative path like
/// "/api/artifact/<slug>/cell/<name>" — joined with `baseURL` here.
struct ArtifactView: View {
    let name: String
    let baseURL: String
    let path: String

    @State private var content: ArtifactContent?
    @State private var errorMessage: String?

    private var kind: ArtifactKind { ArtifactKind(name: name) }

    var body: some View {
        Group {
            if let content {
                ArtifactWebView(content: content)
                    .background(KTheme.ground)
            } else if let errorMessage {
                VStack(spacing: 16) {
                    Text(errorMessage)
                        .font(.voice(15))
                        .foregroundStyle(KTheme.counter)
                        .multilineTextAlignment(.center)
                    Button {
                        Task { await load() }
                    } label: {
                        Label("Retry", systemImage: "arrow.clockwise")
                    }
                    .buttonStyle(.borderedProminent)
                }
                .padding()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(KTheme.ground)
            } else {
                ProgressView()
                    .tint(KTheme.accent)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(KTheme.ground)
            }
        }
        .navigationTitle(name)
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func load() async {
        errorMessage = nil
        guard let url = URL(string: baseURL + path) else {
            errorMessage = "invalid artifact URL"
            return
        }
        switch kind {
        case .page:
            // Already renderable server-side (text/html or image/svg+xml);
            // no fetch needed, WKWebView loads it directly.
            content = .url(url)
        case .plain:
            do {
                let text = try await APIClient.shared.fetchArtifactText(baseURL: baseURL, path: path)
                content = .html(artifactShell(body: "<pre>\(escapeHTML(text))</pre>"))
            } catch {
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        case .markdown:
            do {
                let text = try await APIClient.shared.fetchArtifactText(baseURL: baseURL, path: path)
                content = .html(artifactShell(body: markdownToHTML(text)))
            } catch {
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

// MARK: - markdown → HTML, ported from web/app.js's esc()/inlineMD()/renderMD()
//
// This exists so .md (and other plain-text) artifacts read exactly like they
// do in the browser UI instead of dumping raw markdown in a WKWebView.
// Escaping happens FIRST inside inlineMarkdown, exactly like esc() does in
// app.js's inlineMD() — the inline-pattern regexes below only ever run over
// already-entity-escaped text. Matching that ordering precisely matters: do
// it the other way around (format, then escape) and `<`/`&` inside e.g. a
// backtick span would either double-escape or — worse — not escape at all,
// opening an HTML-injection gap the web UI doesn't have.

private func escapeHTML(_ s: String) -> String {
    var out = ""
    out.reserveCapacity(s.count)
    for c in s {
        switch c {
        case "&": out += "&amp;"
        case "<": out += "&lt;"
        case ">": out += "&gt;"
        case "\"": out += "&quot;"
        case "'": out += "&#39;"
        default: out.append(c)
        }
    }
    return out
}

private func regexReplaceAll(_ pattern: String, in text: String, template: String) -> String {
    guard let re = try? NSRegularExpression(pattern: pattern) else { return text }
    let range = NSRange(text.startIndex..., in: text)
    return re.stringByReplacingMatches(in: text, range: range, withTemplate: template)
}

/// Matches `pattern` against a single line and returns its capture groups
/// (empty string for any group that didn't participate), or nil if no match.
private func regexGroups(_ pattern: String, in line: String) -> [String]? {
    guard let re = try? NSRegularExpression(pattern: pattern) else { return nil }
    let range = NSRange(line.startIndex..., in: line)
    guard let m = re.firstMatch(in: line, range: range) else { return nil }
    var groups: [String] = []
    for i in 1..<m.numberOfRanges {
        if let r = Range(m.range(at: i), in: line) {
            groups.append(String(line[r]))
        } else {
            groups.append("")
        }
    }
    return groups
}

/// Ports app.js's inlineMD(): backtick code, **bold**, *italic*, then
/// [text](https://url) links — in that exact order, over already-escaped text.
private func inlineMarkdown(_ s: String) -> String {
    var out = escapeHTML(s)
    out = regexReplaceAll(#"`([^`]+)`"#, in: out, template: "<code>$1</code>")
    out = regexReplaceAll(#"\*\*([^*]+)\*\*"#, in: out, template: "<strong>$1</strong>")
    out = regexReplaceAll(#"\*([^*]+)\*"#, in: out, template: "<em>$1</em>")
    out = regexReplaceAll(#"\[([^\]]+)\]\((https?:[^)\s]+)\)"#, in: out,
                           template: #"<a href="$2" target="_blank" rel="noreferrer">$1</a>"#)
    return out
}

/// Ports app.js's renderMD(): a small block-level pass over headings (#-####),
/// fenced code blocks, unordered/ordered lists, blockquotes, paragraphs, and
/// blank-line breaks. Line-oriented, single forward pass, exactly like the
/// original.
private func markdownToHTML(_ src: String) -> String {
    var out: [String] = []
    let lines = src.components(separatedBy: "\n")
    var i = 0
    var openList: String?

    func closeList() {
        if let l = openList {
            out.append("</\(l)>")
            openList = nil
        }
    }

    while i < lines.count {
        let line = lines[i]

        if line.hasPrefix("```") {
            closeList()
            var buf: [String] = []
            i += 1
            while i < lines.count && !lines[i].hasPrefix("```") {
                buf.append(lines[i])
                i += 1
            }
            i += 1 // skip the closing fence (or run off the end if unterminated)
            out.append("<pre><code>\(escapeHTML(buf.joined(separator: "\n")))</code></pre>")
            continue
        }

        if let g = regexGroups(#"^(#{1,4})\s+(.*)$"#, in: line) {
            closeList()
            let level = g[0].count
            out.append("<h\(level)>\(inlineMarkdown(g[1]))</h\(level)>")
            i += 1
            continue
        }

        if let g = regexGroups(#"^\s*[-*]\s+(.*)$"#, in: line) {
            if openList != "ul" { closeList(); out.append("<ul>"); openList = "ul" }
            out.append("<li>\(inlineMarkdown(g[0]))</li>")
            i += 1
            continue
        }
        if let g = regexGroups(#"^\s*\d+[.)]\s+(.*)$"#, in: line) {
            if openList != "ol" { closeList(); out.append("<ol>"); openList = "ol" }
            out.append("<li>\(inlineMarkdown(g[0]))</li>")
            i += 1
            continue
        }

        if line.hasPrefix(">") {
            closeList()
            let text = String(line.dropFirst()).trimmingCharacters(in: .whitespaces)
            out.append("<blockquote>\(inlineMarkdown(text))</blockquote>")
            i += 1
            continue
        }

        if line.trimmingCharacters(in: .whitespaces).isEmpty {
            closeList()
            i += 1
            continue
        }

        closeList()
        out.append("<p>\(inlineMarkdown(line))</p>")
        i += 1
    }
    closeList()
    return out.joined(separator: "\n")
}

/// Wraps rendered content in a dark shell matching web/style.css's .md rules
/// (ground bg, accent h1, hairline-underlined h2, raised-bg inline code,
/// bordered pre blocks, accent links) plus a viewport meta tag and a
/// touch-friendly base font size — the same reading experience as the web UI.
private func artifactShell(body: String) -> String {
    """
    <!doctype html>
    <html>
    <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
      :root { color-scheme: dark; }
      * { box-sizing: border-box; -webkit-text-size-adjust: 100%; }
      body {
        margin: 0; padding: 16px;
        background: #0A0E14; color: #D7E2EC;
        font: 15px/1.6 -apple-system, "Avenir Next", "Helvetica Neue", sans-serif;
      }
      h1, h2, h3, h4 { margin: 1.1em 0 0.4em; font-weight: 600; }
      h1 { font-size: 20px; color: #9E7BFF; }
      h2 { font-size: 17px; border-bottom: 1px solid #1E2836; padding-bottom: 3px; }
      h3 { font-size: 16px; }
      h4 { font-size: 15px; }
      p, ul, ol { margin: 0.55em 0; }
      ul, ol { padding-left: 20px; }
      li { margin: 0.15em 0; }
      code { font: 13px "SF Mono", Menlo, monospace; background: #151C28; color: #F5B84D; padding: 1px 4px; }
      pre {
        background: #0A0E14; border: 1px solid #1E2836; padding: 10px 12px;
        overflow-x: auto; margin: 0.7em 0; white-space: pre-wrap; word-break: break-word;
      }
      pre code { background: none; padding: 0; color: #D7E2EC; }
      a { color: #9E7BFF; }
      strong { color: #D7E2EC; font-weight: 600; }
      blockquote { border-left: 2px solid #2A3A50; padding-left: 10px; color: #6B7A8C; margin: 0.55em 0; }
    </style>
    </head>
    <body>
    \(body)
    </body>
    </html>
    """
}
