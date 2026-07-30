package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kraken/internal/util"
)

// mine.go — the mechanical read of claude's own transcripts, for the nightly
// sweep. Two entry points, both pure readers of files another program owns:
// MineTranscripts (one digest per transcript touched in the window — counts
// and signals only, cheap enough to run over every project dir) and
// ReduceTranscript (the ~13% of one transcript that carries a decision:
// user prose, tool calls, assistant text — what a per-transcript scout
// actually reads).
//
// Everything here is defensive by contract, the same rule the rest of core
// applies to claude-owned data: an unreadable file, a malformed line, a
// truncated final line, a missing field all skip that line or that file and
// the sweep keeps going. MineTranscripts errors only when the walk itself is
// impossible.
//
// Size discipline: transcripts are appended forever and a 42 MB file exists
// on this machine, with single LINES seen at 8.3 MB (a tool result inlined
// whole). Every read here streams with a scanner sized for that — never
// io.ReadAll, which readPermissionMode in branch.go does and which is the
// counter-example, not the model.

// mineScanBufMax is the scanner ceiling for a single transcript line, sized
// like scanPendingQuestion's in internal/jobs: bufio's 64 KB default silently
// ends the scan at the first oversized line, which on a real transcript means
// dropping everything after the first big inlined tool result.
const mineScanBufMax = 16 << 20

// mineScanBufInit is the starting buffer — most lines are well under it, so
// the 16 MB ceiling is a ceiling and not an allocation.
const mineScanBufInit = 64 * 1024

// krakenCellMarker identifies a project dir belonging to a kraken cell
// worktree (~/.kraken/cells/<slug>/wt-<repo>, munged by projectDirName).
//
// A kraken CELL is not the same thing as kraken CONTENT: cells target other
// repos too, so Category "kraken" means "this session ran inside a cell",
// never "this session was about the kraken codebase". The distinction matters
// to anything downstream that wants to reason about kraken's own development
// rather than about the harness a session ran under.
const krakenCellMarker = "-kraken-cells-"

// SessionDigest is the mechanical read of one transcript.
type SessionDigest struct {
	// SessionID is the file's stem: the session uuid for a root transcript,
	// "agent-<id>" for a subagent sidecar (whose in-file sessionId is the
	// PARENT's, and so would collide across every sidecar of one session).
	// Path is the identity that is always unique.
	SessionID       string
	Path            string
	ProjectDir      string // the munged dir name
	Category        string // "kraken" (a kraken cell dir) or "other"
	Subagent        bool   // true for a subagents/agent-*.jsonl sidecar
	Lines           int    // TIMESTAMPED records inside the window that parsed
	FirstTS, LastTS int64  // unix millis, from in-file record timestamps
	// Truncated marks a digest built from only PART of its file: the scan
	// hit a line over mineScanBufMax or a mid-file read error and stopped
	// there. Every count below is then a floor, not a total. This must never
	// be silent — a session that inlines a 20 MB tool result would otherwise
	// report 12 tool calls instead of 400 and zero errors instead of nine,
	// and the night would read clean. Same failure and same fix as
	// scanPendingQuestion's in internal/jobs/status.go.
	Truncated bool
	// In/Out/CacheWrite/CacheRead are the session's four token counters
	// (tokens.go's Tokens, flattened), summed from the `usage` object on
	// every assistant record inside the window. They ride along in
	// digestTranscript's existing forward pass — it already decodes every
	// one of those records — so they cost four adds and no extra I/O.
	// Truncated applies to them exactly as it does to the counts below:
	// a partial scan makes them a floor.
	In, Out, CacheWrite, CacheRead int64
	ToolCounts                     map[string]int // tool name -> calls
	ErrorCounts                    map[string]int // tool name -> tool_results with is_error true
	Interrupts                     int            // "[Request interrupted" user turns
	SyntheticErrors                int            // "<synthetic>" assistant records
	TurnDurations                  []int64        // system/turn_duration durationMs
	RepeatedCalls                  map[string]int // identical tool-call signature -> count, only entries > 1
}

