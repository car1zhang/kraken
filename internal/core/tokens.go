package core

import (
	"bufio"
	"bytes"
	"encoding/json"
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
	Type    string `json:"type"`
	Message struct {
		Usage tokenUsage `json:"usage"`
	} `json:"message"`
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
// Errors are the partial-scan channel and nothing else. A missing project
// dir, an unreadable file, an unreadable subtree and a malformed line are
// all normal and cost only themselves — they yield a smaller sum, never an
// error. A non-nil error means at least one file was scanned only PART way
// (a line over the 16 MB ceiling, or a mid-file read error), so the returned
// counters are a FLOOR and must not be presented as a total. The returned
// Tokens and offsets are still valid and still worth folding in: the offset
// of a truncated scan stops at the last complete line it did count, so the
// next pass retries from there rather than skipping the gap.
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
			// A missing project dir (the normal state for a job whose cell
			// never came up) arrives here as the root's own error, so this
			// one branch covers both "no dir" and "unreadable subtree".
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
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

		tk, end, scanErr := sumTokensFrom(path, prior, size)
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
// An unopenable/unseekable file degrades to "nothing summed, offset
// unchanged" with no error, matching this package's contract for data
// another program owns. Only a PARTIAL scan errors — sc.Err() being non-nil
// means the scan STOPPED early (a line over the ceiling, a read error), so
// everything after it is invisible, and reporting a clean zero there is the
// exact bug class jobs/status.go's scanPendingQuestion was fixed for. The
// offset is left at the last counted line rather than skipped past the
// offending one: re-reading it next pass is a bounded cost, whereas jumping
// the gap would under-count permanently and silently.
func sumTokensFrom(path string, from, limit int64) (Tokens, int64, error) {
	var tk Tokens
	f, err := os.Open(path)
	if err != nil {
		return tk, from, nil
	}
	defer f.Close()

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
			addTokenLine(&tk, pending)
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
			addTokenLine(&tk, pending)
			off += int64(len(pending)) + 1
		}
		return tk, off, fmt.Errorf("%s: %v", filepath.Base(path), err)
	}
	if havePending && complete {
		addTokenLine(&tk, pending)
		off += int64(len(pending)) + 1
	}
	return tk, off, nil
}

// addTokenLine folds one transcript line into tk. Only `assistant` records
// carry a usage object; gating on the type keeps a future record shape that
// happens to echo one from being counted twice. A blank or malformed line
// costs only itself.
func addTokenLine(tk *Tokens, line []byte) {
	if len(bytes.TrimSpace(line)) == 0 {
		return
	}
	var rec tokenRecord
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	if rec.Type != "assistant" {
		return
	}
	u := rec.Message.Usage
	tk.In += u.Input
	tk.Out += u.Output
	tk.CacheWrite += u.CacheCreation
	tk.CacheRead += u.CacheRead
}
