package jobs

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// standingrescue.go — the health pass and the usage-limit rescue for the two
// STANDING agents: kronos and iris. They are the claudes kraken keeps alive
// that belong to no job, and until now every durability pass in this binary
// was written against a *Job — so a kronos that died on an exhausted account
// stayed dead until carl noticed the pane, which is precisely the thing an
// orchestrator exists to stop happening to everything else.
//
// Everything here is the CELL machinery with the job removed, and that is a
// deliberate reuse rather than a resemblance: limitRescueDecide,
// limitRescueApply, parseLimitReset and limitHitTriggerDue are called
// unchanged, so a standing agent's rescue takes the same gates in the same
// order a cell's does — a landed swap outranks a manual pin, a park with a
// parseable reset stays unresolved until the window reopens, attempts are
// increment-then-test. Two records that decided the same question by two
// chains would be two chains to fix.
//
// Three things differ, and each is a fact about a standing agent rather than
// a preference.
//
// The RESUME is a session kill plus Ensure*, not a pane respawn. A cell's
// zeus is resumed by `claude -r <session>` into its own pane because the pane
// and its worktree are the thing being preserved; a standing agent's session
// is kraken's own one-pane tmux session, and EnsureKronos/EnsureIris already
// know how to build the exact command that brings that conversation back —
// same session id, same brief, same model, and their own resume prompt ("You
// were restarted…"), which says the same three things limitRescueResumePrompt
// says to zeus. Writing a second resume prompt here would be a second place
// for that text to drift.
//
// CLASSIFICATION runs only on the RECORDED session id — the one kraken minted
// and stored in kv — never on a discovered or guessed one. A standing agent
// has exactly one conversation for its whole life, kraken knows its id before
// the pane exists, and reading someone else's transcript is how a rescue
// swaps the machine's login on another process' evidence.
//
// And there is NO orch inbox row anywhere in this file. A cell's health pass
// writes one so kronos hears about the cell; a standing stall is kronos being
// told about itself, which either arrives in a pane that cannot read it or
// arrives at iris, who has no business with it.

// standingAgent is one row of the standing arm: the identity the passes below
// key on, plus the four behaviours that differ between kronos and iris. It is
// a descriptor rather than an interface because every field but Enabled is
// already a package function — the table is the whole abstraction, and a
// second implementation of it would be a third standing agent, not a fake.
type standingAgent struct {
	ID        string // the agent id: the kv key's suffix, and what carl reads
	Session   string // the tmux session it lives in
	AgentName string // the claude --name it runs under
	// Enabled is the feature gate: kronos' env file, iris' Slack credential.
	// Consulted on every tick by both passes, so it must stay cheap.
	Enabled func() bool
	// RecordedSession returns the session id kraken minted for this agent, and
	// whether one has ever been recorded. The ONLY session id anything here
	// classifies or kills on.
	RecordedSession func(*store.Store) (string, bool)
	// Find locates the agent's live peer in a discovery snapshot. Its
	// positional fallback (a nameless depth-0 claude, still starting) is fine
	// for "is it there?" and is deliberately NOT enough to authorize a kill —
	// see standingKillTarget.
	Find func([]*core.Peer) *core.Peer
	// Respawn brings the agent back after its session has been killed, with
	// the session removed from the live map it is handed. It must bypass the
	// spawn backoff and must report a spawn it did not actually attempt.
	Respawn func(*store.Store, map[string]bool) error
	// Model is the exact model id the agent is spawned on — what the Fable
	// fallback reads to decide whether a hit can be a Fable-tier one.
	Model string
}

// standingAgents is the table the daemon walks. Its ids are exactly
// standingAgentIDs(), in the same order — the API route validates an inbound
// agent id against that list and this file acts on it, so the two drifting
// apart would mean a hit recorded under a key no pass ever reads.
func standingAgents() []standingAgent {
	return []standingAgent{
		{
			ID:        KronosSessionName,
			Session:   KronosSessionName,
			AgentName: KronosAgentName,
			Enabled:   KronosEnabled,
			RecordedSession: func(st *store.Store) (string, bool) {
				a, ok := ReadKronosAgent(st)
				return a.SessionID, ok
			},
			Find:    findKronos,
			Respawn: respawnKronos,
			Model:   core.KronosModel,
		},
		{
			ID:        IrisSessionName,
			Session:   IrisSessionName,
			AgentName: IrisAgentName,
			Enabled:   irisEnabled,
			RecordedSession: func(st *store.Store) (string, bool) {
				// readIrisAgent dereferences the store without checking it,
				// since EnsureIris only ever calls it past its own nil gate.
				// This caller has no such gate above it, so the check is here.
				if st == nil {
					return "", false
				}
				a, ok := readIrisAgent(st)
				return a.SessionID, ok
			},
			Find:    findIris,
			Respawn: respawnIris,
			Model:   core.IrisModel,
		},
	}
}

