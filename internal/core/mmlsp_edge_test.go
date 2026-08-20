package core

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// mmlsp_edge_test.go — the EDGE dimension of 50-contract.md slice S6's three
// frozen core seams: ReadLSPFrame, WriteLSPFrame, LocateBlock. Written blind
// from the contract text alone; internal/core/lsp.go was never opened, and
// neither was anything under cmd/kraken.
//
// The framing half is where an LSP server dies quietly rather than loudly. A
// caller loop reads frames until io.EOF, so the identity of the error matters
// as much as its presence: a truncated frame that surfaces as io.EOF reads as
// "the editor closed the pipe" and the server exits mid-session with nothing
// logged. That is why the cases below split the two — a CLEAN boundary (no
// bytes at all) must be io.EOF, and a frame that began and did not finish must
// not be.
//
// The LocateBlock half is about line ARITHMETIC, not about matching. The
// diagnostic range a mark publishes is drawn from these two integers, so an
// endLine taken from the marked TEXT's own line count rather than from the
// buffer block it matched underlines the wrong region of carl's editor — and
// under the whitespace arm those two numbers are not the same number. The
// contract says "endLine derives from the matched block's real line count in
// the buffer"; three cases below hold exactly that sentence.
//
// Every package-level identifier is prefixed `edgeMMLSP`: `contractMM`,
// `edgeMM`, `contractMMLSP` and the `edgeAnchor`/`edgeZed` families are
// already taken in this package's test namespace.

// --- fixtures --------------------------------------------------------------

// edgeMMLSPReader wraps a raw wire string in the *bufio.Reader the seam takes.
func edgeMMLSPReader(s string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(s))
}

// edgeMMLSPFrame builds one well-formed CRLF frame around a payload. It is
// deliberately spelled out here rather than obtained from WriteLSPFrame: a
// reader test whose fixtures come from the writer under test can only prove
// the two agree with each other.
func edgeMMLSPFrame(payload string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
}

// edgeMMLSPWantErr fails unless the read refused. It prints the payload
// because the failure worth catching is a refusal that silently became a
// SHORT success — a partial payload handed up with a nil error.
func edgeMMLSPWantErr(t *testing.T, what string, payload []byte, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: ReadLSPFrame returned payload %q with a nil error — the contract's \"-> error\" rung", what, payload)
	}
}

// edgeMMLSPWantPayload reads one frame and demands exactly these bytes.
func edgeMMLSPWantPayload(t *testing.T, what string, r *bufio.Reader, want string) {
	t.Helper()
	got, err := ReadLSPFrame(r)
	if err != nil {
		t.Fatalf("%s: ReadLSPFrame: %v", what, err)
	}
	if string(got) != want {
		t.Fatalf("%s: payload = %q, want %q", what, got, want)
	}
}

// edgeMMLSPCountWriter records how many Write calls reach it, so the
// contract's "one call" clause is measurable.
type edgeMMLSPCountWriter struct {
	buf    bytes.Buffer
	writes int
}

func (w *edgeMMLSPCountWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.buf.Write(p)
}

// edgeMMLSPErrWriter is a stdout that has gone away mid-session.
type edgeMMLSPErrWriter struct{ err error }

func (w edgeMMLSPErrWriter) Write(p []byte) (int, error) { return 0, w.err }

// edgeMMLSPWantBlock demands a located block at exactly these lines.
func edgeMMLSPWantBlock(t *testing.T, what, buffer, text string, wantStart, wantEnd int) {
	t.Helper()
	start, end, ok := LocateBlock(buffer, text)
	if !ok {
		t.Fatalf("%s: LocateBlock(%q, %q) not ok — want lines %d-%d", what, buffer, text, wantStart, wantEnd)
	}
	if start != wantStart || end != wantEnd {
		t.Fatalf("%s: LocateBlock(%q, %q) = (%d, %d), want (%d, %d)", what, buffer, text, start, end, wantStart, wantEnd)
	}
}

// edgeMMLSPWantNoBlock demands a refusal.
func edgeMMLSPWantNoBlock(t *testing.T, what, buffer, text string) {
	t.Helper()
	if start, end, ok := LocateBlock(buffer, text); ok {
		t.Fatalf("%s: LocateBlock(%q, %q) located lines %d-%d — want not ok", what, buffer, text, start, end)
	}
}

// --- ReadLSPFrame: the boundary between "done" and "broken" -----------------

