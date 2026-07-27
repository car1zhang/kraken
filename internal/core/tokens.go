package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tokens.go — the four token counters Claude Code stamps on every assistant
// record, summed over a whole project dir INCREMENTALLY.
//
// The word is deliberately "tokens" and never "usage": `usage` is already
// taken in this binary and means ACCOUNT QUOTA (usage.go, usagepool.go,
// jobs/usage.go's ~/.kraken/usage.json, api's /api/usage). Colliding on it
// would make two unrelated numbers indistinguishable in every future grep.
//
// Why incremental, rather than a sum on read: the numbers are wanted per JOB,
// on a board payload that carries ~74 jobs and rebuilds roughly every two
// seconds. A full re-decode of one 20-60 MB cell project dir costs
// 0.1-0.3s warm in Go, so even the live cells alone would put seconds inside
// the board build's mutex. Transcripts are append-only JSONL, which makes the
// cheap shape obvious: remember how many bytes of each file have already been
// summed and read only past that. A settled job then costs one stat per file
// forever after, and the daemon — not the board — pays even that.
//
// Everything here follows mine.go's contract for claude-owned data, because
// it reads exactly the same files: an unreadable file or a malformed line
// skips that file or that line and the sum keeps going. The one thing that is
// NOT degraded to silence is a partial scan — see SumTokensDelta's error
// contract. A failed read is not a zero.

// Tokens is the four counters Claude Code reports on an assistant record,
// summed, plus the number of transcript files they were summed from.
//
// All four are carried RAW and there is no weighted total: the four are
// billed at wildly different rates (cache reads are ~14x the volume of
// everything else on a real cell and the cheapest per token), and baking a
// price ratio in here would go stale the moment pricing moves. Consumers
// weight them themselves.
type Tokens struct {
	In         int64 // input_tokens — uncached input
	Out        int64 // output_tokens — generated
	CacheWrite int64 // cache_creation_input_tokens
	CacheRead  int64 // cache_read_input_tokens
	// Sessions is a FILE COUNT (root transcripts + subagent sidecars), which
	// is why it is the one field of a SumTokensDelta result that is a TOTAL
	// rather than a delta — see that function's doc comment.
	Sessions int
}

// tokenUsage is the `usage` object on an assistant record's message. It is
// its own type rather than four fields inline because both readers of it —
// mine.go's per-session digest and tokenRecord below — must agree on the
// wire names exactly, and a second transcription of
// `cache_creation_input_tokens` is precisely the sort of near-invisible typo
// that would report a plausible number that is silently wrong.
type tokenUsage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

// tokenRecord is the whole of a transcript line the sum cares about, and it
// is deliberately NARROWER than mine.go's mineRecord: that one carries
// Message.Content as a json.RawMessage, which on an assistant record copies
// the entire content array (a Write call inlines a whole file body there).
// The sum touches every line of every transcript on the machine and needs
// none of it, so decoding into this instead skips that copy entirely.
type tokenRecord struct {
	Type string `json:"type"`
	// SessionID is `session_id` (SNAKE case) — the session that actually made
	// the API call this record reports. It is NOT `sessionId` (camel case),
	// which names the file the record currently lives in and is REWRITTEN on
	// every record when a session is forked. The snake-case one survives the
	// rewrite, which is the only thing that makes a replayed record
	// identifiable. See tokenRecordOwned.
	SessionID string `json:"session_id"`
	Message   struct {
		// ID is the API message id. One assistant message is written as one
		// JSONL record PER CONTENT BLOCK (thinking / text / each tool_use),
		// and claude stamps the same cumulative usage on every one of them —
		// so this is what collapses a group back to one contribution.
		ID    string     `json:"id"`
		Usage tokenUsage `json:"usage"`
	} `json:"message"`
}