// MineResult is one night's mechanical sweep.
type MineResult struct {
	Since   int64
	Digests []SessionDigest
	Scanned int // files actually parsed
	Skipped int // candidates skipped (mtime outside window, unreadable, …)
	// Truncated counts the Scanned files whose digest is partial (see
	// SessionDigest.Truncated). Non-zero means the sweep did NOT see
	// everything in the window — the one number that separates a quiet night
	// from a half-read one.
	Truncated int
	// Tokens is the four counters summed across every digest in Digests,
	// with Sessions the number of those digests. It is what lets the nightly
	// pass rank a night's waste by real spend rather than by tool-call count.
	// A non-zero Truncated above makes it a floor, for the same reason it
	// makes every other count one.
	Tokens Tokens
}

// MineTranscripts walks ~/.claude/projects for transcripts touched at or
// after since (unix millis) and returns one digest per file.
//
// File mtime only SELECTS candidates; the per-record timestamp is what bounds
// the window. A long-running session's file has an mtime inside the window
// while its records stretch back days (a single observed turn ran 92
// minutes), so a file is opened whenever it was touched recently and each
// record inside it is then admitted or excluded on its own timestamp.
//
// Both root transcripts and subagent sidecars
// (<project>/<session-id>/subagents/agent-<id>.jsonl) are included: sidecars
// hold ~24% of all tool calls, and on current transcripts every sidechain
// record lives there rather than in the root file, so excluding them would
// silently drop a quarter of the evidence.
//
// Returns an error only when the walk itself is impossible (no projects
// directory at all). Every per-file failure is a Skipped, never an error.
func MineTranscripts(since int64) (*MineResult, error) {
	root := filepath.Join(util.Home(), ".claude", "projects")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read projects dir: %v", err)
	}

	res := &MineResult{Since: since}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		project := e.Name()
		// WalkDir rather than ReadDir: a project dir holds its root
		// transcripts at the top level and its sidecars two levels down
		// under <session-id>/subagents/, and a walk covers both without
		// hardcoding that shape (which claude owns and may extend).
		_ = filepath.WalkDir(filepath.Join(root, project), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable subtree: skip it, keep sweeping
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				res.Skipped++
				return nil
			}
			if info.ModTime().UnixMilli() < since {
				res.Skipped++
				return nil
			}
			dg, ok := digestTranscript(path, project, since)
			if !ok || digestEmpty(dg) {
				// A file touched inside the window whose every record
				// predates it (the 92-minute-turn shape, seen from the other
				// side) yields a row with no counts and no timespan. Counting
				// it as Scanned would overstate coverage with content-free
				// rows, so it lands in Skipped where the other
				// nothing-to-see candidates are.
				res.Skipped++
				return nil
			}
			res.Scanned++
			if dg.Truncated {
				res.Truncated++
			}
			res.Tokens.In += dg.In
			res.Tokens.Out += dg.Out
			res.Tokens.CacheWrite += dg.CacheWrite
			res.Tokens.CacheRead += dg.CacheRead
			res.Tokens.Sessions++
			res.Digests = append(res.Digests, dg)
			return nil
		})
	}
	sort.Slice(res.Digests, func(i, j int) bool { return res.Digests[i].Path < res.Digests[j].Path })
	return res, nil
}

// digestEmpty reports a digest that says nothing at all — no dated record in
// the window and not one signal. Truncated is checked too: a partial scan is
// itself the loudest signal there is, so a truncated digest is never empty
// however few counts it managed to collect.
func digestEmpty(dg SessionDigest) bool {
	return dg.Lines == 0 && !dg.Truncated &&
		len(dg.ToolCounts) == 0 && len(dg.ErrorCounts) == 0 &&
		dg.Interrupts == 0 && dg.SyntheticErrors == 0 && len(dg.TurnDurations) == 0
}

// mineRecord is the slice of a transcript line the digest cares about.
// Everything else in a real record is ignored by virtue of not being here;
// content stays raw so a multi-MB tool result is only decoded when the record
// type actually calls for it.
type mineRecord struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	DurationMs int64  `json:"durationMs"`
	Timestamp  string `json:"timestamp"`
	// SessionID is `session_id` (SNAKE case): the session that actually made
	// the API call. Not `sessionId`, which a fork rewrites on every replayed
	// record. See tokenRecordOwned (tokens.go).
	SessionID string `json:"session_id"`
	Message   struct {
		// ID is the API message id — one message spans N records, so this is
		// what collapses them to one. NOT all with the same usage: three of
		// the four counters repeat verbatim, `output_tokens` streams up across
		// a sidecar's group. tokenGroup.commit (tokens.go) is the rule.
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		// Usage is claude's own per-assistant-record token report. Shared
		// verbatim with tokens.go's sum (same type, one transcription of
		// the four wire names) so the digest and the per-job total can
		// never disagree about what a field is called.
		Usage tokenUsage `json:"usage"`
	} `json:"message"`
}

