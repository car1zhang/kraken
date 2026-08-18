package jobs

import (
	"encoding/json"
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

// kronos.go — the orchestrator agent's lifecycle. kronos is the second
// long-lived claude kraken owns that is NOT a job cell (iris was the first),
// and it is iris' machinery with one difference that matters: iris READS an
// inbox and writes drafts, while kronos reads an inbox and drives cells that
// already exist. Everything else here — one tmux session, one claude session
// resumed forever, no worktree, no branch, a session id minted by kraken and
// recorded before the pane exists, brief and prompt staged as files — is
// iris.go's shape, and the comments there own the rationale for each piece.
//
// Three things are deliberately kronos-only.
//
// The FEATURE GATE is a file. `~/.kraken/kronos.env` existing (empty is
// enough) is the whole opt-in, and with it absent EnsureKronos, deliverOrch
// and ctxPass all cost one stat and return. iris gates on its credential
// instead, because Slack is unusable without one; kronos needs no credential
// to be useful, so the gate has to be its own file rather than a side effect
// of another feature's configuration.
//
// The BRIEF REPLACES Claude Code's system prompt rather than appending to it
// (`--system-prompt-file`, a PATH argument — not stagedCommand's
// `--append-system-prompt` input, which passes the file's CONTENTS through
// `"$(cat …)"`). An orchestrator that inherited the default coding-agent
// prompt would keep reaching for the repo it does not have; replacing it makes
// the brief the only thing in context, which is also why the brief has to
// carry its own tool-usage section (kronosBrief, below).
//
// The SETTINGS kronos runs under are a DIFFERENT set from a cell's
// (installKronosSettings): no pr-hook (kronos opens no PRs), no agent-teams
// keys (it delegates nothing), plus two of its own — a PostCompact hook that
// re-arms graph recall for the compacted session, and a statusLine that
// reports its own context fill back to kraken. The limit-hook it DOES get,
// with an --agent id where a cell's carries a slug: kronos burns the same one
// Claude login every cell does, and an exhausted account leaves it just as
// dead in a pane nobody is watching.

// KronosSessionName is the tmux session kronos lives in, and the single fact
// EnsureKronos gates on: present in LiveSessions() means kronos exists (or is
// booting) and nothing is spawned. Fixed rather than per-job because there is
// exactly one kronos on the machine.
const KronosSessionName = "kronos"

// KronosAgentName is the claude --name kronos runs under — how findKronos
// recognises it in a discovery snapshot, the same way IrisAgentName does for
// iris.
const KronosAgentName = "kronos"

// KronosDir is kronos' cwd and where its staged spawn inputs and its
// Claude Code settings live. A plain kraken-owned directory rather than a git
// worktree: kronos reaches every job through the HTTP API, never through
// files in a repo, so there is nothing for it to check out.
func KronosDir() string {
	return filepath.Join(util.Home(), ".kraken", "kronos")
}

func kronosBriefPath() string  { return filepath.Join(KronosDir(), "kronos-brief.md") }
func kronosPromptPath() string { return filepath.Join(KronosDir(), "kronos-prompt.md") }

// KronosEnvPath is the feature's opt-in switch: the file's EXISTENCE is the
// whole configuration, and an empty file is a complete one. It is named
// `.env` and sits beside the other credential files because that is where a
// knob for this feature would go if one is ever added, and a switch that moved
// house the first time it grew a key would strand every machine that had
// already turned the feature on.
func KronosEnvPath() string {
	return filepath.Join(util.Home(), ".kraken", "kronos.env")
}

// KronosEnabled reports whether the orchestrator is turned on for this
// machine.
//
// It is a STAT rather than a parsed config for one reason: every pass in this
// feature calls it on every tick, and the answer must be cheap enough that a
// machine which has never opted in pays nothing measurable. An unreadable
// path, a permission error, anything that is not "this exists" reads as OFF —
// the same posture core.ErrNoEmbedder gets, because a feature nobody
// configured must never become an error somebody has to silence.
func KronosEnabled() bool {
	_, err := os.Stat(KronosEnvPath())
	return err == nil
}

// kronosAgentKVKey holds the session id kraken minted for kronos, so every
// later spawn resumes the SAME conversation. A kv singleton rather than a
// table, for irisAgentKVKey's reason: one small JSON value written by the
// daemon, which already holds the store's single writer handle.
const kronosAgentKVKey = "kronos_agent"

// KronosAgent is what that key holds. SpawnedAt is stamped on every spawn
// ATTEMPT, not on every success — it is what kronosSpawnBackoff measures
// against, and a failed attempt is exactly the one worth backing off from.
//
// Exported, unlike iris' equivalent, because GET /api/kronos serves both
// fields: the session id is how the board names the conversation and
// spawned_at is how it says how long this one has been up. A second reader
// hand-decoding the same kv key is how a status route quietly reports zero
// forever.
type KronosAgent struct {
	SessionID string `json:"session_id"`
	SpawnedAt int64  `json:"spawned_at"` // unix millis
}

// kronosSpawnBackoff is the minimum gap between two spawn ATTEMPTS —
// irisSpawnBackoff's argument verbatim: the session gate only answers "is
// there a session right now", so a claude that dies immediately would
// otherwise be respawned on every 15-second tick forever. No effect on the
// healthy path; a live kronos never reaches it.
const kronosSpawnBackoff = 5 * time.Minute

// ReadKronosAgent returns the recorded session id, and whether one exists. An
// unset key is the honest "kronos has never been spawned"; an UNPARSEABLE one
// is treated the same way, because the only repair available is to mint a
// fresh id — and a garbage row that blocked every spawn would leave the
// feature dead with nothing able to fix it.
func ReadKronosAgent(st *store.Store) (KronosAgent, bool) {
	if st == nil {
		return KronosAgent{}, false
	}
	raw, err := st.GetKV(kronosAgentKVKey)
	if err != nil || raw == "" {
		return KronosAgent{}, false
	}
	var a KronosAgent
	if err := json.Unmarshal([]byte(raw), &a); err != nil || a.SessionID == "" {
		util.LogEvent("jobs", "error", "kronos-agent", fmt.Sprintf("kronos_agent kv unparseable (%q) — minting a fresh session", raw))
		return KronosAgent{}, false
	}
	return a, true
}

func writeKronosAgent(st *store.Store, a KronosAgent) error {
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return st.SetKV(kronosAgentKVKey, string(data))
}

// kronosSpawnCommand renders the shell command kronos' pane runs, for both
// paths. They differ only in the resume flags: a first spawn NAMES the session
// (--session-id) so kraken knows it before the pane exists, a later spawn
// resumes it (-r).
//
// The brief file is written on BOTH paths, resume included, for the reason
// irisSpawnCommand stages its brief on both: a resumed session does not retain
// an earlier run's system prompt (measured, in `thread.go`), and a resumed
// kronos without its brief is a plain claude holding an orchestrator's history
// — it would keep reading the inbox and lose every rule about what it may do
// with what it finds.
//
// The brief rides on `--system-prompt-file <path>`, which is why it cannot go
// through stagedCommand: that helper stages a body and passes its CONTENTS via
// `"$(cat …)"`, which is right for `--append-system-prompt` and wrong here —
// this flag wants the PATH. The initial prompt still goes through stagedCommand
// (contents, `--` last, so it is claude's first message rather than another
// flag's value), which also keeps the rendered command a few hundred bytes
// regardless of how long either input grows: tmux rejects a command over ~16KB
// with a bare `command too long`.
func kronosSpawnCommand(sessionID string, resume bool) (string, error) {
	// MkdirAll here as well as in EnsureKronos: this function WRITES a file
	// before it returns, and a caller that has not made the directory yet
	// would get a failed spawn out of a missing parent rather than out of
	// anything real.
	if err := os.MkdirAll(KronosDir(), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %v", KronosDir(), err)
	}
	if err := os.WriteFile(kronosBriefPath(), []byte(kronosBrief()), 0o644); err != nil {
		return "", fmt.Errorf("write kronos brief %s: %v", kronosBriefPath(), err)
	}
	argv := append(cellClaudeArgs(core.KronosModel), "--name", KronosAgentName)
	argv = append(argv, "--system-prompt-file", kronosBriefPath())
	if resume {
		argv = append(argv, "-r", sessionID)
	} else {
		argv = append(argv, "--session-id", sessionID)
	}
	return stagedCommand(argv, spawnInput{"--", kronosPrompt(resume), kronosPromptPath()})
}

// EnsureKronos keeps exactly one kronos alive while the feature is on. Called
// every tick from inside the daemon's !mirror block; every arm below is a
// no-op returning nil, so the common tick costs one stat and one map lookup.
//
// The gate is the tmux SESSION, from the tick's own LiveSessions() snapshot —
// never the discovered peer, for EnsureIris' measured reason: claude writes
// its session file seconds into startup, so a peer-keyed gate would read a
// booting kronos as absent and spawn a second `claude -r` on the same session
// id, which is two writers on one transcript. The cost is that a wedged claude
// in a live session is never replaced automatically — carl kills the session
// to force a respawn.
//
// Order inside the spawn matters twice, both times copied from EnsureIris
// deliberately. The session id is minted and RECORDED before tmux is touched,
// so a spawn that dies halfway leaves a resumable id rather than an orphaned
// conversation; and the attempt is STAMPED before tmux is touched, so a spawn
// that fails still costs the backoff.
func EnsureKronos(st *store.Store, live map[string]bool) error {
	if st == nil {
		return nil // nowhere to record the session id
	}
	if !KronosEnabled() {
		return nil // feature off: never an error, never a log line
	}
	if live[KronosSessionName] {
		return nil
	}
	now := time.Now()
	agent, resume := ReadKronosAgent(st)
	if resume && agent.SpawnedAt > 0 && now.Sub(time.UnixMilli(agent.SpawnedAt)) < kronosSpawnBackoff {
		return nil // an attempt was made recently — silent, by design
	}
	if err := os.MkdirAll(KronosDir(), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %v", KronosDir(), err)
	}
	if !resume {
		sessionID, err := newSessionUUID()
		if err != nil {
			return err
		}
		agent.SessionID = sessionID
	}
	agent.SpawnedAt = now.UnixMilli()
	// Recorded BEFORE the spawn: if anything below fails, the next tick
	// resumes this id rather than minting a second one, and the fresh stamp is
	// what holds the backoff over that failure.
	if err := writeKronosAgent(st, agent); err != nil {
		return fmt.Errorf("record kronos session: %v", err)
	}
	// Installed before the pane exists, because Claude Code reads settings.json
	// at startup: written afterwards, the hooks and the statusline would not
	// take effect until the NEXT respawn, which on a healthy kronos is never.
	// A failure here is a warning rather than a refusal — kronos without its
	// statusline still orchestrates, and a spawn refused over a settings write
	// would take the whole feature down for a permissions problem in one file.
	krakenBin, err := os.Executable()
	if err != nil {
		krakenBin = "kraken"
	}
	if err := installKronosSettings(KronosDir(), krakenBin); err != nil {
		util.LogEvent("jobs", "error", "kronos-settings", fmt.Sprintf("kronos settings install failed (spawning anyway): %v", err))
	}
	cmd, err := kronosSpawnCommand(agent.SessionID, resume)
	if err != nil {
		return err
	}
	// -x/-y like every other detached session kraken creates: a bare
	// `new-session -d` starts an 80x24 pane, and claude renders into it.
	spawn := exec.Command("tmux", "new-session", "-d",
		"-x", strconv.Itoa(core.DetachedSessionCols), "-y", strconv.Itoa(core.DetachedSessionRows),
		"-s", KronosSessionName, "-c", KronosDir(),
		"-n", KronosAgentName, cmd)
	// the pane's PATH is this process' PATH: see cellSpawnEnv
	spawn.Env = cellSpawnEnv()
	if out, err := spawn.CombinedOutput(); err != nil {
		if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
			return fmt.Errorf("tmux new-session %s: %v: %s", KronosSessionName, err, trimmed)
		}
		return fmt.Errorf("tmux new-session %s: %v", KronosSessionName, err)
	}
	// KronosDir is a path claude has never been launched in before its first
	// spawn, which raises the folder-trust dialog; unanswered it blocks startup
	// entirely — no session file, no name, no work.
	acceptTrustPrompts(KronosSessionName)
	util.LogEvent("jobs", "info", "kronos-spawn", fmt.Sprintf("kronos up in session %s (session %s, resume %v)", KronosSessionName, agent.SessionID, resume))
	return nil
}

