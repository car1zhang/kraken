package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Edge tests for slice A of the dream-2026-09-28 contract: a transcript
// record naming a session other than its file's own is judged by comparing
// its timestamp with the file's creation time. Authored blind from
// artifacts/50-contract.md; every expectation below cites the obligation id
// it pins.

// --- fixture helpers (edgeOwnerBorn prefix: shared package with the logic author) ---

// edgeOwnerBornBase is a fixed moment for the pure tokenRecordOwned tests,
// which touch no file and so need no wall clock.
var edgeOwnerBornBase = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// edgeOwnerBornMin is one minute in milliseconds; the contract requires a
// record meant to predate or follow creation to sit at least this far away.
const edgeOwnerBornMin = int64(60_000)

// edgeOwnerBornWithSID re-serializes a fixture line with `session_id` set.
func edgeOwnerBornWithSID(t *testing.T, line, sid string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("reparse fixture: %v", err)
	}
	m["session_id"] = sid
	return mineJSON(t, m)
}

// edgeOwnerBornWithTS re-serializes a fixture line with `timestamp` replaced
// by raw text, or removed entirely when raw is nil.
func edgeOwnerBornWithTS(t *testing.T, line string, raw *string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("reparse fixture: %v", err)
	}
	if raw == nil {
		delete(m, "timestamp")
	} else {
		m["timestamp"] = *raw
	}
	return mineJSON(t, m)
}

// edgeOwnerBornWant compares the four counters only (Sessions and the zeus
// half are not this slice's subject).
func edgeOwnerBornWant(t *testing.T, label string, got Tokens, in, out, cw, cr int64) {
	t.Helper()
	if got.In != in || got.Out != out || got.CacheWrite != cw || got.CacheRead != cr {
		t.Fatalf("%s: counters in/out/cw/cr = %d/%d/%d/%d, want %d/%d/%d/%d",
			label, got.In, got.Out, got.CacheWrite, got.CacheRead, in, out, cw, cr)
	}
}

// edgeOwnerBornShape is one transcript shape: the file name (whose stem is
// the file's own session id), its lines in order, and the four counters the
// shape's obligation implies.
type edgeOwnerBornShape struct {
	name  string
	rel   string
	lines []string
	want  [4]int64
}

// edgeOwnerBornShapes builds the three contract shapes plus a resumed fork,
// with every timestamp derived from now (the wall clock read BEFORE the file
// is created): pre is 3 minutes before, post 3 minutes after. Each shape
// holds a message.id group spread over several records, with the repeat
// record carrying deliberately different input/cache values so that counting
// them per record would be visible (A14).
func edgeOwnerBornShapes(t *testing.T, now int64) []edgeOwnerBornShape {
	t.Helper()
	pre := now - 3*edgeOwnerBornMin
	post := now + 3*edgeOwnerBornMin
	a := func(ts int64, u tokUsage, id, sid string) string { return tokAssistantFull(t, ts, u, id, sid) }
	res := func(ts int64, sid string) string {
		return edgeOwnerBornWithSID(t, mineUser(t, ts, []any{mineToolResult("t1", false, "ok")}), sid)
	}

	forkLines := []string{
		// copied from the parent, all dated before the fork file existed (A11)
		a(pre, tokUsage{in: 100, out: 1, cw: 1000, cr: 10000}, "msg_P1", "parent"),
		res(pre, "parent"),
		a(pre, tokUsage{in: 777, out: 900, cw: 777, cr: 777}, "msg_P1", "parent"),
		a(pre+1000, tokUsage{in: 3, out: 3, cw: 3, cr: 3}, "msg_P2", "parent"),
		// the fork's own work
		a(post, tokUsage{in: 2, out: 5, cw: 20, cr: 200}, "msg_F1", "fork"),
		a(post, tokUsage{in: 50, out: 40, cw: 50, cr: 50}, "msg_F1", "fork"),
		// an own record dated before creation still counts (A2)
		a(pre+2000, tokUsage{in: 1, out: 1, cw: 1, cr: 1}, "msg_F2", "fork"),
	}
	frLines := append([]string{}, forkLines...)
	frLines = append(frLines,
		// a later process resumes the fork (A13)
		a(post+1000, tokUsage{in: 9, out: 9, cw: 9, cr: 9}, "msg_T1", "third"),
		res(post+1000, "third"),
		a(post+1000, tokUsage{in: 400, out: 90, cw: 400, cr: 400}, "msg_T1", "third"),
	)

	return []edgeOwnerBornShape{
		{
			name: "continuation",
			rel:  "cont.jsonl",
			lines: []string{
				a(post, tokUsage{in: 4, out: 2, cw: 10, cr: 100}, "msg_C1", "prev"),
				res(post, "prev"),
				a(post, tokUsage{in: 900, out: 50, cw: 900, cr: 900}, "msg_C1", "prev"),
				a(post+1000, tokUsage{in: 1, out: 7, cw: 2, cr: 3}, "msg_C2", "prev"),
			},
			want: [4]int64{5, 57, 12, 103},
		},
		{
			name:  "fork",
			rel:   "fork.jsonl",
			lines: forkLines,
			want:  [4]int64{3, 41, 21, 201},
		},
		{
			name: "resume",
			rel:  "res.jsonl",
			lines: []string{
				a(post, tokUsage{in: 6, out: 60, cw: 600, cr: 6000}, "msg_R1", "res"),
				a(post+1000, tokUsage{in: 1, out: 3, cw: 5, cr: 7}, "msg_O1", "other"),
				res(post+1000, "other"),
				a(post+1000, tokUsage{in: 800, out: 30, cw: 800, cr: 800}, "msg_O1", "other"),
			},
			want: [4]int64{7, 90, 605, 6007},
		},
		{
			name:  "resumed fork",
			rel:   "fork.jsonl",
			lines: frLines,
			want:  [4]int64{3 + 9, 41 + 90, 21 + 9, 201 + 9},
		},
	}
}