// irisEnabled is iris' feature gate in the shape standingAgent wants it.
// EnsureIris asks the same question inline and distinguishes ErrNoSlack from a
// real load failure so it can return the latter; here a credential that cannot
// be loaded for ANY reason means there is no iris to keep healthy, and the
// half-configured case (`ErrSlackHalfConfigured`) already logs where it is
// loaded. Treating a load error as "on" would send the passes below looking
// for a pane that was never spawned.
func irisEnabled() bool {
	_, err := core.LoadSlackConfig()
	return err == nil
}

// respawnKronos and respawnIris are the two Respawn implementations, and both
// exist to do one thing the Ensure* functions will not do on their own:
// spawn NOW.
//
// Ensure* declines silently for five minutes after any spawn ATTEMPT
// (kronosSpawnBackoff / irisSpawnBackoff), which is correct for the tick loop
// — it is what stops a claude that dies on startup being respawned four times
// a minute forever — and wrong here. A rescue respawn is not a crash loop: the
// pane is being killed deliberately, by a pass that has already recorded a
// usage-limit hit and swapped the account behind it, and letting the backoff
// swallow it would leave a killed agent dead for five minutes with the hit
// resolved as if the resume had landed. So the recorded stamp is zeroed first,
// which is the only knob Ensure* reads.
//
// The check AFTER the call is the other half, and it is what makes the seam's
// contract honest. Ensure* returns nil on several arms that spawn nothing (the
// feature turned off mid-tick, a nil store, a session tmux still reports as
// live), and the caller reads a nil as "the agent is back" and resolves the
// hit. Ensure* stamps SpawnedAt BEFORE it touches tmux — that ordering is
// load-bearing there for the backoff, and it is what lets a caller here tell a
// spawn that was attempted from one that was declined. A stamp that is still
// zero after the call means no attempt was made, and that is an error.
func respawnKronos(st *store.Store, live map[string]bool) error {
	if st == nil {
		return errors.New("kronos: no store wired — nothing to respawn from")
	}
	prev := int64(0)
	if a, ok := ReadKronosAgent(st); ok && a.SpawnedAt != 0 {
		prev = a.SpawnedAt
		a.SpawnedAt = 0
		if err := writeKronosAgent(st, a); err != nil {
			return fmt.Errorf("kronos: clearing the spawn backoff failed: %w", err)
		}
	}
	if err := EnsureKronos(st, live); err != nil {
		restoreKronosStamp(st, prev)
		return err
	}
	if a, ok := ReadKronosAgent(st); !ok || a.SpawnedAt == 0 {
		// No attempt was made, so the zeroed stamp must not stay on disk: left
		// at zero it disarms the crash-loop backoff for the ordinary Ensure
		// pass on the very next tick, which is the one guard that pass has.
		restoreKronosStamp(st, prev)
		return errors.New("kronos: no spawn was attempted — the feature gate or the session gate declined it")
	}
	return nil
}

// restoreKronosStamp puts the pre-rescue SpawnedAt back after a respawn that
// never attempted a spawn. Best-effort: the write that could fail here is the
// same write that just succeeded at zeroing, and a stamp lost this way costs
// one unguarded attempt, not a loop.
func restoreKronosStamp(st *store.Store, prev int64) {
	if prev == 0 {
		return
	}
	if a, ok := ReadKronosAgent(st); ok && a.SpawnedAt == 0 {
		a.SpawnedAt = prev
		if err := writeKronosAgent(st, a); err != nil {
			dlogErr("kronos: restoring the spawn backoff stamp failed: %v", err)
		}
	}
}

