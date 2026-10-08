package jobs

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// cellctx.go — a cell's context meter and the one thing kraken does with it:
// type /handoff into zeus' pane once its context window is 75% full.
//
// The measurement arrives the same way kronos' does (kronosctx.go): the cell's
// statusline command is `kraken ctx-hook --slug <slug>`, which POSTs the live
// session's numbers to /api/cell/context, and RecordCellContext persists them.
// The pass below only reacts to what was recorded.
//
// Reports are keyed per (job, session) rather than per job: every claude in a
// cell worktree — zeus, its teammates, side threads — runs the same statusline
// command, so a per-job key would hold whichever of them rendered last. The
// pass reads one key per job, (j.ID, j.Session) — zeus' stamped session — and
// types only into CellZeus' pane.

// CellContext is one context report from a cell's statusline hook.
type CellContext struct {
	SessionID string  `json:"session_id"`
	Pct       float64 `json:"pct"`
	Window    int64   `json:"window"`
	At        int64   `json:"at"` // unix millis
}

// cellCtxKVKey is the kv key one session's latest report lives under.
func cellCtxKVKey(jobID int64, sessionID string) string {
	return fmt.Sprintf("cell_ctx:%d:%s", jobID, sessionID)
}

// RecordCellContext persists one report. The refusals mirror
// RecordKronosContext's: a blank session id makes the report unattributable,
// and a percentage that is NaN or outside [0,100] is not a fill (NaN is tested
// separately because every range comparison against it is false).
func RecordCellContext(st *store.Store, jobID int64, sessionID string, pct float64, window int64) error {
	if st == nil {
		return fmt.Errorf("cell context: no store")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("cell context: blank session id")
	}
	if math.IsNaN(pct) || pct < 0 || pct > 100 {
		return fmt.Errorf("cell context: percentage out of range")
	}
	data, err := json.Marshal(CellContext{
		SessionID: sessionID,
		Pct:       pct,
		Window:    window,
		At:        time.Now().UnixMilli(),
	})
	if err != nil {
		return err
	}
	return st.SetKV(cellCtxKVKey(jobID, sessionID), string(data))
}

// ReadCellContext returns one session's last report, and whether one exists.
func ReadCellContext(st *store.Store, jobID int64, sessionID string) (CellContext, bool) {
	if st == nil {
		return CellContext{}, false
	}
	raw, err := st.GetKV(cellCtxKVKey(jobID, sessionID))
	if err != nil || raw == "" {
		return CellContext{}, false
	}
	var c CellContext
	if err := json.Unmarshal([]byte(raw), &c); err != nil || strings.TrimSpace(c.SessionID) == "" {
		return CellContext{}, false
	}
	return c, true
}

// autoHandoffThreshold is the fill at or above which a handoff is due.
const autoHandoffThreshold = 75.0

// autoHandoffRetry is how long after a fire, with no drop below the threshold
// since, the pass fires again.
const autoHandoffRetry = 15 * time.Minute

// autoHandoffMaxFires caps a streak of fires — fires within
// autoHandoffFireWindow of the last one; past it the pass stops typing into
// the pane and notifies carl once instead.
const autoHandoffMaxFires = 3

// autoHandoffFireWindow ends a streak: a state whose last fire is at least
// this old has its fire count and exhausted flag reset before any other rule,
// so the cap bounds a streak rather than a session's whole life.
const autoHandoffFireWindow = 2 * time.Hour

// autoHandoffFresh is the oldest a report may be and still fire: older, it
// may be a fill recorded before the session was resumed rather than one from
// this run.
const autoHandoffFresh = 5 * time.Minute

// autoHandoffState is the pass' per-job memory, stored under
// autoHandoffKVKey. It is durable rather than process state so a `kraken
// serve` restart neither re-fires an armed-off handoff nor forgets the cap.
type autoHandoffState struct {
	SessionID  string `json:"session_id"`
	Fires      int    `json:"fires"`
	LastFireAt int64  `json:"last_fire_at"` // unix millis
	Armed      bool   `json:"armed"`
	Exhausted  bool   `json:"exhausted"`
}

// autoHandoffKVKey is the kv key a job's autoHandoffState lives under.
func autoHandoffKVKey(jobID int64) string {
	return fmt.Sprintf("auto_handoff:%d", jobID)
}