// tokenRecordOwned reports whether an assistant record represents an API call
// this FILE actually paid for, as opposed to one replayed into it from
// another session.
//
// A kraken side thread is `claude --fork-session` in the cell's worktree (and
// `k b` is the same), so a fork's transcript is SEEDED with the parent
// conversation — every replayed assistant record carried over verbatim,
// `usage` and all. Both files live in the one project dir the sum walks, so
// without this the pre-fork history is counted once per fork. Measured on a
// real cell dir (`btw-on-jobs`, 53 files): 260 message ids appearing in more
// than one file, and a 1.43x over-count on top of the group inflation above.
// The bias is the wrong way round in the worst possible sense — it falls
// hardest on jobs that used side threads, which is exactly how a block gets
// investigated, and so exactly the jobs the nightly pass should look hardest
// at.
//
// The rule: a record is owned when it names no session at all, or names THIS
// file's own session.
//
//   - Absent is owned. Subagent sidecars (`subagents/agent-*.jsonl`) carry no
//     `session_id` whatsoever — verified, 159 of 159 records on a real cell —
//     and they hold ~24% of a cell's work, so treating absent as foreign
//     would throw away more than the double-count it fixed.
//   - Present and equal to `own` is owned.
//   - Present and different is a replay: skip it.
//
// `own` is the file's own session id, which for a root transcript is its
// filename stem. A sidecar's stem is `agent-<id>` and never matches anything,
// but its records have no `session_id` to compare, so it is carried by the
// absent rule rather than by the stem.
//
// Verified against an independent whole-corpus dedupe by globally-unique
// message id: this rule reproduces that number EXACTLY on 118 of 119 real
// cell project dirs, and to within 0.001% across the corpus as a whole. The
// single divergence is a synthetic fixture whose records predate
// `session_id` entirely — which is the honest limit of the rule and is
// stated on SumTokensDelta.
func tokenRecordOwned(rec tokenRecord, own string) bool {
	return rec.SessionID == "" || rec.SessionID == own
}

// fileSessionID is the session a transcript file belongs to — the value
// tokenRecordOwned compares a record's `session_id` against.
//
// A root transcript is NAMED for its session, so the stem is the answer. A
// subagent sidecar is not: it lives at
// <project>/<session-id>/subagents/agent-<id>.jsonl and its stem is
// `agent-<id>`, a value no `session_id` can ever equal. Deriving the parent
// session from the path instead of falling back on the stem matters because
// the stem makes the ownership test unsatisfiable for sidecars, leaving them
// carried ENTIRELY by the "absent is owned" arm — true today (159 of 159
// records carry no `session_id`) but a format kraken does not control. If
// Claude Code ever starts stamping sidecars with their parent's session id,
// the stem would reject every one of them and silently drop ~24% of a cell's
// work; the parent id accepts them, which is the correct answer either way.
func fileSessionID(path, name string) string {
	if filepath.Base(filepath.Dir(path)) == "subagents" {
		// <project>/<session-id>/subagents/<name>
		return filepath.Base(filepath.Dir(filepath.Dir(path)))
	}
	return strings.TrimSuffix(name, ".jsonl")
}

// ProjectDirName encodes a cwd the way Claude Code names its transcript dirs
// under ~/.claude/projects. It is the exported face of projectDirName
// (discover.go) — the same implementation, never a second copy of the
// munging rule — for callers outside this package that need to go from a
// directory to the transcripts written in it. internal/jobs is the one such
// caller: a job's cell worktree path munges to the dir holding every
// transcript zeus, its side threads and its whole teammate crew wrote.
func ProjectDirName(cwd string) string { return projectDirName(cwd) }