func respawnIris(st *store.Store, live map[string]bool) error {
	if st == nil {
		return errors.New("iris: no store wired — nothing to respawn from")
	}
	prev := int64(0)
	if a, ok := readIrisAgent(st); ok && a.SpawnedAt != 0 {
		prev = a.SpawnedAt
		a.SpawnedAt = 0
		if err := writeIrisAgent(st, a); err != nil {
			return fmt.Errorf("iris: clearing the spawn backoff failed: %w", err)
		}
	}
	if err := EnsureIris(st, live); err != nil {
		restoreIrisStamp(st, prev)
		return err
	}
	if a, ok := readIrisAgent(st); !ok || a.SpawnedAt == 0 {
		// Same restore respawnKronos makes, for the same reason.
		restoreIrisStamp(st, prev)
		return errors.New("iris: no spawn was attempted — the feature gate or the session gate declined it")
	}
	return nil
}

// restoreIrisStamp is restoreKronosStamp for iris' record.
func restoreIrisStamp(st *store.Store, prev int64) {
	if prev == 0 {
		return
	}
	if a, ok := readIrisAgent(st); ok && a.SpawnedAt == 0 {
		a.SpawnedAt = prev
		if err := writeIrisAgent(st, a); err != nil {
			dlogErr("iris: restoring the spawn backoff stamp failed: %v", err)
		}
	}
}

// standingKillAndRespawn is the resume half of the rescue. Its seam,
// `standingRespawn`, is declared in rotation.go's single seam block beside
// respawnPeerPane — the block whose comment explains why this package keeps
// ONE place to swap these in tests — while the body stays here with the rest
// of the standing machinery.
//
// It kills the SESSION rather than the pane, which is a wider blast radius
// than the cell rescue's and is still the narrow choice here: a standing
// agent's session is one kraken made, holds one window running one claude, and
// Ensure*'s whole gate is "is there a session with this name". Killing the
// pane alone would leave the session standing and empty, and every later tick
// would read that as "the agent is up".
//
// A kill that FAILS is logged and does not stop the respawn. The likeliest
// cause is a session that is already gone (the process died between the
// snapshot and now), which is the state the respawn wants anyway; and if the
// session really did survive, Ensure* will fail loudly on `tmux new-session`
// refusing a duplicate name rather than quietly reporting success.
//
// One outcome REFUSES instead: a fresh session-wide pane listing taken
// immediately before the kill finding more than one pane defers the round —
// the identity gate vouched for one pane, and the kill takes the session, so
// a second pane (something carl opened by hand) must not die on one pane's
// evidence.
func standingKillAndRespawn(a standingAgent, st *store.Store, live map[string]bool) error {
	// Checked before anything is killed, not after: a row with no Respawn has
	// no way back, and discovering that on the far side of the kill would take
	// the agent down permanently to report a wiring bug.
	if a.Respawn == nil {
		return fmt.Errorf("%s: no respawn wired — not killing a session nothing can bring back", a.ID)
	}
	// Verify before acting, the same re-check discipline core.RespawnPeerPane
	// applies before its own kill: the snapshot that authorized this kill is a
	// tick old, and the kill takes the whole SESSION where the identity gate
	// only ever vouched for one pane. A fresh pane listing closes both gaps —
	// a session that has grown a second pane since the snapshot (something
	// carl opened by hand) is refused for this round rather than killed on
	// one pane's evidence, and a session already gone skips straight to the
	// respawn. The listing erroring is read as "already gone": tmux reports a
	// missing session as an error, and a genuinely live session that failed
	// to list makes the kill below fail loudly rather than report success.
	// -s is load-bearing: without it tmux resolves -t as a target-WINDOW and a
	// bare session name lists only the session's current window's panes —
	// measured on a live 3-window session returning 1 — while kill-session
	// below destroys every window. The flag is what makes the count session-wide.
	if out, err := exec.Command("tmux", "list-panes", "-s", "-t", a.Session, "-F", "#{pane_id}").Output(); err == nil {
		if panes := strings.Fields(strings.TrimSpace(string(out))); len(panes) > 1 {
			return fmt.Errorf("%s: session %s holds %d panes and the identity gate vouched for one — not killing it this round", a.ID, a.Session, len(panes))
		}
	}
	if out, err := exec.Command("tmux", "kill-session", "-t", a.Session).CombinedOutput(); err != nil {
		dlogEvent("info", "limit-rescue", "%s: tmux kill-session %s failed — proceeding to the respawn (the likeliest cause is a session already gone): %v: %s",
			a.ID, a.Session, err, strings.TrimSpace(string(out)))
	}
	// A COPY with the session dropped, never a mutation of the tick's own map:
	// the snapshot is shared by every other pass in this tick, and deleting a
	// session from it here would tell the state derivation of an unrelated job
	// that a tmux session it never asked about had vanished.
	next := make(map[string]bool, len(live))
	for name, alive := range live {
		if name == a.Session {
			continue
		}
		next[name] = alive
	}
	return a.Respawn(st, next)
}

