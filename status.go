package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// status.go — cell status, derived by kraken rather than declared by any
// agent. There is no status file any agent writes: working/blocked is read
// straight off session-history activity (transcript jsonl mtimes), and done
// is explicit. Pending interactive questions are likewise pulled from the
// zeus session's own transcript.

// --- activity ---

// activityStaleAfter: how long the cell's transcripts must be silent before
// the pod counts as stopped. Claude appends transcript events every few
// seconds mid-turn; minutes of silence means every agent is at its prompt.
// long single tool calls (test suites, builds) append nothing mid-run; 5m
// biases against false blocked at the cost of slower blocked detection.
const activityStaleAfter = 5 * time.Minute

// newestMtime returns the newest modification time among the *.jsonl files
// directly in dir ("" or an unreadable dir reads as the zero time — never an
// error). Pure and testable; podLastActivity is a thin wrapper pointing it
// at a cell's transcript directory.
func newestMtime(dir string) time.Time {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}
	}
	var best time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(best) {
			best = info.ModTime()
		}
	}
	return best
}

// podLastActivity returns the newest mtime across all transcript files of
// the cell's project dir (~/.claude/projects/<projectDirName(worktree)>/):
// the main zeus session AND subagent sidechain files both append there, so
// this is "is anyone in the pod churning" from session history alone — pane
// content is never consulted, it's too flaky. Zero time when no transcripts
// exist yet.
func podLastActivity(worktree string) time.Time {
	return newestMtime(filepath.Join(home(), ".claude", "projects", projectDirName(worktree)))
}

// --- pending questions ---

// A PendingQuestion is an AskUserQuestion tool call awaiting an answer: the
// tool_use appears in the zeus transcript with no later tool_result for its
// id.
type PendingQuestion struct {
	ToolUseID string         `json:"tool_use_id"`
	Questions []QuestionItem `json:"questions"`
}

