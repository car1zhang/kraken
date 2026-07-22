package core

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// --- k r ---

type block struct {
	Kind string // "user" | "thinking" | "text" | "tool" | "teammate"
	Text string
	From string // teammate_id, set only for kind "teammate"
}

// teammateMsgRe matches the harness-wrapped envelope another kraken cell's
// zeus injects into a user turn when messaging this session (see
// SendMessage's tool description). Attribute order/extras beyond
// teammate_id are tolerated; a wrapper missing teammate_id simply fails to
// match, so callers fall back to treating the text as a plain user block.
var teammateMsgRe = regexp.MustCompile(`(?s)<teammate-message[^>]*\bteammate_id="([^"]+)"[^>]*>\s*(.*?)\s*</teammate-message>`)

// commandRe matches the harness-synthesized record for a typed slash
// command: the raw keystrokes are replaced with a wrapper of
// <command-name>, <command-message>, and (if the user typed args)
// <command-args> tags. Two orders are observed in the wild: modern
// transcripts put <command-message> first (…</command-message><command-name>/foo</command-name>
// [<command-args>bar</command-args>]), older ones put <command-name> first
// with <command-message> trailing. <command-args> is entirely absent when
// the command was typed with no arguments — it is not merely empty.
//
// The match is anchored to the start of the (trimmed) text: a genuine user
// message that merely quotes one of these tags mid-sentence has real prose
// before it and so must NOT collapse to "/foo".
var commandRe = regexp.MustCompile(`^\s*(?:<command-message>[^<]*</command-message>\s*)?<command-name>([^<]*)</command-name>\s*(?:<command-message>[^<]*</command-message>\s*)?(?:<command-args>([^<]*)</command-args>)?`)

// ReadTranscriptPath applies k r's pre-flight refusals — shared by cmdRead
// and the HTTP read endpoint so both give byte-identical error text — and
// resolves the peer's transcript path.
func ReadTranscriptPath(p *Peer) (string, error) {
	if p.Role == "shell" {
		return "", fmt.Errorf("%s is a shell pane — no transcript to read", DisplayName(p))
	}
	if p.Agent == "codex" {
		return "", fmt.Errorf("reading Codex transcripts is not supported yet")
	}
	if p.SessionID == "" {
		return "", fmt.Errorf("no session id resolved for %s", DisplayName(p))
	}
	if p.SessionIDGuessed {
		return "", fmt.Errorf("session id for %s is a guess (no session file) — could read another claude's transcript", DisplayName(p))
	}
	path := transcriptPath(p.SessionID)
	if path == "" {
		return "", fmt.Errorf("no transcript yet for session %s (fresh forks write theirs on the first turn)", p.SessionID)
	}
	return path, nil
}

// LastBlocks returns the transcript's trailing blocks: the last n
// user/thinking/text blocks plus any tool one-liners interleaved after the
// first of them. Reads the file tail backward in growing windows so huge
// transcripts stay fast.
func LastBlocks(path string, n int) ([]block, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open transcript: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat transcript: %v", err)
	}
	size := info.Size()
	for window := int64(256 * 1024); ; window *= 2 {
		start := size - window
		if start < 0 {
			start = 0
		}
		if _, err := f.Seek(start, 0); err != nil {
			return nil, 0, fmt.Errorf("seek transcript: %v", err)
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return nil, 0, fmt.Errorf("read transcript: %v", err)
		}
		lines := strings.Split(string(data), "\n")
		if start > 0 && len(lines) > 0 {
			lines = lines[1:] // drop partial first line
		}
		blocks := parseBlocks(lines)
		counted := 0
		cut := 0
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].Kind != "tool" {
				counted++
				if counted == n {
					cut = i
					break
				}
			}
		}
		if counted >= n || start == 0 {
			if counted == 0 {
				return nil, 0, nil
			}
			return blocks[cut:], counted, nil
		}
	}
}

