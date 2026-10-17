package core

// Blind edge tests for slice A of dream-2026-09-27 (50-contract.md): the
// transcript head probe's per-line ceiling, record bound, window bound, and
// the refusals of isAgentTranscript / headAgentName. Authored from the
// contract text alone; every assertion names the obligation it pins.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// edgeWrite writes lines joined by "\n" (with a trailing "\n" when trailing
// is true) to name under a fresh temp dir and returns the path.
func edgeWrite(t *testing.T, name string, lines []string, trailing bool) string {
	t.Helper()
	body := strings.Join(lines, "\n")
	if trailing {
		body += "\n"
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func edgeOpen(t *testing.T, p string) *os.File {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// edgeSizedLine returns one valid JSONL record of exactly n bytes (newline
// excluded) carrying the given type and, when name != "", agentName.
func edgeSizedLine(t *testing.T, typ, name string, n int) string {
	t.Helper()
	prefix := `{"type":"` + typ + `",`
	if name != "" {
		prefix += `"agentName":"` + name + `",`
	}
	prefix += `"pad":"`
	suffix := `"}`
	padLen := n - len(prefix) - len(suffix)
	if padLen < 0 {
		t.Fatalf("edgeSizedLine: n=%d too small", n)
	}
	line := prefix + strings.Repeat("x", padLen) + suffix
	if len(line) != n {
		t.Fatalf("edgeSizedLine: built %d bytes, want %d", len(line), n)
	}
	if !json.Valid([]byte(line)) {
		t.Fatalf("edgeSizedLine: invalid JSON")
	}
	return line
}

// edgeOverCap is a valid record one byte past the per-line ceiling.
func edgeOverCap(t *testing.T) string {
	t.Helper()
	return edgeSizedLine(t, "attachment", "", agentMarkerBytes+1)
}

func edgeFiller(t *testing.T) string {
	t.Helper()
	return mineJSON(t, map[string]any{"type": "user", "message": "hi"})
}

// edgeVisits runs headScan over p with a visit that never stops, returning
// every visited record.
func edgeVisits(t *testing.T, p string) []headMarker {
	t.Helper()
	f := edgeOpen(t, p)
	var got []headMarker
	headScan(f, func(rec headMarker) bool {
		got = append(got, rec)
		return false
	})
	return got
}

func edgeProbes(t *testing.T, p string) (bool, string) {
	t.Helper()
	return isAgentTranscript(edgeOpen(t, p), p), headAgentName(edgeOpen(t, p))
}

// --- per-line ceiling ---

func TestHeadProbeEdgeLineExactlyAtCeilingIsDecoded(t *testing.T) {
	line := edgeSizedLine(t, "agent-name", "hephaestus-ceil", agentMarkerBytes)
	p := edgeWrite(t, "s.jsonl", []string{line}, true)
	agent, name := edgeProbes(t, p)
	if !agent {
		t.Errorf("isAgentTranscript = false; a line of exactly agentMarkerBytes carrying a non-lead agentName must be decoded → true")
	}
	if name != "hephaestus-ceil" {
		t.Errorf("headAgentName = %q; want %q (exact-ceiling line is decoded)", name, "hephaestus-ceil")
	}
	if v := edgeVisits(t, p); len(v) != 1 || v[0].AgentName != "hephaestus-ceil" {
		t.Errorf("headScan visited %d records; want exactly 1 carrying the name", len(v))
	}
}

func TestHeadProbeEdgeLineOneByteOverCeilingIsSkippedNotVisited(t *testing.T) {
	line := edgeSizedLine(t, "agent-name", "hephaestus-over", agentMarkerBytes+1)
	p := edgeWrite(t, "s.jsonl", []string{line}, true)
	agent, name := edgeProbes(t, p)
	if agent {
		t.Errorf("isAgentTranscript = true; a line one byte over the ceiling must never be decoded")
	}
	if name != "" {
		t.Errorf("headAgentName = %q; want \"\" (over-cap line is skipped)", name)
	}
	if v := edgeVisits(t, p); len(v) != 0 {
		t.Errorf("headScan visited %d records; an over-cap line must not be visited", len(v))
	}
}

func TestHeadProbeEdgeOverCapAgentSettingIsNotAMarker(t *testing.T) {
	line := edgeSizedLine(t, "agent-setting", "", agentMarkerBytes+1)
	p := edgeWrite(t, "s.jsonl", []string{line}, true)
	if agent, _ := edgeProbes(t, p); agent {
		t.Errorf("isAgentTranscript = true; an over-cap agent-setting line is skipped, never decoded")
	}
}

func TestHeadProbeEdgeMarkerOnLineAfterOverCapIsFound(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{edgeOverCap(t), tokAgentName(t, "hephaestus-after")}, true)
	agent, name := edgeProbes(t, p)
	if !agent {
		t.Errorf("isAgentTranscript = false; a marker on the line after an over-cap line must be found")
	}
	if name != "hephaestus-after" {
		t.Errorf("headAgentName = %q; want %q", name, "hephaestus-after")
	}
	if v := edgeVisits(t, p); len(v) != 1 {
		t.Errorf("headScan visited %d records; want 1 (over-cap skipped, marker visited)", len(v))
	}
}

func TestHeadProbeEdgeAgentSettingAfterConsecutiveOverCapLinesIsFound(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{edgeOverCap(t), edgeOverCap(t), edgeOverCap(t), tokAgentSetting(t, "s")}, true)
	if agent, _ := edgeProbes(t, p); !agent {
		t.Errorf("isAgentTranscript = false; agent-setting on record 4 after three over-cap lines must be found")
	}
}

// --- record bound (agentMarkerLines), over-cap lines counted ---

// edgeRecordFixture builds a file whose marker is record number `at`
// (1-based) with over-cap lines at the given 1-based positions before it.
func edgeRecordFixture(t *testing.T, at int, overCapAt map[int]bool, marker string) string {
	t.Helper()
	var lines []string
	for i := 1; i < at; i++ {
		if overCapAt[i] {
			lines = append(lines, edgeOverCap(t))
		} else {
			lines = append(lines, edgeFiller(t))
		}
	}
	lines = append(lines, marker)
	return edgeWrite(t, "s.jsonl", lines, true)
}

func TestHeadProbeEdgeMarkerOnLastCountedRecordWithOverCapMixedIsFound(t *testing.T) {
	over := map[int]bool{1: true, 5: true, 9: true, 15: true}
	p := edgeRecordFixture(t, agentMarkerLines, over, tokAgentName(t, "hephaestus-16"))
	agent, name := edgeProbes(t, p)
	if !agent {
		t.Errorf("isAgentTranscript = false; a marker on record agentMarkerLines is within the record bound")
	}
	if name != "hephaestus-16" {
		t.Errorf("headAgentName = %q; want %q", name, "hephaestus-16")
	}
}

func TestHeadProbeEdgeMarkerPastRecordBoundWithOverCapCountedIsNotFound(t *testing.T) {
	// Only 13 of the first 16 records are decodable; were skipped lines not
	// counted, the marker at record 17 would be the 14th decoded record.
	over := map[int]bool{1: true, 5: true, 9: true}
	p := edgeRecordFixture(t, agentMarkerLines+1, over, tokAgentName(t, "hephaestus-17"))
	agent, name := edgeProbes(t, p)
	if agent {
		t.Errorf("isAgentTranscript = true; a marker on record agentMarkerLines+1 (over-cap lines counted) must not be found")
	}
	if name != "" {
		t.Errorf("headAgentName = %q; want \"\" past the record bound", name)
	}
	pSet := edgeRecordFixture(t, agentMarkerLines+1, over, tokAgentSetting(t, "s"))
	if agent, _ := edgeProbes(t, pSet); agent {
		t.Errorf("isAgentTranscript = true; agent-setting on record agentMarkerLines+1 must not be found")
	}
}

func TestHeadScanVisitsAtMostRecordBoundMinusSkipped(t *testing.T) {
	var lines []string
	for i := 0; i < 3; i++ {
		lines = append(lines, edgeOverCap(t))
	}
	for i := 0; i < 20; i++ {
		lines = append(lines, edgeFiller(t))
	}
	p := edgeWrite(t, "s.jsonl", lines, true)
	if v := edgeVisits(t, p); len(v) != agentMarkerLines-3 {
		t.Errorf("headScan visited %d records; want %d (bound %d, three skipped lines counted)", len(v), agentMarkerLines-3, agentMarkerLines)
	}
}

func TestHeadScanAllOverCapRecordBoundVisitsNothing(t *testing.T) {
	var lines []string
	for i := 0; i < agentMarkerLines; i++ {
		lines = append(lines, edgeOverCap(t))
	}
	lines = append(lines, tokAgentName(t, "hephaestus-late"))
	p := edgeWrite(t, "s.jsonl", lines, true)
	if v := edgeVisits(t, p); len(v) != 0 {
		t.Errorf("headScan visited %d records; agentMarkerLines over-cap lines exhaust the bound before the marker", len(v))
	}
	if _, name := edgeProbes(t, p); name != "" {
		t.Errorf("headAgentName = %q; want \"\"", name)
	}
}

// --- window bound (agentMarkerWindow) — the one window-crossing test ---

func TestHeadProbeEdgeWindowBoundOnMarkerLineEnd(t *testing.T) {
	marker := tokAgentName(t, "hephaestus-win")
	mLen := len(marker) + 1 // with its newline
	cases := []struct {
		name string
		// first is the over-cap first line's length, newline excluded.
		first int
		found bool
	}{
		// marker's newline is the window's last byte: ends within → decoded.
		{"marker ends exactly at window", agentMarkerWindow - mLen - 1, true},
		// marker begins inside the window but its newline lands one byte past it.
		{"marker straddles window by one byte", agentMarkerWindow - mLen, false},
		// marker begins exactly at byte agentMarkerWindow.
		{"marker begins at window", agentMarkerWindow - 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := edgeWrite(t, "s.jsonl", []string{edgeSizedLine(t, "attachment", "", c.first), marker}, true)
			agent, name := edgeProbes(t, p)
			if agent != c.found {
				t.Errorf("isAgentTranscript = %v; want %v", agent, c.found)
			}
			wantName := ""
			if c.found {
				wantName = "hephaestus-win"
			}
			if name != wantName {
				t.Errorf("headAgentName = %q; want %q", name, wantName)
			}
			wantVisits := 0
			if c.found {
				wantVisits = 1
			}
			if v := edgeVisits(t, p); len(v) != wantVisits {
				t.Errorf("headScan visited %d records; want %d", len(v), wantVisits)
			}
		})
	}
}