// findKronos returns the live kronos peer in a discovery snapshot, or nil.
//
// Preferring the name and falling back positionally is findIris' rationale
// unchanged: Peer.Name comes from claude's session file, so a just-started
// kronos has none, and a name-only lookup reads it as gone — which here would
// mean the delivery pass silently dropping every bump for the first seconds of
// every kronos' life. The fallback is restricted to a NAMELESS depth-0 claude:
// anything else in that session under a different name is not kronos, and
// typing a bump at it would type at a stranger.
func findKronos(all []*core.Peer) *core.Peer {
	var starting *core.Peer
	for _, p := range all {
		if p.TmuxSession != KronosSessionName || p.Depth != 0 || p.Agent != "claude" {
			continue
		}
		if p.Name == KronosAgentName {
			return p
		}
		if p.Name == "" && (starting == nil || paneIndex(p.Pane) < paneIndex(starting.Pane)) {
			starting = p
		}
	}
	return starting
}

// FindKronos is findKronos for callers outside this package — internal/api
// resolves the pane GET /api/kronos reports (and the board's conversation view
// reads) from the same discovery snapshot the daemon uses. One implementation
// with an exported face rather than two lookups, so the pane the board renders
// is by construction the pane the delivery pass bumps.
func FindKronos(all []*core.Peer) *core.Peer { return findKronos(all) }

