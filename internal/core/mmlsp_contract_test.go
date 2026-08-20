package core

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// mmlsp_contract_test.go — athena-zed-logic's BLIND contract tests for
// artifacts/50-contract.md, slice S6's three FROZEN CORE SEAMS:
// `core.ReadLSPFrame`, `core.WriteLSPFrame` and `core.LocateBlock`.
//
// Written from the frozen contract table alone. internal/core/lsp.go and the
// cmd/kraken files of this slice were never read by any route — no Read, no
// grep, no sed, no diff, no git show, no build output quoting them. Every
// assertion below transcribes a clause of the S6 contract ROW that names the
// symbol it exercises.
//
// SCOPE: the core seams only. The stdio protocol loop (`kraken mm lsp`), the
// `k mm drop` verb and the Zed extension are integration surfaces gated by the
// lead's scripted session and the manual gate, and nothing here touches them.
//
// Dimension: LOGIC — the stated invariants and the happy paths. Error shapes
// (missing or unparseable Content-Length, short reads, io.EOF at a clean frame
// boundary), absence (`ok == false`), and degenerate inputs (empty text, empty
// buffer) are athena-zed-edge's ground and are deliberately not duplicated
// here. Where a clause is stated as an absolute ("returns exactly that many
// payload bytes", "exact contiguous FIRST", "endLine derives from the matched
// block's REAL line count in the buffer") it is pinned here, because the happy
// path's meaning depends on it.
//
// Every package-level identifier declared here is prefixed `contractMMLSP`:
// internal/core's test namespace is shared with the edge author of this slice
// and with the S1 authors, whose prefixes `edgeMMLSP`, `contractMM`, `edgeMM`,
// `contractAnchor` and `edgeAnchor` are taken.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// contractMMLSPWire spells the contract's wire form ONCE — `Content-Length:
// N\r\n\r\n` followed by the payload, N counted in BYTES — so a fixture below
// reads as its payload rather than as a hand-counted header. The literal form
// itself is asserted separately, by hand, in
// TestContractMMLSPWriteFrameWireForm; this helper is never the only witness
// to it.
func contractMMLSPWire(payload string) string {
	return "Content-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n" + payload
}

// contractMMLSPReadOne reads one frame and fails the test on any error, so a
// happy-path case reads as its payload comparison alone.
func contractMMLSPReadOne(t *testing.T, r *bufio.Reader, what string) string {
	t.Helper()
	got, err := ReadLSPFrame(r)
	if err != nil {
		t.Fatalf("ReadLSPFrame(%s): unexpected error %v — this frame is well formed", what, err)
	}
	return string(got)
}