// --- final line without newline ---

func TestHeadProbeEdgeFinalLineWithoutNewlineIsDecoded(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{edgeFiller(t), tokAgentName(t, "hephaestus-eof")}, false)
	agent, name := edgeProbes(t, p)
	if !agent {
		t.Errorf("isAgentTranscript = false; a final unterminated line within both bounds is decoded")
	}
	if name != "hephaestus-eof" {
		t.Errorf("headAgentName = %q; want %q", name, "hephaestus-eof")
	}
	if v := edgeVisits(t, p); len(v) != 2 {
		t.Errorf("headScan visited %d records; want 2", len(v))
	}
}

func TestHeadProbeEdgeSoleUnterminatedAgentSettingIsDecoded(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{tokAgentSetting(t, "s")}, false)
	if agent, _ := edgeProbes(t, p); !agent {
		t.Errorf("isAgentTranscript = false; a lone agent-setting line with no trailing newline is decoded")
	}
}

func TestHeadProbeEdgeUnterminatedFinalAfterOverCapIsDecoded(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{edgeOverCap(t), tokAgentName(t, "hephaestus-tail")}, false)
	if _, name := edgeProbes(t, p); name != "hephaestus-tail" {
		t.Errorf("headAgentName = %q; want %q", name, "hephaestus-tail")
	}
}