// --- tokenRecordOwned: A1–A6 at their boundaries ---

func edgeOwnerBornTS(ms int64, extra time.Duration) string {
	return time.UnixMilli(ms).Add(extra).UTC().Format(time.RFC3339Nano)
}

// TestEdgeOwnerBornEmptySessionIsAlwaysOwned pins A1 against every
// timestamp/Born combination that would otherwise refuse a record.
func TestEdgeOwnerBornEmptySessionIsAlwaysOwned(t *testing.T) {
	born := edgeOwnerBornBase.UnixMilli()
	for _, tc := range []struct {
		name string
		ts   string
		born int64
	}{
		{"no timestamp, born known", "", born},
		{"no timestamp, born unknown", "", 0},
		{"garbage timestamp", "yesterday-ish", born},
		{"dated a day before born", edgeOwnerBornTS(born-86_400_000, 0), born},
		{"dated 1ms before born", edgeOwnerBornTS(born-1, 0), born},
		{"dated after born, born unknown", edgeOwnerBornTS(born+edgeOwnerBornMin, 0), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tokenRecord{SessionID: "", Timestamp: tc.ts}
			if !tokenRecordOwned(rec, fileOwner{ID: "own", Born: tc.born}) {
				t.Fatalf("A1: empty SessionID must be owned (ts=%q born=%d)", tc.ts, tc.born)
			}
		})
	}
}

// TestEdgeOwnerBornOwnSessionIsAlwaysOwned pins A2: a record naming the
// file's own session is owned even when predated, undated, garbled, or when
// the creation time is unknown.
func TestEdgeOwnerBornOwnSessionIsAlwaysOwned(t *testing.T) {
	born := edgeOwnerBornBase.UnixMilli()
	for _, tc := range []struct {
		name string
		ts   string
		born int64
	}{
		{"dated a day before born", edgeOwnerBornTS(born-86_400_000, 0), born},
		{"dated 1ms before born", edgeOwnerBornTS(born-1, 0), born},
		{"no timestamp", "", born},
		{"garbage timestamp", "not-a-time", born},
		{"born unknown", edgeOwnerBornTS(born, 0), 0},
		{"born unknown and no timestamp", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tokenRecord{SessionID: "own", Timestamp: tc.ts}
			if !tokenRecordOwned(rec, fileOwner{ID: "own", Born: tc.born}) {
				t.Fatalf("A2: own-session record must be owned (ts=%q born=%d)", tc.ts, tc.born)
			}
		})
	}
}