// standingStallReason is stallReason for a standing agent: the same three
// classifications, worded for an agent that is not a cell's lead.
//
// It is a separate function rather than a parameterised stallReason because
// the cell's strings are load-bearing in a way these are not —
// IsUsageLimitReason and RetryableHealth both prefix-match them, and the
// board and viewer render them — while these are read by carl in a
// notification and stored as a hit's Details. What they must NOT do is say
// "zeus": these reasons name the agent that stalled, and a notification about
// kronos that says zeus sends carl looking through job cells for a pane that
// is not in one.
//
// retryable is st.Transient and only on the api-error arm, exactly as
// RetryableHealth decides for a cell: a usage limit is never nudged (the
// account cannot answer, and the rescue below is what fixes it), and neither
// is a credit/auth/prompt-length failure, which needs carl.
func standingStallReason(agentID string, st core.Stall) (reason string, isLimit, retryable bool) {
	switch st.Kind {
	case "usage-limit":
		return agentID + " hit a usage limit: " + st.Text, true, false
	case "api-error":
		if st.Transient {
			return agentID + " hit an API error: " + st.Text, false, true
		}
		return agentID + " needs attention: " + st.Text + " — fix and restart it by hand", false, false
	default:
		// none, or an interrupted turn — carl's own esc is not a stall to
		// report and not one to nudge.
		return "", false, false
	}
}

// standingHealth is CellHealth for a standing agent, and its refusals are the
// interesting part.
//
// A missing peer is NOT reported as unhealthy here, which is where this parts
// company with CellHealth's "zeus process is gone". Ensure* already owns that
// case: a standing agent with no session is respawned on the next tick without
// anyone being told, because a restart nobody has to act on is not news. What
// this pass is for is an agent that IS running and cannot make progress.
//
// A NAMELESS peer is a claude still starting — Peer.Name arrives with the
// session file, seconds in — and classifying one would read the previous
// process' transcript tail as the new process' stall.
//
// And an empty recorded session id ends the pass, rather than falling back to
// the peer's discovered one. That is the file's standing rule: a discovered id
// can be guessed (SessionIDGuessed: cwd plus newest transcript), and the whole
// consequence chain from here is a machine-wide credential swap and a killed
// pane. An agent whose id kraken never recorded is one kraken did not spawn.
func standingHealth(a standingAgent, all []*core.Peer, recorded string) (reason string, isLimit, retryable bool) {
	if a.Find == nil {
		return "", false, false
	}
	p := a.Find(all)
	if p == nil || p.Name == "" || recorded == "" {
		return "", false, false
	}
	transcript := core.TranscriptPathForSession(recorded)
	if transcript == "" {
		return "", false, false
	}
	return standingStallReason(a.ID, core.LastStall(transcript))
}

// standingHitTriggerKey is limitHitTriggerKey for a standing agent — same
// shape, same pairing with limitHitTriggerDue, keyed on the agent id where the
// cell keys on the job's immutable id. The id here is already immutable (there
// is exactly one kronos and one iris on a machine), so the reason the cell's
// key avoids slugs does not arise; what carries over is the EXPIRY, which is
// the half that matters. A stalled standing agent keeps the same banner at the
// tail of its transcript indefinitely, so an un-expiring key would record the
// first exhaustion and then silently decline every later one — including the
// next week's, whose banner text is byte-identical.
func standingHitTriggerKey(agentID, reason string) string {
	return "limithit:" + agentID + "|" + reason
}