func TestHeadProbeEdgeEmptyFileVisitsNothing(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", nil, false)
	if v := edgeVisits(t, p); len(v) != 0 {
		t.Errorf("headScan visited %d records on an empty file; want 0", len(v))
	}
	agent, name := edgeProbes(t, p)
	if agent || name != "" {
		t.Errorf("probes on empty file = (%v, %q); want (false, \"\")", agent, name)
	}
}

// --- undecodable lines ---

func TestHeadScanUndecodableLineIsVisitedAsZeroMarker(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{`{not json`, tokAgentName(t, "hephaestus-bad")}, true)
	v := edgeVisits(t, p)
	if len(v) != 2 {
		t.Fatalf("headScan visited %d records; want 2 (a decode failure is still visited)", len(v))
	}
	if v[0].Type != "" || v[0].AgentName != "" {
		t.Errorf("undecodable line visited as {Type:%q AgentName:%q}; want zero headMarker", v[0].Type, v[0].AgentName)
	}
	if _, name := edgeProbes(t, p); name != "hephaestus-bad" {
		t.Errorf("headAgentName = %q; want %q after an undecodable line", name, "hephaestus-bad")
	}
}

func TestHeadProbeEdgeUndecodableLinesCountAgainstRecordBound(t *testing.T) {
	var within, past []string
	for i := 0; i < agentMarkerLines-1; i++ {
		within = append(within, `garbage`)
	}
	past = append(append([]string{}, within...), `garbage`)
	within = append(within, tokAgentName(t, "hephaestus-g"))
	past = append(past, tokAgentName(t, "hephaestus-g"))
	pIn := edgeWrite(t, "in.jsonl", within, true)
	pPast := edgeWrite(t, "past.jsonl", past, true)
	if _, name := edgeProbes(t, pIn); name != "hephaestus-g" {
		t.Errorf("headAgentName = %q; marker on record agentMarkerLines after undecodable lines must be found", name)
	}
	if agent, name := edgeProbes(t, pPast); agent || name != "" {
		t.Errorf("probes = (%v, %q); marker on record agentMarkerLines+1 after undecodable lines must not be found", agent, name)
	}
}

