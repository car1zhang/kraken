package api

import (
	"net/http"
	"sort"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
)

// readUsageSnapshot is jobs.ReadUsageSnapshot, swapped out in tests so
// usageHandler doesn't need a real ~/.kraken/usage.json on disk.
var readUsageSnapshot = jobs.ReadUsageSnapshot

// usageNow is time.Now, swapped out in tests for a deterministic age_seconds.
var usageNow = time.Now

// readRotationState is jobs.ReadRotationState, swapped out in tests so
// usageHandler doesn't need a real ~/.kraken/rotation.json on disk — mirrors
// readUsageSnapshot's swappable-var pattern above.
var readRotationState = jobs.ReadRotationState

// webUsage is GET /api/usage's response shape. Additive endpoint — apiContract
// is not bumped for it (no key on any existing shape is retyped, renamed, or
// removed). RotationMode/RotationSetBy/RotationSetAt were themselves added
// additively (Slice C) — see jobs.RotationState.
type webUsage struct {
	Available     bool                `json:"available"`
	FetchedAt     int64               `json:"fetched_at"`  // unix millis of the pass; 0 when unavailable
	AgeSeconds    int64               `json:"age_seconds"` // 0 when unavailable
	DaemonOK      bool                `json:"daemon_ok"`
	Accounts      []core.AccountUsage `json:"accounts"`
	RotationMode  string              `json:"rotation_mode"`             // "auto" or "manual"
	RotationSetBy string              `json:"rotation_set_by,omitempty"` // "cli"/"web"/"" (auto-default)
	RotationSetAt int64               `json:"rotation_set_at,omitempty"` // unix millis; 0 when auto-default
}

// usageHandler serves the daemon's last usage pass. Read-only: GET only.
func usageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	st, ok := jobs.ReadDaemonStatus()
	daemonOK := ok && jobs.ProcessAlive(st.PID)

	snap, available := readUsageSnapshot()
	accounts := snap.Accounts
	if accounts == nil {
		accounts = []core.AccountUsage{}
	}
	// the snapshot is already daemon-ordered (main first, then slots by
	// slug), but re-sort defensively rather than trust that invariant holds
	// across every future daemon change
	sort.SliceStable(accounts, func(i, j int) bool {
		if accounts[i].Current != accounts[j].Current {
			return accounts[i].Current
		}
		return accounts[i].Slug < accounts[j].Slug
	})

	var ageSeconds int64
	// FetchedAt > 0 guards a hand-corrupted snapshot (parses, but no
	// fetched_at) from reporting an age of ~now; the daemon always stamps it
	if available && snap.FetchedAt > 0 {
		if age := usageNow().Sub(time.UnixMilli(snap.FetchedAt)); age > 0 {
			ageSeconds = int64(age / time.Second)
		}
	}

	rot := readRotationState()
	webJSON(w, http.StatusOK, webUsage{
		Available:     available,
		FetchedAt:     snap.FetchedAt,
		AgeSeconds:    ageSeconds,
		DaemonOK:      daemonOK,
		Accounts:      accounts,
		RotationMode:  rot.Mode,
		RotationSetBy: rot.SetBy,
		RotationSetAt: rot.SetAt,
	})
}
