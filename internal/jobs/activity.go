package jobs

import (
	"os"
	"strings"
	"sync"

	"kraken/internal/core"
)

// activity.go — a job's ACTIVITY for the board's agent map: what the cell's
// lead is doing right now, its last few events, and what each of its
// teammates last called, all read out of transcripts by core.TailEvents. The
// board serves it as webJob.activity on a live cell, and the steer modal's
// events panel reads a longer tail through JobEvents. Subagents are never
// read here: they have no transcript of their own outside the lead's sidecar
// dir, and the map draws them with generic poses.

// ActivityBoardEvents is how many of the lead's events ride a board card.
const ActivityBoardEvents = 24

// jobEventsMax caps JobEvents' n.
const jobEventsMax = 200

// crewEventsTail is how many of a teammate's newest events are read to find
// its newest call; a teammate whose last crewEventsTail events are all
// arriving messages is read again over the jobEventsMax tail.
const crewEventsTail = 16

// CrewActivity is one teammate's newest call.
type CrewActivity struct {
	Name  string `json:"name"`
	Act   string `json:"act"`
	Label string `json:"label,omitempty"`
	At    int64  `json:"at"`
}

// JobActivity is a job's activity on the wire: the lead's current activity
// (Act, Label — "think" with no label when it has no call among its recent
// events), the newest event's time, the lead's recent events and the crew's.
//
// Model is the model id the lead runs on (LeadModel), which the map turns
// into the lead character's coat; BuildJobActivity leaves it empty and the
// board build fills it, since only that caller knows the lead's live pid.
type JobActivity struct {
	Act    string            `json:"act"`
	Label  string            `json:"label,omitempty"`
	At     int64             `json:"at"`
	Model  string            `json:"model,omitempty"`
	Events []core.AgentEvent `json:"events"`
	Crew   []CrewActivity    `json:"crew,omitempty"`
}

// LeadModel is the model id the cell's lead is running on, which the agent
// map draws as the lead's coat: a codex lead's `-m` value (codexZeusModel);
// else the Fable fallback tier when the fallback record names this very pid,
// since kraken typed `/model` into that process; else the `--model` its
// claude process was spawned with (zeusLaunchedModel, which falls back to the
// configured model when the cell dir has no stamp). A pid of 0 (no live lead
// peer found) skips the fallback check. It cannot see a `/model` the
// operator typed by hand.
//
// The record is written when `/model` is typed, before the switch is
// confirmed, so the lead reads as switched from that moment. A switch that
// expired with no turn confirmed (ExpiredAt) is not trusted either way and
// reads as the spawn model.
func LeadModel(j *Job, pid int) string {
	if j.Agent == "codex" {
		return codexZeusModel(j)
	}
	if pid != 0 {
		if rec := readLastFallback(limitHitKey(j.ID)); rec.PID == pid && rec.ExpiredAt == 0 {
			return fableFallbackModel
		}
	}
	return zeusLaunchedModel(j)
}

// BuildJobActivity assembles a JobActivity from transcript paths: the lead's
// and one per teammate, in crewOrder. nil when the lead's transcript yields
// no events.
func BuildJobActivity(leadTranscript string, crewTranscripts map[string]string, crewOrder []string) *JobActivity {
	if leadTranscript == "" {
		return nil
	}
	events, _ := core.TailEvents(leadTranscript, ActivityBoardEvents)
	if len(events) == 0 {
		return nil
	}
	a := &JobActivity{Act: "think", At: events[len(events)-1].T, Events: events}
	if ev, ok := newestCall(events); ok {
		a.Act, a.Label = ev.Act, ev.Label
	}
	seen := map[string]bool{}
	for _, name := range crewOrder {
		if seen[name] {
			continue
		}
		seen[name] = true
		path := crewTranscripts[name]
		if path == "" {
			continue
		}
		ev, ok := newestCallIn(path)
		if !ok {
			continue
		}
		a.Crew = append(a.Crew, CrewActivity{Name: name, Act: ev.Act, Label: ev.Label, At: ev.T})
	}
	return a
}

// newestCall is the newest event that is not an arriving message.
func newestCall(events []core.AgentEvent) (core.AgentEvent, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Act != "hear" {
			return events[i], true
		}
	}
	return core.AgentEvent{}, false
}

// newestCallIn is a transcript's newest call: a short tail first, the long
// one only when the short one held nothing but messages.
func newestCallIn(path string) (core.AgentEvent, bool) {
	events, _ := core.TailEvents(path, crewEventsTail)
	if ev, ok := newestCall(events); ok {
		return ev, true
	}
	if len(events) < crewEventsTail {
		return core.AgentEvent{}, false
	}
	events, _ = core.TailEvents(path, jobEventsMax)
	return newestCall(events)
}

// JobActivityFor is BuildJobActivity for a job: its stamped lead session's
// transcript, and each named teammate's own transcript found among the job's
// project dirs. nil for a nil job, a job with no stamped session, or a lead
// transcript that is not on disk.
func JobActivityFor(j *Job, teammates []string) *JobActivity {
	_, lead := stampedLeadTranscript(j)
	if lead == "" {
		return nil
	}
	var crew map[string]string
	if len(teammates) > 0 {
		dirs := activityProjectDirs(j)
		crew = make(map[string]string, len(teammates))
		for _, name := range teammates {
			if name == "" {
				continue
			}
			if _, done := crew[name]; done {
				continue
			}
			crew[name] = core.NewestAgentTranscript(dirs, name)
		}
	}
	return BuildJobActivity(lead, crew, teammates)
}

// activityProjectDirs is the job's transcript dirs. A short list (an
// unlistable projects root) is still searched: a teammate missed this build
// is found on the next.
func activityProjectDirs(j *Job) []string {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return nil
	}
	dirs, _ := JobProjectDirs(j, repo)
	return dirs
}

// JobEvents is the lead's newest n events (n clamped to 1..200), oldest
// first; an empty, non-nil slice when there is nothing to read.
func JobEvents(j *Job, n int) ([]core.AgentEvent, error) {
	if n < 1 {
		n = 1
	}
	if n > jobEventsMax {
		n = jobEventsMax
	}
	_, path := stampedLeadTranscript(j)
	if path == "" {
		return []core.AgentEvent{}, nil
	}
	events, err := core.TailEvents(path, n)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []core.AgentEvent{}
	}
	return events, nil
}

// leadPathCache remembers where each session's transcript was found, so the
// board does not glob every project dir for every live cell on each build. A
// remembered path is used only while it still exists.
var (
	leadPathMu    sync.Mutex
	leadPathCache = map[string]string{}
)

// stampedLeadTranscript is stampedZeusTranscript (transcript.go) through
// leadPathCache.
func stampedLeadTranscript(j *Job) (sid, path string) {
	if j == nil {
		return "", ""
	}
	sid = strings.TrimSpace(j.Session)
	if sid == "" {
		return "", ""
	}
	leadPathMu.Lock()
	p, ok := leadPathCache[sid]
	leadPathMu.Unlock()
	if ok {
		if _, err := os.Stat(p); err == nil {
			return sid, p
		}
	}
	p = core.TranscriptPathForSession(sid)
	leadPathMu.Lock()
	if p == "" {
		delete(leadPathCache, sid)
	} else {
		if len(leadPathCache) >= 1024 {
			leadPathCache = map[string]string{}
		}
		leadPathCache[sid] = p
	}
	leadPathMu.Unlock()
	return sid, p
}
