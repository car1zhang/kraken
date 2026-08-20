package core

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// lsp.go — the two pure seams the mental model's Zed integration needs: the
// LSP base protocol's framing, and "where in this buffer is the text carl
// marked".
//
// The integration itself is a language server (`kraken mm lsp`,
// cmd/kraken/mmlsp.go) because a Zed extension cannot draw a decoration: the
// one channel an extension has for painting something onto a line is
// diagnostics published by a language server. Everything about that server
// that can be a total function of strings lives HERE rather than there, for
// the reason the rest of this package exists — a protocol loop over stdio is
// integration-tested by hand at best, while framing arithmetic and a line
// locate are table rows.
//
// Nothing here knows what a diagnostic is, what kraken serve returns, or that
// Zed exists. It reads and writes frames, and it answers one question about a
// buffer.

// maxLSPPayload is the first Content-Length ReadLSPFrame refuses: 64 MiB.
//
// The comparison is `>=`, so the constant itself is the smallest refused
// size rather than the largest accepted one — 64 MiB is already three orders
// of magnitude past the largest frame this protocol carries (a didOpen of a
// big source file), so the boundary is not a value anything real sits on, and
// putting the refusal AT it rather than above it means the ceiling can never
// be the size that gets allocated.
//
// A ceiling is not a memory budget: it is what turns "the process dies" into
// "this frame is refused", which the header loop already knows how to report.
const maxLSPPayload = 64 << 20

// ReadLSPFrame reads one LSP base-protocol message off r: the header block,
// terminated by a blank line, then exactly the payload Content-Length names.
//
// The tolerances are the protocol's, not a convenience: header names are
// case-insensitive (`content-length` is what several clients send), and any
// header this does not recognise — `Content-Type`, a vendor `X-…` — is
// SKIPPED rather than refused, since the base protocol explicitly allows
// unknown fields and a server that died on one would die on a client upgrade.
//
// The two EOFs are different answers and the caller depends on the
// difference. A clean frame boundary — the stream ends where a header would
// have started — is io.EOF, which is how the read loop learns the client is
// gone and shuts down normally. A stream that ends PART WAY through a header
// block or a payload is a truncated frame, which returns an error that is not
// io.EOF: reporting that as a clean end would let a half-written message be
// silently swallowed as a normal shutdown.
//
// A missing or unparseable Content-Length is an error for the same reason
// there is no default: the payload's length is the only thing separating one
// message from the next, so guessing it would desynchronise the stream
// permanently rather than lose one message.
//
// An ABSURD Content-Length is refused before anything is allocated, which is
// a different failure from a short read and has to be caught earlier than one.
// `strconv.Atoi` is happy with a ten-digit number, and the allocation that
// followed would reserve the declared bytes — a client that sent
// `Content-Length: 9999999999` would take the process down with an OOM before
// the short-read check ever ran. See maxLSPPayload for the ceiling.
func ReadLSPFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	seen := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && !seen && line == "" {
				// The stream ended exactly where the next frame would have
				// begun: a clean close, not a truncated message.
				return nil, io.EOF
			}
			return nil, fmt.Errorf("lsp: truncated header block: %w", eofAsUnexpected(err))
		}
		seen = true
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			// A header line with no colon is malformed, but it is also not
			// ours to police — skipping keeps one odd client from wedging the
			// server, and the Content-Length check below is what actually
			// guards the stream.
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("lsp: bad Content-Length %q", strings.TrimSpace(value))
		}
		if n >= maxLSPPayload {
			return nil, fmt.Errorf("lsp: Content-Length %d is at or over the %d-byte ceiling", n, maxLSPPayload)
		}
		length = n
	}
	if length < 0 {
		return nil, fmt.Errorf("lsp: frame has no Content-Length header")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("lsp: short read of %d-byte payload: %w", length, eofAsUnexpected(err))
	}
	return payload, nil
}

// eofAsUnexpected turns a bare io.EOF into io.ErrUnexpectedEOF so a truncated
// frame can never be mistaken for the clean boundary above. io.ReadFull
// already does this when it read SOME bytes; it does not when it read none,
// which is exactly the "Content-Length: 40, payload absent" case.
func eofAsUnexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// WriteLSPFrame writes payload as one LSP base-protocol message.
//
// "One call" means ONE FUNCTION CALL emits the complete frame — the caller
// hands over a payload and never assembles a header of its own, which is the
// whole point: a header written at one call site and a body at another is how
// the two drift out of agreement about a length. Atomicity on the underlying
// writer is NOT what this promises, and nothing needs it to: the server
// serialises every outbound frame on a single goroutine, which is where stdio
// interleaving is actually prevented. Assembling into one buffer first is
// therefore a tidiness, not a guarantee.
func WriteLSPFrame(w io.Writer, payload []byte) error {
	var b strings.Builder
	b.Grow(len(payload) + 32)
	b.WriteString("Content-Length: ")
	b.WriteString(strconv.Itoa(len(payload)))
	b.WriteString("\r\n\r\n")
	b.Write(payload)
	_, err := io.WriteString(w, b.String())
	return err
}

