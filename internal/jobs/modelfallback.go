package jobs

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
)

// The Fable fallback is the limit rescue's cheapest arm: when a Fable-tier
// agent (a cell's zeus, iris) stops on the account's FABLE window
// while the account's session, weekly and Opus windows still have room,
// nothing is wrong with the account — only with the tier. Claude Code's own
// banner names the remedy ("You've reached your Fable limit. Run
// /usage-credits to continue or switch models with /model."), so kraken types
// exactly that: `/model <opus>` into the stalled pane, then — on a LATER tick,
// once the slash command has certainly finished — a one-line prompt to carry
// on, re-sent until the pane shows a turn ran.
//
// It runs BEFORE the rotate/resume chain and pre-empts it, for two reasons.
// It is strictly less disruptive — no rebind, no SIGKILL, so background tasks,
// in-flight teammates and the prompt cache all survive, where a resume loses
// every one of them. And when Fable is what ran out, rotation usually has
// nowhere to go: the rotation candidate gate excludes any slot at Fable ≥ 80%,
// and Fable is a weekly window that tends to drain across the whole pool
// together (measured 2026-09-30: every usable slot at Fable 100% with session
// and weekly well short of their caps, and a standing agent parked-no-target
// on this exact banner).
//
// It is not an ACCOUNT decision, so the manual rotation pin does not gate it:
// the pin protects the machine's login from being moved, and this moves no
// login. The switch lives exactly as long as the process: a later respawn
// comes back on its spawn-time --model, which is why what the record pins is
// the PID of the process that was switched.

// fableFallbackModel is the tier a Fable-exhausted agent drops to.
const fableFallbackModel = core.FableFallbackModel

const (
	// fableFallbackAbsorbWindow is how long after a switch a fresh hit on the
	// SAME process is read as a late re-delivery of the exhaustion that was
	// just handled (the transcript arm can sight the banner on the tick after
	// the switch, before the continue turn lands) and is resolved silently
	// rather than handed to rotation, which would kill a zeus now working on
	// Opus.
	fableFallbackAbsorbWindow = 2 * time.Minute
	// fableFallbackPromptWindow bounds how long a switched process whose
	// continue prompt never landed keeps being retried with the prompt alone.
	fableFallbackPromptWindow = 30 * time.Minute
	// fableFallbackRetryGap spaces retries after a refused send (an open
	// question prompt, a pane race), so a refusal costs one attempt per gap
	// rather than one per tick.
	fableFallbackRetryGap = 2 * time.Minute
	// fableFallbackBumpGap is how long a sent continue prompt is given to show
	// up as a running turn before it is sent again. A prompt typed into a pane
	// can land and still start nothing (measured 2026-10-01: a prompt typed one
	// second after /model left zeus idle until carl typed "continue" himself).
	fableFallbackBumpGap = time.Minute
)

// fableBannerRe matches the per-model-tier banner for the Fable tier, with or
// without a version ("Fable limit", "Fable 5 limit", "Fable 5.1 limit"). The
// gap is bounded and line-local rather than "no period", since a dotted
// version would otherwise end the match.
var fableBannerRe = regexp.MustCompile(`(?i)reached your fable\b[^\n]{0,12}?\blimit`)

// otherWindowBanners are the banners that say a window OTHER than Fable is
// exhausted — the non-Fable subset of core's usageLimitMarkers (read.go), so a
// new window banner added there belongs here too. Two readers: the fallback
// (any of these in the details means switching tier cannot help), and the
// limit-hit fold, through namesLimitWindow, which adopts a delivery naming a
// window over stored details that name none.
var otherWindowBanners = []string{"hit your session limit", "hit your weekly limit", "hit your opus limit", "usage credits are required"}