// TestEdgeMMLSPReadFrameCleanEOFIsIOEOF: the contract's one named error
// identity. A reader with no bytes left is the editor having closed stdin
// between frames, and the server loop terminates on exactly this value.
func TestEdgeMMLSPReadFrameCleanEOFIsIOEOF(t *testing.T) {
	payload, err := ReadLSPFrame(edgeMMLSPReader(""))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("empty reader: err = %v, want io.EOF", err)
	}
	if len(payload) != 0 {
		t.Errorf("empty reader: payload = %q, want none", payload)
	}
}

// TestEdgeMMLSPReadFrameCleanEOFAfterAWholeFrame: the same boundary reached
// the way it is actually reached in service — after a frame was consumed
// whole. The first read must not leave a stray byte behind that turns the
// second into a header parse error instead of a clean shutdown.
func TestEdgeMMLSPReadFrameCleanEOFAfterAWholeFrame(t *testing.T) {
	r := edgeMMLSPReader(edgeMMLSPFrame(`{"jsonrpc":"2.0"}`))
	edgeMMLSPWantPayload(t, "first frame", r, `{"jsonrpc":"2.0"}`)
	if _, err := ReadLSPFrame(r); !errors.Is(err, io.EOF) {
		t.Fatalf("after a whole frame: err = %v, want io.EOF", err)
	}
}

// TestEdgeMMLSPReadFrameMissingContentLength: a complete, well-formed header
// block that simply never names a length. No EOF is involved — the payload
// bytes are sitting right there — so this is a protocol error and must not
// arrive as the value that means "clean shutdown".
func TestEdgeMMLSPReadFrameMissingContentLength(t *testing.T) {
	const wire = "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n{}"
	payload, err := ReadLSPFrame(edgeMMLSPReader(wire))
	edgeMMLSPWantErr(t, "no Content-Length", payload, err)
	if errors.Is(err, io.EOF) {
		t.Errorf("no Content-Length: err = io.EOF — a header block with bytes behind it is not a clean frame boundary")
	}
}

// TestEdgeMMLSPReadFrameUnparseableContentLength: the contract's
// "unparseable" rung, spelled with a word.
func TestEdgeMMLSPReadFrameUnparseableContentLength(t *testing.T) {
	payload, err := ReadLSPFrame(edgeMMLSPReader("Content-Length: twelve\r\n\r\nhello world!"))
	edgeMMLSPWantErr(t, "word length", payload, err)
}

// TestEdgeMMLSPReadFrameNegativeContentLength: CONTRACT SILENCE, pinned. A
// negative length PARSES, so "unparseable" does not reach it by its own
// words — but "returns exactly that many payload bytes" is unsatisfiable for
// -1, and the strict reading is that an unsatisfiable frame is refused rather
// than silently treated as zero. The failure this forbids is a server that
// answers a hostile length by desynchronising its own stream.
func TestEdgeMMLSPReadFrameNegativeContentLength(t *testing.T) {
	payload, err := ReadLSPFrame(edgeMMLSPReader("Content-Length: -1\r\n\r\nhello"))
	edgeMMLSPWantErr(t, "negative length", payload, err)
}

