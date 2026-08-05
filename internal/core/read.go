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
	// UUID is the "uuid" of the transcript RECORD this block came out of —
	// several blocks can share one (a single assistant record yields a
	// thinking + a text + N tool blocks). Carried for exactly one reader:
	// the side-thread read endpoint (api/threads.go), which splits a forked
	// thread's transcript into inherited pre-fork history and the thread's
	// own turns by comparing against the fork anchor LastMessageUUID
	// recorded at branch time (store.Thread.ForkUUID) — forks preserve
	// message uuids, so the anchor still names a record inside the fork's
	// own copy of the history. Blank when the record carried no uuid; every
	// other caller ignores it.
	UUID string
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

// tailStartWindow is the first (and, for the fixed-window readers, only) tail
// slice every transcript reader here starts from: big enough that the last
// turn of a multi-MB transcript is always inside it, small enough to read and
// JSON-parse on a 2s board tick. A var, not a const, so tests can shrink it
// and exercise the window-growth path on a small file.
var tailStartWindow int64 = 256 * 1024

// lastBlocksScanLimit bounds how far back LastBlocks will grow its window,
// the same hazard (and the same fix) as lastUserScanLimit below. Callers ask
// for anything from n=5 (`k r`, /api/peers/read) to a thread overlay's whole
// conversation, and a forked side thread's transcript is the worst case: it
// opens with zeus' entire history copied in, and a `/do` phase full of
// sidechain (subagent) records — which parseBlocks skips — can never satisfy
// a large n at all, so an unbounded scan walks a 40 MB file to byte 0, and
// re-parses every doubling on the way, on every 3s poll. Past the limit
// LastBlocks returns what it found rather than reading further: a partial
// tail is exactly what a tail read is for. A var, not a const, so tests can
// shrink it.
var lastBlocksScanLimit int64 = 4 << 20

// tailWindow reads the last window bytes of an already-open file as lines,
// dropping the leading partial line whenever the window doesn't reach the
// file's head. The one place the open/seek/ReadAll/split prologue every
// reader in this file needs is written down.
func tailWindow(f *os.File, size, window int64) ([]string, error) {
	start := size - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, fmt.Errorf("seek transcript: %v", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // drop partial first line
	}
	return lines, nil
}

// tailLines is tailWindow's never-an-error, one-shot form: a missing file, an
// unreadable one, or a failed seek all come back as nil lines. LastStall is
// its caller — best-effort by contract, since it reads a file claude owns —
// so that degradation lives here rather than as five early returns in it.
func tailLines(path string, window int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	lines, err := tailWindow(f, info.Size(), window)
	if err != nil {
		return nil
	}
	return lines
}

// LastBlocks returns the transcript's trailing blocks: the last n
// user/thinking/text blocks plus any tool one-liners interleaved after the
// first of them. Reads the file tail backward in growing windows so huge
// transcripts stay fast, stopping at the file's head or at
// lastBlocksScanLimit — whichever comes first — and returning whatever it
// found by then (counted < n is a normal answer, not an error).
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
	for window := tailStartWindow; ; window *= 2 {
		lines, err := tailWindow(f, size, window)
		if err != nil {
			return nil, 0, err
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
		if counted >= n || window >= size || window >= lastBlocksScanLimit {
			if counted == 0 {
				return nil, 0, nil
			}
			return blocks[cut:], counted, nil
		}
	}
}

// Stall classifies how the last turn recorded in a transcript ended.
// Kind "" = healthy/none, "api-error" = the turn died on an API failure
// (Claude Code appends a synthetic assistant entry only after its own
// retries give up), "usage-limit" = an api-error that is specifically an
// exhausted account rather than a fault, "interrupted" = carl pressed esc
// mid-turn.
type Stall struct {
	Kind      string
	Text      string // display text for api-error entries ("API Error: …")
	Transient bool   // api-error only: false for credit/auth/prompt-length failures
}