// kronosPrompt is the first message kronos' pane receives. The two paths say
// different things because the two situations differ: a fresh kronos has no
// history at all, while a resumed one may have been dead through any number of
// bumps and has to re-derive from the rows rather than from what it remembers.
func kronosPrompt(resume bool) string {
	if resume {
		return "You were restarted (your pane died, the machine rebooted, or your context was compacted) and this is the same conversation resumed. Do not assume anything about what has happened since your last message: run `kraken orch inbox` and `kraken board get` now and work from what they actually say."
	}
	return "You are starting for the first time. Run `kraken orch inbox` to see whether anything is waiting, then `kraken board get` to learn the current board. If the inbox is empty, say so and go idle — kraken will bump this pane when something happens."
}

// kronosBrief is kronos' system prompt: who it is, the loop it runs, the
// commands it has, and the five rules the whole feature's safety rests on.
//
// It is prose in Go rather than a skill file for irisBrief's reason, sharpened:
// a skill is read once, a system prompt is re-sent every turn, and kronos'
// context auto-compacts — so anything kronos must still obey after a day of
// orchestrating has to be here. It restates no skill and points at none;
// kronos runs no pipeline phase and has no worktree to read one from.
//
// It also carries a TOOL-USAGE section, which iris' brief does not need. This
// prompt is installed with --system-prompt-file, which REPLACES Claude Code's
// default system prompt rather than appending to it, so nothing else in
// context explains what Read and Bash are for or that kronos' whole control
// surface is CLI verbs run through Bash. Left out, the agent has the tools and
// no account of them.
func kronosBrief() string {
	var b strings.Builder
	b.WriteString("You are kronos — carl's orchestrator. You do not write code, you do not open PRs, and you have no worktree and no repo. What you have is the board: the jobs kraken is running, the cells behind them, and this pane — carl reads and writes it through the board's kronos panel, and it is your one channel to him. You watch what happens, you tell carl about it, and you carry out the instructions he sends back.\n")
	fmt.Fprintf(&b, "Kraken spawned you on `%s` and keeps you alive: one tmux session, one conversation, resumed rather than restarted. Nobody is watching this pane, so never ask an interactive question here — decide, act, and say what you decided in your final message, its first sentence the whole TLDR.\n", core.KronosModel)

	b.WriteString("\n## Your tools\n\n")
	b.WriteString("This system prompt replaces the default one, so here is what you have and what it is for.\n\n")
	b.WriteString("**Bash** runs shell commands, and it is your control surface: every kraken verb listed below is a command you run with Bash. Run one, read its output, decide. They are ordinary programs — they print JSON or a confirmation line and exit; a non-zero exit means the action did NOT happen, and the message on stderr says why. Nothing you run through Bash touches a repository, so there is no build to break.\n")
	b.WriteString("**Read** reads a file off disk. You will rarely need it: your inputs arrive as command output, not as files. Use it when carl points you at a path, and prefer it over `cat`.\n")
	b.WriteString("Do not reach for anything else. You have no editing to do, nothing to search a codebase for, and no reason to fetch a URL. If a task seems to need one of those, it is a task for a cell, not for you.\n")

	b.WriteString("\n## The loop\n\n")
	b.WriteString("Kraken watches the board and writes an inbox row every time something happens that carl would want to know about: a job became blocked, a cell's session vanished, a cell went unhealthy, a PR merged, a job kraken created failed to start. When new rows land it types a one-line bump into this pane.\n")
	b.WriteString("Every time you are bumped — and every time you resume — run `kraken orch inbox`. It prints the unacked rows as JSON: id, kind, slug, summary and when it was seen. For each row: understand what happened, tell carl, ack it.\n")
	b.WriteString("Everything that arrives in this pane except kraken's own `orch:` bump line is carl talking to you, from the board's kronos panel or the pane itself. Those are instructions, and the rest of this brief is about how you carry them out.\n")

	b.WriteString("\n## Your commands\n\n")
	b.WriteString("Reading:\n\n")
	b.WriteString("- `kraken orch inbox` — the unacked rows, JSON. Your work queue.\n")
	b.WriteString("- `kraken board get [slug]` — the whole board as JSON, or one job's object. Every state, every question, every load estimate carl might ask about is in here.\n")
	b.WriteString("- `kraken graph search <query>` — kraken's durable memory of its own history. Search it before you answer a question about anything that happened before your current context.\n")
	b.WriteString("- `kraken graph get <handle>` — the full fact or source episode behind a `[F… E…]` handle a search printed.\n")
	b.WriteString("- `kraken goals get [slug]` — carl's weekly and daily goals, and which goals each job serves (a job may serve several, or none). This is what makes \"what should I look at first\" answerable.\n\n")
	b.WriteString("Acting:\n\n")
	b.WriteString("- `kraken orch ack --ids 42,43 --disposition \"<what you did>\"` — closes inbox rows. Several ids at once only when they genuinely share one disposition.\n")
	b.WriteString("- `kraken cell steer <slug>` — sends text (on stdin) into a job's cell. This is how an instruction from carl becomes work.\n")
	b.WriteString("- `kraken cell submit <slug>` — starts a draft job's cell.\n")
	b.WriteString("- `kraken cell answer <slug> --digit N` — answers a cell's pending multiple-choice question with option N.\n")
	b.WriteString("- `kraken cell answer <slug> --question Qn` — answers an open question of a job's artifact set, text on stdin.\n")
	b.WriteString("- `kraken graph add …` — records a durable fact. Use it for something carl told you that later-you would otherwise have to ask about twice.\n\n")
	b.WriteString("The three graph verbs together are `kraken graph search/add/get`, and they are the only memory you have that survives a compaction.\n")

	b.WriteString("\n## Five rules that are not negotiable\n\n")
	b.WriteString("**You steer, submit, or answer a cell only on carl's explicit instruction — never on your own initiative.** A blocked cell is a thing to REPORT, not a thing to unblock. You are looking at one job's question with none of the context carl has, and a plausible-looking answer typed into a cell is a decision he never made and cannot see being made. Tell him what the cell is asking, tell him what you would need to answer it, and wait.\n")
	b.WriteString("**Ack every inbox row you read, exactly once.** An unacked row keeps coming back; a row you ack twice is an error, and the server will tell you so. `kraken orch inbox` only ever returns UNACKED rows, so a row that is gone from the list is a row you have already handled — never re-ack it and never re-act on it. Put the real disposition in the ack: it is the only durable record of why the row left the inbox.\n")
	b.WriteString("**Receipt every action back to the channel the instruction came from.** carl told you to do something in this pane; he finds out whether it happened in this pane — a short reply naming what you did and to which job, including when it failed, and most of all when it failed. An action nobody was told about is indistinguishable from one that never ran.\n")
	b.WriteString("**Your memory is the rows and the graph, not this conversation.** Your context auto-compacts as it fills and this pane can be restarted at any time. Neither loses anything, because you keep NO durable state in your head: what still needs handling is exactly `kraken orch inbox`, and the current truth of the board is exactly `kraken board get`. After any compaction or restart, re-derive from those two commands rather than from what you think you remember — and never act on a row you no longer see.\n")
	b.WriteString("**Never restate a secret's value.** Messages and job specs sometimes carry a password, an API key, a test-account login. Your words are transcript, transcripts are swept into kraken.db and future contexts indefinitely, and the automatic redaction only knows vendor token shapes — a human-chosen password sails straight through. Describe it categorically (\"a test-account credential for X\") in your messages, your acks and your facts. Your summary is exactly as useful without the value in it.\n")

	b.WriteString("\n## When in doubt, escalate\n\n")
	b.WriteString("Your default disposition for anything you are not certain about is to tell carl and stop. Not to guess, not to pick the likely option, not to do the smaller safer half of it — to say what you see, say what you would do, and wait for him to answer. Escalating something he considers obvious costs him one glance at a message; acting on something he would have decided differently costs a cell doing the wrong work for an hour, and he finds out afterwards.\n")
	b.WriteString("That default is the whole reason you are trusted with commands that change things. Keep it and the surface stays safe; spend it on being helpful and the next version of you gets fewer verbs.\n")
	return b.String()
}

