package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// cmd_handoffhook.go — `kraken handoff-hook`: the SessionStart hook a cell's
// settings.json installs under the "compact" matcher (jobs' handoffHookEntry).
// After a compaction it restates the two standing rules a compaction summary
// drops (handoffStandingRules), quotes the first line of the agent's last
// words before the compaction as the transcript records them (lastSaidBefore
// — a summary's Pending list can list that line as still to be sent), then tells the
// resumed agent which 80-handoff*.md docs sit in the cell's artifacts dir,
// newest first, so the agent reads its own handoff before doing anything else.
//
// It shares cmd_ctx.go's posture: it NEVER FAILS. Every bad input — empty or
// malformed stdin, a non-compact source — degrades to printing nothing and
// exiting 0, and nothing is ever written to stderr; a missing dir or no
// handoff docs still prints the rules, since an idle auto-compaction has no
// doc and is exactly the case the rules exist for. Kept out of usage()
// beside the rest of the hook family.

// handoffDocPattern is the artifact name shape the hook lists.
const handoffDocPattern = "80-handoff*.md"

// handoffClosing is the sentence the doc listing always ends with.
const handoffClosing = "Read the one that is yours before anything else; a doc older than the work you remember is history."

// handoffStandingRules opens the injected context after EVERY compaction,
// handoff doc or none: a compaction summary carries the agent's state, not
// its rules, and the two below are the ones that drop out of it — the
// resumed agent then follows the harness's own bash-first reminder instead.
// Stated here rather than only in the skills because this hook is the one
// path that reaches an idle auto-compaction the agent never asked for.
const handoffStandingRules = "Compaction kept your state, not your rules; two hold whatever the summary or the harness's file-editing reminder says: repo files are edited with the Edit or Write tool, never through a Bash heredoc, `sed -i` or a python rewrite — and a teammate is spawned from the worktree root, never from a subdirectory your shell is still in."

// cmdHandoffHook is the dispatch entry for
// `kraken handoff-hook <slug> [--artifacts <dir>] [--serve-addr A] [--db-path P]`.
// Flags are parsed with ctxHookFlags' leniency: unknown words and a trailing
// half flag are ignored.
func cmdHandoffHook(args []string) {
	ctxHookFlags(args)
	dir := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--artifacts" {
			if i+1 >= len(args) {
				break
			}
			i++
			dir = args[i]
		}
	}
	runHandoffHook(os.Stdin, os.Stdout, dir, time.Now())
}

// handoffDoc is one listed handoff doc.
type handoffDoc struct {
	path    string
	modTime time.Time
}

