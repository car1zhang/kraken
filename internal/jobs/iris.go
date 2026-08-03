package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// iris.go — the Slack intake agent's lifecycle. iris is the first long-lived
// claude kraken owns that is NOT a job cell: one tmux session, one claude
// session resumed forever, no worktree and no branch. It reads the inbox
// slack.go fills, and its only write into kraken is a job DRAFT.
//
// Everything here is deliberately the CELL machinery with the cell removed.
// The session id is minted by kraken and recorded before the pane exists
// (thread.go's newSessionUUID rationale — a session id read back off discovery
// races the session file and inherits SessionIDGuessed's whole problem class);
// the brief and the initial prompt are staged as FILES and referenced with
// `"$(cat …)"` (stagedCommand, cell.go — tmux refuses a command over ~16KB);
// the spawn runs under cellSpawnEnv() so a launchd-started daemon hands the
// pane a PATH that leads to the toolchain; and a fresh directory gets the
// folder-trust accept pass, without which claude never starts at all.
//
// What is NOT borrowed from cells is the liveness rule. A cell derives
// working/blocked from a roll call of its agents; iris has no state to derive,
// so the whole question is "is there an iris?" and the answer kraken trusts is
// the tmux SESSION, not the peer: a session file takes seconds to appear, so a
// peer-keyed check would spawn a second claude on one session id during every
// boot. carl kills the session when he wants a fresh one.

// IrisSessionName is the tmux session iris lives in, and the single fact
// EnsureIris gates on: present in LiveSessions() means iris exists (or is
// booting) and nothing is spawned. It is a fixed name rather than a per-job
// one because there is exactly one iris on the machine.
const IrisSessionName = "iris"

// IrisAgentName is the claude --name iris runs under — how findIris recognises
// it in a discovery snapshot, the same way ZeusName/ThreadAgentName work for a
// cell's agents.
const IrisAgentName = "iris"

// IrisDir is iris' cwd and where its two staged spawn inputs live. It is a
// plain kraken-owned directory rather than a git worktree: iris edits job
// drafts through the HTTP API, never files in a repo, so there is nothing for
// it to check out.
func IrisDir() string {
	return filepath.Join(util.Home(), ".kraken", "iris")
}

func irisBriefPath() string  { return filepath.Join(IrisDir(), "iris-brief.md") }
func irisPromptPath() string { return filepath.Join(IrisDir(), "iris-prompt.md") }

// irisAgentKVKey holds the session id kraken minted for iris, so every later
// spawn resumes the SAME conversation. A kv singleton rather than a table, for
// dreamRunKVKey's reason: it is one small JSON value written by the daemon,
// which already holds the store's single writer handle.
const irisAgentKVKey = "iris_agent"

// irisAgent is what that key holds. SpawnedAt is stamped on every spawn
// ATTEMPT, not on every success — it is what irisSpawnBackoff measures against,
// and a failed attempt is exactly the one worth backing off from.
type irisAgent struct {
	SessionID string `json:"session_id"`
	SpawnedAt int64  `json:"spawned_at"` // unix millis
}

// irisSpawnBackoff is the minimum gap between two spawn ATTEMPTS.
//
// The session gate alone is not enough, because it only answers "is there a
// session right now". A claude that dies immediately — a launcher that is not
// on PATH, a trust prompt nobody answers, a bad session id — leaves no session,
// so every 15-second tick would spawn another one: four tmux sessions and four
// claude processes a minute, indefinitely, with a log line each. The backoff
// turns that into one attempt per five minutes, which is frequent enough that a
// genuinely transient failure recovers on its own and rare enough that a
// permanent one is merely noisy.
//
// It has no effect on the healthy path: a live iris never reaches here at all.
const irisSpawnBackoff = 5 * time.Minute

// readIrisAgent returns the recorded session id, and whether one exists. An
// unset key is the honest "iris has never been spawned"; an unparseable one is
// treated the same way, because the only repair available is to mint a fresh
// id — and a garbage row that blocked every spawn would leave the feature dead
// with nothing able to fix it.
func readIrisAgent(st *store.Store) (irisAgent, bool) {
	raw, err := st.GetKV(irisAgentKVKey)
	if err != nil || raw == "" {
		return irisAgent{}, false
	}
	var a irisAgent
	if err := json.Unmarshal([]byte(raw), &a); err != nil || a.SessionID == "" {
		util.LogEvent("jobs", "error", "iris-agent", fmt.Sprintf("iris_agent kv unparseable (%q) — minting a fresh session", raw))
		return irisAgent{}, false
	}
	return a, true
}