// mineBlock covers both content-block shapes a message's content array can
// hold — a tool_use ({type,id,name,input}) or a tool_result
// ({type,tool_use_id,is_error}) — plus the plain text part both roles use.
// Fields absent from whichever shape a block actually is decode to zero.
type mineBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Text      string          `json:"text"`
}

// interruptPrefix is the harness artifact a user turn opens with when the
// human pressed escape mid-turn.
const interruptPrefix = "[Request interrupted"

// digestTranscript streams one transcript and returns its digest. ok=false
// only when the file can't be opened — a malformed line and a truncated final
// line both cost only themselves.
//
// A line over the scanner ceiling, or a mid-file read error, is different in
// kind: it ends the scan, so everything after it is invisible. That leaves a
// partial digest standing (worth more than nothing) but marks it
// dg.Truncated, which MineTranscripts counts, because a partial digest that
// LOOKS complete is worse than no digest at all.
func digestTranscript(path, project string, since int64) (SessionDigest, bool) {
	f, err := os.Open(path)
	if err != nil {
		return SessionDigest{}, false
	}
	defer f.Close()

	category := "other"
	if strings.Contains(project, krakenCellMarker) {
		category = "kraken"
	}
	dg := SessionDigest{
		SessionID:     strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Path:          path,
		ProjectDir:    project,
		Category:      category,
		Subagent:      filepath.Base(filepath.Dir(path)) == "subagents",
		ToolCounts:    map[string]int{},
		ErrorCounts:   map[string]int{},
		RepeatedCalls: map[string]int{},
	}

	// pending correlates tool_use -> tool_result by id in ONE forward pass
	// (a tool_use always precedes its result in the file), so an is_error can
	// be attributed to the tool that actually failed. It is filled for
	// out-of-window records too, and only the COUNTING is gated on the
	// window: a call made just before `since` whose result lands inside it
	// would otherwise lose its name.
	pending := map[string]string{}

	// group is the tracker for the token counters below — the same tokenGroup
	// SumTokensDelta uses (tokens.go), and deliberately the same type rather
	// than a second pair of locals, so the two readers of one file cannot
	// drift on what "one message" contributes. It carries the previous
	// assistant record's message id, so the N records one API message is split
	// across contribute their shared usage exactly once, AND how much of that
	// message's output_tokens has already been committed, because that one
	// counter is a streaming partial on the early records of a sidecar's group
	// rather than a verbatim repeat. tokenGroup states the measurement.
	var group tokenGroup

	// own is the session this FILE belongs to, which for a sidecar is its
	// parent's rather than its `agent-<id>` stem — the same derivation
	// SumTokensDelta uses, so the two readers cannot disagree about which
	// records are replayed.
	own := fileSessionID(path, filepath.Base(path))

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, mineScanBufInit), mineScanBufMax)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec mineRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// A wrongly-TYPED field is not a malformed line: encoding/json
			// fills in everything it could and reports the type error at the
			// end, so discarding the record would throw away every good field
			// beside the bad one. addTokenLine (tokens.go) already makes that
			// distinction, and the two must agree — otherwise the board's
			// per-job number and this digest's per-session number differ by
			// exactly the records with one bad field, on rules that are
			// otherwise identical.
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				continue // malformed or truncated line: skip it, keep the rest
			}
		}
		ts := isoMillis(rec.Timestamp)
		// A record with no parseable timestamp (custom-title, agent-name,
		// mode, permission-mode, bridge-session, last-prompt, agent-setting —
		// about half of all records) can't be dated and so can't be excluded:
		// it stays eligible for its signals rather than being dropped on a
		// technicality. It does NOT count toward Lines, which is exactly the
		// dated-and-inside-the-window population FirstTS/LastTS bound, so a
		// file with nothing in the window reads Lines == 0 rather than
		// "Lines > 0 with a zero timespan".
		inWindow := ts == 0 || ts >= since
		if ts > 0 && inWindow {
			dg.Lines++
			if dg.FirstTS == 0 || ts < dg.FirstTS {
				dg.FirstTS = ts
			}
			if ts > dg.LastTS {
				dg.LastTS = ts
			}
		}

		switch rec.Type {
		case "assistant":
			if inWindow && rec.Message.Model == "<synthetic>" {
				// an API failure the harness gave up retrying
				dg.SyntheticErrors++
			}
			// Token counters, through tokenGroup.commit (tokens.go) — the ONE
			// definition of what a message contributes, shared verbatim with
			// the per-job sum. It used to be a second copy of the rule written
			// inline right here, and that duplication is precisely why a
			// premise false of every sidecar ever written read as ground
			// truth: the two readers agreed with each other and both disagreed
			// with the transcripts. A naive per-record sum over-counts by ~2x;
			// a first-wins one under-counts a subagent-heavy session's output
			// by 6.5x, which this digest then feeds to the dream loop as a
			// session RANKING. Do not re-inline it.
			//
			// This digest always reads a whole file, so unlike the incremental
			// sum it needs no boundary seed.
			//
			// The eligibility gate is the window AND ownership — the window
			// because every other count here is gated on it, so a digest's
			// spend describes the same records its tool counts and its
			// FirstTS/LastTS span do; ownership because a record replayed in
			// from a forked parent is not this session's spend. commit zeroes
			// the group's committed output for an ineligible record, which is
			// what makes a group STRADDLING `since` add up: it contributes its
			// output (in the window its final record falls in, where a
			// cumulative figure belongs) but not its other three. The
			// alternative — the pre-fix behaviour — dropped all four, and half
			// a straddling group is closer than none of it. Groups span
			// milliseconds and `since` is a day boundary, so the shape
			// essentially never occurs either way.
			d := group.commit(rec.Message.ID, rec.Message.Usage,
				inWindow && tokenRecordOwned(tokenRecord{SessionID: rec.SessionID}, own))
			dg.In += d.Input
			dg.Out += d.Output
			dg.CacheWrite += d.CacheCreation
			dg.CacheRead += d.CacheRead
			for _, b := range mineBlocks(rec.Message.Content) {
				if b.Type != "tool_use" || b.Name == "" {
					continue
				}
				if b.ID != "" {
					pending[b.ID] = b.Name
				}
				if !inWindow {
					continue
				}
				dg.ToolCounts[b.Name]++
				dg.RepeatedCalls[callSignature(b.Name, b.Input)]++
			}
		case "user":
			if text, ok := mineUserString(rec.Message.Content); ok {
				if inWindow && strings.HasPrefix(strings.TrimSpace(text), interruptPrefix) {
					dg.Interrupts++
				}
				continue
			}
			for _, b := range mineBlocks(rec.Message.Content) {
				switch b.Type {
				case "text":
					if inWindow && strings.HasPrefix(strings.TrimSpace(b.Text), interruptPrefix) {
						dg.Interrupts++
					}
				case "tool_result":
					name, known := pending[b.ToolUseID]
					delete(pending, b.ToolUseID)
					if !inWindow || !b.IsError {
						continue
					}
					if !known {
						// result whose call was never seen (transcript
						// forked mid-call, or the call line was malformed):
						// the failure is real, only its attribution is lost
						name = "unknown"
					}
					dg.ErrorCounts[name]++
				}
			}
		case "system":
			if inWindow && rec.Subtype == "turn_duration" && rec.DurationMs > 0 {
				dg.TurnDurations = append(dg.TurnDurations, rec.DurationMs)
			}
		}
	}

	// The one check that cannot be skipped: sc.Scan() returning false means
	// EITHER end of file OR a line over the buffer ceiling / a read error,
	// and those are indistinguishable without asking.
	if sc.Err() != nil {
		dg.Truncated = true
	}

	for sig, n := range dg.RepeatedCalls {
		if n < 2 { // "repeated" means repeated: a one-off call is not a signal
			delete(dg.RepeatedCalls, sig)
		}
	}
	return dg, true
}