// contractMMLSPCRLF rewrites an LF fixture into its CRLF spelling. Every
// fixture constant in this file is authored with LF endings only, so this can
// never double an existing \r.
func contractMMLSPCRLF(s string) string {
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// contractMMLSPBlock asserts one LocateBlock happy path. `ok` false is fatal
// rather than an error: the line numbers of an unlocated block are meaningless
// and reporting two failures for one cause obscures which clause broke.
func contractMMLSPBlock(t *testing.T, what, buffer, text string, wantStart, wantEnd int) {
	t.Helper()
	start, end, ok := LocateBlock(buffer, text)
	if !ok {
		t.Fatalf("LocateBlock: %s — ok=false, want the block located at lines %d-%d\ntext:\n%s",
			what, wantStart, wantEnd, text)
	}
	if start != wantStart || end != wantEnd {
		t.Errorf("LocateBlock: %s — got lines %d-%d, want %d-%d\ntext:\n%s",
			what, start, end, wantStart, wantEnd, text)
	}
}

// ---------------------------------------------------------------------------
// core.WriteLSPFrame — the exact wire form
// ---------------------------------------------------------------------------

// TestContractMMLSPWriteFrameWireForm pins the emitted bytes LITERALLY. The
// contract row is a byte-level statement — "writes `Content-Length: N\r\n\r\n`
// + payload" — so the expectation is spelled out by hand rather than rebuilt
// from a helper: a test that constructed the header the same way the
// implementation does would pass whatever spelling either of them chose.
//
// The single space after the colon, the CRLF line ending, the blank CRLF that
// closes the header block, the ABSENCE of any other header (no Content-Type),
// and the absence of any trailing byte after the payload are all part of that
// literal.
func TestContractMMLSPWriteFrameWireForm(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "a two-byte payload",
			payload: "hi",
			want:    "Content-Length: 2\r\n\r\nhi",
		},
		{
			name:    "a JSON-RPC payload",
			payload: `{"jsonrpc":"2.0","id":1}`,
			want:    "Content-Length: 24\r\n\r\n" + `{"jsonrpc":"2.0","id":1}`,
		},
		{
			// N is a BYTE count. "héllo" is five runes and six bytes; a
			// rune-counted header desynchronises the stream on the first
			// non-ASCII diagnostic message, which the mark's own text
			// routinely is.
			name:    "a multi-byte payload counts bytes, not runes",
			payload: "héllo",
			want:    "Content-Length: 6\r\n\r\nhéllo",
		},
		{
			// The payload is opaque: a frame boundary inside it is data.
			name:    "a payload containing its own blank CRLF line",
			payload: "a\r\n\r\nb",
			want:    "Content-Length: 6\r\n\r\na\r\n\r\nb",
		},
		{
			name:    "a payload containing bare newlines",
			payload: "{\n  \"x\": 1\n}",
			want:    "Content-Length: 12\r\n\r\n{\n  \"x\": 1\n}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteLSPFrame(&buf, []byte(tc.payload)); err != nil {
				t.Fatalf("WriteLSPFrame: unexpected error %v", err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("WriteLSPFrame wrote %q, want %q", got, tc.want)
			}
		})
	}
}