// --- marker text inside prose ---

func TestHeadProbeEdgeMarkerTextInsideProseIsNotAMarker(t *testing.T) {
	prose := mineJSON(t, map[string]any{
		"type":    "user",
		"content": `see {"type":"agent-setting","agentSetting":"x"} and "agentName":"hephaestus-prose"`,
	})
	p := edgeWrite(t, "s.jsonl", []string{prose}, true)
	agent, name := edgeProbes(t, p)
	if agent {
		t.Errorf("isAgentTranscript = true; marker text inside a content string is not a marker")
	}
	if name != "" {
		t.Errorf("headAgentName = %q; marker text inside prose must not be returned", name)
	}
}

// --- sidecar path arm ---

func TestIsAgentTranscriptEdgeSubagentsPathArmNeverReadsFile(t *testing.T) {
	p := edgeWrite(t, filepath.Join("proj", "sess", "subagents", "agent-1.jsonl"), []string{tokAgentName(t, "zeus-myjob")}, true)
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.Close() // any read would now fail; the path arm must answer without one
	if !isAgentTranscript(f, p) {
		t.Errorf("isAgentTranscript = false; a file directly under a subagents/ dir is true by path alone")
	}
}

func TestIsAgentTranscriptEdgeNearMissSubagentsPathsUseContent(t *testing.T) {
	for _, rel := range []string{
		filepath.Join("proj", "subagents-x", "a.jsonl"),
		filepath.Join("proj", "subagents", "nested", "a.jsonl"),
		filepath.Join("proj", "Subagents", "a.jsonl"),
	} {
		p := edgeWrite(t, rel, []string{edgeFiller(t)}, true)
		if isAgentTranscript(edgeOpen(t, p), p) {
			t.Errorf("isAgentTranscript(%s) = true; only a parent dir named exactly subagents triggers the path arm", rel)
		}
	}
}

// --- agent-setting and the name probe ---

func TestIsAgentTranscriptEdgeAgentSettingBeforeLeadNameAnswersTrue(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{tokAgentSetting(t, "s"), tokAgentName(t, "zeus-myjob")}, true)
	if agent, _ := edgeProbes(t, p); !agent {
		t.Errorf("isAgentTranscript = false; the agent-setting record answers first and ends the scan")
	}
}

func TestIsAgentTranscriptEdgeLeadNameBeforeAgentSettingAnswersFalse(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{tokAgentName(t, "zeus-myjob"), tokAgentSetting(t, "s")}, true)
	if agent, _ := edgeProbes(t, p); agent {
		t.Errorf("isAgentTranscript = true; a lead agentName answers first (false) and ends the scan")
	}
}