// --- settings ---

// kronosRearmHookCommand and kronosCtxHookCommand build the two commands
// unique to kronos' settings.
//
// Both carry hookCommandFlags() for the family's standing reason: each runs as
// a brand new OS process spawned by Claude Code, inheriting none of kraken
// serve's flags or environment, so a non-default --serve-addr or a
// KRAKEN_DB_PATH has to be baked into the command string itself. On a
// default-configured machine the flag run is empty and both commands are
// exactly `<bin> rearm-hook` / `<bin> ctx-hook`.
func kronosRearmHookCommand(krakenBin string) string {
	return krakenBin + " rearm-hook" + hookCommandFlags()
}

func kronosCtxHookCommand(krakenBin string) string {
	return krakenBin + " ctx-hook" + hookCommandFlags()
}

// kronosLimitHookCommand is the cell limit-hook command with the slug replaced
// by kronos' agent id. kronos has no job, so there is no slug to bake in — and
// the id is what the route keys the record on (standingHitKey), which is the
// whole reason the flag exists rather than a slug that names nothing.
//
// Built with the same hookCommandFlags run its siblings carry, for the
// family's standing reason: this runs as a brand new OS process spawned by
// Claude Code, inheriting none of kraken serve's flags or environment, so a
// non-default --serve-addr has to be baked into the string itself. On a
// default machine the run is empty and the command is exactly
// `<bin> limit-hook --agent kronos`.
// The id is spelled out rather than taken from KronosSessionName or
// KronosAgentName, which happen to hold the same word: what belongs here is
// the STANDING AGENT id the record is keyed on (standingAgentIDs). The read
// side builds its descriptor ids from those same constants, so the spelling
// alone would not survive a rename — what actually holds the two ends
// together is the pin in standingrescue_contract_test.go asserting
// standingAgents()' ids equal standingAgentIDs()' verbatim.
func kronosLimitHookCommand(krakenBin string) string {
	return krakenBin + " limit-hook" + hookCommandFlags() + " --agent kronos"
}

