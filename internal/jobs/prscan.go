package jobs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"kraken/internal/core"
)

// prscan.go — the daemon's transcript-scanning PR backstop. listBranchPRs and
// the pr-hook (prpoll.go, prhook.go) cover the common cases: a branch-list
// query and a real-time PostToolUse hook on `gh pr create`. Neither catches a
// PR whose URL only ever landed in zeus' own transcript: the hook broken,
// unconfigured, or predating the cell's hook config, or a create whose URL
// showed up in the tool_result content rather than the hook payload's
// stdout/stderr. This file closes that gap by scanning the transcript
// directly — the retrospective observer of the same `gh pr create` events
// the hook watches live.
//
// The scan is structural, not textual: only the RESULT of a Bash tool call
// that INVOKES `gh pr create` is searched for a PR URL. A zeus transcript
// routinely quotes other jobs' PR URLs verbatim — a test fixture it Read, a
// daemon-log dump pasted for debugging, a chat mention of a teammate's PR —
// and matching raw line text against those (the prior implementation)
// attached every one of them to this job. Scoping to create-call results
// makes those quotations structurally invisible: they never appear as the
// tool_result of a `gh pr create` Bash call, so they're never even
// candidates. PR resolution is own-repo only via prefix — unlike the hook,
// there is no fail-open here: an unresolved repo prefix means scan nothing
// (carl's Q7 decision).

// ghPrCreatePattern is shared by this scanner and the pr-hook (prhook.go's
// extractPRURLs): both need the same "is this command an invocation, not a
// mention" test, and PostToolUse firing on e.g. `grep -n "gh pr create" ...`
// is exactly the same false-attach shape whether observed live via the hook
// or retrospectively via the transcript.
//
// It matches a command that INVOKES `gh pr create` — not one
// that merely mentions the literal. A plain strings.Contains false-positives
// on any command that quotes the phrase: `grep -rn "gh pr create" file.go`,
// an `rg` search, a heredoc analysis script — all routine in PR-pipeline
// cells — would then have their own (unrelated) tool_result scanned for a
// URL and misattach whatever PR another job happens to be quoted mentioning.
// Requiring the match to sit at an invocation position (start of command, or
// right after a shell separator/operator, optionally preceded by leading
// VAR=value env assignments) rules those out while still matching real
// invocation shapes: `VAR=$(...) gh pr create`, `cd x\ngh pr create`,
// `... && gh pr create`. This narrowing is statistical, not airtight: a
// shell-operator character inside a quoted string or heredoc body (e.g. a
// grep alternation `\|`, or literal text in a heredoc) can still precede the
// literal and match, so a quoted/heredoc mention isn't guaranteed to be
// excluded. On all real zeus transcripts observed so far this yields zero
// spurious candidates, and even a false candidate only matters if its own
// result text happens to carry an own-repo PR URL.
//
// The env-value alternation — $(...) with one level of nested parens, a
// "..." or '...' quoted string, or a plain \S+ run — exists because the
// dominant real create shape in this environment is
// `GH_TOKEN=$(gh auth token --user car1zhang) gh pr create --title "…"`:
// the space inside `$(…)` breaks a plain \S+ env-value match, silently
// dropping every token-substitution create. Real invocations only ever use
// this handful of shapes, so a fuller shell-word parser isn't needed.
//
// The invocation itself is matched as the literal "gh pr create" (single
// spaces, not \s+) rather than a whitespace-tolerant pattern. That's a
// deliberate match with the bytes.Contains(line, []byte("gh pr create"))
// prefilter below: the prefilter must never reject a line this regex would
// otherwise accept, and a literal-vs-literal comparison makes that a
// syntactic guarantee (the regex's match set is a subset of the prefilter's)
// rather than something that has to be reasoned about separately. All real
// `gh pr create` invocations use single spaces, so this costs nothing.
var ghPrCreatePattern = regexp.MustCompile(`(^|[\n;&|(]|&&|\|\|)\s*(\w+=(\$\((?:[^()]|\([^()]*\))*\)|"[^"]*"|'[^']*'|\S+)\s+)*gh pr create\b`)