func writeIrisAgent(st *store.Store, a irisAgent) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return st.SetKV(irisAgentKVKey, string(data))
}

// irisSpawnCommand renders the shell command iris' pane runs, for both paths.
// They differ only in the resume flags: a first spawn NAMES the session
// (--session-id) so kraken knows it before the pane exists, a later spawn
// resumes it (-r).
//
// The brief is staged on BOTH paths, resume included. A resumed session does
// not retain an earlier run's --append-system-prompt (measured, thread.go:438),
// and a resumed iris without its brief is a plain claude holding an intake
// agent's history — it would keep reading the inbox and lose every rule about
// what it may write. `--` stays last so the initial prompt is claude's first
// message rather than another flag's value.
func irisSpawnCommand(sessionID string, resume bool) (string, error) {
	argv := append(cellClaudeArgs(core.IrisModel), "--name", IrisAgentName)
	if resume {
		argv = append(argv, "-r", sessionID)
	} else {
		argv = append(argv, "--session-id", sessionID)
	}
	return stagedCommand(argv,
		spawnInput{"--append-system-prompt", irisBrief(), irisBriefPath()},
		spawnInput{"--", irisPrompt(resume), irisPromptPath()})
}

// EnsureIris keeps exactly one iris alive while Slack is configured. Called
// every tick from inside the daemon's !mirror block; every arm below is a
// no-op returning nil, so the common tick costs one map lookup.
//
// The gate is the tmux SESSION, from the tick's own LiveSessions() snapshot —
// never the discovered peer. claude writes its session file seconds into
// startup, so a peer-keyed gate would read a booting iris as absent and spawn
// a second `claude -r` on the same session id: two writers on one transcript,
// which thread.go calls unrecoverable and which is just as unrecoverable here.
// The cost of trusting the session is that a wedged claude in a live session is
// never replaced automatically — carl kills the session to force a respawn,
// which is the documented move.
//
// Order inside the spawn matters twice. The session id is minted and RECORDED
// before tmux is touched, so a spawn that dies halfway leaves a resumable id
// rather than an orphaned conversation nothing can ever reach again — and the
// attempt is STAMPED before tmux is touched, so a spawn that fails still costs
// the backoff. Stamping on success only would leave a permanently-failing spawn
// firing every tick, which is the exact runaway irisSpawnBackoff exists to stop.
func EnsureIris(st *store.Store, live map[string]bool) error {
	if st == nil {
		return nil // file-mode entrypoints have nowhere to record the session id
	}
	if live[IrisSessionName] {
		return nil
	}
	if _, err := core.LoadSlackConfig(); err != nil {
		if errors.Is(err, core.ErrNoSlack) {
			return nil // feature off: never an error, never a log line
		}
		return err
	}
	now := time.Now()
	agent, resume := readIrisAgent(st)
	if resume && agent.SpawnedAt > 0 && now.Sub(time.UnixMilli(agent.SpawnedAt)) < irisSpawnBackoff {
		return nil // an attempt was made recently — silent, by design
	}
	if err := os.MkdirAll(IrisDir(), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %v", IrisDir(), err)
	}
	if !resume {
		sessionID, err := newSessionUUID()
		if err != nil {
			return err
		}
		agent.SessionID = sessionID
	}
	agent.SpawnedAt = now.UnixMilli()
	// Recorded BEFORE the spawn: if tmux fails below, the next tick resumes
	// this id rather than minting a second one (the transcript claude may
	// already have created stays reachable), and the fresh stamp is what holds
	// the backoff over that failure.
	if err := writeIrisAgent(st, agent); err != nil {
		return fmt.Errorf("record iris session: %v", err)
	}
	cmd, err := irisSpawnCommand(agent.SessionID, resume)
	if err != nil {
		return err
	}
	// -x/-y like every other detached session kraken creates: a bare
	// `new-session -d` starts an 80x24 pane, and claude renders into it.
	spawn := exec.Command("tmux", "new-session", "-d",
		"-x", strconv.Itoa(core.DetachedSessionCols), "-y", strconv.Itoa(core.DetachedSessionRows),
		"-s", IrisSessionName, "-c", IrisDir(),
		"-n", IrisAgentName, cmd)
	// the pane's PATH is this process' PATH: see cellSpawnEnv
	spawn.Env = cellSpawnEnv()
	if out, err := spawn.CombinedOutput(); err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			return fmt.Errorf("tmux new-session %s: %v: %s", IrisSessionName, err, trimmed)
		}
		return fmt.Errorf("tmux new-session %s: %v", IrisSessionName, err)
	}
	// IrisDir is a path claude has never been launched in before its first
	// spawn, which raises the folder-trust dialog; unanswered it blocks startup
	// entirely — no session file, no name, no work.
	acceptTrustPrompts(IrisSessionName)
	// Fires only here, on the far side of an attempt that actually reached
	// tmux: a backed-off tick above returns before this line, so the event
	// counts spawns rather than ticks.
	util.LogEvent("jobs", "info", "iris-spawn", fmt.Sprintf("iris up in session %s (session %s, resume %v)", IrisSessionName, agent.SessionID, resume))
	return nil
}