// nonTransientAPIErrors flags API-error text that nudging/retrying won't
// fix — a fresh turn will hit the exact same wall.
var nonTransientAPIErrors = []string{"credit balance", "prompt is too long", "invalid api key", "authentication", "billing"}

// usageLimitMarkers flag the api-error texts that mean the ACCOUNT is
// exhausted rather than something being broken. They are split out of
// nonTransientAPIErrors — which they would otherwise land in, since neither
// kind is fixable by a retry — because the two want opposite responses: a
// credit or auth failure needs carl, while an exhausted window is fixable
// with no human at all by swapping the main login to a pool account that
// still has headroom. The list is one generic phrase plus the three
// subscription-window banners Claude Code renders for a plan limit ("You've
// hit your session limit · resets …", and the weekly and opus equivalents).
//
// A bare "rate limit" is deliberately NOT here, though it looks like the
// most obvious member: it also matches an organisation burst 429, which is
// genuinely transient and which the existing nudge recovers unattended.
// Listing it would classify that as an exhausted account, take the retry
// away, and swap the machine's login to fix something that was about to fix
// itself.
var usageLimitMarkers = []string{"usage limit", "hit your session limit", "hit your weekly limit", "hit your opus limit"}

// LastStall reads the final message entry of a transcript JSONL file and
// classifies it. It reads only a fixed 256 KiB tail window (huge
// transcripts stay cheap; a stall, if any, is always in the last entry),
// scans backward past trailing bookkeeping records (summary, system,
// progress, ...) for the last assistant/user entry carrying a message
// object, and classifies that single entry. Any open/read error, or a
// transcript with no such entry, returns a zero Stall — never an error.
func LastStall(path string) Stall {
	lines := tailLines(path, tailStartWindow)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // partial trailing line, or non-JSON bookkeeping
		}
		t, _ := rec["type"].(string)
		if t != "assistant" && t != "user" {
			continue
		}
		// Subagent traffic and harness-injected records ride in the same
		// transcript file as zeus' own turns. A sidechain entry landing
		// last must neither be misread as zeus's stall (false positive)
		// nor mask a genuine synthetic api-error written just before it
		// (false negative), so skip past both here exactly as parseBlocks
		// does.
		if side, _ := rec["isSidechain"].(bool); side {
			continue
		}
		if meta, _ := rec["isMeta"].(bool); meta {
			continue
		}
		msg, ok := rec["message"].(map[string]any)
		if !ok {
			continue
		}
		text := strings.TrimSpace(stallText(msg["content"]))
		return classifyStall(t, msg, text)
	}
	return Stall{}
}

// LastMessageUUID returns the uuid of the last transcript record that a
// READER would still see — the message a fork of that session hangs off, and
// which kraken stores as a side thread's fork anchor (store.Thread.ForkUUID)
// so the read endpoint can split the fork's transcript into inherited history
// and the thread's own turns.
//
// The anchor is therefore taken from parseBlocks' OWN output, not from the raw
// lines: the consumer (api's threadForkSplit) matches it against block.UUID,
// so an anchor from a record parseBlocks drops is unmatchable by construction
// and silently disables the divider. That is not a corner case — trailing
// bookkeeping records DO carry uuids, and a `system` record is the common tail
// (every cell installs a PostToolUse `kraken pr-hook`, which writes one), as do
// attachments, sidechain/meta records, and content-less assistant/user records.
// Walking blocks backward for the last non-blank UUID picks the last record
// that both survives parseBlocks' type/sidechain/isMeta gate AND produces at
// least one block, which is exactly what the split can find again.
//
// The window grows like LastBlocks' — under the same lastBlocksScanLimit
// ceiling — rather than staying fixed at one tail slice, because the tail
// slice of a zeus mid-`/do` is often ENTIRELY subagent (sidechain) records
// and tool results, which is exactly when carl branches a thread to ask about
// the block he's staring at. Growing costs nothing on a poll: this runs once
// per branch, never on a tick.
//
// Provenance only, never a resume target, so this is deliberately
// best-effort: a missing file, an unreadable one, a truncated tail, or a
// transcript with no readable message inside the ceiling all return "" rather
// than an error, and no caller may fail a spawn over it.
func LastMessageUUID(path string) string {
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
	for window := tailStartWindow; ; window *= 2 {
		lines, err := tailWindow(f, size, window)
		if err != nil {
			return ""
		}
		blocks := parseBlocks(lines)
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].UUID != "" {
				return blocks[i].UUID
			}
		}
		if window >= size || window >= lastBlocksScanLimit {
			return ""
		}
	}
}

