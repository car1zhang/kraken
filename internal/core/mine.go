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
	"regexp"
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
	// MaxTokensStops counts MESSAGES stopped at stop_reason "max_tokens":
	// the agent hit its own output ceiling mid-message. It is the one failure
	// mode invisible to every other counter here — not an is_error, not an
	// interrupt, not a <synthetic> record — so a ceiling stop inside a
	// thinking block leaves no deliverable behind while its lead waits on a
	// file that is never coming, and the most expensive failure of a night
	// otherwise reads as a perfectly clean session. Messages, never records:
	// stop_reason is stamped on EVERY record of a message's group exactly
	// like the three verbatim usage counters (measured 2026-07-31: 126
	// max_tokens records on this machine collapse to 45 distinct messages,
	// with single messages spanning up to 9 records — a per-record count
	// inflates 2.80x overall and lands hardest on the messages that emitted
	// the most tool_use blocks, inverting the ranking this digest feeds).
	MaxTokensStops int
	TurnDurations  []int64        // system/turn_duration durationMs
	RepeatedCalls  map[string]int // re-issued tool calls, grouped by callIdentity (see its residuals) -> count, only entries > 1
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
		dg.Interrupts == 0 && dg.SyntheticErrors == 0 && dg.MaxTokensStops == 0 &&
		len(dg.TurnDurations) == 0
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
	// Operation/Content are the `queue-operation` record's fields, and
	// Attachment is the `attachment` record's payload: the two shapes a
	// message typed MID-TURN is stored as (it never becomes a `user` record).
	// reducer.line reads them; everything else ignores them.
	Operation  string `json:"operation"`
	Content    string `json:"content"`
	Attachment struct {
		Type   string `json:"type"`
		Prompt string `json:"prompt"`
	} `json:"attachment"`
	Message struct {
		// ID is the API message id — one message spans N records, so this is
		// what collapses them to one. NOT all with the same usage: three of
		// the four counters repeat verbatim, `output_tokens` streams up across
		// a sidecar's group. tokenGroup.commit (tokens.go) is the rule.
		ID    string `json:"id"`
		Model string `json:"model"`
		// StopReason is why the API stopped emitting: "max_tokens" here means
		// the message was cut at the output ceiling, counted as MaxTokensStops.
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
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

	// lastMaxTokID dedupes MaxTokensStops to one count per message: every
	// record of a ceiling-stopped group carries the same stop_reason, and
	// counting each would be the per-record over-count tokenGroup exists to
	// prevent. Updated only inside the window so a group straddling `since`
	// still counts once (in the window its later records fall in) rather
	// than zero.
	var lastMaxTokID string

	// lastSID carries the most recent non-empty session_id forward, because
	// ownership is stamped unevenly by record TYPE: system/turn_duration
	// records carry NO session_id at all (0 of ~1,700 measured), and a small
	// share of interrupt-bearing user records drop it too — so testing each
	// record's own field left exactly those signals ungated on a fork, while
	// the fixture that "covered" them stamped a session_id real records never
	// have. A record with no session_id inherits its neighborhood's owner.
	// Replay regions are contiguous runs of foreign-session_id records
	// (though not always a prefix — both FFF…OOO and OOO…FFF occur), so
	// positional inheritance assigns each undated-owner record correctly;
	// a sidecar carries no session_id anywhere, lastSID stays "", and
	// tokenRecordOwned treats absent as own — unchanged.
	var lastSID string

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
		// counted gates every COUNTED signal, mechanical and token alike, on
		// the window AND ownership. A forked side thread's transcript is
		// seeded with its parent's records wholesale — tool calls, errors,
		// interrupts, durations, all of it — and gating only the token
		// counters (the pre-fix state) made a resume replay double every
		// mechanical count while its spend stayed correct: one real cell's
		// digest reported 26 tool errors where 16 exist, the other 10 being
		// the parent's errors counted a second time through the replay.
		// Lines/FirstTS/LastTS deliberately stay window-only: they bound the
		// FILE's dated window, not the session's own work, which also means
		// digestEmpty can read a fork's all-replayed window as non-empty —
		// the honest shape, since the file does hold dated records.
		sid := rec.SessionID
		if sid == "" {
			sid = lastSID
		} else {
			lastSID = sid
		}
		counted := inWindow && tokenRecordOwned(tokenRecord{SessionID: sid}, own)
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
			if counted && rec.Message.Model == "<synthetic>" {
				// an API failure the harness gave up retrying
				dg.SyntheticErrors++
			}
			if counted && rec.Message.StopReason == "max_tokens" &&
				(rec.Message.ID == "" || rec.Message.ID != lastMaxTokID) {
				// output ceiling hit — once per message, see MaxTokensStops
				dg.MaxTokensStops++
				lastMaxTokID = rec.Message.ID
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
			// The eligibility gate is `counted` — the window because every
			// other count here shares it, so a digest's spend describes the
			// same records its tool counts do (the FirstTS/LastTS span stays
			// window-only; see the counted comment above); ownership because
			// a record replayed in from a forked parent is not this
			// session's spend. commit zeroes
			// the group's committed output for an ineligible record, which is
			// what makes a group STRADDLING `since` add up: it contributes its
			// output (in the window its final record falls in, where a
			// cumulative figure belongs) but not its other three. The
			// alternative — the pre-fix behaviour — dropped all four, and half
			// a straddling group is closer than none of it. Groups span
			// milliseconds and `since` is a day boundary, so the shape
			// essentially never occurs either way.
			d := group.commit(rec.Message.ID, rec.Message.Usage, counted)
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
				if !counted {
					continue
				}
				dg.ToolCounts[b.Name]++
				dg.RepeatedCalls[callIdentity(b.Name, b.Input)]++
			}
		case "user":
			if text, ok := mineUserString(rec.Message.Content); ok {
				if counted && strings.HasPrefix(strings.TrimSpace(text), interruptPrefix) {
					dg.Interrupts++
				}
				continue
			}
			for _, b := range mineBlocks(rec.Message.Content) {
				switch b.Type {
				case "text":
					if counted && strings.HasPrefix(strings.TrimSpace(b.Text), interruptPrefix) {
						dg.Interrupts++
					}
				case "tool_result":
					name, known := pending[b.ToolUseID]
					delete(pending, b.ToolUseID)
					if !counted || !b.IsError {
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
			if counted && rec.Subtype == "turn_duration" && rec.DurationMs > 0 {
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

// mineDisplayWindow is the display truncation `k r` uses for a tool line;
// callSignature and the reduced view's [tool] lines keep to it.
const mineDisplayWindow = 100

// callSignature is the DISPLAY form of a tool call: the tool name plus the
// same short input summary `k r` renders a tool block with (summarizeInput's
// first-present-key rule, truncated to mineDisplayWindow runes). The reduced
// view's [tool] lines use it; RepeatedCalls does NOT — grouping on the
// display window collapsed distinct calls whenever their inputs shared a
// 100-rune prefix, which cell work makes routine (scratchpad paths and
// staged-transcript paths run past 100 runes before they differ, and
// analysis heredocs share their import preamble), so the counter reported
// clusters of 9-11 where zero literal repeats existed. That false positive
// was measured independently by three separate readers before the grouping
// moved to callIdentity below.
func callSignature(name string, input json.RawMessage) string {
	return name + "(" + mineInputSummary(input) + ")"
}

// mineIdentityWindow bounds a RepeatedCalls grouping key. It exists to keep
// map keys and digest JSON bounded, not to collapse anything: every measured
// false-positive cluster came from prefixes shared for 100-200 runes, so a
// 1000-rune window is an order of magnitude past where real distinct inputs
// diverge, while a genuinely identical re-run still matches whole.
const mineIdentityWindow = 1000

// callIdentity is the identity a RepeatedCalls entry counts: the same
// first-present-key rule as callSignature, but over a window wide enough
// that a cluster nearly always means the same call re-issued — the same
// command re-run, the same file re-read. Two deliberate residuals keep
// "nearly": commands differing ONLY in stripped boilerplate (a cd prefix,
// an env assignment) still fold, which is the strip working as intended;
// and the first-present-key rule keys some tools on a field that never
// distinguishes them at any width — Grep's `path` precedes `pattern`, so N
// different searches under one directory read as one entry, and Agent's
// short `description` precedes `prompt` — so a cluster on those tools says
// "same key", not "same call".
func callIdentity(name string, input json.RawMessage) string {
	return name + "(" + mineInputSummaryN(input, mineIdentityWindow) + ")"
}

// mineSummaryKeys are the input keys that identify a call, in the priority
// order summarizeInput uses — same list, same order, so a signature that
// lands on one of these keys reads identically to the tool line `k r`
// prints, with one deliberate exception: Bash, where mineInputSummary
// strips leading shell boilerplate that `k r` shows as typed (the comment
// inside it says why). The key-less raw fallback can also differ — raw
// transcript bytes here, a re-marshalled map there.
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

// shellBoilerplateRE matches the boilerplate a Bash command can open with —
// any run of `VAR=value;` assignments and `cd <path>` fragments terminated by
// `;`, `&&` or a NEWLINE — which mineInputSummaryN strips so a signature
// window (mineDisplayWindow for display, mineIdentityWindow for grouping)
// lands on the command itself rather than on a path prefix longer than the
// display window. Three deliberate bounds, each measured against the
// full command corpus: the value/path classes exclude WHITESPACE, so an
// env-prefix form (`HOME=/tmp/x go test …`) is one command and never
// stripped; the newline is a terminator, because `cd <path>\n<cmd>` is the
// dominant real shape and has no other separator; and a first draft whose
// classes admitted whitespace ate through 591 real commands to whatever
// followed the first `;` — collapsing distinct commands into one signature,
// the exact false positive this strip exists to remove. A quoted separator
// in a value (`S="a;b"; cmd`) still mangles the head cosmetically; distinct
// values stay distinct, so nothing collapses.
var shellBoilerplateRE = regexp.MustCompile(`^(?:[ \t]*(?:[A-Za-z_][A-Za-z0-9_]*=[^\s;&|]*[ \t]*(?:;|\n)|cd[ \t]+[^\s;&|]+[ \t]*(?:;|&&|\n))\s*)+`)

// mineRawSummaryCap bounds the bytes a key-less input contributes to a
// signature, so converting the raw never copies a multi-megabyte input to
// keep a window of it. It must stay comfortably above the WIDEST caller's
// window in worst-case UTF-8 (mineIdentityWindow runes at up to 4 bytes
// each, and TruncateOneLine collapses whitespace runs before counting, so
// bytes overestimate runes): 8192 bytes leaves a 2x margin where 4096 left
// 1.02x, and below the margin two distinct key-less inputs could collide
// again — the exact defect callIdentity exists to remove.
const mineRawSummaryCap = 8192

// mineInputSummary is the display-window form of mineInputSummaryN — the
// one callers use for anything a person reads.
func mineInputSummary(input json.RawMessage) string {
	return mineInputSummaryN(input, mineDisplayWindow)
}

// mineInputSummaryN is summarizeInput's rule applied to a still-encoded tool
// input, deliberately WITHOUT materialising it, with the truncation window
// as a parameter (mineDisplayWindow for display, mineIdentityWindow for
// RepeatedCalls grouping — one rule, two windows, so the display form and
// the grouping identity cannot drift apart in WHAT they summarize, only in
// how much of it they keep). summarizeInput takes a map[string]any, and the
// digest is the one caller that would have to build one per tool call: a
// single Write carries its whole file body in `content`, so decoding into a
// map allocates megabytes to read a `file_path` beside it — ~11.7k times
// over a night's corpus, the digest's dominant cost. Decoding into a narrow
// struct skips every unrecognised key without allocating it.
//
// A key present with a non-string value yields an UnmarshalTypeError, which
// encoding/json returns only AFTER decoding the rest — so the error is
// deliberately ignored and the fields that did decode are used, matching
// summarizeInput's type-assertion skip.
func mineInputSummaryN(input json.RawMessage, window int) string {
	if len(input) == 0 {
		return ""
	}
	var k mineSummaryKeys
	_ = json.Unmarshal(input, &k)
	if k.Command != "" {
		// Strip leading shell boilerplate before the truncation window. Cell
		// scratchpad paths run well past the display window, so a command
		// opening with `S=<scratchpad>; ` or `cd <worktree> && ` spends the
		// WHOLE signature on the prefix — zero command bytes reach it, and
		// every such call collapses into one bucket: one real session's 19
		// distinct analysis commands digested as one call "repeated" 19
		// times — the loop-shaped false positive callIdentity's wider window
		// now removes for prefixes under it; the strip is what removes it at
		// the display width and past the window. This is also where the digest's
		// Bash signature deliberately diverges from summarizeInput's tool
		// line (`k r` shows the command as typed; the digest signature wants
		// distinct commands distinct). Stripping reveals the command; if the
		// command WAS only boilerplate, keep the original rather than an
		// empty signature.
		if cmd := shellBoilerplateRE.ReplaceAllString(k.Command, ""); strings.TrimSpace(cmd) != "" {
			return TruncateOneLine(cmd, window)
		}
		return TruncateOneLine(k.Command, window)
	}
	for _, s := range []string{k.FilePath, k.Path, k.Pattern, k.Description, k.Prompt, k.Query, k.URL, k.Skill} {
		if s != "" {
			return TruncateOneLine(s, window)
		}
	}
	raw := input
	if len(raw) > mineRawSummaryCap {
		raw = raw[:mineRawSummaryCap] // TruncateOneLine keeps only the window of it
	}
	return TruncateOneLine(string(raw), window)
}

// reducer applies the reduction rules, one JSONL line at a time. Its line
// method is the single implementation those rules have: ReduceTranscript
// (whole file) and ReduceJSONLBytes (a span the graph ingest already holds
// in memory) both call it, so the two readers cannot drift — the same
// one-copy rule core.tokenGroup.commit exists to enforce for the token
// counters. The one piece of cross-line state is midTurnSeen: one mid-turn
// message is routinely stored as BOTH a queue-operation and a
// queued_command attachment (measured corpus-wide: 516 of 935 distinct
// queued texts carry both records, 305 enqueue-only, 114 attachment-only),
// so emitting both unconditionally would print most steers twice. The
// dedupe is per-reducer, so a graph-ingest span boundary that splits a
// message's two records can still store one steer as two episodes — a
// duplicate, never a loss.
type reducer struct {
	// midTurnSeen holds every mid-turn text already emitted, for collapsing
	// the enqueue/attachment double-write of one message to one section. A
	// set rather than a last-emitted string because two messages sent
	// seconds apart INTERLEAVE their records (enq1, enq2, qc1, qc2 —
	// observed on real steers), which adjacency cannot catch. The cost is
	// that a verbatim repeat of an earlier steer is shown once; measured
	// zero intra-file duplicate steer texts across 500 real transcripts, so
	// the cost is currently theoretical and bounded by the handful of real
	// mid-turn messages a session ever carries.
	midTurnSeen map[string]struct{}
}

func (r *reducer) line(out *bytes.Buffer, line []byte, since int64) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var rec mineRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		// Same tolerance digestTranscript applies: a wrongly-TYPED field is
		// not a malformed line — encoding/json fills every good field and
		// reports the type error at the end, and the struct now carries
		// top-level content/attachment fields whose types other record
		// kinds could plausibly collide with. Dropping the whole record
		// would silently shorten the reduced view, the failure class this
		// file's error rules exist to prevent.
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return
		}
	}
	if ts := isoMillis(rec.Timestamp); ts > 0 && ts < since {
		return
	}
	// A message typed while a turn is RUNNING never becomes a `user` record:
	// it is stored as a `queue-operation` (enqueue) and/or an `attachment`
	// carrying a `queued_command`, and dropping those silenced exactly the
	// messages a mid-turn send is — course corrections. A reader of the
	// reduced view saw the agent pivot with no visible prompt. The label is
	// distinct from [user] because kraken's own daemon bumps (PR-watch and
	// colour sends into a busy pane) queue through this same channel with
	// nothing in the record to tell them apart — the text itself says which.
	switch rec.Type {
	case "queue-operation":
		if rec.Operation == "enqueue" {
			r.midTurn(out, rec.Content)
		}
		return
	case "attachment":
		if rec.Attachment.Type == "queued_command" {
			r.midTurn(out, rec.Attachment.Prompt)
		}
		return
	case "assistant", "user":
	default:
		return // mode, file-history, system bookkeeping
	}
	if rec.Type == "user" {
		if text, ok := mineUserString(rec.Message.Content); ok {
			writeUserish(out, text)
			return
		}
		for _, b := range mineBlocks(rec.Message.Content) {
			if b.Type == "text" {
				writeUserish(out, b.Text)
			}
			// tool_result blocks are the dropped mass — the call above
			// already named what was attempted.
		}
		return
	}
	for _, b := range mineBlocks(rec.Message.Content) {
		switch b.Type {
		case "text":
			writeReduced(out, "[assistant]", b.Text)
		case "tool_use":
			if b.Name != "" {
				out.WriteString("[tool] " + callSignature(b.Name, b.Input) + "\n\n")
			}
		}
		// "thinking" falls through: dropped.
	}
}

// backgroundAgentsNoticeRE matches the harness's own "N background agents
// were stopped by the user" notice, which arrives through the queue channel
// beside real steers. It records a user ACTION but its prose is the
// harness's — quoted as [user mid-turn] it would enter the dream loop's
// prompting feedback as words carl never typed.
var backgroundAgentsNoticeRE = regexp.MustCompile(`^\d+ background agents? (?:was|were) stopped by the user`)

// midTurn emits one queued mid-turn message as [user mid-turn] — or as
// [teammate <id> mid-turn] when it carries a teammate envelope, the same
// routing writeUserish gives ordinary user records, since mislabelling
// another agent's words as carl's is the one thing the reduction must never
// do. Dropped: blanks, the task-notifications that queue through the same
// channel in bulk (system bookkeeping, exactly the mass the reduction
// exists to drop), the harness's stopped-agents notice, and ANY text
// already emitted (the enqueue/attachment double-write of one message —
// see midTurnSeen for why a set rather than adjacency).
func (r *reducer) midTurn(out *bytes.Buffer, text string) {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "<task-notification>") || backgroundAgentsNoticeRE.MatchString(t) {
		return
	}
	if r.midTurnSeen == nil {
		r.midTurnSeen = make(map[string]struct{})
	}
	if _, dup := r.midTurnSeen[t]; dup {
		return
	}
	r.midTurnSeen[t] = struct{}{}
	if b, ok := teammateBlock(t); ok {
		writeReduced(out, "[teammate "+b.From+" mid-turn]", b.Text)
		return
	}
	writeReduced(out, "[user mid-turn]", t)
}

// ReduceJSONLBytes reduces a JSONL span already in memory, per reducer.line's
// rules with no since filter (the caller chose the span). The graph ingest
// uses it so kg_episodes stores decision-relevant text rather than raw
// records — storing raw would have written the tool-result and attachment
// mass (the large majority of transcript bytes) into kraken.db at the full
// drain rate, unboundedly (review finding).
func ReduceJSONLBytes(data []byte) []byte {
	var out bytes.Buffer
	var r reducer
	for _, line := range bytes.Split(data, []byte("\n")) {
		r.line(&out, line, 0)
	}
	return bytes.TrimSpace(out.Bytes())
}

// ReduceTranscript returns the decision-relevant view of one transcript:
// user prose, tool calls and assistant text, with thinking, tool results
// and attachments dropped — except a queued mid-turn message, which rides
// in a `queue-operation` or `attachment` record and is emitted as
// `[user mid-turn]` (see reducer.line). This is what a per-transcript scout
// reads.
//
// Output is line-oriented and turn-ordered, one labelled section per block:
//
//	[user]
//	go fix the flaky test
//
//	[user mid-turn]
//	actually scope it to the parser only
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
	var r reducer
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, mineScanBufInit), mineScanBufMax)
	for sc.Scan() {
		r.line(&out, sc.Bytes(), since)
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
