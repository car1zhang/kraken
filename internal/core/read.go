package core

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
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
		// Discovery already read this peer's rollout out of the threads row
		// it identified the session from, and that row came from the pane's
		// OWN codex home. Re-deriving it by session id would re-query the
		// machine login's index, which holds no row at all for a session a
		// pool-bound cell recorded — so the lookup below is the fallback for
		// a peer discovery did not resolve a rollout for, never the answer
		// for one it did.
		if p.RolloutPath != "" {
			return p.RolloutPath, nil
		}
		if rp := codexRolloutPath(p.SessionID); rp != "" {
			return rp, nil
		}
		return "", fmt.Errorf("no rollout resolved for %s", DisplayName(p))
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

// TranscriptTurnTail bounds how much of a transcript TranscriptTurnSince
// reads: the records that can answer "did a turn run after the cut" are at
// the end, and a long session's file is tens of MB.
const TranscriptTurnTail int64 = 2 << 20

// TranscriptTurnSince reports whether the transcript at path holds a REAL
// assistant turn after `since` (unix seconds): a `type:"assistant"` record
// whose timestamp falls in a later second than `since` and whose
// `message.model` is neither empty nor Claude Code's "<synthetic>" — the
// model a limit-latched pane stamps on the banner it writes instead of
// calling the API. Only the file's last TranscriptTurnTail bytes are read
// (tailWindow, which drops the partial line a tail read starts inside). The
// comparison is at second granularity because every cut a caller holds is
// `now.Unix()`-shaped: a record written in the SAME second as the cut is
// not after it, since the hit's own failing turn can leave a real record
// a few hundred milliseconds before the delivery that stamps the second.
// A missing or unreadable file, an undecodable line, or a record with no
// parseable timestamp never satisfies it. The jobs package's limit rescue
// is its caller (a revived zeus has no latch left to clear).
func TranscriptTurnSince(path string, since int64) bool {
	if path == "" || since == 0 {
		return false
	}
	for _, line := range tailLines(path, TranscriptTurnTail) {
		if !strings.Contains(line, "\"assistant\"") {
			continue
		}
		var rec struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec.Type != "assistant" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if err != nil || ts.Unix() <= since {
			continue
		}
		if rec.Message.Model == "" || rec.Message.Model == "<synthetic>" {
			continue
		}
		return true
	}
	return false
}