// stallText concatenates a message's text content — a plain string, or the
// "text" blocks of a content-part array (tool_use/tool_result parts carry
// no display text and are skipped).
func stallText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			pm, ok := part.(map[string]any)
			if !ok || pm["type"] != "text" {
				continue
			}
			if s, _ := pm["text"].(string); s != "" {
				b.WriteString(s)
			}
		}
		return b.String()
	default:
		return ""
	}
}

// classifyStall applies the api-error/interrupted rules to a single
// already-located assistant or user entry.
func classifyStall(entryType string, msg map[string]any, text string) Stall {
	switch entryType {
	case "assistant":
		model, _ := msg["model"].(string)
		if model != "<synthetic>" {
			return Stall{} // a real model merely mentioning "API Error" isn't a stall
		}
		lower := strings.ToLower(text)
		if !strings.HasPrefix(lower, "api error") {
			return Stall{}
		}
		// This arm returns, so it has to precede the nonTransientAPIErrors
		// loop below, which merely sets a flag and falls through — the
		// ordering is a consequence of that shape rather than a tie-break
		// between two tests that both match.
		//
		// The arm itself is what earns its place: without it, the very text
		// this feature exists for ("You've hit your session limit · resets
		// …") matches nothing in the non-transient list, so it classifies
		// api-error TRANSIENT and the daemon's health pass nudges it every
		// ten minutes, forever, at an account that cannot answer. Transient
		// is therefore stated here rather than left to the zero value: an
		// exhausted window does pass on its own, but not on the timescale a
		// nudge assumes.
		for _, s := range usageLimitMarkers {
			if strings.Contains(lower, s) {
				return Stall{Kind: "usage-limit", Text: TruncateOneLine(text, 120), Transient: false}
			}
		}
		transient := true
		for _, s := range nonTransientAPIErrors {
			if strings.Contains(lower, s) {
				transient = false
				break
			}
		}
		return Stall{Kind: "api-error", Text: TruncateOneLine(text, 120), Transient: transient}
	case "user":
		if strings.HasPrefix(text, "[Request interrupted by user") {
			return Stall{Kind: "interrupted"}
		}
	}
	return Stall{}
}

// lastUserScanLimit bounds how far back LastUserText will grow its scan
// window — the same ceiling lastBlocksScanLimit puts on LastBlocks, but a
// looser one, because the thing it hunts for is genuinely rare rather than
// merely deep: a job that was never steered has its only real user text —
// the spawn prompt — sitting
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
	for window := tailStartWindow; ; window *= 2 {
		lines, err := tailWindow(f, size, window)
		if err != nil {
			return ""
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
		if window >= size || window >= lastUserScanLimit {
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
		// stamped onto every block this record produces (see block.UUID)
		uuid, _ := rec["uuid"].(string)
		if t == "user" {
			ub := userBlocks(msg["content"])
			for i := range ub {
				ub[i].UUID = uuid
			}
			blocks = append(blocks, ub...)
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
					blocks = append(blocks, block{Kind: "thinking", Text: s, UUID: uuid})
				}
			case "text":
				if s, _ := pm["text"].(string); strings.TrimSpace(s) != "" {
					blocks = append(blocks, block{Kind: "text", Text: s, UUID: uuid})
				}
			case "tool_use":
				name, _ := pm["name"].(string)
				blocks = append(blocks, block{Kind: "tool", Text: name + "(" + summarizeInput(pm["input"]) + ")", UUID: uuid})
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