// SumTokensDelta sums one project dir's token counters incrementally.
//
// projectDir is an ABSOLUTE path to a dir under ~/.claude/projects.
// priorOffsets maps an absolute transcript path to the number of bytes of it
// already summed by an earlier call; a path missing from the map is summed
// from zero.
//
// The returned Tokens is a DELTA — the tokens found PAST each prior offset —
// with one deliberate exception: Sessions is the TOTAL number of transcript
// files present in the dir right now, not a count of newly-appeared ones.
// A file count does not accumulate (a caller that added it every pass would
// report a job with 12 transcripts as having 4,000 of them), so a caller
// folds the four counters in with += and Sessions in with =. Both halves of
// that rule are stated on store.BumpJobTokens, which is what actually applies
// it.
//
// The returned map is the NEW offsets, one entry per transcript file found —
// which is also how a caller learns that a file vanished (it is absent) and
// how it learns a file was truncated or replaced (its offset went DOWN).
// Append-only is the assumption everywhere else; a file SHORTER than its
// recorded offset can only be a file that is no longer the one that was
// summed, so it is re-read whole from zero.
//
// Offsets always land on a COMPLETE line. A transcript being appended to
// right now can end mid-record, and recording the file's size as the offset
// would skip the rest of that record forever; the final line is therefore
// only counted once it is known to be newline-terminated.
//
// WHAT COUNTS AS ONE CONTRIBUTION. Two rules, both of which exist because the
// naive reading of a transcript over-counts by about 2x on real data:
//
//   - One API message is written as N records, all carrying the SAME
//     cumulative `usage`, so a message contributes exactly ONCE — see
//     sumTokensFrom's group handling. Measured inflation without this:
//     1.98x across every kraken cell dir on this machine.
//   - A record replayed into a forked session's file is not that file's
//     spend — see tokenRecordOwned. Measured inflation without this: a
//     further 1.43x on a cell that used side threads.
//
// Together they reproduce a whole-corpus dedupe by globally-unique message id
// exactly on 118 of 119 real cell project dirs. Two cases they cannot see,
// both documented rather than claimed away:
//
//   - A fork whose replayed records carry no `session_id` at all (a transcript
//     format predating that field) still counts twice. Corpus-wide that is
//     0.001% of the total, all of it in one synthetic fixture.
//   - A replayed record whose PARENT transcript is not in this project dir
//     (a fork started from a different cwd) is skipped here and counted
//     nowhere — an under-count. It does not arise for kraken side threads or
//     `k b` inside a cell, since both inherit the worktree cwd.
//
// Errors mean the sum for this pass is NOT EXACT, and the message says how.
// A missing project dir, an unreadable file and a malformed line are all
// normal and cost only themselves — they yield a smaller sum, never an error.
// Everything else is reported, in one of two flavours:
//
//   - SHORT (a floor): a file scanned only part way (a line over the 16 MB
//     ceiling, a mid-file read error), a SUBTREE that could not be listed, a
//     file that could not be STAT'd. The counters must not be presented as a
//     total, and — the part a caller must not skip — the returned offset map
//     is INCOMPLETE, so an absent path no longer means "this file is gone".
//     A caller that replaces its stored watermarks with this map will drop
//     the watermark of a file it merely failed to see, and re-sum that file
//     from zero into counters that only ever accumulate. refreshJobTokens
//     (internal/jobs/tokens.go) is what carries the unseen ones forward.
//   - LONG (possibly): the group seed at a resume boundary could not be
//     resolved within the lookback bounds, so one message may be counted
//     twice. See precedingGroupID.
//
// The two are distinguished only by the message, deliberately: a caller's
// correct response to both is the same — fold the result in, say out loud
// that it is not exact — and a typed error would invite branching on a
// distinction nothing downstream can act on differently.
//
// The returned Tokens and offsets are still valid and still worth folding in:
// the offset of a truncated scan stops at the last complete line it did
// count, so the next pass retries from there rather than skipping the gap.
func SumTokensDelta(projectDir string, priorOffsets map[string]int64) (Tokens, map[string]int64, error) {
	var total Tokens
	offsets := map[string]int64{}
	var partial []string

	// WalkDir rather than ReadDir, for MineTranscripts' reason: a project dir
	// holds its root transcripts at the top level and its subagent sidecars
	// two levels down under <session-id>/subagents/. The sidecars hold ~24%
	// of all tool calls (mine.go), and their assistant records carry their
	// own usage — a sum that skipped them would be wrong by roughly that
	// much, with nothing anywhere reporting a problem.
	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// The ROOT failing is the normal "this job's cell never came up"
			// state — no dir, no transcripts, nothing wrong — and must stay
			// silent, or the daemon would log once a tick forever for every
			// job that never started.
			//
			// A failure DEEPER than the root is a different animal: the dir
			// exists and we could not list part of it (EMFILE in a long-lived
			// daemon, a permission blip, a subtree removed mid-walk). Files
			// under it are then missing from `offsets` and indistinguishable
			// from deleted ones, which is precisely the case that makes a
			// caller drop their watermarks and re-sum them from zero. So it
			// is reported, and the reporting is the whole fix.
			if path != projectDir {
				partial = append(partial, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			// The sibling of the walk error above, and it has to be reported
			// for the identical reason: the file is real, we simply could not
			// stat it (an un-traversable parent, an lstat that lost a race
			// with a rename), so it is missing from `offsets` while still
			// existing on disk. Left silent, a caller doing a replace-all
			// write deletes its watermark and re-sums the whole file into
			// accumulate-only counters the moment it becomes readable again.
			partial = append(partial, fmt.Sprintf("%s: stat: %v", d.Name(), err))
			return nil
		}
		size := info.Size()
		total.Sessions++

		prior := priorOffsets[path]
		if prior > size {
			prior = 0 // truncated or replaced: this is not the file we summed
		}
		if prior == size {
			// The settled case, and the one the whole design exists for:
			// the file has not grown, so it is never opened. One stat.
			offsets[path] = prior
			return nil
		}

		own := fileSessionID(path, d.Name())
		tk, end, scanErr := sumTokensFrom(path, prior, size, own)
		total.In += tk.In
		total.Out += tk.Out
		total.CacheWrite += tk.CacheWrite
		total.CacheRead += tk.CacheRead
		offsets[path] = end
		if scanErr != nil {
			partial = append(partial, scanErr.Error())
		}
		return nil
	})

	if len(partial) > 0 {
		return total, offsets, fmt.Errorf("partial token scan in %s: %s", projectDir, strings.Join(partial, "; "))
	}
	return total, offsets, nil
}