// findIris returns the live iris peer in a discovery snapshot, or nil.
//
// Preferring the name and falling back positionally is CellZeus' rationale
// applied to a one-pane session: Peer.Name comes from claude's session file, so
// a just-started iris has none, and a name-only lookup reads it as gone —
// which here would mean the delivery pass silently dropping every bump for the
// first seconds of every iris' life. The fallback is restricted to a NAMELESS
// depth-0 claude: anything else in that session under a different name is not
// iris, and typing a bump at it would type at a stranger.
func findIris(all []*core.Peer) *core.Peer {
	var starting *core.Peer
	for _, p := range all {
		if p.TmuxSession != IrisSessionName || p.Depth != 0 || p.Agent != "claude" {
			continue
		}
		if p.Name == IrisAgentName {
			return p
		}
		if p.Name == "" && (starting == nil || paneIndex(p.Pane) < paneIndex(starting.Pane)) {
			starting = p
		}
	}
	return starting
}

// paneIndex orders panes the way CellZeus' fallback does — by tmux's numeric
// pane id, lowest first, so "positionally" means the same thing in both places.
// An unparseable pane sorts last rather than first, since a pane id that
// doesn't look like one is the weakest candidate available.
func paneIndex(pane string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(pane, "%"))
	if err != nil {
		return 1 << 30
	}
	return n
}

// irisPrompt is the first message iris' pane receives. The two paths say
// different things because the two situations differ: a fresh iris has no
// history at all, while a resumed one may have been dead through any number of
// bumps and has to re-derive from the rows rather than from what it remembers.
func irisPrompt(resume bool) string {
	if resume {
		return "You were restarted (your pane died, or the machine rebooted) and this is the same conversation resumed. Do not assume anything about what has happened since your last message: run `kraken slack inbox` and `kraken job list` now and work from what they actually say."
	}
	return "You are starting for the first time. Run `kraken slack inbox` to see whether anything is waiting, then `kraken job list` to learn the current board. If the inbox is empty, say so and go idle — kraken will bump this pane when a message arrives."
}