// mineBlocks decodes a message's content array into blocks, tolerating both
// the plain-string shape (which yields none) and individual parts that fail
// to decode.
func mineBlocks(content json.RawMessage) []mineBlock {
	if len(content) == 0 {
		return nil
	}
	var raws []json.RawMessage
	if json.Unmarshal(content, &raws) != nil {
		return nil
	}
	blocks := make([]mineBlock, 0, len(raws))
	for _, raw := range raws {
		var b mineBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		blocks = append(blocks, b)
	}
	return blocks
}

// mineUserString reports the plain-string form of a user record's content
// (the shape a typed message takes); ok=false means it was the parts array
// instead.
func mineUserString(content json.RawMessage) (string, bool) {
	if len(content) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(content, &s) != nil {
		return "", false
	}
	return s, true
}

// callSignature is the identity a RepeatedCalls entry counts: the tool name
// plus the same short input summary `k r` renders a tool block with
// (summarizeInput's first-present-key rule, truncated). Two calls with the
// same signature are the same call as far as a reader is concerned — the
// same command re-run, the same file re-read — which is the signal, and the
// truncation deliberately makes near-identical long inputs collapse together
// rather than each looking unique.
func callSignature(name string, input json.RawMessage) string {
	return name + "(" + mineInputSummary(input) + ")"
}