// namesOtherWindow reports whether details carry one of otherWindowBanners.
func namesOtherWindow(details string) bool {
	lower := strings.ToLower(details)
	for _, m := range otherWindowBanners {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// namesLimitWindow reports whether details carry a usage-limit banner — the
// Fable banner or one of otherWindowBanners — as opposed to a raw 429 body,
// which names none.
func namesLimitWindow(details string) bool {
	return fableBannerRe.MatchString(details) || namesOtherWindow(details)
}

// fallbackRecord is one agent's last switch: when, which process, when the
// continue prompt was last sent, whether the process was since seen running —
// Prompted means CONFIRMED continued, not merely typed at — or when it was
// given up on.
type fallbackRecord struct {
	At  int64 `json:"at"`
	PID int   `json:"pid"`
	// PromptedAt is when the continue prompt was last sent (0 = not yet).
	PromptedAt int64 `json:"prompted_at,omitempty"`
	// Prompted and ConfirmedAt record the pane being seen running after the
	// prompt; the absorb window counts from ConfirmedAt, since the switch can
	// be minutes older than the resolve.
	Prompted    bool  `json:"prompted"`
	ConfirmedAt int64 `json:"confirmed_at,omitempty"`
	// ExpiredAt is when the prompt window ran out with no turn confirmed.
	// Set once; a later hit on the same process is then a re-delivery of an
	// exhaustion already given up on, never a fresh expiry to resolve and
	// notify again.
	ExpiredAt int64 `json:"expired_at,omitempty"`
}

type fallbackVerdict string

const (
	fallbackNo     fallbackVerdict = "no"     // not a fallback case — the ordinary chain decides
	fallbackSwitch fallbackVerdict = "switch" // type /model, then the continue prompt
	fallbackPrompt fallbackVerdict = "prompt" // already switched; the continue is owed until the pane runs
	fallbackAbsorb fallbackVerdict = "absorb" // late re-delivery of a handled exhaustion: resolve silently
	fallbackExpire fallbackVerdict = "expire" // switched, but no turn confirmed in the prompt window: resolve once per process, never rotate
)

// fableFallbackCandidate is the cheap half of the decision — no snapshot, no
// kv, no discovery — shared by the decision and by tryFableFallback's early
// return so the two cannot drift.
func fableFallbackCandidate(hit LimitHit, agentModel string) bool {
	return hit.At != 0 && hit.HandledAt == 0 && hit.Error == "rate_limit" && hit.SwappedAt == 0 &&
		strings.Contains(strings.ToLower(agentModel), "fable")
}

// fableFallbackDecide is the pure decision. acct is the usage-snapshot row of
// the account the agent bills (nil when unknown), last is the agent's previous
// switch, and peerPID/peerSession identify the live process the keys would go
// to.
//
// Fable exhaustion is read from two independent sources because each alone
// has a hole: the StopFailure hook delivers a generic 429 body that never
// names the tier (a later transcript delivery folding onto it replaces those
// details only because mergeLimitHitDelivery adopts a banner that names a
// window), while the snapshot is a poll that can lag the hit — and whose row
// for a limited account is often itself an errored 429, which
// pickFallbackAccount skips, leaving the banner as the only evidence.
// Headroom can only come from the snapshot — with no row the banner alone is
// trusted, since its own text offers /model as the remedy and a wrong guess
// costs one more hit, which then goes to the ordinary chain.
func fableFallbackDecide(hit LimitHit, agentModel string, acct *core.AccountUsage, last fallbackRecord, peerPID int, peerSession string, now time.Time) fallbackVerdict {
	if !fableFallbackCandidate(hit, agentModel) {
		return fallbackNo
	}
	// Every claude in a cell posts onto the job's one hit key; a hit from a
	// side thread or a teammate is not this pane's, and keys typed here would
	// fix nothing.
	if hit.SessionID != "" && peerSession != "" && hit.SessionID != peerSession {
		return fallbackNo
	}
	// Whether the details name a non-Fable window — read by both branches below.
	otherWindow := namesOtherWindow(hit.Details)
	// The process kraken already switched is on Opus: whatever stopped it now
	// is not its Fable window. The configured model still says Fable, so this
	// is the only thing that can tell.
	if last.PID != 0 && last.PID == peerPID {
		// The prompt window runs from the switch, not from the hit: the hit
		// that triggered a switch can be far older than it (it sits
		// unresolved through the rotate chain's retry backoff or a
		// parked-until-reset wait until the snapshot shows Fable spent, and a
		// transcript-arm hit's At can itself lag the banner).
		if !last.Prompted {
			// An expired switch stays expired. The transcript arm re-records
			// the banner for as long as the pane sits on it, each a fresh
			// hit the resolved one no longer folds, and every one of them
			// would otherwise expire — and notify — again. A hit naming
			// another window is a real follow-on limit, as below.
			if last.ExpiredAt != 0 {
				if otherWindow {
					return fallbackNo
				}
				return fallbackAbsorb
			}
			if now.Sub(time.Unix(last.At, 0)) < fableFallbackPromptWindow {
				return fallbackPrompt
			}
			// Rotation would kill a process already on Opus to fix a Fable
			// window it no longer bills — give up on the continue instead.
			return fallbackExpire
		}
		// The absorb window compares a FRESH hit against the switch, and a
		// hit naming another window is a real follow-on limit, never a
		// re-delivery of the Fable one.
		ref := last.At
		if last.ConfirmedAt != 0 {
			ref = last.ConfirmedAt
		}
		since := time.Duration(hit.At-ref) * time.Second
		if since < 0 {
			since = -since
		}
		if since < fableFallbackAbsorbWindow && !otherWindow {
			return fallbackAbsorb
		}
		return fallbackNo
	}
	if otherWindow {
		return fallbackNo
	}
	banner := fableBannerRe.MatchString(hit.Details)
	if acct == nil {
		if banner {
			return fallbackSwitch
		}
		return fallbackNo
	}
	if !banner && modelPercent(acct, "Fable", now) < 100 {
		return fallbackNo
	}
	if acct.Session != nil && acct.Session.Percent >= 100 {
		return fallbackNo
	}
	if acct.Weekly != nil && acct.Weekly.Percent >= 100 {
		return fallbackNo
	}
	// Opus has its own model window: switching onto a spent one would trade
	// one limit for the next.
	if modelPercent(acct, "Opus", now) >= 100 {
		return fallbackNo
	}
	return fallbackSwitch
}

// holdsInRetryGap is whether a tick inside the retry gap after a refused send
// keeps the hit (true) or hands it to the ordinary chain (false). A refused
// /model hands it on. Every verdict about a process kraken has already
// switched holds it, because that process is on Opus and rotation would kill
// it: an owed continue prompt, an expired prompt window, and a re-delivery to
// absorb. Each is acted on at the first tick past the gap. A refused prompt
// re-stamps the gap on every attempt, so an expiry can land inside it.
func holdsInRetryGap(v fallbackVerdict) bool {
	return v == fallbackPrompt || v == fallbackExpire || v == fallbackAbsorb
}

// modelPercent is the account's usage of one model window, the highest when
// the snapshot carries more than one row of that name, 0 when it carries none.
// A row whose window has already reset is ignored: a stale poll can keep
// reporting 100% past the reset.
func modelPercent(a *core.AccountUsage, name string, now time.Time) float64 {
	var pct float64
	for _, m := range a.Models {
		if !strings.EqualFold(m.Name, name) {
			continue
		}
		if m.ResetsAt != 0 && m.ResetsAt <= now.UnixMilli() {
			continue
		}
		if m.Percent > pct {
			pct = m.Percent
		}
	}
	return pct
}

// pickFallbackAccount is the snapshot row for email: rows whose last poll
// errored are skipped, and among the rest a fresh row beats a stale one, then
// the newest fetch wins — main's identity appears twice (the main row and its
// pool mirror), and the two can be polls apart.
func pickFallbackAccount(snap core.UsageSnapshot, email string) *core.AccountUsage {
	if email == "" {
		return nil
	}
	var best *core.AccountUsage
	for i := range snap.Accounts {
		a := &snap.Accounts[i]
		if !strings.EqualFold(a.Email, email) || a.Error != "" {
			continue
		}
		if best == nil || (best.Stale && !a.Stale) || (best.Stale == a.Stale && a.FetchedAt > best.FetchedAt) {
			best = a
		}
	}
	return best
}

func fallbackAccount(email string) *core.AccountUsage {
	snap, ok := ReadUsageSnapshot()
	if !ok {
		return nil
	}
	return pickFallbackAccount(snap, email)
}

// transcriptWrittenSince reports whether the peer's transcript was appended
// to after the given unix second — the confirmation that survives a lead whose
// session file stays "busy" across turns (resident teammates), where the
// status clock never moves. A prompt left unsubmitted in the input box writes
// nothing, so it cannot confirm.
func transcriptWrittenSince(p *core.Peer, at int64) bool {
	path, err := core.ReadTranscriptPath(p)
	if err != nil {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.ModTime().Unix() > at
}

// fableFallbackPending reports a switch whose continue is still unconfirmed.
// The rescue passes skip adopting a landed main swap while it holds: adoption
// stamps SwappedAt, which closes the fallback's gate and sends a pane already
// on Opus to the kill-and-resume chain.
func fableFallbackPending(hitKey string, now time.Time) bool {
	last := readLastFallback(hitKey)
	return last.PID != 0 && !last.Prompted && now.Sub(time.Unix(last.At, 0)) < fableFallbackPromptWindow
}

// fableFallbackKey is the kv row holding an agent's last switch, keyed beside
// the agent's limit-hit row (a job id or a standing agent's id).
func fableFallbackKey(hitKey string) string {
	return "fable_fallback:" + strings.TrimPrefix(hitKey, "limit_hit:")
}

func readLastFallback(hitKey string) fallbackRecord {
	var rec fallbackRecord
	st := ActiveStore()
	if st == nil {
		return rec
	}
	raw, err := st.GetKV(fableFallbackKey(hitKey))
	if err != nil || raw == "" {
		return rec
	}
	_ = json.Unmarshal([]byte(raw), &rec)
	return rec
}

func recordLastFallback(hitKey string, rec fallbackRecord) {
	st := ActiveStore()
	if st == nil {
		return
	}
	data, _ := json.Marshal(rec)
	if err := st.SetKV(fableFallbackKey(hitKey), string(data)); err != nil {
		dlogErr("%s: record fable fallback failed: %v", hitKey, err)
	}
}

// fableFallbackPrompt is the turn typed after /model. The agent's brief still
// states its Fable tier, so the prompt corrects that as well as restarting it.
func fableFallbackPrompt() string {
	return "kraken: this account's Fable usage ran out (its session and weekly usage did not), so kraken switched you to " + fableFallbackModel + " with /model. Nothing else changed — same account, same conversation. Pick up exactly where you left off and continue."
}

// tryFableFallback is the shared effect half for both passes: decide, switch,
// resolve the hit. It reports whether it handled the hit — true means the
// caller's tick is done. email is a func so the account lookup (a
// ~/.claude.json read for main) is paid only once the cheap gates pass.
//
// Both sends run SendToPeer's full guard chain (identity re-check, open
// question prompt refusal). The switch and the continue are split across
// ticks: /model goes out and is recorded, and the hit stays unresolved, so
// the next tick reads fallbackPrompt and owes the continue. That verdict is
// then held until the pane shows a turn ran since the send — a typed prompt is
// not proof of a turn — re-sending the prompt every fableFallbackBumpGap, and
// the hit is resolved on that confirmation, or unconfirmed once
// fableFallbackPromptWindow runs out (fallbackExpire, once per process, never
// rotation).
func (d *daemonState) tryFableFallback(hitKey, label, agentModel string, email func() string, hit LimitHit, find func() *core.Peer, now time.Time) bool {
	if !fableFallbackCandidate(hit, agentModel) {
		return false // cheap gates first — the snapshot, kv and discovery below are not free
	}
	p := find()
	if p == nil {
		return false
	}
	last := readLastFallback(hitKey)
	verdict := fableFallbackDecide(hit, agentModel, fallbackAccount(email()), last, p.PID, p.SessionID, now)
	retryKey := "fable-retry:" + hitKey
	if at := d.Poked[retryKey]; at != 0 && now.Sub(time.Unix(at, 0)) < fableFallbackRetryGap {
		return holdsInRetryGap(verdict)
	}
	resolve := func(outcome string) {
		hit.HandledAt = now.Unix()
		hit.Outcome = outcome
		hit.LastAttempt = now.Unix()
		if err := saveLimitRescueStateByKey(hitKey, label, hit); err != nil {
			dlogErr("%s: persist limit-hit record failed: %v", label, err)
		}
	}
	switch verdict {
	case fallbackAbsorb:
		if last.ExpiredAt != 0 {
			resolve("model-fallback-expired")
			dlogEvent("info", "limit-rescue-skipped", "%s: limit re-delivered after the expired switch to %s — same process, still needs a manual nudge", label, fableFallbackModel)
			return true
		}
		resolve("model-fallback")
		dlogEvent("info", "limit-rescue-skipped", "%s: limit re-delivered after the switch to %s — same process, resolved without action", label, fableFallbackModel)
		return true
	case fallbackExpire:
		// Stamped before the resolve so a later re-delivery reads as
		// absorb even if the hit-record persist below fails; the notify
		// is keyed on the switch as well, so a failed kv write cannot
		// re-fire it within this daemon's run.
		last.ExpiredAt = now.Unix()
		recordLastFallback(hitKey, last)
		resolve("model-fallback-expired")
		dlogEvent("error", "limit-rescue-failed", "%s: switched to %s but no turn was seen in %s of continue prompts — resolved, needs a manual nudge", label, fableFallbackModel, fableFallbackPromptWindow)
		nkey := "limit:fable-expire:" + hitKey + "|" + strconv.FormatInt(last.At, 10)
		if d.Notified[nkey] == 0 {
			notify("kraken ⇣ "+label, "Fable limit — switched to "+fableFallbackModel+" but the agent never continued")
			d.Notified[nkey] = now.Unix()
		}
		return true
	case fallbackSwitch:
		if _, err := core.SendToPeer(p, "/model "+fableFallbackModel); err != nil {
			d.Poked[retryKey] = now.Unix()
			dlogEvent("error", "limit-rescue-failed", "%s: fable fallback could not switch the pane to %s: %v", label, fableFallbackModel, err)
			return false
		}
		recordLastFallback(hitKey, fallbackRecord{At: now.Unix(), PID: p.PID})
		dlogEvent("info", "limit-rescue", "%s: fable window exhausted — switched the pane to %s, continue prompt follows next tick", label, fableFallbackModel)
		return true // the continue goes out once the slash command has certainly finished
	case fallbackPrompt:
	default:
		return false
	}
	// Evidence of a turn since the send: the pane running or stopped on a
	// question it asked, or — for a turn that started and finished between two
	// ticks — the session file's status clock moved past the send. Nothing
	// counts before the first send: a session file's busy is trusted unrefined
	// for a few seconds, and could confirm a continue that was never typed.
	ranSincePrompt := last.PromptedAt != 0 &&
		(p.Status == "running" || p.Status == "question" || p.StatusAt > last.PromptedAt*1000 ||
			transcriptWrittenSince(p, last.PromptedAt))
	if !ranSincePrompt {
		if last.PromptedAt != 0 && now.Sub(time.Unix(last.PromptedAt, 0)) < fableFallbackBumpGap {
			return true // a prompt is out and still has time to start a turn
		}
		if _, err := core.SendToPeer(p, fableFallbackPrompt()); err != nil {
			// The model is switched; only the prompt is owed, and the next tick
			// past the retry gap sends it alone (fallbackPrompt).
			d.Poked[retryKey] = now.Unix()
			dlogEvent("error", "limit-rescue-failed", "%s: switched to %s but the continue prompt was refused: %v", label, fableFallbackModel, err)
			return true
		}
		if last.PromptedAt != 0 {
			dlogEvent("info", "limit-rescue", "%s: continue prompt started no turn — bumped again", label)
		}
		last.PromptedAt = now.Unix()
		recordLastFallback(hitKey, last)
		return true // confirmed only once a later tick sees a turn ran
	}
	last.Prompted = true
	last.ConfirmedAt = now.Unix()
	recordLastFallback(hitKey, last)
	resolve("model-fallback")
	nkey := "limit:fable:" + hitKey + "|" + strconv.FormatInt(hit.At, 10)
	if d.Notified[nkey] == 0 {
		notify("kraken ⇣ "+label, "Fable limit — switched to "+fableFallbackModel+" on the same account")
		d.Notified[nkey] = now.Unix()
	}
	dlogEvent("info", "limit-rescue-done", "%s: fable window exhausted, account has headroom — switched to %s", label, fableFallbackModel)
	return true
}