// standingKillTarget answers whether the snapshot contains positive evidence
// that the agent's session is running the agent — and returns a REASON when it
// does not, because a refusal here is a deferral rather than a resolution: the
// caller counts the round and tries again next tick, when a name has usually
// landed.
//
// Identity is the whole point, which is why a.Find is not consulted. Find
// prefers the name and falls back to a nameless depth-0 claude, which is
// correct for a nudge and is exactly the pane this must not kill: a claude
// carl started by hand in that session, or a still-booting process whose
// death would cost the conversation its first turns. The two accepted
// signatures are the agent's own --name and the session id kraken itself
// minted and recorded.
//
// The peer is returned rather than merely a bool because the caller logs the
// pane it acted on; the kill itself goes through standingRespawn, which takes
// the session.
func standingKillTarget(a standingAgent, all []*core.Peer, recorded string) (*core.Peer, string) {
	var found *core.Peer
	for _, p := range all {
		if p == nil || p.TmuxSession != a.Session || p.Depth != 0 || p.Agent != "claude" {
			continue
		}
		if p.Name == a.AgentName {
			return p, ""
		}
		if recorded != "" && p.SessionID == recorded {
			return p, ""
		}
		// Remembered only so the refusal can name what was there instead —
		// lowest pane index first — the ordering findIris' fallback applies.
		// Only the ORDERING is shared: findIris remembers a nameless peer
		// where this remembers any non-matching depth-0 claude, so the two
		// can disagree about which peer that is.
		if found == nil || paneIndex(p.Pane) < paneIndex(found.Pane) {
			found = p
		}
	}
	if found == nil {
		return nil, a.ID + ": no depth-0 claude in tmux session " + a.Session + " — nothing to kill this round"
	}
	return nil, fmt.Sprintf("%s: pane %s is not %s by identity (name %q, session %q) — not killing it",
		a.ID, found.Pane, a.ID, found.Name, found.SessionID)
}