// kronosRecallHookCommand is the cell recall command with the slug dropped.
// recallHookCommand always renders a trailing slug argument, and kronos has no
// job — RunRecallHook takes the slug variadically and an absent one is legal,
// so the honest rendering is the command without it rather than the command
// with an empty string where a slug goes. Built from recallHookCommand so the
// flag run cannot drift between a cell's recall hook and kronos'.
func kronosRecallHookCommand(krakenBin string) string {
	return strings.TrimSpace(recallHookCommand("", krakenBin))
}

// kronosRearmHookMarker identifies kraken's own PostCompact entry when the
// exact command string cannot be relied on — which is every install after the
// first with a different --serve-addr, --db-path or binary path. Same role
// recallHookMarker plays for the recall entries.
const kronosRearmHookMarker = "rearm-hook"

// kronosBashLintMarker does the same for kronos' PreToolUse entry. That entry
// is REPLACE-merged too, and the round-2 comment claiming otherwise was half
// wrong: the reasoning offered was that bashLintCommand carries no flags, so
// ensureHookGroup's exact-string test is a real identity — but the command also
// embeds krakenBin, os.Executable()'s answer, and THAT is the component that
// actually varies (hookCommandFlags() renders empty on a default machine, so
// the binary path is the only varying part of the rearm and recall commands
// too). The documented parallel-server drill runs a scratch build from /tmp,
// which under append-if-absent leaves a second PreToolUse entry naming a binary
// that is then deleted — a failing hook on every Bash call kronos makes, on the
// one agent that runs unattended. Cells' own bash-lint entry has the same shape
// and is deliberately out of scope here.
const kronosBashLintMarker = "bash-lint"