// TestContractMMLSPWriteFrameEmitsTheWholeFrameItself is the "one call" clause
// read at the API level: ONE WriteLSPFrame call puts the header AND the payload
// on the writer, so a caller never appends the body itself. Asserted by
// consecutive frames — two calls produce two complete frames back to back with
// nothing between them, which is the property the stdio server depends on.
func TestContractMMLSPWriteFrameEmitsTheWholeFrameItself(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteLSPFrame(&buf, []byte("one")); err != nil {
		t.Fatalf("WriteLSPFrame(one): %v", err)
	}
	if err := WriteLSPFrame(&buf, []byte("two")); err != nil {
		t.Fatalf("WriteLSPFrame(two): %v", err)
	}
	want := "Content-Length: 3\r\n\r\none" + "Content-Length: 3\r\n\r\ntwo"
	if got := buf.String(); got != want {
		t.Errorf("two frames wrote %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// core.ReadLSPFrame — framing, header tolerance, exact payload bytes
// ---------------------------------------------------------------------------

// TestContractMMLSPFrameRoundTrip is the seam's central obligation: what
// WriteLSPFrame emits, ReadLSPFrame returns — BYTE FOR BYTE. The payloads are
// chosen so that a reader which scanned for a delimiter instead of counting
// bytes fails: one carries a blank CRLF line, one carries header-shaped text,
// one crosses bufio's default 4096-byte buffer so the count has to survive a
// refill, and one is non-ASCII so the count has to be in bytes.
func TestContractMMLSPFrameRoundTrip(t *testing.T) {
	payloads := []struct {
		name    string
		payload string
	}{
		{"a JSON-RPC request", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`},
		{"a payload containing a blank CRLF line", "before\r\n\r\nafter"},
		{"a payload that looks like a frame header", "Content-Length: 99\r\n\r\nnot a frame"},
		{"a payload with bare newlines", "line one\nline two\n"},
		{"a non-ASCII payload", "diagnostic — mm 12 · anchor origin-main"},
		{"a payload larger than bufio's default buffer", strings.Repeat("x", 5000)},
		{"a payload of one byte", "x"},
	}
	for _, p := range payloads {
		t.Run(p.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteLSPFrame(&buf, []byte(p.payload)); err != nil {
				t.Fatalf("WriteLSPFrame: %v", err)
			}
			got := contractMMLSPReadOne(t, bufio.NewReader(&buf), p.name)
			if got != p.payload {
				t.Errorf("round trip returned %d bytes %q, want %d bytes %q",
					len(got), got, len(p.payload), p.payload)
			}
		})
	}
}

// TestContractMMLSPReadFrameContentLengthIsCaseInsensitive walks the stated
// clause over the spellings a real client emits. The header NAME is matched
// case-insensitively; nothing else about the frame changes.
func TestContractMMLSPReadFrameContentLengthIsCaseInsensitive(t *testing.T) {
	const payload = `{"jsonrpc":"2.0","method":"initialized"}`
	names := []string{
		"Content-Length",
		"content-length",
		"CONTENT-LENGTH",
		"CoNtEnT-LeNgTh",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			wire := name + ": " + strconv.Itoa(len(payload)) + "\r\n\r\n" + payload
			got := contractMMLSPReadOne(t, bufio.NewReader(strings.NewReader(wire)), name)
			if got != payload {
				t.Errorf("header %q: payload %q, want %q", name, got, payload)
			}
		})
	}
}

// TestContractMMLSPReadFrameToleratesUnknownHeaders pins the second stated
// tolerance: headers other than Content-Length are read past and ignored,
// wherever in the block they sit. `Content-Type` is the one the base protocol
// actually defines and the one the contract names; an invented header is
// included because "unknown" is the stated rule, not "Content-Type".
func TestContractMMLSPReadFrameToleratesUnknownHeaders(t *testing.T) {
	const payload = `{"jsonrpc":"2.0","id":7,"result":null}`
	length := strconv.Itoa(len(payload))
	cases := []struct {
		name    string
		headers string
	}{
		{
			name:    "Content-Type before Content-Length",
			headers: "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\nContent-Length: " + length + "\r\n",
		},
		{
			name:    "Content-Type after Content-Length",
			headers: "Content-Length: " + length + "\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n",
		},
		{
			name:    "an unknown header the base protocol never defined",
			headers: "X-Kraken-Trace: 1\r\nContent-Length: " + length + "\r\n",
		},
		{
			name: "several unknown headers around the real one",
			headers: "X-Kraken-Trace: 1\r\n" +
				"Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n" +
				"Content-Length: " + length + "\r\n" +
				"X-Kraken-After: 2\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tc.headers + "\r\n" + payload))
			if got := contractMMLSPReadOne(t, r, tc.name); got != payload {
				t.Errorf("payload %q, want %q", got, payload)
			}
		})
	}
}

// TestContractMMLSPReadFrameConsumesExactlyOneFrame is "returns exactly that
// many payload bytes" read as a statement about the READER's position: after a
// frame, the next byte available is the next frame's first header byte —
// neither a byte of the previous payload left behind (under-read) nor a byte of
// the next frame eaten (over-read).
//
// The second payload is deliberately header-shaped text carrying its own
// `Content-Length: 99`: a reader that resynchronised by scanning for the next
// blank line rather than by trusting the count would return that payload's
// interior as frame three and be permanently out of step.
func TestContractMMLSPReadFrameConsumesExactlyOneFrame(t *testing.T) {
	payloads := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		"Content-Length: 99\r\n\r\nthis is payload text, not a frame",
		`{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics"}`,
	}
	var wire strings.Builder
	for _, p := range payloads {
		wire.WriteString(contractMMLSPWire(p))
	}
	r := bufio.NewReader(strings.NewReader(wire.String()))
	for i, want := range payloads {
		got := contractMMLSPReadOne(t, r, "frame "+strconv.Itoa(i+1))
		if got != want {
			t.Fatalf("frame %d: payload %q, want %q — the stream is out of step from here on",
				i+1, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// core.LocateBlock — fixtures
// ---------------------------------------------------------------------------

// contractMMLSPBuf is the exact-arm fixture, written one line per term so every
// expected line number below can be read off the constant:
//
//	1  package p
//	2  func a() {
//	3  \tx := 1
//	4  }
//	5  func b() {
//	6  \tx := 1
//	7  }
//
// Lines 3 and 6 are byte-identical, which is what makes "first occurrence" a
// testable claim rather than a coincidence.
const contractMMLSPBuf = "package p\n" +
	"func a() {\n" +
	"\tx := 1\n" +
	"}\n" +
	"func b() {\n" +
	"\tx := 1\n" +
	"}\n"

// contractMMLSPWSBuf is the whitespace-normalised arm's fixture — a block
// indented with TABS, which a mark captured from a differently-indented
// rendering (or an editor set to spaces) can only match after StripWS:
//
//	1  func main() {
//	2  \tif x {
//	3  \t\ty()
//	4  \t}
//	5  }
const contractMMLSPWSBuf = "func main() {\n" +
	"\tif x {\n" +
	"\t\ty()\n" +
	"\t}\n" +
	"}\n"

// contractMMLSPInlineBuf holds on ONE line what the marked text spells across
// three — the mirror image of contractMMLSPWSBuf, and the fixture that makes
// "endLine derives from the matched block's real line count in the BUFFER"
// distinguishable from "endLine = startLine + the text's own line count".
//
//	1  package p
//	2  if x { y() }
//	3  done
const contractMMLSPInlineBuf = "package p\n" +
	"if x { y() }\n" +
	"done\n"

// contractMMLSPArmOrderBuf carries the same block twice: lines 1-3 indented
// with tabs (whitespace-equal to the query, byte-different) and lines 4-6
// byte-identical to it. The exact arm's hit is LATER in the file than the
// whitespace arm's, so the contract's "exact contiguous first, ELSE
// whitespace-normalised" is the only rule that picks 4-6.
//
//	1  \tif x {
//	2  \t\ty()
//	3  \t}
//	4  if x {
//	5      y()
//	6  }
const contractMMLSPArmOrderBuf = "\tif x {\n" +
	"\t\ty()\n" +
	"\t}\n" +
	"if x {\n" +
	"    y()\n" +
	"}\n"

// contractMMLSPTwoWSBuf carries two windows that are whitespace-equal to the
// query and neither of which is a byte match — so the whitespace arm alone
// decides, and "first occurrence" is what picks 1-3 over 5-7.
//
//	1  \tif x {
//	2  \t\ty()
//	3  \t}
//	4  hello
//	5    if x {
//	6        y()
//	7    }
const contractMMLSPTwoWSBuf = "\tif x {\n" +
	"\t\ty()\n" +
	"\t}\n" +
	"hello\n" +
	"  if x {\n" +
	"      y()\n" +
	"  }\n"

// ---------------------------------------------------------------------------
// core.LocateBlock — the exact arm
// ---------------------------------------------------------------------------

// TestContractMMLSPLocateBlockExactArm walks the exact-hit arm over one shared
// buffer: a single line, a multi-line block, a block that starts at line 1, a
// block that ends on the last line, a block spanning a boundary between two
// logical chunks, and — the clause with teeth — a line that occurs twice,
// where the FIRST occurrence is the answer.
//
// The buffer is shared across rows deliberately: a rule that over-matches shows
// up as a wrong line number on a neighbouring row rather than as a fixture
// tailored to hide it.
func TestContractMMLSPLocateBlockExactArm(t *testing.T) {
	cases := []struct {
		name             string
		text             string
		wantStart, wantE int
	}{
		{
			name:      "the file's first line",
			text:      "package p",
			wantStart: 1, wantE: 1,
		},
		{
			name:      "a single interior line",
			text:      "func a() {",
			wantStart: 2, wantE: 2,
		},
		{
			// Lines 3 and 6 are identical. The contract says FIRST occurrence.
			name:      "a line that occurs twice — the first hit wins",
			text:      "\tx := 1",
			wantStart: 3, wantE: 3,
		},
		{
			name:      "a three-line block",
			text:      "func a() {\n\tx := 1\n}",
			wantStart: 2, wantE: 4,
		},
		{
			// Same three-line shape as the row above, distinguished only by its
			// first line: the second block is found at its own coordinates, not
			// dragged to the first block's.
			name:      "the second three-line block, ending on the last line",
			text:      "func b() {\n\tx := 1\n}",
			wantStart: 5, wantE: 7,
		},
		{
			name:      "a block spanning the file's first two lines",
			text:      "package p\nfunc a() {",
			wantStart: 1, wantE: 2,
		},
		{
			name:      "a two-line block straddling the two functions",
			text:      "}\nfunc b() {",
			wantStart: 4, wantE: 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contractMMLSPBlock(t, tc.name, contractMMLSPBuf, tc.text, tc.wantStart, tc.wantE)
		})
	}
}

// TestContractMMLSPLocateBlockLineNumbersAreOneBasedAndInclusive walks every
// line of a buffer as its own query, so the arithmetic is pinned across the
// whole range rather than at one sampled row: line i must come back as (i, i).
// A zero-based implementation passes no row; an off-by-one passes none either.
func TestContractMMLSPLocateBlockLineNumbersAreOneBasedAndInclusive(t *testing.T) {
	lines := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	buffer := strings.Join(lines, "\n") + "\n"
	for i, line := range lines {
		want := i + 1
		t.Run(line, func(t *testing.T) {
			contractMMLSPBlock(t, "line "+strconv.Itoa(want), buffer, line, want, want)
		})
	}
}

// TestContractMMLSPLocateBlockFindsTheLastLineWithoutATrailingNewline pins the
// happy path on a buffer that does not end in a newline — the shape a Zed
// buffer takes whenever the file has no final newline. The last line is a real
// line and carries the last line number.
func TestContractMMLSPLocateBlockFindsTheLastLineWithoutATrailingNewline(t *testing.T) {
	const buffer = "alpha\nbravo\ncharlie"
	contractMMLSPBlock(t, "the unterminated last line", buffer, "charlie", 3, 3)
	contractMMLSPBlock(t, "a block ending on the unterminated last line", buffer, "bravo\ncharlie", 2, 3)
}

// ---------------------------------------------------------------------------
// core.LocateBlock — CRLF normalisation on BOTH sides
// ---------------------------------------------------------------------------

// TestContractMMLSPLocateBlockNormalisesCRLFOnBothSides pins "both sides
// `\r\n`-normalised" as the four-way statement it is: either side may arrive
// with CRLF endings and the located coordinates are the same. Line NUMBERING is
// part of the claim — normalisation must not shift, drop or add a line.
//
// The three-line block at lines 2-4 of contractMMLSPBuf is the query in every
// case, so the expected answer is a constant across the matrix.
func TestContractMMLSPLocateBlockNormalisesCRLFOnBothSides(t *testing.T) {
	const text = "func a() {\n\tx := 1\n}"
	cases := []struct {
		name   string
		buffer string
		text   string
	}{
		{"LF buffer, LF text", contractMMLSPBuf, text},
		{"CRLF buffer, LF text", contractMMLSPCRLF(contractMMLSPBuf), text},
		{"LF buffer, CRLF text", contractMMLSPBuf, contractMMLSPCRLF(text)},
		{"CRLF buffer, CRLF text", contractMMLSPCRLF(contractMMLSPBuf), contractMMLSPCRLF(text)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contractMMLSPBlock(t, tc.name, tc.buffer, tc.text, 2, 4)
		})
	}
}

// TestContractMMLSPLocateBlockCRLFDoesNotShiftLaterLineNumbers is the same
// clause read at the far end of the buffer, where a normalisation that
// mis-counted line breaks would show up as a drift proportional to the number
// of lines above the match rather than as a constant off-by-one.
func TestContractMMLSPLocateBlockCRLFDoesNotShiftLaterLineNumbers(t *testing.T) {
	crlf := contractMMLSPCRLF(contractMMLSPBuf)
	contractMMLSPBlock(t, "the last line of a CRLF buffer", crlf, "func b() {\n\tx := 1\n}", 5, 7)
}

// ---------------------------------------------------------------------------
// core.LocateBlock — the whitespace-normalised arm
// ---------------------------------------------------------------------------

// TestContractMMLSPLocateBlockWhitespaceArmFindsAReindentedBlock is the arm's
// headline case, and the reason it exists: carl marked a block, the file was
// reindented (or the mark was captured from a rendering with different
// indentation), and the block is still that block. Equality is the StripWS
// shape — every whitespace rune removed from both sides — so the indentation
// character, the indentation depth and interior blank lines are all
// immaterial, while the marked block's real coordinates in the buffer are what
// comes back.
func TestContractMMLSPLocateBlockWhitespaceArmFindsAReindentedBlock(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			name: "spaces where the buffer has tabs",
			text: "if x {\n    y()\n}",
		},
		{
			name: "a different indentation depth",
			text: "\t\t\tif x {\n\t\t\t\ty()\n\t\t\t}",
		},
		{
			name: "no indentation at all",
			text: "if x {\ny()\n}",
		},
		{
			name: "blank lines inside the marked text",
			text: "if x {\n\n    y()\n\n}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contractMMLSPBlock(t, tc.name, contractMMLSPWSBuf, tc.text, 2, 4)
		})
	}
}

// TestContractMMLSPLocateBlockEndLineComesFromTheBuffersOwnBlock pins the
// clause that separates this seam from a naive one: endLine is derived from the
// matched block IN THE BUFFER, never from the query's own line count. The two
// directions are tested because they fail differently — a query spanning more
// lines than the buffer's block over-runs the end, one spanning fewer
// under-runs it, and an implementation that only ever matched equal-length
// windows finds neither.
func TestContractMMLSPLocateBlockEndLineComesFromTheBuffersOwnBlock(t *testing.T) {
	t.Run("the query is one line, the buffer's block is three", func(t *testing.T) {
		contractMMLSPBlock(t,
			"a one-line query against a three-line block",
			contractMMLSPWSBuf, "if x { y() }", 2, 4)
	})
	t.Run("the query is three lines, the buffer's block is one", func(t *testing.T) {
		contractMMLSPBlock(t,
			"a three-line query against a one-line block",
			contractMMLSPInlineBuf, "if x {\n\ty()\n}", 2, 2)
	})
}

// TestContractMMLSPLocateBlockPrefersTheExactArm pins the ORDER the contract
// states — "exact contiguous first ... else a whitespace-normalized line-window
// match". The fixture makes the two arms disagree: a whitespace-equal window
// sits at lines 1-3 and the byte-identical block at 4-6, so an implementation
// that ran the tolerant arm first (or ran both and took the earlier hit)
// answers 1-3. The exact block is the one carl's mark actually names.
func TestContractMMLSPLocateBlockPrefersTheExactArm(t *testing.T) {
	contractMMLSPBlock(t,
		"an exact hit below an earlier whitespace-only window",
		contractMMLSPArmOrderBuf, "if x {\n    y()\n}", 4, 6)
}

// TestContractMMLSPLocateBlockWhitespaceArmTakesTheFirstOccurrence is "text's
// FIRST occurrence in buffer" read inside the tolerant arm, where no byte match
// exists anywhere: of two whitespace-equal windows, the earlier one is the
// answer.
func TestContractMMLSPLocateBlockWhitespaceArmTakesTheFirstOccurrence(t *testing.T) {
	contractMMLSPBlock(t,
		"the earlier of two whitespace-equal windows",
		contractMMLSPTwoWSBuf, "if x {\n  y()\n}", 1, 3)
}

// TestContractMMLSPLocateBlockWhitespaceArmNormalisesCRLF closes the CRLF claim
// over the tolerant arm too: a mark captured with CRLF endings against an LF
// buffer, and the reverse, both locate the same block. Stated once for the
// seam, so it must hold on whichever arm answers.
func TestContractMMLSPLocateBlockWhitespaceArmNormalisesCRLF(t *testing.T) {
	const text = "if x {\n    y()\n}"
	contractMMLSPBlock(t, "CRLF text, LF buffer",
		contractMMLSPWSBuf, contractMMLSPCRLF(text), 2, 4)
	contractMMLSPBlock(t, "LF text, CRLF buffer",
		contractMMLSPCRLF(contractMMLSPWSBuf), text, 2, 4)
	contractMMLSPBlock(t, "CRLF on both sides",
		contractMMLSPCRLF(contractMMLSPWSBuf), contractMMLSPCRLF(text), 2, 4)
}