// TestEdgeMMLSPReadFrameZeroLengthPayload: CONTRACT SILENCE, pinned the other
// way — 0 IS satisfiable, so a zero-length frame is a success carrying no
// bytes, not an error. The second half is the part that matters in service:
// the reader must be left positioned at the NEXT frame's first header byte,
// since a zero-length frame consumed as if it had a body eats the frame after
// it.
func TestEdgeMMLSPReadFrameZeroLengthPayload(t *testing.T) {
	r := edgeMMLSPReader("Content-Length: 0\r\n\r\n" + edgeMMLSPFrame("after"))
	got, err := ReadLSPFrame(r)
	if err != nil {
		t.Fatalf("zero-length frame: err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("zero-length frame: payload = %q, want no bytes", got)
	}
	edgeMMLSPWantPayload(t, "frame after a zero-length one", r, "after")
}

// TestEdgeMMLSPReadFrameShortPayloadIsAnError: the declared length overshoots
// what is on the wire. Some bytes arrived, so the stream is torn mid-frame —
// the one case where returning io.EOF would make the server exit on a
// corrupted message as though the session had ended normally.
func TestEdgeMMLSPReadFrameShortPayloadIsAnError(t *testing.T) {
	payload, err := ReadLSPFrame(edgeMMLSPReader("Content-Length: 64\r\n\r\nshort"))
	edgeMMLSPWantErr(t, "declared 64, sent 5", payload, err)
	if errors.Is(err, io.EOF) {
		t.Errorf("declared 64, sent 5: err = io.EOF — a partially-read frame is not a clean boundary")
	}
	if len(payload) != 0 && len(payload) < 64 {
		t.Errorf("declared 64, sent 5: a short payload %q was handed up alongside the error — the contract returns exactly N bytes or nothing", payload)
	}
}

// TestEdgeMMLSPReadFrameOversizedDeclaredLength: the same tear at a size no
// buffer should be allocated for outright. The assertion is only that it
// refuses — a reader that tries to reserve 64 MB for five bytes fails this by
// never returning.
func TestEdgeMMLSPReadFrameOversizedDeclaredLength(t *testing.T) {
	payload, err := ReadLSPFrame(edgeMMLSPReader("Content-Length: 67108864\r\n\r\nfive!"))
	edgeMMLSPWantErr(t, "declared 64MB, sent 5", payload, err)
}

// TestEdgeMMLSPReadFrameNoPayloadBytesAtAll: headers complete, blank line
// consumed, then nothing. CONTRACT SILENCE on the error's identity here (a
// bare io.ReadFull of a positive count over an exhausted reader reports
// io.EOF, not io.ErrUnexpectedEOF), so only the refusal itself is pinned.
func TestEdgeMMLSPReadFrameNoPayloadBytesAtAll(t *testing.T) {
	payload, err := ReadLSPFrame(edgeMMLSPReader("Content-Length: 8\r\n\r\n"))
	edgeMMLSPWantErr(t, "declared 8, sent none", payload, err)
}

// TestEdgeMMLSPReadFrameEOFMidHeaders: the stream dies inside the header line
// itself, with no terminator. Refusal only — see the note above on identity.
func TestEdgeMMLSPReadFrameEOFMidHeaders(t *testing.T) {
	payload, err := ReadLSPFrame(edgeMMLSPReader("Content-Length: 5"))
	edgeMMLSPWantErr(t, "truncated header line", payload, err)
}

// TestEdgeMMLSPReadFrameEOFBeforeTheBlankLine: a complete header line, then
// EOF where the blank line separating headers from payload should be. The
// length is known and parseable, which is exactly the trap: a reader that
// commits to the length before it has seen the blank line reads the next
// frame's headers as this frame's body.
func TestEdgeMMLSPReadFrameEOFBeforeTheBlankLine(t *testing.T) {
	payload, err := ReadLSPFrame(edgeMMLSPReader("Content-Length: 5\r\n"))
	edgeMMLSPWantErr(t, "no blank line", payload, err)
}

// TestEdgeMMLSPReadFrameHeaderWhitespaceAndCaseTolerated: the contract's
// case-insensitivity rung crossed with the whitespace the value may be padded
// with. All three spellings name the same seven bytes.
func TestEdgeMMLSPReadFrameHeaderWhitespaceAndCaseTolerated(t *testing.T) {
	cases := []struct{ what, header string }{
		{"padded value", "Content-Length:   7   "},
		{"no space after the colon", "Content-Length:7"},
		{"mixed case with padding", "cOnTeNt-LeNgTh:  7"},
	}
	for _, c := range cases {
		r := edgeMMLSPReader(c.header + "\r\n\r\npayload")
		edgeMMLSPWantPayload(t, c.what, r, "payload")
	}
}

// TestEdgeMMLSPReadFrameBareLFHeaderEndingsTolerated: CONTRACT SILENCE,
// pinned. The base protocol writes \r\n, and the WRITE side of this contract
// is frozen to emit exactly that — but nothing in the READ side's obligations
// says a bare \n is refused, and every other framing rung it does state is a
// tolerance rung (unknown headers, header case). The strict reading taken
// here is Postel's: emit \r\n, accept \n. A hand-typed frame in a scripted
// stdio session is the case this exists for.
func TestEdgeMMLSPReadFrameBareLFHeaderEndingsTolerated(t *testing.T) {
	edgeMMLSPWantPayload(t, "bare LF headers", edgeMMLSPReader("Content-Length: 2\n\nhi"), "hi")
}

// TestEdgeMMLSPReadFrameIgnoresOtherHeadersCarryingNumbers: the tolerance
// rung with teeth on it. Content-Type's value carries a colon and a charset,
// and the unknown header carries a number — a reader that splits on the first
// colon of any line, or that greps the block for digits, takes 99.
func TestEdgeMMLSPReadFrameIgnoresOtherHeadersCarryingNumbers(t *testing.T) {
	const wire = "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n" +
		"X-Kraken-Length: 99\r\n" +
		"Content-Length: 2\r\n" +
		"\r\nhi"
	edgeMMLSPWantPayload(t, "decoy headers", edgeMMLSPReader(wire), "hi")
}

// TestEdgeMMLSPReadFramePayloadIsOpaqueBytes: the payload carries an embedded
// blank-line sequence and multi-byte runes. Content-Length is a BYTE count,
// and the payload is never re-scanned for headers — a reader that looks for
// \r\n\r\n inside the body truncates this message at the escape.
func TestEdgeMMLSPReadFramePayloadIsOpaqueBytes(t *testing.T) {
	payload := "{\"m\":\"a\r\n\r\nContent-Length: 3\",\"u\":\"héllo ✓\"}"
	edgeMMLSPWantPayload(t, "opaque payload", edgeMMLSPReader(edgeMMLSPFrame(payload)), payload)
}

// TestEdgeMMLSPReadFrameLeavesTheNextFrameIntact: back-to-back frames with no
// separator between them, which is what the wire actually looks like. An
// off-by-one in the payload read shows up here and nowhere else.
func TestEdgeMMLSPReadFrameLeavesTheNextFrameIntact(t *testing.T) {
	r := edgeMMLSPReader(edgeMMLSPFrame("one") + edgeMMLSPFrame(`{"id":2}`))
	edgeMMLSPWantPayload(t, "frame 1", r, "one")
	edgeMMLSPWantPayload(t, "frame 2", r, `{"id":2}`)
	if _, err := ReadLSPFrame(r); !errors.Is(err, io.EOF) {
		t.Fatalf("after two frames: err = %v, want io.EOF", err)
	}
}

// --- WriteLSPFrame ---------------------------------------------------------

// TestEdgeMMLSPWriteFrameEmptyPayload: the degenerate write. Both the nil and
// the empty slice are the same zero bytes, and the header must still be
// emitted — a writer that skips the frame entirely for an empty body leaves
// the peer waiting.
func TestEdgeMMLSPWriteFrameEmptyPayload(t *testing.T) {
	const want = "Content-Length: 0\r\n\r\n"
	for _, c := range []struct {
		what    string
		payload []byte
	}{
		{"nil payload", nil},
		{"empty slice", []byte{}},
	} {
		var buf bytes.Buffer
		if err := WriteLSPFrame(&buf, c.payload); err != nil {
			t.Fatalf("%s: WriteLSPFrame: %v", c.what, err)
		}
		if buf.String() != want {
			t.Errorf("%s: wrote %q, want %q", c.what, buf.String(), want)
		}
	}
}

// TestEdgeMMLSPWriteFrameCountsBytesNotRunes: "héllo ✓" is seven runes and
// ten bytes. A length written from the rune count under-declares the frame and
// desynchronises every message after it.
func TestEdgeMMLSPWriteFrameCountsBytesNotRunes(t *testing.T) {
	payload := []byte("héllo ✓")
	if len(payload) != 10 || len([]rune(string(payload))) != 7 {
		t.Fatalf("fixture drift: %d bytes / %d runes, want 10 / 7", len(payload), len([]rune(string(payload))))
	}
	var buf bytes.Buffer
	if err := WriteLSPFrame(&buf, payload); err != nil {
		t.Fatalf("WriteLSPFrame: %v", err)
	}
	if want := "Content-Length: 10\r\n\r\nhéllo ✓"; buf.String() != want {
		t.Fatalf("wrote %q, want %q", buf.String(), want)
	}
	// and it reads back as the same ten bytes, nothing left over
	r := edgeMMLSPReader(buf.String())
	edgeMMLSPWantPayload(t, "multi-byte round trip", r, "héllo ✓")
	if _, err := ReadLSPFrame(r); !errors.Is(err, io.EOF) {
		t.Fatalf("after the round trip: err = %v, want io.EOF", err)
	}
}

// TestEdgeMMLSPWriteFrameEmitsTheWholeFrameInOneCall: the row's "one call"
// clause, as AMENDED AT THE JOIN — ONE call to WriteLSPFrame emits the whole
// frame, and writer-level atomicity is explicitly NOT required (the server
// serializes frames on one goroutine, so nothing can interleave between a
// header and its body). The assertion is therefore on the bytes the writer
// received in total, never on how many Write calls delivered them; this file's
// earlier count assertion was arbitrated a TEST BUG and is gone.
func TestEdgeMMLSPWriteFrameEmitsTheWholeFrameInOneCall(t *testing.T) {
	w := &edgeMMLSPCountWriter{}
	if err := WriteLSPFrame(w, []byte(`{"id":1}`)); err != nil {
		t.Fatalf("WriteLSPFrame: %v", err)
	}
	if want := "Content-Length: 8\r\n\r\n{\"id\":1}"; w.buf.String() != want {
		t.Fatalf("wrote %q, want %q", w.buf.String(), want)
	}
	if w.writes == 0 {
		t.Errorf("WriteLSPFrame made no Write call at all")
	}
}

// TestEdgeMMLSPWriteFramePropagatesWriterError: stdout closed under the
// server. The one thing a framing writer must never do is report success on a
// pipe that took nothing.
func TestEdgeMMLSPWriteFramePropagatesWriterError(t *testing.T) {
	boom := errors.New("edgeMMLSP: pipe closed")
	err := WriteLSPFrame(edgeMMLSPErrWriter{err: boom}, []byte(`{"id":1}`))
	if err == nil {
		t.Fatalf("WriteLSPFrame over a failing writer returned nil")
	}
	// the empty payload takes the same path — the header alone still fails
	if err := WriteLSPFrame(edgeMMLSPErrWriter{err: boom}, nil); err == nil {
		t.Fatalf("WriteLSPFrame(nil payload) over a failing writer returned nil")
	}
}

// --- LocateBlock: absence ---------------------------------------------------

// TestEdgeMMLSPLocateBlockEmptyTextOrBuffer: the contract's explicit refusal.
// CONTRACT SILENCE on what the two ints are when ok is false; pinned to the
// zero values, since a diagnostic built from a refused locate must not land on
// line 1 by accident.
func TestEdgeMMLSPLocateBlockEmptyTextOrBuffer(t *testing.T) {
	cases := []struct{ what, buffer, text string }{
		{"empty text", "alpha\nbeta\n", ""},
		{"empty buffer", "", "alpha"},
		{"both empty", "", ""},
	}
	for _, c := range cases {
		start, end, ok := LocateBlock(c.buffer, c.text)
		if ok {
			t.Fatalf("%s: LocateBlock located lines %d-%d — want not ok", c.what, start, end)
		}
		if start != 0 || end != 0 {
			t.Errorf("%s: LocateBlock = (%d, %d) with ok=false, want (0, 0)", c.what, start, end)
		}
	}
}

// TestEdgeMMLSPLocateBlockTextAbsent: the ordinary miss — carl's marked lines
// are simply not in the buffer he has open. No diagnostic, no invalidation.
func TestEdgeMMLSPLocateBlockTextAbsent(t *testing.T) {
	edgeMMLSPWantNoBlock(t, "absent", "alpha\nbeta\n", "gamma")
}

// TestEdgeMMLSPLocateBlockTextLongerThanBuffer: more lines marked than the
// buffer holds. The whitespace arm is the risk here: a window walk that lets
// its end index run past the last line finds a match against a short buffer
// that does not contain one.
func TestEdgeMMLSPLocateBlockTextLongerThanBuffer(t *testing.T) {
	edgeMMLSPWantNoBlock(t, "text longer than buffer", "alpha\n", "alpha\nbeta\ngamma\n")
	edgeMMLSPWantNoBlock(t, "text longer, whitespace-differing", "  alpha\n", "alpha\n  beta\n")
}

// TestEdgeMMLSPLocateBlockRealContentDifferenceNeverMatches: the whitespace
// arm normalizes SPACING, not identifiers. Foo is not Bar, and a mark that
// matched here would underline the wrong function forever.
func TestEdgeMMLSPLocateBlockRealContentDifferenceNeverMatches(t *testing.T) {
	buffer := "func Foo(a int) {\n\treturn a\n}\n"
	edgeMMLSPWantNoBlock(t, "renamed symbol", buffer, "func Bar(a int) {")
	edgeMMLSPWantNoBlock(t, "changed body", buffer, "func Foo(a int) {\n\treturn a + 1\n}")
}

// TestEdgeMMLSPLocateBlockWSArmIsEqualityNotSubstring: the fallback arm is
// stated as an EQUALITY over a line window ("the StripWS equality shape"). A
// fallback implemented as a substring test over the stripped buffer matches
// "phabe" against "alpha beta" and reports a block that shares no token with
// the mark.
func TestEdgeMMLSPLocateBlockWSArmIsEqualityNotSubstring(t *testing.T) {
	edgeMMLSPWantNoBlock(t, "stripped substring, not a window", "alpha beta\ngamma\n", "phabe")
	edgeMMLSPWantNoBlock(t, "stripped substring spanning two lines", "alpha beta\ngamma\n", "betagam")
}

// --- LocateBlock: line arithmetic ------------------------------------------

// TestEdgeMMLSPLocateBlockAtBufferStart: the first line is line 1, not line 0.
func TestEdgeMMLSPLocateBlockAtBufferStart(t *testing.T) {
	edgeMMLSPWantBlock(t, "first line", "alpha\nbeta\ngamma\n", "alpha", 1, 1)
	edgeMMLSPWantBlock(t, "first two lines", "alpha\nbeta\ngamma\n", "alpha\nbeta", 1, 2)
}

// TestEdgeMMLSPLocateBlockAtBufferEndWithoutTrailingNewline: an unsaved buffer
// whose last line has no terminator. A line count taken by counting '\n'
// characters is one short here, and the last line becomes unreachable.
func TestEdgeMMLSPLocateBlockAtBufferEndWithoutTrailingNewline(t *testing.T) {
	edgeMMLSPWantBlock(t, "last line, no trailing newline", "alpha\nbeta\ngamma", "gamma", 3, 3)
	edgeMMLSPWantBlock(t, "last two lines, no trailing newline", "alpha\nbeta\ngamma", "beta\ngamma", 2, 3)
}

// TestEdgeMMLSPLocateBlockFirstOfTwoOccurrences: "first occurrence" — the
// LocateText first-hit rule, restated at this seam. A mark that jumps to the
// last duplicate moves under carl as he edits above it.
func TestEdgeMMLSPLocateBlockFirstOfTwoOccurrences(t *testing.T) {
	edgeMMLSPWantBlock(t, "duplicate line", "dup\nmid\ndup\n", "dup", 1, 1)
	edgeMMLSPWantBlock(t, "duplicate block", "a\nb\nmid\na\nb\n", "a\nb", 1, 2)
}

// TestEdgeMMLSPLocateBlockSingleCharacterText: the smallest non-empty mark.
// The fixture places the character exactly once so the first-hit rule and the
// line arithmetic can be read apart from each other.
func TestEdgeMMLSPLocateBlockSingleCharacterText(t *testing.T) {
	edgeMMLSPWantBlock(t, "one character", "alpha\nQ\nbeta\n", "Q", 2, 2)
}

// TestEdgeMMLSPLocateBlockMidLineStartCountsItsOwnLine: a selection that
// began mid-line, which is exactly what a Zed selection usually is. Per the
// LocateText rule the contract points at, the reported line is the one holding
// the match's FIRST byte — line 1 here, not line 2.
func TestEdgeMMLSPLocateBlockMidLineStartCountsItsOwnLine(t *testing.T) {
	edgeMMLSPWantBlock(t, "mid-line start", "xx foo\nbar yy\n", "foo\nbar", 1, 2)
}

// TestEdgeMMLSPLocateBlockTrailingNewlineDoesNotAdvanceEndLine: CONTRACT
// SILENCE, pinned. A selection captured through a full-line grab carries its
// terminator, and the naive endLine = startLine + count('\n') then reports a
// block one line taller than the block it matched. The contract's own words
// decide it: endLine derives from the matched block's real line count IN THE
// BUFFER, and "alpha\n" is one whole line of this buffer — the terminator
// closes line 1, it does not open line 2.
func TestEdgeMMLSPLocateBlockTrailingNewlineDoesNotAdvanceEndLine(t *testing.T) {
	edgeMMLSPWantBlock(t, "one line with its terminator", "alpha\nbeta\ngamma\n", "alpha\n", 1, 1)
	edgeMMLSPWantBlock(t, "two lines with the terminator", "alpha\nbeta\ngamma\n", "alpha\nbeta\n", 1, 2)
}

// --- LocateBlock: \r\n normalization ---------------------------------------

// TestEdgeMMLSPLocateBlockCRLFBufferLFText: the buffer is CRLF on disk, the
// mark was stored LF-normalized by the route. Both sides normalize, so this
// locates — and the line numbers are counted on the normalized text, which is
// the same count either way.
func TestEdgeMMLSPLocateBlockCRLFBufferLFText(t *testing.T) {
	buffer := "alpha\r\nbeta\r\ngamma\r\n"
	edgeMMLSPWantBlock(t, "CRLF buffer, LF mark", buffer, "beta", 2, 2)
	edgeMMLSPWantBlock(t, "CRLF buffer, multi-line LF mark", buffer, "beta\ngamma", 2, 3)
}

// TestEdgeMMLSPLocateBlockLFBufferCRLFText: the other direction — a mark that
// arrived carrying CRLF against an LF buffer. Normalizing only the buffer
// passes the case above and fails this one.
func TestEdgeMMLSPLocateBlockLFBufferCRLFText(t *testing.T) {
	buffer := "alpha\nbeta\ngamma\n"
	edgeMMLSPWantBlock(t, "LF buffer, CRLF mark", buffer, "beta\r\ngamma", 2, 3)
}

// TestEdgeMMLSPLocateBlockCRLFOnBothSides: the pass-through case, included
// because a normalizer applied twice, or applied to only one arm, breaks here
// while both single-sided cases above still pass.
func TestEdgeMMLSPLocateBlockCRLFOnBothSides(t *testing.T) {
	edgeMMLSPWantBlock(t, "CRLF both sides", "alpha\r\nbeta\r\ngamma\r\n", "beta\r\ngamma", 2, 3)
}

// --- LocateBlock: the whitespace arm's line-count fidelity ------------------

// TestEdgeMMLSPLocateBlockExactArmBeatsAnEarlierWSMatch: the two arms are
// ordered — "exact contiguous first ... else a whitespace-normalized"
// window. Line 1 here is a whitespace-only variant of the mark and line 3 is
// the mark verbatim; an implementation that runs the tolerant arm first, or
// that merges the two into one pass, underlines line 1.
func TestEdgeMMLSPLocateBlockExactArmBeatsAnEarlierWSMatch(t *testing.T) {
	buffer := "  foo( a )\nmiddle\nfoo(a)\nend\n"
	edgeMMLSPWantBlock(t, "exact arm wins", buffer, "foo(a)", 3, 3)
}

// TestEdgeMMLSPLocateBlockWSArmSpansTheBuffersRealLineCount: the case the
// contract's endLine sentence exists for. The mark is ONE line as carl copied
// it; the buffer has since been reformatted across THREE. Under the whitespace
// arm they are equal, and the diagnostic must cover lines 2-4 — the block in
// the buffer — not line 2 alone, which is what an endLine taken from the
// mark's own line count produces.
func TestEdgeMMLSPLocateBlockWSArmSpansTheBuffersRealLineCount(t *testing.T) {
	buffer := "alpha\nfoo(\n\ta,\n\tb)\nomega\n"
	edgeMMLSPWantBlock(t, "1-line mark, 3-line block", buffer, "foo(a, b)", 2, 4)
}

// TestEdgeMMLSPLocateBlockWSArmTextWiderThanTheBufferBlock: the same
// divergence inverted — a three-line mark against a buffer that has since
// joined those lines into one. The block is one line, so start and end are the
// same line; an endLine of start+2 runs off the end of a short file.
func TestEdgeMMLSPLocateBlockWSArmTextWiderThanTheBufferBlock(t *testing.T) {
	buffer := "alpha\nfoo( a, b )\nomega\n"
	edgeMMLSPWantBlock(t, "3-line mark, 1-line block", buffer, "foo(\n\ta,\n\tb)", 2, 2)
}

// TestEdgeMMLSPLocateBlockWSArmAtTheBufferEdges: the tolerant arm at both
// boundaries — a reindented first line, and a reindented last line with no
// trailing newline. Window walks tend to be written for the interior.
func TestEdgeMMLSPLocateBlockWSArmAtTheBufferEdges(t *testing.T) {
	edgeMMLSPWantBlock(t, "ws match on line 1", "\t\tif x {\nbody\n}\n", "if x{", 1, 1)
	edgeMMLSPWantBlock(t, "ws match on the unterminated last line", "head\nbody\n\t}else{  ", "} else {", 3, 3)
}