// kronosLimitHookMarker does the same for kronos' StopFailure entry, which is
// REPLACE-merged like all three of its siblings and for the identical reason:
// the command embeds the resolved binary path (and hookCommandFlags' address),
// so append-if-absent would leave a stale entry beside a fresh one — here a
// hook pointing at a deleted scratch binary, which is the one entry whose
// silent failure means an exhausted kronos is never rescued at all.
const kronosLimitHookMarker = "limit-hook"

// withoutKronosRearmHooks is withoutRecallHooks for the PostCompact group, and
// it exists because the rearm command has exactly the property that made the
// recall entries replace-merged rather than append-if-absent: it embeds
// hookCommandFlags() AND the resolved binary path, both of which change between
// the launchd daemon and a scratch build run on another port. Append-if-absent
// then leaves the old entry in place and adds a second one beside it — the
// stale one POSTs at a dead port, gets a connection error, and `rearm-hook`
// drops it and exits 0 by contract, so the file grows one inert entry per
// distinct address forever with nothing anywhere reporting it.
//
// A whole GROUP goes rather than the matching hook inside it, and a group whose
// shape is not the expected object is left alone — withoutRecallHooks' reasons
// verbatim: kraken writes its entry as a group of exactly one hook, so a group
// carrying kraken's command plus somebody else's is a hand-edit of kraken's own
// entry, and there is no reading of that under which keeping half is right.
func withoutKronosRearmHooks(groups []any) []any {
	return withoutKronosHookVerb(groups, kronosRearmHookMarker)
}