// ghPrCreateInvoked is the ONE answer to "did this Bash command run gh pr
// create", shared by the transcript scanner below and the pr-hook's gate
// (prhook.go): ghPrCreatePattern applied after every heredoc BODY and every
// quoted span has been blanked, the same preprocessing bash-lint uses.
// Matched over the raw text, the pattern was quote-blind: a grep whose
// regex ARGUMENT contained `|gh pr create`, or a script fed through a
// heredoc that merely mentioned the command, satisfied it, and the command's
// stdout — a transcript dump carrying another job's PR URL verbatim — was
// then read as that job's create. Blanking keeps the quote characters
// themselves, so an env assignment whose value is a quoted string before the
// command (`GH_TOKEN="…" gh pr create`) still matches through the pattern's
// quoted-value alternative.
func ghPrCreateInvoked(cmd string) bool {
	return ghPrCreatePattern.MatchString(stripQuotedAndCond(stripHeredocBodies(cmd)))
}

// prScanLine is the tiny slice of a transcript jsonl line scanTranscriptPRURLs
// cares about: the message's content blocks (tool_use / tool_result), and the
// top-level toolUseResult field a tool_result line carries alongside its
// content block. Everything else in a real event is ignored by virtue of not
// being in this struct.
type prScanLine struct {
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

// prScanBlock covers both content-block shapes prScanLine's blocks can be:
// a tool_use ({type, id, name, input.command}) or a tool_result ({type,
// tool_use_id, content}). Unused fields simply decode to their zero value for
// whichever shape isn't present.
type prScanBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input struct {
		Command string `json:"command"`
	} `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// prScanContentText pulls the plain text out of a tool_result block's
// content field, which Claude Code emits as either a plain string or a list
// of {"type":"text","text":"…"} blocks (other block types, e.g. images, are
// skipped — a PR URL is never one of those). Pure and table-testable.
func prScanContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type != "text" {
				continue
			}
			sb.WriteString(b.Text)
			sb.WriteString("\n")
		}
		return sb.String()
	}
	return ""
}

// prScanResultText builds the full searchable text of a tool_result: its
// content block (via prScanContentText) plus the line's top-level
// toolUseResult field, which hookOutput already knows how to read in either
// shape it comes in — a plain string or a {"stdout":...,"stderr":...}
// object — since that's the same dual shape a PostToolUse payload's
// tool_output carries, just observed from the transcript instead of the
// hook's stdin. Each piece is joined with "\n", the same uniform separator
// throughout, so a URL split across a piece boundary (e.g. content text
// ending mid-word right where stdout begins) can never accidentally fuse
// into a lookalike. Pure and table-testable.
func prScanResultText(content, toolUseResult json.RawMessage) string {
	stdout, stderr := hookOutput(toolUseResult)
	var sb strings.Builder
	sb.WriteString(prScanContentText(content))
	sb.WriteString("\n")
	sb.WriteString(stdout)
	sb.WriteString("\n")
	sb.WriteString(stderr)
	return sb.String()
}

// scanTranscriptPRURLs streams path line by line (bufio.Scanner, sized like
// scanPendingQuestion's — transcript lines can run to several MB of inlined
// tool output) and returns every distinct PR URL under prefix that appeared
// in the result of a `gh pr create` Bash call, in first-seen order.
//
// Single forward pass: a tool_use block always precedes its tool_result in
// the file, so a pending set of in-flight create-call ids (keyed by tool_use
// id) is enough — no second pass or id->line index needed. A line that fails
// to decode as JSON is skipped silently (transcript schema is owned by
// Claude Code, not kraken — defensive reads, degrade to blank, never error).
//
// Best-effort: an unreadable file returns nil, and a line that overflows the
// scanner's buffer just ends the scan there — whatever URLs were already
// found stand, since this is a backstop behind the hook and the branch-list
// tracker, not the sole record.
//
// Prefilter: ~99% of transcript lines are neither a `gh pr create` tool_use
// nor the tool_result of one already pending, so a cheap bytes.Contains scan
// of the raw line — against the literal and against the (small) pending id
// set — skips the json.Unmarshal + block walk entirely for those, before
// paying for a full decode. This literal-string prefilter is a provable
// superset of ghPrCreatePattern (see that var's doc comment): the pattern
// only ever matches text containing the literal "gh pr create", so the
// prefilter can only ever be permissive relative to it, never reject a line
// the regex would accept.
func scanTranscriptPRURLs(path, prefix string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var urls []string
	seen := map[string]bool{}
	pending := map[string]bool{} // tool_use ids of in-flight `gh pr create` Bash calls, awaiting their result

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte("gh pr create")) && !lineMentionsPendingID(line, pending) {
			continue
		}
		var ev prScanLine
		if json.Unmarshal(line, &ev) != nil {
			continue // malformed line: skip it, don't lose the rest of the transcript
		}
		for _, raw := range ev.Message.Content {
			var b prScanBlock
			if json.Unmarshal(raw, &b) != nil {
				continue
			}
			switch b.Type {
			case "tool_use":
				if b.Name == "Bash" && b.ID != "" && ghPrCreateInvoked(b.Input.Command) {
					pending[b.ID] = true
				}
			case "tool_result":
				if !pending[b.ToolUseID] {
					continue // not a result we're waiting on: a Read, a log dump, any other tool call
				}
				delete(pending, b.ToolUseID)
				for _, url := range matchPRURLs(prScanResultText(b.Content, ev.ToolUseResult), prefix) {
					if seen[url] {
						continue
					}
					seen[url] = true
					urls = append(urls, url)
				}
			}
		}
	}
	return urls
}

// lineMentionsPendingID reports whether line contains any currently-pending
// tool_use id, i.e. it might be that call's tool_result. pending is small
// (in-flight `gh pr create` calls awaiting a result — normally zero or one)
// so a linear scan here is cheaper than the json.Unmarshal + block walk it's
// gating.
func lineMentionsPendingID(line []byte, pending map[string]bool) bool {
	for id := range pending {
		if bytes.Contains(line, []byte(id)) {
			return true
		}
	}
	return false
}

// prScanCache memoizes scanTranscriptPRURLs per transcript path on (size,
// mtime), mirroring pendingQuestionCache in status.go: the daemon's PR pass
// hits the same live transcripts every tick, and re-reading a multi-MB file
// when nothing has been appended is wasted work. Transcripts only append, so
// a change always means a re-scan of the whole file — simple, correct.
var (
	prScanCacheMu sync.Mutex
	prScanCache   = map[string]prScanCacheEntry{}
)

type prScanCacheEntry struct {
	size   int64
	mtime  time.Time
	prefix string
	urls   []string
}

// transcriptPRURLs is scanTranscriptPRURLs with that memoization. Keyed by
// transcript path; a prefix mismatch against the cached entry also forces a
// rescan (defensive — a job's repo prefix never actually changes underneath
// a live transcript, but a stale cross-job cache hit must never happen).
func transcriptPRURLs(path, prefix string) []string {
	info, statErr := os.Stat(path)
	if statErr == nil {
		prScanCacheMu.Lock()
		if e, ok := prScanCache[path]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) && e.prefix == prefix {
			prScanCacheMu.Unlock()
			return e.urls
		}
		prScanCacheMu.Unlock()
	}

	urls := scanTranscriptPRURLs(path, prefix)

	if statErr == nil {
		prScanCacheMu.Lock()
		prScanCache[path] = prScanCacheEntry{size: info.Size(), mtime: info.ModTime(), prefix: prefix, urls: urls}
		prScanCacheMu.Unlock()
	}
	return urls
}

// zeusTranscriptPath resolves a job's zeus transcript the same way the board
// does (api/board.go's cellZeusTranscript) — jobs sits below api in the
// package DAG and can't import it, so this reimplements the same two-step
// lookup against the same core primitives rather than inventing a new one:
// prefer the live zeus peer's transcript (core.ReadTranscriptPath), falling
// back to the job's own stamped session id (core.TranscriptPathForSession)
// for a done/dead cell whose pane is already gone. "" when neither resolves
// (fresh fork with no transcript yet, session gone, no session ever stamped)
// — the caller scans nothing, same as any other unreadable-file case.
func zeusTranscriptPath(j *Job, all []*core.Peer) string {
	if zeus := CellZeus(j, all); zeus != nil {
		if path, err := core.ReadTranscriptPath(zeus); err == nil {
			return path
		}
	}
	return core.TranscriptPathForSession(j.Session)
}