// TestEdgeOwnerBornForeignAtTheCreationBoundary pins A3/A4 at the threshold:
// exactly equal is owned, one millisecond before is not, and a sub-millisecond
// offset is judged as the moment it parses to (half a millisecond after the
// Born millisecond is at-or-after it; half a millisecond before is before).
func TestEdgeOwnerBornForeignAtTheCreationBoundary(t *testing.T) {
	born := edgeOwnerBornBase.Add(123 * time.Millisecond).UnixMilli()
	own := fileOwner{ID: "own", Born: born}
	for _, tc := range []struct {
		name string
		ts   string
		want bool
	}{
		{"exactly born (A3: equal counts as owned)", edgeOwnerBornTS(born, 0), true},
		{"1ms after born", edgeOwnerBornTS(born, time.Millisecond), true},
		{"half a ms after born", edgeOwnerBornTS(born, 500*time.Microsecond), true},
		{"1ms before born (A4)", edgeOwnerBornTS(born, -time.Millisecond), false},
		{"half a ms before born (A4)", edgeOwnerBornTS(born, -500*time.Microsecond), false},
		{"a day before born (A4)", edgeOwnerBornTS(born, -24*time.Hour), false},
		{"a year after born", edgeOwnerBornTS(born, 365*24*time.Hour), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tokenRecord{SessionID: "other", Timestamp: tc.ts}
			if got := tokenRecordOwned(rec, own); got != tc.want {
				t.Fatalf("tokenRecordOwned(ts=%s, born=%d) = %v, want %v", tc.ts, born, got, tc.want)
			}
		})
	}
}

// TestEdgeOwnerBornForeignTimestampIsAMomentNotText pins that A3/A4 compare
// the MOMENT a timestamp parses to: an RFC 3339 value written with a non-UTC
// offset, or without fractional seconds, is judged by the instant it names.
func TestEdgeOwnerBornForeignTimestampIsAMomentNotText(t *testing.T) {
	born := edgeOwnerBornBase.UnixMilli() // 12:00:00.000Z, whole second
	own := fileOwner{ID: "own", Born: born}
	plus2 := time.FixedZone("plus2", 2*3600)
	minus5 := time.FixedZone("minus5", -5*3600)
	for _, tc := range []struct {
		name string
		ts   string
		want bool
	}{
		{"same instant, +02:00 offset", edgeOwnerBornBase.In(plus2).Format(time.RFC3339Nano), true},
		{"same instant, no fraction", edgeOwnerBornBase.Format(time.RFC3339), true},
		{"one second earlier, -05:00 offset", edgeOwnerBornBase.Add(-time.Second).In(minus5).Format(time.RFC3339), false},
		// lexically "13:00" sorts after "12:00", but the instant is 11:00Z
		{"lexically later, instantly earlier", edgeOwnerBornBase.Add(-time.Hour).In(plus2).Format(time.RFC3339), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := tokenRecord{SessionID: "other", Timestamp: tc.ts}
			if got := tokenRecordOwned(rec, own); got != tc.want {
				t.Fatalf("tokenRecordOwned(ts=%s) = %v, want %v", tc.ts, got, tc.want)
			}
		})
	}
}

// TestEdgeOwnerBornForeignWithUnknownBornIsNeverOwned pins A5: with Born == 0
// no timestamp, however late, makes a foreign record owned.
func TestEdgeOwnerBornForeignWithUnknownBornIsNeverOwned(t *testing.T) {
	own := fileOwner{ID: "own", Born: 0}
	for _, ts := range []string{
		edgeOwnerBornTS(edgeOwnerBornBase.UnixMilli(), 0),
		edgeOwnerBornTS(time.Now().Add(24*time.Hour).UnixMilli(), 0),
		"1970-01-01T00:00:00Z", // the moment Born==0 would name, were it a moment
		"",
		"garbage",
	} {
		if tokenRecordOwned(tokenRecord{SessionID: "other", Timestamp: ts}, own) {
			t.Errorf("A5: foreign record with Born==0 owned (ts=%q)", ts)
		}
	}
}

// TestEdgeOwnerBornForeignUnparseableTimestampIsNotOwned pins A6 over the
// shapes a timestamp can fail in: empty, whitespace, date-only, a unix number
// written as text, a non-RFC-3339 layout, and trailing junk.
func TestEdgeOwnerBornForeignUnparseableTimestampIsNotOwned(t *testing.T) {
	born := edgeOwnerBornBase.UnixMilli()
	own := fileOwner{ID: "own", Born: born}
	later := edgeOwnerBornBase.Add(time.Hour)
	for _, ts := range []string{
		"",
		" ",
		later.Format("2006-01-02"),
		"1790000000000",
		later.Format("2006-01-02 15:04:05"),
		later.Format(time.RFC1123),
		later.Format(time.RFC3339) + "junk",
		"not a time at all",
	} {
		if tokenRecordOwned(tokenRecord{SessionID: "other", Timestamp: ts}, own) {
			t.Errorf("A6: foreign record with unparseable timestamp %q owned", ts)
		}
	}
}