// standingLimitRescue is limitRescue for a standing agent — the same pass over
// the same record shape, with rescueLimitedCell replaced by rescueSwap (the
// same body with the job reduced to a label) and resumeCellAgents replaced by
// the kill-and-Ensure seam above.
//
// Everything the cell pass owns, this owns identically: the two halves land on
// different ticks (a rotate stamps SwappedAt and leaves HandledAt zero), a
// lock-busy rotate records nothing while a refused or failed resume COUNTS the
// round without resolving the hit (increment-then-test against the
// path-relative cap — see the no-agents arm below), and the notification key
// carries the hit so a second exhaustion weeks later is announced rather than
// swapped for silently.
//
// It shares the landed-swap ADOPTION the cell pass also gained. A cell and a standing agent
// share one machine-wide login, so a rotate performed for a job's cell fixes
// the account kronos was stalled on too — without the watermark, kronos would
// hold an unresolved hit, walk the pool for a second swap it does not need,
// and burn a healthy account. adoptLandedSwap stamps the swap that already
// happened onto this record, and the decide chain's landed-swap gate then does
// what it always does: resume, once the settle window has passed.
func (d *daemonState) standingLimitRescue(a standingAgent, st *store.Store, all []*core.Peer, live map[string]bool, now time.Time) {
	key := standingHitKey(a.ID)
	hit := readLimitHitByKey(key)
	if hit.At == 0 || hit.HandledAt != 0 {
		// Gates 1 and 2 say the same thing, and short-circuiting them here is
		// what keeps the pass free on the overwhelmingly common tick: nothing
		// ever deletes this row, so an agent that hit a limit once would
		// otherwise pay a usage-snapshot read and a full rotation ranking every
		// 15 seconds for the rest of the daemon's life.
		return
	}
	if hit.Error != "rate_limit" {
		// The decide chain's gate 3 would skip this hit anyway, but it sits
		// BELOW the watermark read and the selectRescueTarget probe. A cell's
		// non-rate_limit hit stops costing anything when the job is archived; a
		// standing agent is never archived, so one recorded auth failure would
		// otherwise buy a usage-snapshot read and a full rotation ranking every
		// tick for the rest of the daemon's life.
		return
	}
	if hit.ResetAt == 0 {
		hit.ResetAt = parseLimitReset(hit.Details, time.Unix(hit.At, 0))
	}
	// Adoption, before the decision and persisted the same tick: the record
	// the chain decides on and the record the store holds must agree, or the
	// next tick re-adopts and re-decides from a state this one already moved
	// past. A failed write is logged and the tick proceeds on the adopted
	// record in memory — the alternative is deciding on a record we know to be
	// stale, which is the second swap this whole watermark exists to prevent.
	//
	// One enumerated cost of adopting ahead of the liveness gate: an agent
	// whose SESSION died on the limit adopts here while dead, Ensure* respawns
	// it healthy on the swapped account later this same tick, and the next
	// tick's landed-swap gate then kills and resumes a process that was
	// already fine — one redundant restart, conversation intact. Accepted:
	// ordering adoption after liveness would only defer the same kill to the
	// tick the agent came back on.
	if swapAt, swapTo := readMainSwap(); swapAt != 0 && !fableFallbackPending(key, now) {
		if adopted, changed := adoptLandedSwap(hit, swapAt, swapTo); changed {
			hit = adopted
			// A rotate silently becoming a resume is a machine-wide credential
			// decision worth one line, same as the cell pass logs for it.
			dlogEvent("info", "limit-rescue-adopted", "%s: adopted the machine-wide swap to %s — no second rotate", a.ID, swapTo)
			if err := saveLimitRescueStateByKey(key, a.ID, hit); err != nil {
				dlogErr("%s: persist adopted swap watermark failed: %v", a.ID, err)
			}
		}
	}
	// The parked-until-reset wait, skipped before selectRescueTarget is paid
	// for — the cell pass' reasoning verbatim. In auto mode the chain would
	// return skip anyway; under a MANUAL pin it would return park-manual, and
	// re-parking would resolve the record and kill the pending reset resume.
	if hit.SwappedAt == 0 && hit.Attempts >= limitRescueMaxAttempts && hit.ResetAt != 0 &&
		now.Sub(time.Unix(hit.ResetAt, 0)) < limitResetSettle {
		return
	}
	// The Fable fallback, ahead of any main swap: both standing agents run on
	// a Fable tier, and switching one to Opus on main moves nothing else on
	// the machine where a swap moves every unbound cell with it.
	if live[a.Session] && a.Find != nil && d.tryFableFallback(key, a.ID, a.Model, mainIdentityEmail, hit,
		func() *core.Peer { return a.Find(all) }, now) {
		return
	}
	target, mainEmail, _ := selectRescueTarget(now)
	action, reason := limitRescueDecide(hit, RotationMode(), live[a.Session], target != "", now)
	if action == rescueSkip {
		return
	}

	// Keyed on the hit, not on the agent and the action: nothing clears
	// d.Notified and the daemon runs for weeks, so an agent+action key would
	// announce kronos' first rescue and then silently swap the machine's login
	// for every later one.
	notifyOnce := func(title, msg string) {
		// "limit:standing:" rather than the cell pass' "limit:" namespace: a
		// job legitimately slugged "kronos" would otherwise share dedup keys
		// with the agent for an identical action and hit second.
		nkey := "limit:standing:" + a.ID + "|" + string(action) + "|" + strconv.FormatInt(hit.At, 10)
		if d.Notified[nkey] != 0 {
			return
		}
		notify(title, msg)
		d.Notified[nkey] = now.Unix()
	}
	if mainEmail == "" {
		mainEmail = "main"
	}

	switch action {
	case rescueRotate:
		to, outcome, err := rescueSwap(a.ID, now)
		if outcome == "lock-busy" {
			// The same lock every cell spawn takes, so losing it is ordinary —
			// spending one of three attempts and starting a ten-minute backoff
			// on it would park a hit that never once reached the pool.
			dlogEvent("info", "limit-rescue-skipped", "%s: rotation lock busy — retrying next tick", a.ID)
			return
		}
		hit = limitRescueApply(hit, action, outcome, to, now)
		saveErr := saveLimitRescueStateByKey(key, a.ID, hit)
		switch {
		case outcome == "swapped":
			notifyOnce("kraken ⇄ "+a.ID, fmt.Sprintf("usage limit — switched %s → %s", mainEmail, to))
			dlogEvent("info", "limit-rescue-done", "%s: %s: swapped %s → %s", a.ID, reason, mainEmail, to)
			if saveErr != nil {
				// A lost SwappedAt is no longer a guaranteed second swap — the
				// next tick re-adopts it from the main_swap watermark rescueSwap
				// just stamped — but the recovery now rides on a second kv row
				// surviving where this one did not, so it is still worth the
				// error line.
				dlogEvent("error", "limit-rescue-failed", "%s: swapped to %s but recording it failed — recovery depends on the main_swap watermark: %v", a.ID, to, saveErr)
			}
		case err != nil:
			dlogEvent("error", "limit-rescue-failed", "%s: rotate attempt %d failed (%s): %v", a.ID, hit.Attempts, outcome, err)
		default:
			dlogEvent("info", "limit-rescue-skipped", "%s: rotate attempt %d did not swap (%s)", a.ID, hit.Attempts, outcome)
		}
		if saveErr != nil && outcome != "swapped" {
			dlogErr("%s: persist limit-hit record failed: %v", a.ID, saveErr)
		}

	case rescueResume:
		recorded := ""
		if a.RecordedSession != nil {
			recorded, _ = a.RecordedSession(st)
		}
		peer, why := standingKillTarget(a, all, recorded)
		var respawnErr error
		if peer == nil {
			// A refusal is a DEFERRAL: the identity evidence was not there this
			// tick, so nothing is killed and the round is counted below. Logged
			// here, where the specific cause is known, exactly as the cell
			// rescue logs each of its own counted-out refusals.
			dlogEvent("info", "limit-rescue-skipped", "%s: %s", a.ID, why)
			respawnErr = errors.New(why)
		} else {
			respawnErr = standingRespawn(a, st, live)
		}
		if respawnErr != nil {
			// A round that respawned NOTHING. It must not resolve the hit —
			// that strands an agent whose account moved and whose process is
			// still latched onto the old one — but it cannot be unbounded
			// either, since an open hit is re-decided every tick. Counted with
			// limitRescueApply's own increment-then-test, path-relative cap
			// included: a reset re-arm arrives with its attempts already spent
			// by the parks that preceded it.
			hit = limitRescueApply(hit, action, "no-agents", "", now)
			if saveErr := saveLimitRescueStateByKey(key, a.ID, hit); saveErr != nil {
				dlogErr("%s: persist limit-hit record failed: %v", a.ID, saveErr)
			}
			if hit.HandledAt != 0 {
				// Which resume failed decides what carl is told: on the reset
				// path no credential ever moved, and a notice claiming a switch
				// would report a machine-wide change that never happened.
				if hit.SwappedAt != 0 {
					notifyOnce("kraken ⚠ "+a.ID, "usage limit — account switched but "+a.ID+" could not be respawned")
					dlogEvent("info", "limit-rescue-skipped", "%s: parked after %d respawn rounds — account was switched, the agent was not brought back", a.ID, hit.Attempts)
				} else {
					notifyOnce("kraken ⚠ "+a.ID, "usage limit — window reset but "+a.ID+" could not be respawned")
					dlogEvent("info", "limit-rescue-skipped", "%s: parked after %d respawn rounds — window had reset, the agent was not brought back", a.ID, hit.Attempts)
				}
				return
			}
			if peer != nil {
				// The refusal arm already logged its own cause above; logging
				// respawnErr here too would print the same sentence twice per
				// round, once bare and once wrapped.
				dlogEvent("error", "limit-rescue-failed", "%s: %s: respawn round %d did not bring %s back: %v", a.ID, reason, hit.Attempts, a.ID, respawnErr)
			}
			return
		}
		hit = limitRescueApply(hit, action, "", "", now)
		if err := saveLimitRescueStateByKey(key, a.ID, hit); err != nil {
			dlogErr("%s: persist limit-hit record failed: %v", a.ID, err)
		}
		dlogEvent("info", "limit-rescue-done", "%s: %s (pane %s killed, %s respawned)", a.ID, reason, peer.Pane, a.ID)

	case rescueParkManual:
		hit = limitRescueApply(hit, action, "", "", now)
		if err := saveLimitRescueStateByKey(key, a.ID, hit); err != nil {
			dlogErr("%s: persist limit-hit record failed: %v", a.ID, err)
		}
		notifyOnce("kraken ⚠ "+a.ID, "hit a usage limit — "+reason)
		dlogEvent("info", "limit-rescue-skipped", "%s: %s", a.ID, reason)

	case rescueParkNoTarget:
		hit = limitRescueApply(hit, action, "", "", now)
		if err := saveLimitRescueStateByKey(key, a.ID, hit); err != nil {
			dlogErr("%s: persist limit-hit record failed: %v", a.ID, err)
		}
		// Worded for attempt 1, where the notification actually fires
		// (notifyOnce keys on the hit): two more rotate attempts are still
		// ahead, so the reset resume is conditional rather than promised. The
		// time carries a date whenever the target is not today — a bare
		// "10:20pm" for a tomorrow target reads as tonight.
		parkNote := "hit a usage limit — " + reason
		if hit.ResetAt != 0 {
			reset := time.Unix(hit.ResetAt, 0).Local()
			form := "3:04pm"
			if reset.Format("2006-01-02") != now.Local().Format("2006-01-02") {
				form = "3:04pm on Jan 2"
			}
			parkNote += " — will keep retrying, and if no account appears, restart " + a.ID + " after the window resets at " + reset.Format(form)
		}
		notifyOnce("kraken ⚠ "+a.ID, parkNote)
		dlogEvent("info", "limit-rescue-skipped", "%s: %s (attempt %d)", a.ID, reason, hit.Attempts)
	}
}