// runHandoffHook is cmdHandoffHook's body, split out so the whole path runs
// on plain readers and writers. It writes either nothing or exactly one
// SessionStart hookSpecificOutput JSON object plus a newline.
func runHandoffHook(stdin io.Reader, stdout io.Writer, artifactsDir string, now time.Time) {
	defer func() { _ = recover() }()
	if stdin == nil || stdout == nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(trimmed, &payload) != nil || payload == nil {
		return
	}
	// session_id is part of the payload this hook reads; the doc listing does
	// not depend on it.
	if rawSource, present := payload["source"]; present {
		var source string
		if json.Unmarshal(rawSource, &source) != nil || source != "compact" {
			return
		}
	}
	// The rules go out on every compaction; the doc listing only when there is
	// a doc to list. A missing or empty artifacts dir is therefore no longer
	// silence — it is the rules alone.
	var text strings.Builder
	text.WriteString(handoffStandingRules)
	// Between the rules and the docs: what the agent last said before this
	// compaction, read off the transcript the payload names. A summary's
	// Pending list can name that very line as still to be sent — a gate
	// line, a doc-ready line, the handoff's own closing line — and an agent
	// that believes it sends it again. The transcript is the one source that
	// cannot be wrong about it. Absent path, unreadable file or no assistant
	// text all mean this paragraph is simply not there; the output is then
	// byte-identical to the rules-only (or rules-plus-docs) form.
	if rawPath, present := payload["transcript_path"]; present {
		var transcriptPath string
		if json.Unmarshal(rawPath, &transcriptPath) == nil && transcriptPath != "" {
			if ls, ok := lastSaidBefore(transcriptPath); ok {
				text.WriteString("\n\n")
				text.WriteString(handoffLastSaidLine(ls, now))
			}
		}
	}
	if docs := listHandoffDocs(artifactsDir); len(docs) > 0 {
		text.WriteString("\n\nHandoff docs in this cell's artifacts dir, newest first:\n")
		for _, d := range docs {
			age := int64(now.Sub(d.modTime) / time.Minute)
			if age < 0 {
				age = 0
			}
			fmt.Fprintf(&text, "- %s (written %dm ago)\n", d.path, age)
		}
		text.WriteString(handoffClosing)
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": text.String(),
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(out) != nil {
		return
	}
	_, _ = stdout.Write(buf.Bytes())
}

// lastSaid is the agent's last assistant text before a compaction: the text
// itself, whitespace-trimmed and otherwise untouched (the rendering truncates,
// this does not), and the record's timestamp — zero when the record carried
// none or an unparseable one, which still counts as an answer.
type lastSaid struct {
	Text string
	At   time.Time
}

// lastSaidScanBuf is the longest transcript line the scan will read. A line
// past it ends the scan with whatever was found before it: the hook never
// fails, and a truncated answer beats none.
const lastSaidScanBuf = 16 << 20

// lastSaidFirstLineRunes is where handoffLastSaidLine cuts the quoted first
// line, counted in runes.
const lastSaidFirstLineRunes = 240

// lastSaidRecord is the slice of a Claude Code transcript record this scan
// reads. Everything else on the line is ignored. isSidechain is kept raw so
// that only a literal `false` (or an absent key) counts as main-chain: a
// wrongly typed value is not silently read as false.
type lastSaidRecord struct {
	Type        string          `json:"type"`
	IsSidechain json.RawMessage `json:"isSidechain"`
	Timestamp   string          `json:"timestamp"`
	Message     struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// lastSaidSyntheticModel is the model string Claude Code stamps on assistant
// records it writes itself — error and limit banners, null turns. They carry
// ordinary text blocks and are not the agent's words.
const lastSaidSyntheticModel = "<synthetic>"

// lastSaidBefore reads the JSONL transcript at transcriptPath and returns the
// agent's last words: the last assistant text block in the file, in file
// order. Compaction boundaries play no part, and deliberately: when the
// SessionStart(compact) hook runs, this compaction's compact_boundary record
// may not yet be in the file, so "before the last boundary" would then name
// the words before the PREVIOUS compaction — and nothing the agent
// says can follow the new boundary before the hook runs, so the last
// candidate in the file is right whichever write lands first. An assistant
// record counts when it is not a sidechain (isSidechain absent or literally
// false), not synthetic, and its message content is an array holding at
// least one text block with non-whitespace text; the record's LAST such block
// is its text. Lines that are not JSON objects are skipped. False when the
// path is empty or not a regular file, the file cannot be opened, nothing
// qualifies, or the scan fails (an over-cap line, a read error) — a confident
// quote of an OLDER line is worse than none. Never panics, never writes
// anywhere.
func lastSaidBefore(transcriptPath string) (ls lastSaid, ok bool) {
	defer func() {
		if recover() != nil {
			ls, ok = lastSaid{}, false
		}
	}()
	if transcriptPath == "" {
		return lastSaid{}, false
	}
	// Stat before open: a FIFO would block the open until a writer appeared,
	// and a device is not a transcript either way.
	info, err := os.Stat(transcriptPath)
	if err != nil || !info.Mode().IsRegular() {
		return lastSaid{}, false
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return lastSaid{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), lastSaidScanBuf)
	var last lastSaid
	var found bool
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var rec lastSaidRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// A wrongly typed field is not a malformed line; encoding/json
			// fills every good field and reports the type error at the end.
			var typeErr *json.UnmarshalTypeError
			if !errors.As(err, &typeErr) {
				continue
			}
		}
		if rec.Type != "assistant" || !lastSaidMainChain(rec.IsSidechain) || rec.Message.Model == lastSaidSyntheticModel {
			continue
		}
		text, ok := lastSaidText(rec.Message.Content)
		if !ok {
			continue
		}
		last, found = lastSaid{Text: text, At: lastSaidTime(rec.Timestamp)}, true
	}
	if sc.Err() != nil {
		return lastSaid{}, false
	}
	return last, found
}

// lastSaidMainChain reports whether a raw isSidechain value marks a
// main-chain record: the key absent, or exactly the JSON literal false.
func lastSaidMainChain(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("false"))
}

// lastSaidText returns the last non-empty text block of an assistant content
// array, whitespace-trimmed. False when content is not an array or holds no
// such block.
func lastSaidText(content json.RawMessage) (string, bool) {
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return "", false
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i].Type != "text" {
			continue
		}
		if t := strings.TrimSpace(blocks[i].Text); t != "" {
			return t, true
		}
	}
	return "", false
}

// lastSaidTime parses a record timestamp (RFC3339, as Claude Code writes it);
// a missing or unparseable one is the zero time.
func lastSaidTime(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// handoffLastSaidLine renders the last-words paragraph: the first line of the
// text (cut to lastSaidFirstLineRunes runes plus an ellipsis when longer), the
// age in whole minutes with the UTC timestamp, or "at an unknown time" when
// the record carried no usable timestamp.
func handoffLastSaidLine(ls lastSaid, now time.Time) string {
	first := ls.Text
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(first)
	if r := []rune(first); len(r) > lastSaidFirstLineRunes {
		first = string(r[:lastSaidFirstLineRunes]) + "…"
	}
	when := "at an unknown time"
	if !ls.At.IsZero() {
		age := int64(now.Sub(ls.At) / time.Minute)
		if age < 0 {
			age = 0
		}
		when = fmt.Sprintf("%dm ago (%s)", age, ls.At.UTC().Format("2006-01-02 15:04 UTC"))
	}
	return "Your last words before this compaction, sent " + when + ": «" + first + "» — that message was delivered; a summary listing it as still to be sent is wrong, so do not send it again unless the work since has changed it."
}

// listHandoffDocs returns every regular file in dir matching
// handoffDocPattern, by absolute path, newest mtime first (ties by path).
// Nil when dir is empty, unreadable, or holds none.
func listHandoffDocs(dir string) []handoffDoc {
	if dir == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}
	var docs []handoffDoc
	for _, e := range entries {
		if ok, _ := filepath.Match(handoffDocPattern, e.Name()); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		docs = append(docs, handoffDoc{path: filepath.Join(abs, e.Name()), modTime: info.ModTime()})
	}
	sort.SliceStable(docs, func(a, b int) bool {
		if !docs[a].modTime.Equal(docs[b].modTime) {
			return docs[a].modTime.After(docs[b].modTime)
		}
		return docs[a].path < docs[b].path
	})
	return docs
}