func TestIsAgentTranscriptEdgeThreadLeadNameIsNotAgent(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{tokAgentName(t, "thread-myjob-ab12")}, true)
	if agent, _ := edgeProbes(t, p); agent {
		t.Errorf("isAgentTranscript = true; a thread-<slug>-<tid> name is a lead name")
	}
}

func TestHeadAgentNameEdgeAgentSettingDoesNotEndScan(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{tokAgentSetting(t, "s"), edgeOverCap(t), tokAgentName(t, "hephaestus-named")}, true)
	if name := headAgentName(edgeOpen(t, p)); name != "hephaestus-named" {
		t.Errorf("headAgentName = %q; an agent-setting record carries no name and must not end the scan", name)
	}
}

func TestHeadAgentNameEdgeReturnsLeadNameVerbatim(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{tokAgentName(t, "zeus-myjob"), tokAgentName(t, "hephaestus-second")}, true)
	if name := headAgentName(edgeOpen(t, p)); name != "zeus-myjob" {
		t.Errorf("headAgentName = %q; want the FIRST non-empty name %q", name, "zeus-myjob")
	}
}

func TestHeadAgentNameEdgeSkipsEmptyName(t *testing.T) {
	empty := mineJSON(t, map[string]any{"type": "agent-name", "agentName": "", "sessionId": "s"})
	p := edgeWrite(t, "s.jsonl", []string{empty, tokAgentName(t, "hephaestus-real")}, true)
	if name := headAgentName(edgeOpen(t, p)); name != "hephaestus-real" {
		t.Errorf("headAgentName = %q; an empty agentName is not an answer", name)
	}
	if agent, _ := edgeProbes(t, p); !agent {
		t.Errorf("isAgentTranscript = false; empty agentName must not answer, the later non-lead one must")
	}
}

// --- headScan stop and offset obligations ---

func TestHeadScanEdgeStopsAtFirstVisitReturningTrue(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, edgeFiller(t))
	}
	p := edgeWrite(t, "s.jsonl", lines, true)
	n := 0
	headScan(edgeOpen(t, p), func(headMarker) bool {
		n++
		return n == 3
	})
	if n != 3 {
		t.Errorf("visit called %d times; the scan must stop at the first true", n)
	}
}

func TestHeadProbeEdgeCallerOffsetUnchanged(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{edgeOverCap(t), tokAgentSetting(t, "s"), tokAgentName(t, "hephaestus-off")}, true)
	check := func(label string, run func(f *os.File)) {
		f := edgeOpen(t, p)
		if _, err := f.Seek(7, io.SeekStart); err != nil {
			t.Fatalf("seek: %v", err)
		}
		run(f)
		off, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			t.Fatalf("seek current: %v", err)
		}
		if off != 7 {
			t.Errorf("%s moved the caller's offset to %d; want 7", label, off)
		}
	}
	check("headScan", func(f *os.File) { headScan(f, func(headMarker) bool { return false }) })
	check("headAgentName", func(f *os.File) {
		if got := headAgentName(f); got != "hephaestus-off" {
			t.Errorf("headAgentName from offset 7 = %q; the scan reads from byte 0", got)
		}
	})
	check("isAgentTranscript", func(f *os.File) {
		if !isAgentTranscript(f, p) {
			t.Errorf("isAgentTranscript from offset 7 = false; the scan reads from byte 0")
		}
	})
}

func TestHeadScanReadErrorEndsSilently(t *testing.T) {
	p := edgeWrite(t, "s.jsonl", []string{tokAgentName(t, "hephaestus-closed")}, true)
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.Close()
	n := 0
	headScan(f, func(headMarker) bool { n++; return false })
	if n != 0 {
		t.Errorf("headScan on a closed file visited %d records; a read error ends the scan silently", n)
	}
	if name := headAgentName(f); name != "" {
		t.Errorf("headAgentName on a closed file = %q; want \"\"", name)
	}
	if isAgentTranscript(f, p) {
		t.Errorf("isAgentTranscript on a closed non-sidecar file = true; want false")
	}
}