// standingHealthPass is the tick's health arm for one standing agent: the
// transcript-derived trigger that covers what the StopFailure hook cannot.
//
// Both triggers exist for the same reason they do for cells, and the split
// matters more here: the hook is installed into kronos' settings at spawn
// (iris gets none at all — Q4), so a kronos already running when this ships
// has no hook, and iris has no hook ever. The stall classification is the
// hook-independent way to learn the same fact, and it is recorded in the same
// vocabulary ("rate_limit") so the two fold onto one record rather than
// opening two.
//
// No orch inbox row is written for any of this, deliberately — kronos must not
// be bumped about its own stall. The desktop notification is the whole
// escalation, and it fires once per streak: a stalled agent keeps the same
// banner at the tail of its transcript indefinitely, so the key is the reason
// rather than the tick.
func (d *daemonState) standingHealthPass(a standingAgent, st *store.Store, all []*core.Peer, live map[string]bool, now time.Time) {
	if a.Enabled == nil || !a.Enabled() || !live[a.Session] {
		return
	}
	recorded := ""
	if a.RecordedSession != nil {
		recorded, _ = a.RecordedSession(st)
	}
	reason, isLimit, retryable := standingHealth(a, all, recorded)
	if reason == "" {
		return
	}
	// Same idle-stall dedup shape the cell health pass uses: the reason can
	// embed a rounded idle duration, so key on the prefix or every tick
	// re-notifies with a fresh number.
	nkey := "standing-health:" + a.ID + "|" + strings.SplitN(reason, " idle ", 2)[0]
	if d.Notified[nkey] == 0 {
		notify("kraken ⚠ "+a.ID, reason)
		d.Notified[nkey] = now.Unix()
		dlogEvent("error", "standing-unhealthy", "unhealthy %s: %s", a.ID, reason)
	}
	if isLimit {
		lkey := standingHitTriggerKey(a.ID, reason)
		if limitHitTriggerDue(d.Notified[lkey], now) {
			// The recorded session id, never the discovered one — this is the
			// id the rescue's kill authorization is checked against, and a
			// guessed one recorded here would authorize killing whatever pane
			// the guess happened to land on.
			if _, err := recordLimitHitByKey(standingHitKey(a.ID), a.ID, "", "rate_limit", reason, recorded, "", now); err != nil {
				dlogErr("%s: record usage-limit hit from transcript failed: %v", a.ID, err)
			} else {
				d.Notified[lkey] = now.Unix()
				dlogEvent("info", "limit-hit", "%s: usage limit seen in transcript: %s", a.ID, reason)
			}
		}
	}
	if retryable {
		// The same backoff-plus-connectivity gate a cell's auto-retry rides:
		// an API overload comes in waves and a nudge during one just eats the
		// next 529, while an offline machine gets one notification and zero
		// poke spam. probeOnline rather than the tick's memoized closure —
		// gateRetry only calls it when a poke is actually due or the daemon is
		// already flagged offline, and there are two standing agents, so the
		// worst case is two dials on a tick where both are stalled.
		rkey := "retry:standing:" + a.ID
		poke, title, msg := d.gateRetry(now, rkey, probeOnline)
		if title != "" {
			notify(title, msg)
		}
		if poke {
			if err := standingNudge(a, all, reason); err != nil {
				dlogErr("auto-retry %s failed: %v", a.ID, err)
			} else {
				d.Poked[rkey] = now.Unix()
				dlog("auto-retried %s (%s)", a.ID, reason)
			}
		}
	}
}

// standingNudge is RetryCell's send for an agent with no cell: the same plain
// message typed into the same way, resuming a claude sitting at its prompt
// after an errored turn. a.Find rather than standingKillTarget's identity
// test, and that difference is the point — a nudge costs a stray line in the
// wrong pane, a kill costs a conversation, so only the second one demands
// positive identity evidence. "continue" rather than "continue the job":
// there is no job here, and a standing agent told to continue one would go
// looking for it.
func standingNudge(a standingAgent, all []*core.Peer, reason string) error {
	if a.Find == nil {
		return fmt.Errorf("%s: no way to find its pane", a.ID)
	}
	p := a.Find(all)
	if p == nil {
		return fmt.Errorf("%s: agent not found — rerun discovery", a.ID)
	}
	return core.SendMessage(p, "you stalled ("+reason+") — pick up exactly where you left off and continue")
}