// withoutKronosBashLint is the same removal for the PreToolUse entry — see
// kronosBashLintMarker for why that entry needs one.
func withoutKronosBashLint(groups []any) []any {
	return withoutKronosHookVerb(groups, kronosBashLintMarker)
}

// withoutKronosLimitHook is the same removal for the StopFailure entry — see
// kronosLimitHookMarker.
func withoutKronosLimitHook(groups []any) []any {
	return withoutKronosHookVerb(groups, kronosLimitHookMarker)
}

// withoutKronosHookVerb is the shared body. One function rather than two copies
// because the two differ only in the verb, and a rule about which of kraken's
// own entries get replaced is one rule.
func withoutKronosHookVerb(groups []any, verb string) []any {
	var out []any
	for _, g := range groups {
		if group, ok := g.(map[string]any); ok && groupRunsVerb(group, verb) {
			continue
		}
		out = append(out, g)
	}
	return out
}

func kronosRearmHookEntry(krakenBin string) map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": kronosRearmHookCommand(krakenBin),
			},
		},
	}
}

// kronosLimitHookEntry is the StopFailure group, shaped exactly like a cell's
// limitHookEntry: the matcher is "rate_limit" because that is the value Claude
// Code puts in the payload's `error` field for every branch of its HTTP-429
// handler, and a StopFailure matcher matches on that value rather than on a
// tool name.
func kronosLimitHookEntry(krakenBin string) map[string]any {
	return map[string]any{
		"matcher": "rate_limit",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": kronosLimitHookCommand(krakenBin),
			},
		},
	}
}

func kronosRecallHookEntry(krakenBin string) map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": kronosRecallHookCommand(krakenBin),
			},
		},
	}
}

// installKronosSettings writes kronos' project-scoped Claude Code config,
// <dir>/.claude/settings.json, merging kraken's entries into whatever is
// already there exactly as installHookSettings does for a cell — never
// clobbering the file, never touching a key kraken did not write.
//
// The GROUP SET is deliberately different from a cell's, and each difference
// is a fact about kronos rather than a preference:
//
//   - recall, under both recallHookEvents, REPLACE-merged like a cell's. Same
//     reason: the command varies with serve's address, so append-if-absent
//     leaves a stale entry POSTing at a dead port and adds a second one beside
//     it on every install.
//   - bash-lint, under PreToolUse, REPLACE-merged like its two siblings (the
//     command embeds the resolved binary path, the one varying component).
//     kronos runs every one of its verbs through Bash, so the four zsh shapes
//     that abort or lie silently are exactly as dangerous here as in a cell.
//   - rearm-hook, under PostCompact, REPLACE-merged for the recall entries'
//     reason exactly (its command carries the serve address and the binary
//     path) — see withoutKronosRearmHooks. A compaction throws away the conversation
//     graph recall has already delivered into, so the once-per-session facts
//     kg_recall is suppressing are facts kronos no longer has. Clearing that
//     session's recall rows is what lets them arrive again.
//   - limit-hook, under StopFailure, REPLACE-merged like the rest and with a
//     --agent id where a cell's carries a slug. kronos runs on the same one
//     Claude login every cell does, so an exhausted account stops it exactly
//     as dead — and nobody is watching this pane either. The hook is the FAST
//     trigger for the standing rescue; the transcript arm covers a kronos
//     spawned before this entry existed, hours later.
//   - a statusLine running ctx-hook. This is the only path by which kronos'
//     own context fill reaches kraken: Claude Code runs the statusline command
//     with the live session's numbers on stdin, which is a measurement nothing
//     outside the pane can otherwise take.
//
// And two a cell gets that kronos does NOT: pr-hook (kronos opens no PRs) and
// the agent-teams keys (it delegates nothing — a kronos that could spawn
// teammates would be an orchestrator writing code).
//
// autoCompactEnabled is deliberately not set either way. Auto-compact is
// kronos' last-resort safety net and stays at the harness default; the
// threshold warnings ctxPass sends are what give carl the chance to compact
// deliberately first.
func installKronosSettings(dir, krakenBin string) error {
	settingsDir := filepath.Join(dir, ".claude")
	path := filepath.Join(settingsDir, "settings.json")
	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %v", path, err)
	}
	merged, err := mergeKronosSettings(existing, krakenBin)
	if err != nil {
		// Same refusal installHookSettings makes: a file kraken cannot parse is
		// a file kraken must not overwrite.
		return fmt.Errorf("%s is not valid JSON — leaving it alone: %v", path, err)
	}
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %v", settingsDir, err)
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		return fmt.Errorf("write %s: %v", path, err)
	}
	return nil
}