// mineSummaryKeys are the input keys that identify a call, in the priority
// order summarizeInput uses — same list, same order, so a signature here
// reads identically to the tool line `k r` prints.
type mineSummaryKeys struct {
	Command     string `json:"command"`
	FilePath    string `json:"file_path"`
	Path        string `json:"path"`
	Pattern     string `json:"pattern"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Query       string `json:"query"`
	URL         string `json:"url"`
	Skill       string `json:"skill"`
}

// mineRawSummaryCap bounds the bytes a key-less input contributes to a
// signature. TruncateOneLine cuts to 100 runes anyway, so anything past this
// could only ever be thrown away — but converting the raw first would copy
// the whole thing to get there.
const mineRawSummaryCap = 4096

// mineInputSummary is summarizeInput's rule applied to a still-encoded tool
// input, deliberately WITHOUT materialising it. summarizeInput takes a
// map[string]any, and the digest is the one caller that would have to build
// one per tool call: a single Write carries its whole file body in `content`,
// so decoding into a map allocates megabytes to read a `file_path` beside it
// — ~11.7k times over a night's corpus, the digest's dominant cost.
// Decoding into a narrow struct skips every unrecognised key without
// allocating it.
//
// A key present with a non-string value yields an UnmarshalTypeError, which
// encoding/json returns only AFTER decoding the rest — so the error is
// deliberately ignored and the fields that did decode are used, matching
// summarizeInput's type-assertion skip.
func mineInputSummary(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var k mineSummaryKeys
	_ = json.Unmarshal(input, &k)
	for _, s := range []string{k.Command, k.FilePath, k.Path, k.Pattern, k.Description, k.Prompt, k.Query, k.URL, k.Skill} {
		if s != "" {
			return TruncateOneLine(s, 100)
		}
	}
	raw := input
	if len(raw) > mineRawSummaryCap {
		raw = raw[:mineRawSummaryCap] // TruncateOneLine keeps only 100 runes of it
	}
	return TruncateOneLine(string(raw), 100)
}

// ReduceTranscript returns the decision-relevant view of one transcript:
// user prose, tool calls and assistant text, with thinking, tool results
// and attachments dropped. This is what a per-transcript scout reads.
//
// Output is line-oriented and turn-ordered, one labelled section per block:
//
//	[user]
//	go fix the flaky test
//
//	[assistant]
//	Found it — the fixture races on mtime.
//
//	[tool] Bash(go test ./internal/core/...)
//
//	[teammate zeus-demo]
//	implement the slice described in …
//
// The [user] label means CARL, and only carl. A message another agent sent
// into this session — every brief zeus hands hephaestus/athena/hermes, every
// idle heartbeat — arrives inside a user record wrapped in a
// <teammate-message> envelope, and is labelled [teammate <id>] instead. This
// is not cosmetic: a reader of this output is asked what carl's own prompting
// did to an agent, and mislabelling would quote KRAKEN's prompts back to him
// as his own — confidently wrong about the one thing the reduction exists to
// say. (Measured over 300 real transcripts: teammate traffic is 29% of user
// blocks and 38% of their bytes.) Detection reuses read.go's teammateBlock,
// deliberately rather than a second regex — a divergent copy of that
// envelope rule is exactly how this went wrong once — which also collapses
// idle-poll heartbeats to a single "idle" line.
//
// The dropped mass is tool RESULTS (whole files, whole command outputs,
// inlined attachments) plus thinking, none of which a reader needs to
// reconstruct what was decided — the call and its name say what was
// attempted, and the next assistant text says how it went. Measured over a
// real 24h sweep on this machine: 118 MB of transcript across 178 files
// reduced to 3.2 MB (2.7%), against ~13% for a whole-file reduction — the
// extra shrink is the since filter dropping the pre-window history a
// long-lived session's file carries.
//
// Records older than since (unix millis) are excluded, on the same
// per-record-timestamp rule MineTranscripts applies. Errors only when the
// file can't be opened; malformed lines are skipped.
func ReduceTranscript(path string, since int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %v", err)
	}
	defer f.Close()

	var out bytes.Buffer
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, mineScanBufInit), mineScanBufMax)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec mineRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.Type != "assistant" && rec.Type != "user" {
			continue // mode, attachment, file-history, system bookkeeping
		}
		if ts := isoMillis(rec.Timestamp); ts > 0 && ts < since {
			continue
		}
		if rec.Type == "user" {
			if text, ok := mineUserString(rec.Message.Content); ok {
				writeUserish(&out, text)
				continue
			}
			for _, b := range mineBlocks(rec.Message.Content) {
				if b.Type == "text" {
					writeUserish(&out, b.Text)
				}
				// tool_result blocks are the dropped mass — the call above
				// already named what was attempted.
			}
			continue
		}
		for _, b := range mineBlocks(rec.Message.Content) {
			switch b.Type {
			case "text":
				writeReduced(&out, "[assistant]", b.Text)
			case "tool_use":
				if b.Name != "" {
					out.WriteString("[tool] " + callSignature(b.Name, b.Input) + "\n\n")
				}
			}
			// "thinking" falls through: dropped.
		}
	}
	// Fail closed on a truncated scan. A scout handed the first 3% of a
	// session with no error would reason about it as if it were the whole
	// thing, and nothing downstream could tell. Returning nil bytes makes
	// that impossible to ignore even for a caller that drops the error.
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read transcript %s: %v", path, err)
	}
	return out.Bytes(), nil
}

// writeUserish appends one text part of a user record under the label that
// says who actually spoke: [teammate <id>] when it carries another agent's
// message envelope, [user] — meaning carl — otherwise. This is userBlocks'
// full treatment, both halves of it: the <system-reminder> drop AND the
// teammate routing, via the same teammateBlock read.go's own reader uses.
func writeUserish(out *bytes.Buffer, text string) {
	if b, ok := teammateBlock(text); ok {
		writeReduced(out, "[teammate "+b.From+"]", b.Text)
		return
	}
	writeReduced(out, "[user]", text)
}

// mineTextCap bounds one emitted section. Tool inputs were already capped at
// 100 runes while text blocks were bounded by nothing at all — the same class
// of hazard as an unchecked scanner ceiling, just slower to bite: the largest
// real block today is 54 KB, but one 1.1 MB text line would put 600 KB into a
// scout's payload on its own. Set well above any real message so it never
// touches genuine prose, and marked when it fires so a reader is never
// silently handed a fragment.
const mineTextCap = 64 * 1024

// writeReduced appends one labelled section, skipping blanks and the
// harness-injected <system-reminder> parts that ride along in user records
// but are not the user speaking.
func writeReduced(out *bytes.Buffer, label, text string) {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "<system-reminder>") {
		return
	}
	out.WriteString(label + "\n")
	if r := []rune(t); len(r) > mineTextCap {
		out.WriteString(string(r[:mineTextCap]))
		fmt.Fprintf(out, "\n… [%d more characters elided]", len(r)-mineTextCap)
	} else {
		out.WriteString(t)
	}
	out.WriteString("\n\n")
}