// sumTokensFrom sums one transcript's assistant records from byte offset
// `from` up to `limit` (the size stat'd by the caller), returning the new
// offset — always the end of the last COMPLETE line it counted.
//
// limit rather than "to EOF" so the window is exactly the one the caller
// measured: the file may be appended to while this runs, and a scan that ran
// past the stat'd size would count bytes the returned offset does not
// promise were counted. io.LimitReader is what pins that, and it is also why
// this can never be io.ReadAll — the largest transcript on this machine is
// 42 MB with single LINES seen at 8.3 MB, which is what mineScanBufMax
// (16 MB, mine.go) is sized for.
//
// The deferred-line dance is the append-only correctness bit: a line is only
// counted once the NEXT Scan succeeds, which is the only proof that it was
// newline-terminated rather than the half-written tail of a file being
// appended to right now. The final line is counted only if the window's last
// byte is in fact a newline.
//
// GROUP DEDUPE. One API assistant message is written as one record per
// content block (thinking / text / each tool_use), and claude stamps the same
// cumulative `usage` on every one — a real group seen here repeats eight
// times with byte-identical `out=7479 cache_read=430139`. Cache-read is per
// API call and cannot legitimately be counted eight times, so a message
// contributes exactly once. The records of a group are contiguous within a
// file (measured: zero non-contiguous reappearances across a whole 53-file
// cell dir), so "count it when the id CHANGES" is sufficient and needs no
// memory beyond the previous id.
//
// The one hazard in that is this function's own incrementality: if `from`
// lands in the middle of a group, the first record read here would look like
// a fresh group and the message would be counted a second time. Rather than
// persisting the last-counted id (a schema column) or rewinding the offset to
// the group's start (which would leave a settled file's final group
// permanently uncounted AND re-read on every pass, defeating the one-stat
// steady state), the previous id is RECOVERED from the file: precedingGroupID
// reads the single record immediately before `from`. That keeps the boundary
// exact with no stored state, and costs one small read on files that grew.
//
// An unopenable/unseekable file degrades to "nothing summed, offset
// unchanged" with no error, matching this package's contract for data
// another program owns. Only a PARTIAL scan errors — sc.Err() being non-nil
// means the scan STOPPED early (a line over the ceiling, a read error), so
// everything after it is invisible, and reporting a clean zero there is the
// exact bug class jobs/status.go's scanPendingQuestion was fixed for. The
// offset is left at the last counted line rather than skipped past the
// offending one: re-reading it next pass is a bounded cost, whereas jumping
// the gap would under-count permanently and silently.
func sumTokensFrom(path string, from, limit int64, own string) (Tokens, int64, error) {
	var tk Tokens
	f, err := os.Open(path)
	if err != nil {
		return tk, from, nil
	}
	defer f.Close()

	// Seed the group tracker with whatever record precedes the resume point,
	// so a message split across two passes is still counted once. A seed the
	// walk could not determine is reported, never assumed: the group at the
	// boundary is then counted as new, and if it was a continuation that one
	// message is counted twice — an over-count, which nothing downstream can
	// subtract back out.
	lastID, seeded := precedingGroupID(f, from)
	var seedErr error
	if !seeded {
		seedErr = fmt.Errorf("%s: group seed at byte %d unresolved (lookback exhausted); one message may be double-counted",
			filepath.Base(path), from)
	}

	// Is the last byte of the window a newline? ReadAt is a pread and does
	// not disturb the seek offset below, so the order of these two is free.
	complete := false
	if limit > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], limit-1); err == nil {
			complete = last[0] == '\n'
		}
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return tk, from, nil
	}

	sc := bufio.NewScanner(io.LimitReader(f, limit-from))
	sc.Buffer(make([]byte, 0, mineScanBufInit), mineScanBufMax)
	off := from
	var pending []byte
	havePending := false
	for sc.Scan() {
		if havePending {
			addTokenLine(&tk, pending, own, &lastID)
			off += int64(len(pending)) + 1 // +1 for the newline that proved it complete
		}
		pending = append(pending[:0], sc.Bytes()...)
		havePending = true
	}
	if err := sc.Err(); err != nil {
		// pending IS complete here, and that is not a guess: the scan
		// stopped because something FOLLOWED pending in the stream (the
		// oversized line, or the read that failed), which is the same proof
		// a successful next Scan would have given. So it is counted, and off
		// advances to just past it — meaning the next pass restarts at the
		// offending line rather than at the top of the file. It will fail
		// there again, which is deliberate: re-reading one line is a bounded
		// cost, whereas skipping past it would under-count permanently and
		// silently. The error is what says the total is only a floor.
		if havePending {
			addTokenLine(&tk, pending, own, &lastID)
			off += int64(len(pending)) + 1
		}
		return tk, off, fmt.Errorf("%s: %v", filepath.Base(path), err)
	}
	if havePending && complete {
		addTokenLine(&tk, pending, own, &lastID)
		off += int64(len(pending)) + 1
	}
	return tk, off, seedErr
}

