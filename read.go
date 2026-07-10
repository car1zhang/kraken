package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// --- k r ---

type block struct {
	kind string // "user" | "thinking" | "text" | "tool"
	text string
}

func cmdRead(args []string) {
	if len(args) == 0 {
		die("usage: k r <sel> [n]")
	}
	// the last arg is n only when it parses and a selector precedes it, so
	// multi-word selectors like "k r asr tuning" still work
	sel, n := strings.Join(args, " "), 1
	if v, err := strconv.Atoi(args[len(args)-1]); err == nil && len(args) > 1 {
		if v < 1 {
			die("n must be a positive integer")
		}
		n = v
		sel = strings.Join(args[:len(args)-1], " ")
	}
	all, _ := orderAll(discoverAll())
	p := selectPeer(sel, all)
	if p.Role == "shell" {
		die("%s is a shell pane — no transcript to read", displayName(p))
	}
	if p.Agent == "codex" {
		die("reading Codex transcripts is not supported yet")
	}
	if p.SessionID == "" {
		die("no session id resolved for %s", displayName(p))
	}
	if p.SessionIDGuessed {
		die("session id for %s is a guess (no session file) — could read another claude's transcript", displayName(p))
	}
	path := transcriptPath(p.SessionID)
	if path == "" {
		die("no transcript yet for session %s (fresh forks write theirs on the first turn)", p.SessionID)
	}
	blocks, counted := lastBlocks(path, n)
	if counted == 0 {
		fmt.Println("no output blocks yet")
		return
	}
	if counted < n {
		fmt.Fprintf(os.Stderr, "kraken: only %d block(s) available\n", counted)
	}
	color := useColor()
	for i, b := range blocks {
		if i > 0 {
			fmt.Println()
		}
		switch b.kind {
		case "user":
			fmt.Println(paint(color, cyan, "❯ user"))
			fmt.Println(b.text)
		case "thinking":
			fmt.Println(paint(color, dim, "· thinking"))
			fmt.Println(paint(color, dim, b.text))
		case "tool":
			fmt.Println(paint(color, dim, "→ "+b.text))
		default:
			fmt.Println(b.text)
		}
	}
}

// lastBlocks returns the transcript's trailing blocks: the last n
// user/thinking/text blocks plus any tool one-liners interleaved after the
// first of them. Reads the file tail backward in growing windows so huge
// transcripts stay fast.
func lastBlocks(path string, n int) ([]block, int) {
	f, err := os.Open(path)
	if err != nil {
		die("open transcript: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		die("stat transcript: %v", err)
	}
	size := info.Size()
	for window := int64(256 * 1024); ; window *= 2 {
		start := size - window
		if start < 0 {
			start = 0
		}
		if _, err := f.Seek(start, 0); err != nil {
			die("seek transcript: %v", err)
		}
		data, err := io.ReadAll(f)
		if err != nil {
			die("read transcript: %v", err)
		}
		lines := strings.Split(string(data), "\n")
		if start > 0 && len(lines) > 0 {
			lines = lines[1:] // drop partial first line
		}
		blocks := parseBlocks(lines)
		counted := 0
		cut := 0
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].kind != "tool" {
				counted++
				if counted == n {
					cut = i
					break
				}
			}
		}
		if counted >= n || start == 0 {
			if counted == 0 {
				return nil, 0
			}
			return blocks[cut:], counted
		}
	}
}

func parseBlocks(lines []string) []block {
	var blocks []block
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // partial trailing line mid-append, or non-JSON
		}
		t, _ := rec["type"].(string)
		if t != "assistant" && t != "user" {
			continue
		}
		if side, _ := rec["isSidechain"].(bool); side {
			continue // subagent traffic — count only the peer's own blocks
		}
		if meta, _ := rec["isMeta"].(bool); meta {
			continue // harness-injected records (command output, caveats)
		}
		msg, _ := rec["message"].(map[string]any)
		if t == "user" {
			blocks = append(blocks, userBlocks(msg["content"])...)
			continue
		}
		parts, _ := msg["content"].([]any)
		for _, part := range parts {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "thinking":
				if s, _ := pm["thinking"].(string); strings.TrimSpace(s) != "" {
					blocks = append(blocks, block{"thinking", s})
				}
			case "text":
				if s, _ := pm["text"].(string); strings.TrimSpace(s) != "" {
					blocks = append(blocks, block{"text", s})
				}
			case "tool_use":
				name, _ := pm["name"].(string)
				blocks = append(blocks, block{"tool", name + "(" + summarizeInput(pm["input"]) + ")"})
			}
		}
	}
	return blocks
}

// userBlocks extracts what the user actually typed from a user record's
// content, which is either a plain string or a list of parts. Tool results
// and injected system-reminder parts ride along in user records but aren't
// the user speaking, so they're dropped.
func userBlocks(content any) []block {
	var blocks []block
	add := func(s string) {
		if strings.TrimSpace(s) == "" || strings.HasPrefix(strings.TrimSpace(s), "<system-reminder>") {
			return
		}
		blocks = append(blocks, block{"user", s})
	}
	switch c := content.(type) {
	case string:
		add(c)
	case []any:
		for _, part := range c {
			pm, ok := part.(map[string]any)
			if !ok || pm["type"] != "text" {
				continue
			}
			s, _ := pm["text"].(string)
			add(s)
		}
	}
	return blocks
}

func summarizeInput(input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"command", "file_path", "path", "pattern", "description", "prompt", "query", "url", "skill"} {
		if s, ok := m[key].(string); ok && s != "" {
			return truncateOneLine(s, 100)
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return truncateOneLine(string(out), 100)
}

func truncateOneLine(s string, limit int) string {
	// control chars (esp. \x1b) in argv would otherwise reach the terminal raw
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// truncTail keeps the end of the string — for branches, the suffix is the
// distinguishing part (carl/v3-native-caller-rtc → …native-caller-rtc).
func truncTail(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return "…" + string(r[len(r)-limit+1:])
}