// mergeKronosSettings is the pure half of installKronosSettings — the same
// split mergeHookSettings/installHookSettings draw, so the merge rules can be
// exercised without a filesystem.
func mergeKronosSettings(existing []byte, krakenBin string) ([]byte, error) {
	parsed := map[string]any{}
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := json.Unmarshal(existing, &parsed); err != nil {
			return nil, err
		}
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}
	// EVERY one of kronos' hook groups is REPLACE-merged, and there is no
	// asymmetry left in this function: each of their commands embeds the
	// resolved binary path, which is the component that actually varies
	// between the launchd daemon and a scratch build (see kronosBashLintMarker).
	//
	// Reading each existing value with a failed type assertion rather than a
	// presence check is deliberate: a group key that is not an array of groups
	// (a hand-edited object, say) is a shape Claude Code cannot run anyway, and
	// ensureHookGroup's stance there — return, install nothing, report success —
	// silently leaves the hook uninstalled forever. Here it is replaced
	// outright, so the hook is always really there.
	preToolUse, _ := hooks["PreToolUse"].([]any)
	hooks["PreToolUse"] = append(withoutKronosBashLint(preToolUse), bashLintHookEntry(krakenBin))
	postCompact, _ := hooks["PostCompact"].([]any)
	hooks["PostCompact"] = append(withoutKronosRearmHooks(postCompact), kronosRearmHookEntry(krakenBin))
	stopFailure, _ := hooks["StopFailure"].([]any)
	hooks["StopFailure"] = append(withoutKronosLimitHook(stopFailure), kronosLimitHookEntry(krakenBin))
	for _, event := range recallHookEvents {
		groups, _ := hooks[event].([]any)
		// A fresh entry per event rather than one shared map: one shared entry
		// would be the SAME mutable map in two places in the tree, where a
		// later mutation through one key silently edits the other.
		hooks[event] = append(withoutRecallHooks(groups), kronosRecallHookEntry(krakenBin))
	}
	parsed["hooks"] = hooks
	mergeKronosStatusLine(parsed, krakenBin)
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// mergeKronosStatusLine installs the ctx-hook statusline, and it is the one
// key here with a three-way rule rather than a two-way one.
//
// Absent, kraken writes it. Present and running kraken's OWN ctx-hook, kraken
// REWRITES it — the command carries the serve address, so leaving a stale one
// in place would leave the context meter reading from a dead port forever,
// with nothing anywhere reporting a problem (a statusline that fails prints
// nothing by design). Present and running something ELSE, kraken leaves it
// entirely alone: a statusline somebody chose is exactly the "unrelated user
// entry" this file promises not to clobber, and there is only one statusLine
// slot to argue over.
//
// Both leave-alone arms LOG, one line per install call, and that is the
// round-3 amendment's point rather than a courtesy. This key is the ONLY path
// by which kronos' context fill reaches kraken: without ctx-hook installed,
// nothing ever POSTs /api/orch/context, ReadKronosContext returns "never
// reported" forever, ctxPass returns at its first arm on every tick, and the
// whole 50/75/90 warning ladder is off — while the board renders exactly what
// it renders for a kronos that simply has not reported yet. The only symptom of
// the feature being off is the absence of a warning that was never coming, so
// the decision to leave the key alone has to be visible somewhere. Deciding not
// to act is still a decision worth a line.
func mergeKronosStatusLine(parsed map[string]any, krakenBin string) {
	if existing, ok := parsed["statusLine"].(map[string]any); ok {
		cmd, _ := existing["command"].(string)
		if !strings.Contains(cmd, "ctx-hook") {
			// somebody else's statusline; not kraken's to replace
			dlogEvent("info", "kronos-settings", "kronos: statusLine already runs something else — leaving it alone, so the context meter stays off")
			return
		}
	} else if _, present := parsed["statusLine"]; present {
		// present but not an object — leave the file's own shape alone
		dlogEvent("info", "kronos-settings", "kronos: statusLine is present but not an object — leaving it alone, so the context meter stays off")
		return
	}
	parsed["statusLine"] = map[string]any{
		"type":    "command",
		"command": kronosCtxHookCommand(krakenBin),
	}
}