// addTokenLine folds one transcript line into tk, advancing the caller's
// group tracker.
//
// Three gates, in order, and each drops the record for a different reason:
//
//  1. Not an `assistant` record — only those carry a usage object, and gating
//     on the type keeps a future record shape that happens to echo one from
//     being counted.
//  2. Not owned by this file (tokenRecordOwned) — a record replayed in from a
//     forked parent session. `lastID` still advances, because the tracker
//     follows the file's actual record sequence rather than the counted
//     subset.
//  3. Same `message.id` as the previous assistant record — a further content
//     block of a message already counted, carrying a duplicate of the same
//     cumulative usage.
//
// A record with no `message.id` at all cannot be grouped, so it is counted on
// its own; that is the conservative direction (it can only over-count, and
// only for a shape that does not occur on current transcripts).
//
// A blank or malformed line costs only itself. A line that IS valid JSON but
// has one wrongly-typed field is deliberately NOT discarded: encoding/json
// fills in every field it could and reports the type error at the end, so
// throwing the record away would lose three good counters to one bad one.
// Same treatment, and the same reasoning, as mineInputSummary's in mine.go.
func addTokenLine(tk *Tokens, line []byte, own string, lastID *string) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var rec tokenRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return // not JSON at all, or truncated: skip it
		}
	}
	if rec.Type != "assistant" {
		return
	}
	id := rec.Message.ID
	defer func() { *lastID = id }()
	if !tokenRecordOwned(rec, own) {
		return
	}
	if id != "" && id == *lastID {
		return // another content block of a message already counted
	}
	u := rec.Message.Usage
	tk.In += u.Input
	tk.Out += u.Output
	tk.CacheWrite += u.CacheCreation
	tk.CacheRead += u.CacheRead
}

// groupLookbackRecords bounds how many records precedingGroupID will walk
// back over while looking for the previous ASSISTANT one. The effective
// tolerance is groupLookbackRecords-1 INTERVENING records, since one
// iteration is spent reading the assistant record itself.
//
// It has to walk at all — rather than read the single record before the
// offset — because the forward tracker skips non-assistant records without
// resetting, so a message whose blocks are interleaved with tool_result
// records is still one group. That shape is not exotic: 9,385 occurrences
// across every cell transcript on this machine, with an observed maximum of
// FIVE intervening records. 64 leaves an order of magnitude of headroom over
// that, and exhaustion is REPORTED rather than silent (see precedingGroupID).
const groupLookbackRecords = 64