// autoHandoffDue is the pass' pure core: given zeus' latest report and the
// stored state, whether to type /handoff now, whether to tell carl the cap
// was reached, and the state to store next.
//
// A new session resets the state outright (it is a fresh context, and fires
// spent on the old one say nothing about it). A last fire at least
// autoHandoffFireWindow old ends the streak: the fire count and exhausted
// flag reset. A fill back below the threshold re-arms, so the next climb
// fires immediately — but it leaves the exhausted flag alone, so an exhausted
// streak notifies once however often the fill bounces. A fill that stays
// above it is re-fired every autoHandoffRetry, up to autoHandoffMaxFires per
// streak.
func autoHandoffDue(c CellContext, s autoHandoffState, now time.Time) (fire bool, notifyExhausted bool, next autoHandoffState) {
	next = s
	if s.SessionID != c.SessionID {
		next = autoHandoffState{SessionID: c.SessionID, Armed: true}
	}
	if next.Fires > 0 && now.UnixMilli()-next.LastFireAt >= autoHandoffFireWindow.Milliseconds() {
		next.Fires = 0
		next.Exhausted = false
	}
	switch {
	case c.Pct < autoHandoffThreshold:
		next.Armed = true
		return false, false, next
	case next.Fires >= autoHandoffMaxFires:
		notifyExhausted = !next.Exhausted
		next.Exhausted = true
		return false, notifyExhausted, next
	case next.Armed || now.UnixMilli()-next.LastFireAt >= autoHandoffRetry.Milliseconds():
		next.Fires++
		next.LastFireAt = now.UnixMilli()
		next.Armed = false
		return true, false, next
	default:
		return false, false, next
	}
}

// readAutoHandoffState reads a job's stored state; absent or unparseable reads
// as the zero state.
func readAutoHandoffState(st *store.Store, jobID int64) autoHandoffState {
	raw, err := st.GetKV(autoHandoffKVKey(jobID))
	if err != nil || raw == "" {
		return autoHandoffState{}
	}
	var s autoHandoffState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return autoHandoffState{}
	}
	return s
}

// writeAutoHandoffState stores a job's state under autoHandoffKVKey.
func writeAutoHandoffState(st *store.Store, jobID int64, s autoHandoffState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetKV(autoHandoffKVKey(jobID), string(data))
}

// autoHandoffPass types /handoff into a live cell's zeus when its context
// report is at or above autoHandoffThreshold. Called per job every tick from
// the daemon's !mirror block, right after the colour reconcile.
//
// Two gates sit in front of a fire, both leaving the stored state untouched:
// a report older than autoHandoffFresh never fires, and a RETRY fire (the
// stored state for this session was not armed) is held while zeus is
// running, since the earlier /handoff may still be queued or in progress.
// A fire is recorded only once the keystrokes were sent: no zeus found, or a
// send refused (a question prompt open, the pane gone), leaves the stored
// state untouched so the next tick tries again.
func autoHandoffPass(st *store.Store, j *Job, all []*core.Peer, live map[string]bool, now time.Time) {
	if st == nil || j == nil {
		return
	}
	// j.Session is zeus' stamped Claude session id — the key its statusline
	// report lands under. CellSession(j) is the tmux session name, used only
	// for liveness.
	if j.Session == "" || !live[CellSession(j)] {
		return
	}
	c, ok := ReadCellContext(st, j.ID, j.Session)
	if !ok {
		return
	}
	stored := readAutoHandoffState(st, j.ID)
	fire, notifyExhausted, next := autoHandoffDue(c, stored, now)
	switch {
	case fire:
		if now.UnixMilli()-c.At > autoHandoffFresh.Milliseconds() {
			return
		}
		zeus := CellZeus(j, all)
		if zeus == nil {
			return
		}
		retry := stored.SessionID == c.SessionID && !stored.Armed
		if retry && zeus.Status == "running" {
			return
		}
		if _, err := core.SendToPeer(zeus, "/handoff"); err != nil {
			return
		}
		if err := writeAutoHandoffState(st, j.ID, next); err != nil {
			dlogErr("%s: recording the auto-handoff failed: %v", j.Slug, err)
		}
		dlogEvent("info", "auto-handoff", "%s: context at %.0f%% — typed /handoff into zeus", j.Slug, c.Pct)
		notifyFn("kraken: auto-handoff", fmt.Sprintf("%s: context at %.0f%% — typed /handoff into zeus", j.Slug, c.Pct))
	case notifyExhausted:
		if err := writeAutoHandoffState(st, j.ID, next); err != nil {
			dlogErr("%s: recording the auto-handoff cap failed: %v", j.Slug, err)
		}
		// "each within Nh of the last" rather than "within Nh": the window is
		// measured from the LAST fire, so a streak is a chain of fires none
		// of which is more than the window apart, and three spaced 1h50m
		// apart are one streak spanning well over the window.
		notifyFn("kraken: auto-handoff gave up", fmt.Sprintf("%s: context back at ≥%.0f%% after %d handoffs each within %dh of the last — look at the pane",
			j.Slug, autoHandoffThreshold, autoHandoffMaxFires, int(autoHandoffFireWindow.Hours())))
	default:
		if next != stored {
			if err := writeAutoHandoffState(st, j.ID, next); err != nil {
				dlogErr("%s: recording the auto-handoff state failed: %v", j.Slug, err)
			}
		}
	}
}