// lastUserScanLimit bounds how far back LastUserText will grow its scan
// window. Unlike LastBlocks/lastAssistantText, which are effectively
// tail-bounded because assistant blocks are dense at the tail, a job that
// was never steered has its only real user text — the spawn prompt — sitting
// at the file HEAD, so an unbounded backward scan would read and JSON-parse
// the entire transcript (tens of MB for long runs) on every board cache
// miss, which happens per live job every 2s. A user message buried deeper
// than this is stale enough that omitting last_sent is preferable to paying
// for a full-file scan on every poll. A var, not a const, so tests can
// shrink it.
var lastUserScanLimit int64 = 8 << 20

// LastUserText returns the text of the last real user message in a
// transcript — backs the board's last_sent field. Reads the file tail
// backward in growing windows like LastBlocks, walking the parsed blocks
// backward for the first "user" one that isn't a harness interruption
// artifact ("[Request interrupted..."), then normalizes a slash-command
// wrapper down to "/foo bar baz". Returns "" when the file is unreadable,
// empty, contains no acceptable user block, or the scan exceeds
// lastUserScanLimit without finding one.
func LastUserText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	size := info.Size()
	for window := int64(256 * 1024); ; window *= 2 {
		start := size - window
		if start < 0 {
			start = 0
		}
		if _, err := f.Seek(start, 0); err != nil {
			return ""
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return ""
		}
		lines := strings.Split(string(data), "\n")
		if start > 0 && len(lines) > 0 {
			lines = lines[1:] // drop partial first line
		}
		blocks := parseBlocks(lines)
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].Kind != "user" {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(blocks[i].Text), "[Request interrupted") {
				continue
			}
			if m := commandRe.FindStringSubmatch(strings.TrimSpace(blocks[i].Text)); m != nil {
				if name := strings.TrimSpace(m[1]); name != "" {
					if cmdArgs := strings.TrimSpace(m[2]); cmdArgs != "" {
						return name + " " + cmdArgs
					}
					return name
				}
			}
			return blocks[i].Text
		}
		if start == 0 || window >= lastUserScanLimit {
			return ""
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
					blocks = append(blocks, block{Kind: "thinking", Text: s})
				}
			case "text":
				if s, _ := pm["text"].(string); strings.TrimSpace(s) != "" {
					blocks = append(blocks, block{Kind: "text", Text: s})
				}
			case "tool_use":
				name, _ := pm["name"].(string)
				blocks = append(blocks, block{Kind: "tool", Text: name + "(" + summarizeInput(pm["input"]) + ")"})
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
		if b, ok := teammateBlock(s); ok {
			blocks = append(blocks, b)
			return
		}
		blocks = append(blocks, block{Kind: "user", Text: s})
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

// teammateBlock detects a harness-wrapped teammate message inside a user
// text part and, if found, renders it as a "teammate" block. idle
// notifications (the JSON idle-poll heartbeat teams exchange) collapse to
// a one-line summary; anything else is a real message and is kept
// verbatim, since it's often markdown meant to be read as-is. Returns
// ok=false — and the caller falls back to a plain "user" block — when the
// wrapper tag is present but missing teammate_id, or absent entirely.
func teammateBlock(s string) (block, bool) {
	m := teammateMsgRe.FindStringSubmatch(s)
	if m == nil {
		return block{}, false
	}
	from, payload := m[1], strings.TrimSpace(m[2])
	if from == "" {
		return block{}, false
	}
	text := payload
	var rec map[string]any
	if err := json.Unmarshal([]byte(payload), &rec); err == nil {
		if t, _ := rec["type"].(string); t == "idle_notification" {
			text = "idle"
			if reason, _ := rec["idleReason"].(string); reason != "" {
				text = fmt.Sprintf("idle (%s)", reason)
			}
		}
	}
	return block{Kind: "teammate", Text: text, From: from}, true
}

func summarizeInput(input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"command", "file_path", "path", "pattern", "description", "prompt", "query", "url", "skill"} {
		if s, ok := m[key].(string); ok && s != "" {
			return TruncateOneLine(s, 100)
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return TruncateOneLine(string(out), 100)
}

func TruncateOneLine(s string, limit int) string {
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

// TruncTail keeps the end of the string — for branches, the suffix is the
// distinguishing part (carl/v3-native-caller-rtc → …native-caller-rtc).
func TruncTail(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return "…" + string(r[len(r)-limit+1:])
}