type QuestionItem struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	MultiSelect bool             `json:"multiSelect"`
	Options     []QuestionOption `json:"options"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// transcriptEvent is the tiny slice of a transcript jsonl line pendingQuestion
// cares about: an assistant tool_use (possibly AskUserQuestion) or a user
// tool_result answering one. Everything else in a real event is ignored by
// virtue of not being in this struct.
type transcriptEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
		} `json:"content"`
	} `json:"message"`
}

// pendingQuestionCache memoizes pendingQuestion results per transcript path,
// keyed on (size, mtime) via os.Stat: the board/TUI poll this against
// multi-MB transcripts on every tick, and re-scanning unchanged files on
// every poll is wasted work. Follows the spirit of discover.go's cwdCache.
var (
	pendingQuestionCacheMu sync.Mutex
	pendingQuestionCache   = map[string]pendingQuestionCacheEntry{}
)

type pendingQuestionCacheEntry struct {
	size  int64
	mtime time.Time
	q     *PendingQuestion
}

// pendingQuestion scans a session transcript for an unanswered
// AskUserQuestion call. Lenient throughout: an unreadable file, malformed
// lines, or no pending call all read as no pending question, never an error.
// Results are memoized on (size, mtime); an unchanged file returns the cached
// verdict without rescanning.
//
// Algorithm: stream the transcript line by line, remembering only the LAST
// AskUserQuestion tool_use seen (id + decoded questions) — each new one
// supersedes the last, since only the most recent question can still be
// open. A tool_result naming that same id marks it answered; a tool_result
// for any other id is irrelevant (it answered a since-superseded question,
// or something else entirely) and is ignored. Whatever the last call's
// answered state is when the file ends is the verdict.
func pendingQuestion(transcript string) *PendingQuestion {
	info, statErr := os.Stat(transcript)
	if statErr == nil {
		pendingQuestionCacheMu.Lock()
		if e, ok := pendingQuestionCache[transcript]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
			pendingQuestionCacheMu.Unlock()
			return e.q
		}
		pendingQuestionCacheMu.Unlock()
	}

	q := scanPendingQuestion(transcript)

	if statErr == nil {
		pendingQuestionCacheMu.Lock()
		pendingQuestionCache[transcript] = pendingQuestionCacheEntry{size: info.Size(), mtime: info.ModTime(), q: q}
		pendingQuestionCacheMu.Unlock()
	}
	return q
}

// scanPendingQuestion does the actual line-by-line transcript scan behind
// pendingQuestion's cache.
func scanPendingQuestion(transcript string) *PendingQuestion {
	f, err := os.Open(transcript)
	if err != nil {
		return nil
	}
	defer f.Close()

	var lastID string
	var lastQuestions []QuestionItem
	answered := false

	sc := bufio.NewScanner(f)
	// transcript lines can run to hundreds of KB, even several MB (large tool
	// inputs/outputs inline, e.g. an 8.3MB line seen in real transcripts);
	// bufio's 64KB default silently truncates scanning, so give it real
	// headroom.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev transcriptEvent
		if json.Unmarshal(line, &ev) != nil {
			continue // malformed line: skip it, don't lose the rest of the transcript
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type != "tool_use" || c.Name != "AskUserQuestion" {
					continue
				}
				// newest ask always wins: update the tracked id (and reset
				// questions) even if the input fails to decode, so a
				// malformed newer call can never leave a superseded older
				// question looking pending — it just yields nil instead.
				lastID = c.ID
				lastQuestions = nil
				answered = false // a new question call supersedes any prior answered state
				var input struct {
					Questions []QuestionItem `json:"questions"`
				}
				if json.Unmarshal(c.Input, &input) != nil {
					continue // malformed input: not a usable question, leave lastQuestions nil
				}
				lastQuestions = input.Questions
			}
		case "user":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_result" && lastID != "" && c.ToolUseID == lastID {
					answered = true
				}
			}
		}
	}
	if sc.Err() != nil {
		// scan failed partway (e.g. a line over the buffer max): fail closed
		// rather than risk reporting a phantom pending question off a
		// truncated scan.
		return nil
	}
	if lastID == "" || answered || len(lastQuestions) == 0 {
		return nil
	}
	return &PendingQuestion{ToolUseID: lastID, Questions: lastQuestions}
}

// --- state derivation ---

// deriveState maps a job to the five-state board model: draft · queued ·
// working · blocked · done.
//
// Shared precedence (kept in lockstep with displayState in job.go): done
// wins first, then an error reads as draft, then any kraken-managed state
// wins, then !Ready reads as draft, else queued.
//
// Truth table:
//   - j.State == "done"                        → "done"
//   - j.Err != ""                               → "draft" (unparseable job; error is the detail)
//   - j.State == "" && !j.Ready                 → "draft" (not ready, never started)
//   - j.State == "" && j.Ready                  → "queued" (ready, never started)
//   - started (state working/blocked) but !live → j.State (trust the file; the cell may just
//     be between daemon restarts)
//   - live: now.Sub(lastActivity) <= activityStaleAfter → "working", else → "blocked"
//     (a zero lastActivity — no transcripts yet — reads as maximally stale, so "blocked")
//
// deriveState returns board-state strings identical to job State strings;
// persisting a working/blocked transition to the job file is the daemon's
// concern, not this function's — it stays pure so it's testable without
// touching disk.
func deriveState(j *Job, live bool, lastActivity time.Time, now time.Time) string {
	if j.State == "done" {
		return "done"
	}
	if j.Err != "" {
		return "draft"
	}
	if j.State == "" {
		if !j.Ready {
			return "draft"
		}
		return "queued"
	}
	if !live {
		return j.State
	}
	if now.Sub(lastActivity) <= activityStaleAfter {
		return "working"
	}
	return "blocked"
}

// --- job file writes ---

// atomicWrite writes content to path via temp file + rename in the same dir —
// the same crash-safe pattern writeJobFields uses — so a reader never
// observes a half-written file.
func atomicWrite(path, content string) error {
	tmp := path + ".kraken.tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// appendMidRunLog appends one line to a job file's "## Mid-run log" section
// (adding the heading if the file doesn't have one yet), touching nothing
// above it — the frontmatter and human-authored spec body are never rewritten
// by this path. Atomic temp+rename, same as writeJobFields. Used for logging
// carl's direct messages/steering to zeus.
func appendMidRunLog(j *Job, actor, text string) error {
	data, err := os.ReadFile(j.File)
	if err != nil {
		return err
	}
	content := string(data)
	entry := "- [" + time.Now().Format("2006-01-02 15:04") + " " + actor + "] " + text
	// look for the heading in the body only: frontmatter text (e.g. a
	// success block scalar that mentions the heading) must not count, or the
	// real heading never gets written and log lines glue onto the spec
	lines := strings.Split(content, "\n")
	fencesToSkip := 0
	if len(lines) > 0 && isFence(lines[0]) {
		fencesToSkip = 2 // frontmatter present: body starts after its fences
	}
	hasHeading := false
	fences := 0
	for _, line := range lines {
		if fences < fencesToSkip {
			if isFence(line) {
				fences++
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "## Mid-run log") {
			hasHeading = true
			break
		}
	}
	trimmed := strings.TrimRight(content, "\n")
	var newContent string
	if hasHeading {
		newContent = trimmed + "\n" + entry + "\n"
	} else {
		newContent = trimmed + "\n\n## Mid-run log\n" + entry + "\n"
	}
	return atomicWrite(j.File, newContent)
}

// listArtifacts lists a cell's artifact files, sorted by name. Missing dir
// (no cell, or nothing produced yet) reads as no artifacts, not an error.
// listArtifacts returns absolute paths of every file under the cell's
// artifacts dir, recursing into subdirectories (an artifact can be organized
// into folders, e.g. icon-variations/) so the board and the app can list and
// navigate them. Hidden files and dirs are skipped.
func listArtifacts(j *Job) []string {
	root := cellArtifacts(j)
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, keep walking the rest
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out
}