// LastBlocks returns the transcript's trailing blocks: the last n
// user/thinking/text blocks plus any tool one-liners interleaved after the
// first of them. Reads the file tail backward in growing windows so huge
// transcripts stay fast, stopping at the file's head or at
// lastBlocksScanLimit — whichever comes first — and returning whatever it
// found by then (counted < n is a normal answer, not an error).
//
// Dispatch is by PATH, through IsCodexRollout: a Codex rollout is parsed by
// parseCodexBlocks; every other path is a claude transcript and takes the
// parseBlocks path unchanged. The test is the file's SHAPE rather than a
// "/.codex/sessions/" substring, because a pool-bound cell writes its
// rollout under <pool>/accounts/<slug>/sessions/ — no ".codex" anywhere in
// it. The tail-read, window-growth and counting logic is shared — both
// parsers emit the same block vocabulary, so "count the non-tool blocks"
// reads the same either way.
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
	codex := IsCodexRollout(path)
	for window := tailStartWindow; ; window *= 2 {
		lines, err := tailWindow(f, size, window)
		if err != nil {
			return nil, 0, err
		}
		var blocks []block
		if codex {
			blocks = parseCodexBlocks(lines)
		} else {
			blocks = parseBlocks(lines)
		}
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

// LastTurns returns the transcript's trailing TURNS: every block from the
// n-th most recent prompt onward. A turn is one of carl's prompts plus
// everything that follows it until the next — a "user" block that
// harnessUserRecord does not claim (the same prompt test LastTurnUUID makes;
// a tool_result-only user record, a teammate message and a daemon-typed
// `/color` echo start no turn). counted is how many prompts the answer
// spans. It shares LastBlocks' backward growing-window read and its two
// stops: at the file's head every block is inside the last n turns, and at
// lastBlocksScanLimit the answer is cut at the EARLIEST prompt the window
// reached, so the oldest turn shown is a whole one rather than the tail of a
// turn whose prompt is out of the window — unless the window holds no prompt
// at all (one enormous turn), where the window's blocks are the answer and
// counted is 0.
//
// maxBlocks (≤ 0 for no cap) bounds the answer by dropping the OLDEST whole
// turns until it fits, never cutting inside one, and the newest turn is
// always kept even when it alone exceeds the cap: a turn is only legible
// from its prompt, and the panel that asks for turns is the panel carl reads
// to see what zeus is doing now.
func LastTurns(path string, n, maxBlocks int) ([]block, int, error) {
	n = max(n, 1) // n=0 would index prompts[-1] on a prompt-less window
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
	codex := IsCodexRollout(path)
	for window := tailStartWindow; ; window *= 2 {
		lines, err := tailWindow(f, size, window)
		if err != nil {
			return nil, 0, err
		}
		var blocks []block
		if codex {
			blocks = parseCodexBlocks(lines)
		} else {
			blocks = parseBlocks(lines)
		}
		if len(blocks) == 0 && (window >= size || window >= lastBlocksScanLimit) {
			return nil, 0, nil
		}
		// prompt indices, newest first
		var prompts []int
		for i := len(blocks) - 1; i >= 0; i-- {
			if isPromptBlock(blocks[i]) {
				prompts = append(prompts, i)
				if len(prompts) == n {
					break
				}
			}
		}
		whole := window >= size
		if len(prompts) < n && !whole && window < lastBlocksScanLimit {
			continue
		}
		cut := 0
		switch {
		case len(prompts) == n:
			cut = prompts[n-1]
		case whole:
			cut = 0 // fewer than n turns exist: all of them
		case len(prompts) > 0:
			cut = prompts[len(prompts)-1] // scan limit: oldest whole turn seen
		}
		// Drop the oldest whole turns past the cap, keeping the newest. k is
		// the prompts index of the turn cut currently starts at; len(prompts)
		// means cut sits BEFORE the oldest prompt (a whole-file answer with
		// blocks ahead of its first prompt), so the first drop is of those
		// leading blocks alone. With no prompt in the window there is no turn
		// head to protect, so the cap is a plain tail cut.
		k := len(prompts)
		if len(prompts) > 0 && cut == prompts[len(prompts)-1] {
			k = len(prompts) - 1
		}
		for maxBlocks > 0 && len(blocks)-cut > maxBlocks && k > 0 {
			k--
			cut = prompts[k]
		}
		if maxBlocks > 0 && len(prompts) == 0 && len(blocks) > maxBlocks {
			cut = len(blocks) - maxBlocks
		}
		counted := 0
		for _, p := range prompts {
			if p >= cut {
				counted++
			}
		}
		return blocks[cut:], counted, nil
	}
}

// isPromptBlock reports whether a block is one of carl's prompts — the
// record that starts a turn. LastTurnUUID additionally requires a uuid, since
// its answer IS the uuid; a codex rollout's user_message has none and still
// starts a turn here.
func isPromptBlock(b block) bool {
	return b.Kind == "user" && !harnessUserRecord(b.Text)
}

// BlocksAround returns the blocks within radius of the record carrying uuid,
// wherever in the transcript it sits: up to radius blocks before the first
// block stamped with uuid, every block through the last one stamped with it,
// and up to radius after. first and last index the hit span inside the
// returned slice; found is false (with nil blocks) when no block carries the
// uuid.
//
// It is the FORWARD counterpart of LastBlocks, for a reader that knows which
// record it wants rather than how much of the tail: a mental-model item's
// source pointer can name a record from the start of a 40 MB transcript, which
// LastBlocks' lastBlocksScanLimit tail can never reach. Memory stays bounded by
// one line plus 2·radius blocks — the file streams line by line through the
// same per-line parsers (parseBlocks / parseCodexBlocks, dispatched by
// IsCodexRollout exactly as LastBlocks does), a ring holds the blocks before
// the hit, and the scan stops once radius blocks follow the last hit. Lines are
// read with bufio.Reader rather than a Scanner so one oversized record (a huge
// tool result) has no ceiling to trip over and fail the whole read.
func BlocksAround(path, uuid string, radius int) (blocks []block, first, last int, found bool, err error) {
	if uuid == "" {
		return nil, 0, 0, false, nil // codex blocks carry no uuid; "" would match them all
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, false, fmt.Errorf("open transcript: %v", err)
	}
	defer f.Close()
	parse := parseBlocks
	if IsCodexRollout(path) {
		parse = parseCodexBlocks
	}
	radius = max(radius, 0)
	var before []block // ring of the last radius blocks before the first hit
	after := 0         // blocks collected since the last hit
	rd := bufio.NewReaderSize(f, 64*1024)
	for {
		line, rerr := rd.ReadString('\n')
		if line != "" {
			for _, b := range parse([]string{line}) {
				switch {
				case b.UUID == uuid:
					if !found {
						found = true
						blocks = append(blocks, before...)
						before = nil
						first = len(blocks)
					}
					blocks = append(blocks, b)
					last = len(blocks) - 1
					after = 0
				case found:
					if after == radius {
						return blocks, first, last, true, nil
					}
					blocks = append(blocks, b)
					after++
				default:
					if radius == 0 {
						continue
					}
					if len(before) == radius {
						before = append(before[:0], before[1:]...)
					}
					before = append(before, b)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, 0, 0, false, fmt.Errorf("read transcript: %v", rerr)
		}
	}
	if !found {
		return nil, 0, 0, false, nil
	}
	return blocks, first, last, true, nil
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

// usageLimitMarkers flag the synthetic-record texts that mean the ACCOUNT is
// exhausted rather than something being broken. They are split out of
// nonTransientAPIErrors — which they would otherwise land in, since neither
// kind is fixable by a retry — because the two want opposite responses: a
// credit or auth failure needs carl, while an exhausted window is fixable
// with no human at all by swapping the main login to a pool account that
// still has headroom. The list is one generic phrase, the three
// subscription-window banners Claude Code renders for a plan limit ("You've
// hit your session limit · resets …", and the weekly and opus equivalents),
// and the two credit-window shapes measured on this machine 2026-08-06: the
// per-model-tier banner ("You've reached your <model> limit. Run
// /usage-credits to continue or switch models with /model."), matched on its
// remedy token because the model name in it varies with the lead's tier, and
// the flat "Usage credits are required for this model.".
//
// A bare "rate limit" is deliberately NOT here, though it looks like the
// most obvious member: it also matches an organisation burst 429, which is
// genuinely transient and which the existing nudge recovers unattended.
// Listing it would classify that as an exhausted account, take the retry
// away, and swap the machine's login to fix something that was about to fix
// itself.
var usageLimitMarkers = []string{"usage limit", "hit your session limit", "hit your weekly limit", "hit your opus limit", "/usage-credits", "usage credits are required"}

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

// LastTurnUUID returns the uuid of the transcript's last PROMPT record — a
// `user` record whose content is a prompt carl (or a steer) typed, rather
// than a tool result or something the harness wrote — or "" when there is
// none (or the file cannot be read). It was the retired mind thread's "same
// turn" key and has no production caller any more; it is retained because
// existing mind thread rows carry values it produced and its tests pin the
// reader. Every tool call zeus makes inside one turn appends records, so
// LastMessageUUID moves constantly mid-turn, while this one moves only when
// a new prompt arrives.
//
// Deliberately NOT a tail read: one /do turn's tool results can run past
// lastBlocksScanLimit (10 of 43 zeus transcripts measured on 2026-10-08
// carried a prompt-to-prompt span over 4 MB, the longest 11.7 MB), and a
// bounded tail then answers "" for exactly the turns carl reads artifacts
// during, which branched one mind thread per capture when this was the
// capture key. It was never called on a tick, so it reads the whole file
// forward one line at a time — BlocksAround's reader — and keeps the last
// prompt it saw.
//
// parseBlocks (via userBlocks) already gives a tool_result-only user record
// no block at all, drops a bare <system-reminder> injection, and parses a
// harness-wrapped teammate message as a "teammate" block, so none of those
// count. harnessUserRecord drops the rest of what Claude Code itself writes
// as `user` records — background-task notifications, slash-command echoes,
// the compaction summary, an interrupt marker — measured at ~40% of the
// non-prompt user records in real cell transcripts: carl's next prompt is
// what starts a new turn, not the daemon's `/color` or a finished Bash task.
func LastTurnUUID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	last := ""
	rd := bufio.NewReaderSize(f, 64*1024)
	for {
		line, rerr := rd.ReadString('\n')
		if line != "" {
			for _, b := range parseBlocks([]string{line}) {
				if isPromptBlock(b) && b.UUID != "" {
					last = b.UUID
				}
			}
		}
		if rerr != nil {
			break // EOF, or a read error: the last prompt seen is still the answer
		}
	}
	return last
}

// harnessUserRecord reports whether a `user` block's text is one Claude Code
// wrote itself rather than a prompt: a background-task completion, a LOCAL
// slash command's echo and output (`/model`, `/compact`, the daemon's
// `/color` — these begin `<command-name>`), a `!` shell command's input and
// output, the post-compaction summary, or an interrupt marker. Every one of
// these lands as a user record and parses as a "user" block, and none of
// them is a new turn. A SKILL invocation is deliberately not here: carl's
// `/do`, `/aim`, `/info` land as records beginning `<command-message>`
// (38 of 38 such records in 250 cell transcripts, measured 2026-10-08), and
// each of those IS carl's next prompt — the turn carl is reading during.
func harnessUserRecord(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range []string{
		"<task-notification>",
		"<command-name>",
		"<local-command-stdout>",
		"<local-command-caveat>",
		"<bash-input>",
		"<bash-stdout>",
		"<bash-stderr>",
		"[Request interrupted",
		"This session is being continued from a previous conversation",
	} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
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
		// The usage-limit check runs BEFORE the "api error" prefix gate, and
		// that ordering is load-bearing: the limit banners Claude Code
		// actually writes carry no such prefix (measured 2026-08-06 across
		// every transcript on this machine — 177 limit banners, all opening
		// "You've …" or "Usage credits …", zero opening "API Error"), so a
		// marker check placed behind the gate is unreachable on every real
		// banner. It also precedes the nonTransientAPIErrors loop below for
		// a reason of its own: a banner that happens to carry both a limit
		// marker and a non-transient marker must route to the rescue, which
		// can fix it unattended, rather than to carl. Transient is stated
		// rather than left to the zero value: an exhausted window does pass
		// on its own, but not on the timescale a nudge assumes.
		for _, s := range usageLimitMarkers {
			if strings.Contains(lower, s) {
				return Stall{Kind: "usage-limit", Text: TruncateOneLine(text, 120), Transient: false}
			}
		}
		if !strings.HasPrefix(lower, "api error") {
			return Stall{}
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

// parseCodexBlocks maps a Codex rollout transcript's JSONL records to the same
// block vocabulary parseBlocks produces, so LastBlocks' counting and every
// reader downstream treat a codex tail exactly like a claude one. Each record
// is {timestamp,type,payload}; the four shapes that carry display content map
// as: event_msg/user_message → "user", event_msg/agent_message → "text",
// response_item/function_call → "tool" (name + a one-lined argument summary),
// response_item/reasoning → "thinking" (the plaintext summary/content, when the
// reasoning is not encrypted-only). Every other type — turn_context,
// token_count, session_meta, function_call_output, an encrypted-only reasoning,
// an unknown payload, or a malformed line — is skipped silently, and an
// empty-text user/agent/reasoning record is dropped like parseBlocks drops its
// empty content parts, so a codex tail never contributes blank blocks.
func parseCodexBlocks(lines []string) []block {
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
		payload, ok := rec["payload"].(map[string]any)
		if !ok {
			continue
		}
		pt, _ := payload["type"].(string)
		switch {
		case t == "event_msg" && pt == "user_message":
			if s, _ := payload["message"].(string); strings.TrimSpace(s) != "" {
				blocks = append(blocks, block{Kind: "user", Text: s})
			}
		case t == "event_msg" && pt == "agent_message":
			if s, _ := payload["message"].(string); strings.TrimSpace(s) != "" {
				blocks = append(blocks, block{Kind: "text", Text: s})
			}
		case t == "response_item" && pt == "function_call":
			name, _ := payload["name"].(string)
			args, _ := payload["arguments"].(string)
			blocks = append(blocks, block{Kind: "tool", Text: name + "(" + TruncateOneLine(args, 100) + ")"})
		case t == "response_item" && pt == "reasoning":
			if s := codexReasoningText(payload); strings.TrimSpace(s) != "" {
				blocks = append(blocks, block{Kind: "thinking", Text: s})
			}
		}
	}
	return blocks
}

// codexReasoningText pulls the human-readable text out of a codex reasoning
// payload. Codex records the visible chain-of-thought summary as a `summary`
// array of {type:"summary_text", text:…} parts and leaves `content` null; when
// summaries are off the summary is empty and only an `encrypted_content` blob
// remains, which carries no plaintext and yields "". `content` is honoured as a
// fallback (a string, or the same part-array shape) for forward compatibility.
func codexReasoningText(payload map[string]any) string {
	var b strings.Builder
	appendPart := func(part any) {
		pm, ok := part.(map[string]any)
		if !ok {
			return
		}
		if s, _ := pm["text"].(string); s != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(s)
		}
	}
	if summary, ok := payload["summary"].([]any); ok {
		for _, part := range summary {
			appendPart(part)
		}
	}
	if b.Len() == 0 {
		switch c := payload["content"].(type) {
		case string:
			b.WriteString(c)
		case []any:
			for _, part := range c {
				appendPart(part)
			}
		}
	}
	return b.String()
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
