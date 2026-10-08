package main

import (
	"bytes"
	"encoding/json"
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
// After a compaction it tells the resumed agent which 80-handoff*.md docs sit
// in the cell's artifacts dir, newest first, so the agent reads its own
// handoff before doing anything else.
//
// It shares cmd_ctx.go's posture: it NEVER FAILS. Every bad input — empty or
// malformed stdin, a non-compact source, a missing dir, no handoff docs —
// degrades to printing nothing and exiting 0, and nothing is ever written to
// stderr. Kept out of usage() beside the rest of the hook family.

// handoffDocPattern is the artifact name shape the hook lists.
const handoffDocPattern = "80-handoff*.md"

// handoffClosing is the sentence the injected context always ends with.
const handoffClosing = "Read the one that is yours before anything else; a doc older than the work you remember is history."

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
	docs := listHandoffDocs(artifactsDir)
	if len(docs) == 0 {
		return
	}
	var text strings.Builder
	text.WriteString("Handoff docs in this cell's artifacts dir, newest first:\n")
	for _, d := range docs {
		age := int64(now.Sub(d.modTime) / time.Minute)
		if age < 0 {
			age = 0
		}
		fmt.Fprintf(&text, "- %s (written %dm ago)\n", d.path, age)
	}
	text.WriteString(handoffClosing)
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