// TestEdgeOwnerBornTimestampDecodesVerbatimOrEmpty pins the tokenRecord
// obligation at its edges: the field holds the record's `timestamp` TEXT
// verbatim (an offset form is not normalized), an absent key leaves it empty,
// and the camelCase `sessionId` never fills SessionID (the snake field is the
// only marker, per the slice background).
func TestEdgeOwnerBornTimestampDecodesVerbatimOrEmpty(t *testing.T) {
	for _, tc := range []struct {
		name, line, want string
	}{
		{"offset form kept verbatim", `{"type":"assistant","timestamp":"2026-09-28T14:00:00.5+02:00","session_id":"s"}`, "2026-09-28T14:00:00.5+02:00"},
		{"garbage kept verbatim", `{"type":"assistant","timestamp":"soon","session_id":"s"}`, "soon"},
		{"absent key", `{"type":"assistant","session_id":"s"}`, ""},
		{"empty string", `{"type":"assistant","timestamp":"","session_id":"s"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rec tokenRecord
			if err := json.Unmarshal([]byte(tc.line), &rec); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if rec.Timestamp != tc.want {
				t.Fatalf("Timestamp = %q, want %q", rec.Timestamp, tc.want)
			}
		})
	}
	var rec tokenRecord
	if err := json.Unmarshal([]byte(`{"type":"assistant","sessionId":"camel","timestamp":"x"}`), &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.SessionID != "" {
		t.Fatalf("camelCase sessionId filled SessionID = %q; only session_id may", rec.SessionID)
	}
}

// --- fileBorn: A7–A9 ---

// TestEdgeOwnerBornFileBornNilAndClosed pins A9.
func TestEdgeOwnerBornFileBornNilAndClosed(t *testing.T) {
	if got := fileBorn(nil); got != 0 {
		t.Fatalf("A9: fileBorn(nil) = %d, want 0", got)
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "x.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fileBorn(f); got != 0 {
		t.Fatalf("A9: fileBorn(closed) = %d, want 0", got)
	}
}

// TestEdgeOwnerBornFileBornIsCreationNotOpenOrModify pins A7/A8 against the
// two times a wrong implementation would reach for: the file is reopened
// (read-only) more than a second after creation, appended to, and has its
// modification time pushed an hour into the future — and fileBorn, on both
// the original and the reopened handle, still reports the creation moment.
func TestEdgeOwnerBornFileBornIsCreationNotOpenOrModify(t *testing.T) {
	p := filepath.Join(t.TempDir(), "born.jsonl")
	before := time.Now().UnixMilli()
	f, err := os.Create(p)
	after := time.Now().UnixMilli()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	born := fileBorn(f)
	if born < before-1000 || born > after+1000 {
		t.Fatalf("A7: fileBorn = %d, want within [%d, %d]", born, before-1000, after+1000)
	}

	time.Sleep(1500 * time.Millisecond)
	if _, err := f.WriteString(`{"type":"assistant"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if got := fileBorn(f); got != born {
		t.Fatalf("A8: fileBorn changed after append/mtime move: %d -> %d", born, got)
	}

	g, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if got := fileBorn(g); got != born {
		t.Fatalf("A7: reopened handle reports %d, creation was %d (open time or mtime leaked in)", got, born)
	}
}

// TestEdgeOwnerBornFileBornEmptyFile pins A7 for a file with zero bytes: an
// empty transcript has a creation time like any other.
func TestEdgeOwnerBornFileBornEmptyFile(t *testing.T) {
	before := time.Now().UnixMilli()
	f, err := os.Create(filepath.Join(t.TempDir(), "empty.jsonl"))
	after := time.Now().UnixMilli()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := fileBorn(f); got < before-1000 || got > after+1000 {
		t.Fatalf("A7: fileBorn(empty) = %d, want within [%d, %d]", got, before-1000, after+1000)
	}
}

// --- SumTokensDelta: A10–A16 at their edges ---

// TestEdgeOwnerBornSumEachShapeOnePass pins A10–A14 for every shape in one
// pass, each shape alone in its own project dir. Every shape carries a
// streamed message.id group whose repeat record has inflated input/cache
// values (A14), and the fork carries an own record dated before creation (A2
// through the sum).
func TestEdgeOwnerBornSumEachShapeOnePass(t *testing.T) {
	probe := edgeOwnerBornShapes(t, time.Now().UnixMilli())
	for i := range probe {
		name := probe[i].name
		t.Run(name, func(t *testing.T) {
			projects := mineHome(t)
			sh := edgeOwnerBornShapes(t, time.Now().UnixMilli())[i]
			mineWrite(t, projects, "-proj", sh.rel, sh.lines...)
			got, _, err := SumTokensDelta(filepath.Join(projects, "-proj"), nil)
			if err != nil {
				t.Fatalf("SumTokensDelta: %v", err)
			}
			edgeOwnerBornWant(t, name, got, sh.want[0], sh.want[1], sh.want[2], sh.want[3])
		})
	}
}

// TestEdgeOwnerBornSumSplitAtEveryBoundary pins A15 exhaustively: each shape
// is written up to record k, summed, the rest appended, summed again from the
// first pass' offsets — for EVERY k, including splits that land inside a
// streamed group of records naming another session (the resume-boundary seed
// must apply the ownership rule exactly as the forward pass does).
func TestEdgeOwnerBornSumSplitAtEveryBoundary(t *testing.T) {
	probe := edgeOwnerBornShapes(t, time.Now().UnixMilli())
	for i := range probe {
		for k := 1; k < len(probe[i].lines); k++ {
			name := probe[i].name
			t.Run(name+"/split-after-"+string(rune('0'+k)), func(t *testing.T) {
				projects := mineHome(t)
				dir := filepath.Join(projects, "-proj")
				sh := edgeOwnerBornShapes(t, time.Now().UnixMilli())[i]
				p := mineWrite(t, projects, "-proj", sh.rel, sh.lines[:k]...)
				first, offs, err := SumTokensDelta(dir, nil)
				if err != nil {
					t.Fatalf("first pass: %v", err)
				}
				tokAppend(t, p, sh.lines[k:]...)
				second, _, err := SumTokensDelta(dir, offs)
				if err != nil {
					t.Fatalf("second pass: %v", err)
				}
				sum := Tokens{
					In:         first.In + second.In,
					Out:        first.Out + second.Out,
					CacheWrite: first.CacheWrite + second.CacheWrite,
					CacheRead:  first.CacheRead + second.CacheRead,
				}
				edgeOwnerBornWant(t, name, sum, sh.want[0], sh.want[1], sh.want[2], sh.want[3])
			})
		}
	}
}

// TestEdgeOwnerBornSumSidecarIgnoresTimestamps pins A16: a sidecar's records
// carry no session_id, so they count fully whether dated long before the
// sidecar existed, or not dated at all.
func TestEdgeOwnerBornSumSidecarIgnoresTimestamps(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	undated := edgeOwnerBornWithTS(t, tokAssistantFull(t, now, tokUsage{in: 5, out: 6, cw: 7, cr: 8}, "msg_S2", ""), nil)
	garbled := "garbage"
	badTS := edgeOwnerBornWithTS(t, tokAssistantFull(t, now, tokUsage{in: 10, out: 20, cw: 30, cr: 40}, "msg_S3", ""), &garbled)
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now-86_400_000, tokUsage{in: 1, out: 2, cw: 3, cr: 4}, "msg_S1", ""),
		undated,
		badTS,
	)
	got, _, err := SumTokensDelta(filepath.Join(projects, "-proj"), nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	edgeOwnerBornWant(t, "sidecar", got, 16, 28, 40, 52)
}

// TestEdgeOwnerBornSumForeignUndatedOrGarbledContributesNothing carries A6
// through the sum: in a file otherwise shaped as a continuation, a record
// naming another session with no timestamp, or one that does not parse,
// contributes nothing, while its dated sibling contributes fully.
func TestEdgeOwnerBornSumForeignUndatedOrGarbledContributesNothing(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	post := now + 3*edgeOwnerBornMin
	garbled := "tomorrow"
	mineWrite(t, projects, "-proj", "cont.jsonl",
		tokAssistantFull(t, post, tokUsage{in: 1, out: 2, cw: 3, cr: 4}, "msg_A", "prev"),
		edgeOwnerBornWithTS(t, tokAssistantFull(t, post, tokUsage{in: 100, out: 200, cw: 300, cr: 400}, "msg_B", "prev"), nil),
		edgeOwnerBornWithTS(t, tokAssistantFull(t, post, tokUsage{in: 1000, out: 2000, cw: 3000, cr: 4000}, "msg_C", "prev"), &garbled),
	)
	got, _, err := SumTokensDelta(filepath.Join(projects, "-proj"), nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	edgeOwnerBornWant(t, "continuation with undated/garbled foreign", got, 1, 2, 3, 4)
}

// --- MineTranscripts: A17–A20 at their edges ---

// TestEdgeOwnerBornDigestTokensMatchSumPerShape pins A17 per shape, each in a
// HOME of its own so the project dir holds only that file: digest counters
// equal SumTokensDelta's AND the value the shape implies (agreement alone
// would pass if both readers shared one wrong rule).
func TestEdgeOwnerBornDigestTokensMatchSumPerShape(t *testing.T) {
	probe := edgeOwnerBornShapes(t, time.Now().UnixMilli())
	for i := range probe {
		name := probe[i].name
		t.Run(name, func(t *testing.T) {
			projects := mineHome(t)
			now := time.Now().UnixMilli()
			sh := edgeOwnerBornShapes(t, now)[i]
			mineWrite(t, projects, "-proj", sh.rel, sh.lines...)
			sum, _, err := SumTokensDelta(filepath.Join(projects, "-proj"), nil)
			if err != nil {
				t.Fatalf("SumTokensDelta: %v", err)
			}
			res, err := MineTranscripts(now - 6*60*edgeOwnerBornMin)
			if err != nil {
				t.Fatalf("MineTranscripts: %v", err)
			}
			d := mineFindDigest(t, res, sh.rel)
			edgeOwnerBornWant(t, name+" (sum)", sum, sh.want[0], sh.want[1], sh.want[2], sh.want[3])
			if d.In != sh.want[0] || d.Out != sh.want[1] || d.CacheWrite != sh.want[2] || d.CacheRead != sh.want[3] {
				t.Fatalf("%s: digest in/out/cw/cr = %d/%d/%d/%d, want %v (sum reported %d/%d/%d/%d)",
					name, d.In, d.Out, d.CacheWrite, d.CacheRead, sh.want, sum.In, sum.Out, sum.CacheWrite, sum.CacheRead)
			}
		})
	}
}

// TestEdgeOwnerBornDigestContinuationCountsUnmarkedFollowers pins A18 with
// A20 on the continuation shape: the tool error and the interrupt and turn
// duration carry NO session_id (the real shapes for those records), follow a
// record naming the earlier session, and are dated after creation — so they
// are judged as that session, after Born, and counted.
func TestEdgeOwnerBornDigestContinuationCountsUnmarkedFollowers(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	post := now + 3*edgeOwnerBornMin
	mineWrite(t, projects, "-proj", "cont.jsonl",
		edgeOwnerBornWithSID(t, mineAssistant(t, post, "claude-opus-5",
			mineToolUse("c1", "Bash", map[string]any{"command": "go vet ./..."})), "prev"),
		mineUser(t, post+1000, []any{mineToolResult("c1", true, "boom")}),
		mineUser(t, post+2000, "[Request interrupted by user]"),
		mineJSON(t, map[string]any{"type": "system", "subtype": "turn_duration",
			"durationMs": 4242, "timestamp": mineISO(post + 3000)}),
	)
	res, err := MineTranscripts(now - 6*60*edgeOwnerBornMin)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "cont.jsonl")
	if d.ToolCounts["Bash"] != 1 {
		t.Errorf("A18: ToolCounts = %v, want Bash:1", d.ToolCounts)
	}
	if d.ErrorCounts["Bash"] != 1 {
		t.Errorf("A18/A20: ErrorCounts = %v, want Bash:1", d.ErrorCounts)
	}
	if d.Interrupts != 1 {
		t.Errorf("A18/A20: Interrupts = %d, want 1", d.Interrupts)
	}
	if len(d.TurnDurations) != 1 || d.TurnDurations[0] != 4242 {
		t.Errorf("A18/A20: TurnDurations = %v, want [4242]", d.TurnDurations)
	}
}

// TestEdgeOwnerBornDigestResumedForkCountsOnlyOwnAndLater pins A18 and A19
// together on the hardest ordering: copied parent records (every signal kind,
// including a two-record max_tokens group and unmarked followers) are not
// counted; the fork's own work is; a later process' work, dated after
// creation, is.
func TestEdgeOwnerBornDigestResumedForkCountsOnlyOwnAndLater(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	pre := now - 3*edgeOwnerBornMin
	post := now + 3*edgeOwnerBornMin
	sid := func(line, s string) string { return edgeOwnerBornWithSID(t, line, s) }
	ceiling := func(ts int64, text string) string {
		return mineJSON(t, map[string]any{"type": "assistant", "timestamp": mineISO(ts), "session_id": "parent",
			"message": map[string]any{"id": "msg-ceiling", "model": "claude-opus-5", "stop_reason": "max_tokens",
				"content": []any{mineText(text)}}})
	}
	mineWrite(t, projects, "-proj", "fr.jsonl",
		// copied (A19)
		sid(mineAssistant(t, pre, "claude-opus-5", mineToolUse("p1", "Grep", map[string]any{"pattern": "x"})), "parent"),
		sid(mineUser(t, pre+1000, []any{mineToolResult("p1", true, "nope")}), "parent"),
		mineUser(t, pre+2000, "[Request interrupted by user]"), // unmarked: judged as parent, predated
		sid(mineAssistant(t, pre+3000, "<synthetic>", mineText("API Error")), "parent"),
		ceiling(pre+4000, "part one"),
		ceiling(pre+4000, "part two"),
		mineJSON(t, map[string]any{"type": "system", "subtype": "turn_duration",
			"durationMs": 99_000, "timestamp": mineISO(pre + 5000)}), // unmarked, predated
		// the fork's own
		sid(mineAssistant(t, post, "claude-opus-5", mineToolUse("f1", "Read", map[string]any{"file_path": "/tmp/a"})), "fr"),
		sid(mineUser(t, post+1000, []any{mineToolResult("f1", true, "gone")}), "fr"),
		// a later process (A18)
		sid(mineAssistant(t, post+60_000, "claude-opus-5", mineToolUse("l1", "Bash", map[string]any{"command": "ls"})), "later"),
		mineUser(t, post+61_000, []any{mineToolResult("l1", true, "denied")}), // unmarked: judged as later
		mineJSON(t, map[string]any{"type": "system", "subtype": "turn_duration",
			"durationMs": 777, "timestamp": mineISO(post + 62_000)}),
	)
	res, err := MineTranscripts(now - 6*60*edgeOwnerBornMin)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "fr.jsonl")
	if d.ToolCounts["Grep"] != 0 || d.ToolCounts["Read"] != 1 || d.ToolCounts["Bash"] != 1 {
		t.Errorf("ToolCounts = %v, want Read:1 Bash:1 and no Grep", d.ToolCounts)
	}
	if d.ErrorCounts["Grep"] != 0 || d.ErrorCounts["Read"] != 1 || d.ErrorCounts["Bash"] != 1 {
		t.Errorf("ErrorCounts = %v, want Read:1 Bash:1 and no Grep", d.ErrorCounts)
	}
	if d.Interrupts != 0 {
		t.Errorf("A19: Interrupts = %d, want 0", d.Interrupts)
	}
	if d.SyntheticErrors != 0 {
		t.Errorf("A19: SyntheticErrors = %d, want 0", d.SyntheticErrors)
	}
	if d.MaxTokensStops != 0 {
		t.Errorf("A19: MaxTokensStops = %d, want 0", d.MaxTokensStops)
	}
	if len(d.TurnDurations) != 1 || d.TurnDurations[0] != 777 {
		t.Errorf("TurnDurations = %v, want [777]", d.TurnDurations)
	}
}

// TestEdgeOwnerBornDigestUnmarkedRecordInheritance pins A20's orderings. An
// unmarked record takes the MOST RECENT session_id seen earlier in the file
// and is judged with its OWN timestamp — so the verdict can differ from the
// record it follows. Each case is its own file; the carrier is a turn
// duration whose value names the case, and each file holds an own-session
// tool call so its digest is never content-free.
func TestEdgeOwnerBornDigestUnmarkedRecordInheritance(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	pre := now - 3*edgeOwnerBornMin
	post := now + 3*edgeOwnerBornMin
	sid := func(line, s string) string { return edgeOwnerBornWithSID(t, line, s) }
	ownCall := func(stem string, ts int64) string {
		return sid(mineAssistant(t, ts, "claude-opus-5", mineToolUse("o-"+stem, "Read", map[string]any{"file_path": "/tmp/" + stem})), stem)
	}
	dur := func(ms int64, ts *int64) string {
		m := map[string]any{"type": "system", "subtype": "turn_duration", "durationMs": ms}
		if ts != nil {
			m["timestamp"] = mineISO(*ts)
		}
		return mineJSON(t, m)
	}
	foreign := func(s string, ts int64) string { return sid(mineUser(t, ts, "hello"), s) }
	p := func(v int64) *int64 { return &v }

	cases := []struct {
		stem  string
		lines []string
		want  []int64
	}{
		// follows a PREDATED foreign record but is itself dated after Born:
		// judged as that session, at-or-after Born → counted
		{"inherit-copied-dated-after", []string{foreign("parent", pre), dur(111, p(post)), ownCall("inherit-copied-dated-after", post)}, []int64{111}},
		// follows a foreign record dated after Born, but itself predates Born
		{"inherit-later-dated-before", []string{ownCall("inherit-later-dated-before", post), foreign("prev", post), dur(222, p(pre))}, nil},
		// follows a foreign record, carries no timestamp at all (A6)
		{"inherit-later-undated", []string{ownCall("inherit-later-undated", post), foreign("prev", post), dur(333, nil)}, nil},
		// most recent is the OWN session, even though a foreign record came before it
		{"most-recent-is-own", []string{foreign("parent", pre), ownCall("most-recent-is-own", post), dur(444, p(pre))}, []int64{444}},
		// most recent is foreign-predated, even though an own record came before it
		{"most-recent-is-foreign", []string{ownCall("most-recent-is-foreign", post), foreign("parent", pre), dur(555, p(pre))}, nil},
		// nothing seen earlier: judged as naming no session (A1) → counted
		{"nothing-seen-yet", []string{dur(666, p(pre)), ownCall("nothing-seen-yet", post)}, []int64{666}},
	}
	for _, c := range cases {
		mineWrite(t, projects, "-proj", c.stem+".jsonl", c.lines...)
	}
	res, err := MineTranscripts(now - 6*60*edgeOwnerBornMin)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	for _, c := range cases {
		t.Run(c.stem, func(t *testing.T) {
			d := mineFindDigest(t, res, string(os.PathSeparator)+c.stem+".jsonl")
			got := d.TurnDurations
			if len(got) != len(c.want) {
				t.Fatalf("A20: TurnDurations = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("A20: TurnDurations = %v, want %v", got, c.want)
				}
			}
		})
	}
}

// TestEdgeOwnerBornDigestSidecarPredatedCountsFully pins A16's digest twin
// via A20/A1: a sidecar's records carry no session_id and nothing earlier in
// the file names one, so even records dated a day before the sidecar existed
// count, both their signals and their tokens.
func TestEdgeOwnerBornDigestSidecarPredatedCountsFully(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	old := now - 2*60*edgeOwnerBornMin // before the file, inside a 6h window
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		mineAssistant(t, old, "claude-opus-5", mineToolUse("s1", "Glob", map[string]any{"pattern": "*.go"})),
		mineUser(t, old+1000, []any{mineToolResult("s1", true, "denied")}),
		tokAssistantFull(t, old+2000, tokUsage{in: 1, out: 2, cw: 3, cr: 4}, "msg_S", ""),
	)
	res, err := MineTranscripts(now - 6*60*edgeOwnerBornMin)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, strings.Join([]string{"subagents", "agent-1.jsonl"}, string(os.PathSeparator)))
	if d.ToolCounts["Glob"] != 1 || d.ErrorCounts["Glob"] != 1 {
		t.Errorf("sidecar signals: ToolCounts=%v ErrorCounts=%v, want Glob:1 each", d.ToolCounts, d.ErrorCounts)
	}
	if d.In != 1 || d.Out != 2 || d.CacheWrite != 3 || d.CacheRead != 4 {
		t.Errorf("sidecar tokens = %d/%d/%d/%d, want 1/2/3/4", d.In, d.Out, d.CacheWrite, d.CacheRead)
	}
}