// irisBrief is iris' system prompt: who it is, the loop it runs, and the three
// rules the whole feature's safety rests on.
//
// It is prose in Go rather than a skill file for the reason zeus' brief is: a
// skill is read once, a system prompt is re-sent every turn, and iris' context
// auto-compacts — so anything iris must still obey after an hour of intake has
// to be here. The three rules stated verbatim (`ready: false`, `kraken slack
// ack`, `## Source`) are also enforced elsewhere — the draft route refuses a
// ready draft server-side, and the ack is what stops a row being re-read
// forever — but a rule enforced only at the boundary produces an agent that
// keeps trying and keeps failing, which reads to carl as a broken bot.
func irisBrief() string {
	var b strings.Builder
	b.WriteString("You are iris — carl's Slack intake representative. You are not a job agent: you have no worktree, no branch, no PR, and you never write code. You read carl's incoming Slack messages and turn the ones that deserve it into kraken job DRAFTS for him to look at.\n")
	fmt.Fprintf(&b, "Kraken spawned you on `%s` and keeps you alive: one tmux session, one conversation, resumed rather than restarted. Nobody is watching this pane, so never ask an interactive question — decide, act, and say what you decided in your final message.\n", core.IrisModel)
	b.WriteString("\n## The loop\n\n")
	b.WriteString("Kraken polls Slack (DMs, group DMs, and channel messages that @-mention carl directly) and writes each new message into an inbox in kraken.db. When new rows land it types a one-line bump into this pane. Every time you are bumped — and every time you resume — run `kraken slack inbox`. It prints the unacked rows as JSON: id, channel, author, timestamp, source (`dm`, `mpim`, `mention`) and text.\n")
	b.WriteString("For EACH row, decide one of three things and then ack it:\n\n")
	b.WriteString("1. **Ignore it.** Chatter, an acknowledgement, something already handled, anything that is not work for carl. This is the common case and it is the right answer far more often than drafting is. Ack it with `ignored`.\n")
	b.WriteString("2. **Draft a new job.** The message describes work carl would plausibly want kraken to do. Write the full job file text and pipe it to `kraken job draft <slug>`. Ack the row with `drafted:<slug>`.\n")
	b.WriteString("3. **Edit an existing draft or queued job.** The message adds to, corrects, or narrows work already on the board — check with `kraken job list` first. Read the current text with `kraken job raw <slug>`, edit it, and pipe the whole new text back through `kraken job draft <slug> --mtime <the mtime that raw reported>`. Ack the row with `edited:<slug>`.\n\n")
	b.WriteString("Ack with: `kraken slack ack --ids 42,43 --disposition \"ignored\"` — several ids at once when they share a disposition, one at a time when they do not.\n")
	b.WriteString("\n## Three rules that are not negotiable\n\n")
	b.WriteString("**Every job you write stays `ready: false`.** You draft; you never queue and you never start. Putting a job on the board is carl's move, never yours — he reads the draft and queues it himself. The server refuses a `ready: true` draft, so writing one just wastes a round trip; more importantly, a job you queued would start a cell carl never approved.\n")
	b.WriteString("**Ack every row you read, exactly once.** An unacked row keeps coming back; a row you ack twice is an error, and the server will tell you so. `kraken slack inbox` only ever returns UNACKED rows, so a row that is gone from the list is a row you have already handled — never re-ack it, and never re-draft from it.\n")
	b.WriteString("**Every job body you write carries a `## Source` section** quoting the message verbatim and naming its author and channel, so carl can see exactly what he is being asked and by whom without leaving the board. Put it at the end of the body.\n")
	b.WriteString("\n## What a job file looks like\n\n")
	b.WriteString("Frontmatter between `---` lines, then a markdown body. The keys you may set are `job` (the slug, matching the one you pass on the command line), `repo`, `ready`, `solo` and `model`. Never write `state`, `session`, `run`, `prs`, `color` or any other key — those are kraken's, and the server refuses a draft that sets them.\n")
	b.WriteString("`repo:` defaults to `monolith` — use it unless the message clearly points somewhere else (it names a repo, or the work is obviously about kraken itself). `ready: false` always. Leave `solo` and `model` out unless you have a reason.\n\n")
	b.WriteString("```\n---\njob: csv-export-timeout\nrepo: monolith\nready: false\n---\n\nThe CSV export times out for orgs with more than ~50k rows. Investigate where the\ntime goes and make it finish, streaming the response if that is what it takes.\n\n## Source\n\nSlack DM from @dana (2026-08-03):\n\n> the csv export just spins forever for big orgs, mine has like 60k rows and it\n> never comes back\n```\n\n")
	b.WriteString("Write the spec as carl would want to read it: what is wrong or wanted, enough context to act on, and no invented requirements. If the message is too vague to specify, say what is unclear in the body rather than guessing — the draft is a starting point, not a contract.\n")
	b.WriteString("\n## Your memory is the rows, not this conversation\n\n")
	b.WriteString("Your context auto-compacts as it fills, and this pane can be restarted at any time. Neither loses anything, because you keep NO durable state in your head: what still needs handling is exactly `kraken slack inbox`, and what is already on the board is exactly `kraken job list`. After any compaction or restart, re-derive from those two commands rather than from what you think you remember — and in particular never re-ack or re-draft from a row you no longer see.\n")
	b.WriteString("You are read-only toward Slack: you cannot reply, react, or post anything. If a message needs an answer from carl, that is what the draft (or your final message) is for.\n")
	return b.String()
}