// groupLookbackBytes bounds the same walk by volume, which is the bound that
// actually binds: the records being skipped are tool_results, and this repo
// records real single transcript lines at 8.3 MB, so a handful of fat ones
// between the blocks of one message reaches a byte ceiling long before it
// reaches a record count. Set well above the largest intervening run measured
// on this machine (1.1 MB) with room for several 8 MB results on top, and
// bounded at all only so a pathological transcript cannot turn one resume
// into a whole-file backward read.
const groupLookbackBytes = 64 << 20

// precedingGroupID returns the `message.id` of the last assistant record
// before byte offset `from`, skipping over any non-assistant records in
// between, or "" when there is none within the lookback bound.
//
// This is what makes the group dedupe survive the incremental resume
// boundary, and it must mirror the forward tracker EXACTLY: addTokenLine
// leaves lastID alone for blank, malformed and non-assistant lines, so this
// walks past exactly those and stops at the first assistant record. Reading
// the state back out of the file is cheaper and less stateful than persisting
// the id in the store, and unlike rewinding the offset to the group's start
// it leaves a settled file's offset equal to its size — which is what keeps
// the steady-state cost at one stat per file.
//
// The bool distinguishes a CONCLUSIVE answer from a give-up, and that
// distinction is the whole reason this returns two values. Reaching the start
// of the file, or a record that is genuinely the previous assistant one, is
// conclusive — ok=true, even when the id is "". Running out of lookback
// (either bound) or failing to read is NOT: the seed is then unknown, the
// group at the boundary gets counted as if it were new, and if it was in fact
// a continuation that message is counted TWICE. That is an over-count, the
// one direction this number can never recover from, so it must reach the
// caller rather than hide behind an empty string that means five other
// things.
func precedingGroupID(f *os.File, from int64) (string, bool) {
	end := from
	var scanned int64
	for i := 0; i < groupLookbackRecords; i++ {
		line, start, ok := lineBefore(f, end)
		if !ok {
			// Either the file starts here — no preceding record exists, which
			// is a real and correct answer — or a read failed. lineBefore
			// reports the former by returning the first line; reaching this
			// with end <= 1 is the start-of-file case.
			return "", end <= 1
		}
		if scanned += end - start; scanned > groupLookbackBytes {
			return "", false
		}
		if id, isAssistant := assistantMessageID(line); isAssistant {
			return id, true
		}
		end = start
	}
	return "", false
}

// lineBefore returns the complete line whose terminating newline sits at
// end-1, together with that line's start offset.
//
// The search doubles its window until it finds the line's start, and is
// guaranteed to reach one rather than give up: the forward scanner refuses
// any line over mineScanBufMax, so a line that produced an offset was
// necessarily within that ceiling.
func lineBefore(f *os.File, end int64) ([]byte, int64, bool) {
	if end <= 1 {
		return nil, 0, false
	}
	term := end - 1 // the '\n' that terminated the line we want
	for window := int64(mineScanBufInit); ; window *= 2 {
		start := term - window
		if start < 0 {
			start = 0
		}
		buf := make([]byte, term-start)
		if len(buf) == 0 {
			return nil, 0, false
		}
		if _, err := f.ReadAt(buf, start); err != nil {
			return nil, 0, false
		}
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			return buf[i+1:], start + int64(i) + 1, true
		}
		if start == 0 {
			return buf, 0, true // the line we want is the file's first
		}
		if window >= mineScanBufMax {
			return nil, 0, false
		}
	}
}

// assistantMessageID reports one line's `message.id` and whether the line was
// an assistant record at all. The bool is what lets precedingGroupID tell
// "this is a user record, keep walking" apart from "this is an assistant
// record that happens to carry no id, stop here" — collapsing those two into
// a bare "" would walk straight past a real group boundary.
func assistantMessageID(line []byte) (string, bool) {
	var rec tokenRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return "", false
		}
	}
	if rec.Type != "assistant" {
		return "", false
	}
	return rec.Message.ID, true
}