// LocateBlock answers "which lines of this buffer hold the text carl marked",
// as a 1-based inclusive range.
//
// It is the in-editor counterpart of the verify pass' text ladder, run against
// an unsaved buffer instead of a git blob, and it shares that ladder's two
// PRIMITIVES rather than restating them — LocateText for the exact hit,
// StripWS for the whitespace-normalized one.
//
// The rungs built on them are deliberately NOT the same, and the relation is a
// SUPERSET rather than an equality: this arm is strictly the more permissive
// one. Its window is variable-length (see rung 2), where the verify pass'
// mmLocateWS pins the window to the anchor's own line count — so a REFLOWED
// block draws here and does not survive the nightly pass. That direction is
// the safe one and it is the direction on purpose: a mark the verify pass
// keeps is always a mark the editor can still draw, and the extra thing the
// editor draws is a block carl can check with his own eyes against the one
// he marked.
// The reverse would be the bad shape — a mark the pass believes in that the
// editor refuses to show.
//
//  1. EXACT. LocateText's FIRST occurrence. The end row comes from
//     EndRowFromSelection — the same arithmetic the capture side used to
//     derive the mark's end row from its own selected text, so a block
//     captured as N lines is drawn as N lines.
//
//     First-hit, not nearest-hit, because this signature carries no hint to
//     be near: the caller that HAS one — the diagnostics build, which holds
//     the mark's stored start line — resolves a multi-occurrence text itself
//     with core.NearestLine before it ever gets here (cmd/kraken/mmlsp.go's
//     locateMark). The production caller therefore never asks this arm to
//     pick between occurrences; first-hit is what a direct caller of the
//     exported seam gets, and it IS a decision this arm makes for them.
//
//  2. WHITESPACE-NORMALIZED LINE WINDOW. Some window of the buffer's lines
//     whose stripped content equals the mark's stripped content. This is the
//     rung that survives a reindent — the commonest edit to a block nobody
//     meant to change — and it is a line WINDOW rather than a free-floating
//     offset so the range it reports is a real span of the buffer rather than
//     a position in a string with the spaces taken out.
//
//     THE WINDOW IS VARIABLE-LENGTH, and that is the whole content of the
//     "endLine derives from the matched block's REAL line count in the
//     buffer" clause: if the window were pinned to the mark's own line count
//     the end row would just be startLine + N - 1 and the clause would say
//     nothing. Once whitespace stops counting, a mark and the block it names
//     need not agree on how many lines they take. `foo(a, b)` marked as one
//     line is the same block as a three-line `foo(\n\ta,\n\tb)` after a
//     formatter got to it — in either direction — and a blank line inside a
//     marked block strips to nothing at all, so a five-line mark can name a
//     three-line block. All of those must locate, and the range drawn has to
//     be the BUFFER's lines, since that is what the editor underlines.
//
//     WHERE SEVERAL WINDOWS SHARE A START, THE SHORTEST WINS. Blank lines
//     contribute zero stripped length, so a matching window followed by blank
//     lines is a whole family of matching windows; the contract does not
//     choose between them, so this does: the tightest one, which is the only
//     member of the family that draws no line the mark does not account for.
//     For the same reason a window is never STARTED on a blank line — such a
//     window always has a strictly tighter twin starting one line later.
//
// NOT FOUND IS THE ORDINARY CASE and it is not a verdict. A buffer carl is
// mid-edit in simply stops showing the mark until the text is back or the
// verify pass rules on it; nothing here ever invalidates anything.
//
// Both sides are `\r\n`-normalized. An empty text or an empty buffer is not
// ok: there is no such thing as locating nothing, and "matches everywhere"
// would paint every open file.
func LocateBlock(buffer, text string) (startLine, endLine int, ok bool) {
	buffer = normalizeEOL(buffer)
	text = normalizeEOL(text)
	if buffer == "" || text == "" {
		return 0, 0, false
	}

	if hits := LocateText(buffer, text); len(hits) > 0 {
		start := hits[0]
		return start, EndRowFromSelection(start, text), true
	}

	// The whitespace rung needs something left after the whitespace comes
	// out. An all-whitespace mark projects to nothing and would match the
	// first blank window in the file — the same "matches everywhere" failure
	// the empty check above exists for, wearing different bytes.
	wantWS := StripWS(text)
	if wantWS == "" {
		return 0, 0, false
	}

	// Each line stripped ONCE, with a running total, so a window's stripped
	// length is a subtraction rather than a re-strip. That is what keeps the
	// variable-length scan near-linear: the inner loop can only walk forward
	// while the running total is still short of the target, and the moment it
	// overshoots there is nothing further to try from that start, since the
	// totals never decrease.
	lines := splitLines(buffer)
	strip := make([]string, len(lines))
	cum := make([]int, len(lines)+1)
	for i, ln := range lines {
		strip[i] = StripWS(ln)
		cum[i+1] = cum[i] + len(strip[i])
	}
	for i := range lines {
		if strip[i] == "" {
			continue // never start a window on a blank line — see the header
		}
		// j is the window's EXCLUSIVE end, so the 1-based inclusive range is
		// [i+1, j]. The loop stops the moment the running total overshoots —
		// every longer window from this start is then too long as well.
		for j := i + 1; j <= len(lines) && cum[j]-cum[i] <= len(wantWS); j++ {
			if cum[j]-cum[i] == len(wantWS) && windowEquals(strip[i:j], wantWS) {
				return i + 1, j, true
			}
		}
	}
	return 0, 0, false
}

// windowEquals reports whether the stripped lines, concatenated, spell want.
// It walks want's prefixes rather than joining, so a window that fails on its
// first line costs that line rather than a whole allocation — and the equal
// LENGTH the caller has already established is what makes a prefix walk a
// complete test rather than half of one.
func windowEquals(lines []string, want string) bool {
	for _, ln := range lines {
		if !strings.HasPrefix(want, ln) {
			return false
		}
		want = want[len(ln):]
	}
	return want == ""
}

// splitLines splits already-normalized text into its lines, dropping the
// phantom empty element a trailing newline produces. A file ending in "\n"
// has N lines, not N+1, and every line number this file reports is an index
// into that count.
func splitLines(s string) []string {
	out := strings.Split(s, "\n")
	if n := len(out); n > 1 && out[n-1] == "" {
		out = out[:n-1]
	}
	return out
}
