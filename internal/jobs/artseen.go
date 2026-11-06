package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// artseen.go — which of a job's artifacts the operator has opened, and when.
// One kv row per job, `artifacts_seen:<job id>`, holding a JSON object from
// an artifact's name (its path relative to the artifacts dir, "/"-separated,
// the same string the wire's `artifacts[].name` carries) to the unix millis
// of the last time it was served to the operator through GET /api/artifact.
// The board compares that moment with the artifact's mtime: an artifact
// changed since it was last opened — or never opened — is NEW, and the map
// draws the card's lead holding it as a sheet of paper. Keyed on the
// immutable job id, like every other per-job kv row: archiving frees a slug,
// and a new job must not inherit a dead generation's reads.
//
// The read itself is the mark. Nothing the operator does on the board or the
// phone opens an artifact without fetching it from that route, and no agent
// or node proxy goes through it (a cell reads the files, a sibling node calls
// the resolvers directly), so a mark means the operator saw it, with no
// client change and no second request.

// artifactsSeenPrefix is the kv key family; a job's row is the prefix plus
// its id.
const artifactsSeenPrefix = "artifacts_seen:"

// artifactsSeenMax bounds the names one row keeps: past it the names read
// longest ago are dropped, so a job that rewrites generated files under new
// names forever cannot grow its row without bound.
const artifactsSeenMax = 500

// artifactsSeenMu serializes the read-merge-write of MarkArtifactSeen: two
// artifacts opened at once (an html card and the image it embeds) must not
// drop each other's mark.
var artifactsSeenMu sync.Mutex

// ArtifactSeen is one job's record: artifact name to the unix millis it was
// last opened.
type ArtifactSeen map[string]int64

func artifactsSeenKey(jobID int64) string {
	return artifactsSeenPrefix + strconv.FormatInt(jobID, 10)
}

// MarkArtifactSeen records that the operator opened name on j, now. A job
// with no id (a file-mode job in a test), no wired store, or a row that
// cannot be read or written is a logged no-op: the read that triggered it
// must still be served.
func MarkArtifactSeen(j *Job, name string) {
	markArtifactSeenAt(j, name, time.Now().UnixMilli())
}

func markArtifactSeenAt(j *Job, name string, now int64) {
	st := ActiveStore()
	if st == nil || j == nil || j.ID == 0 || name == "" {
		return
	}
	artifactsSeenMu.Lock()
	defer artifactsSeenMu.Unlock()
	key := artifactsSeenKey(j.ID)
	seen, err := readArtifactSeen(st, key)
	if err != nil {
		util.LogEvent("jobs", "error", "artifact-seen-read", err.Error()+" (replaced)")
		seen = ArtifactSeen{}
	}
	seen[name] = now
	evictOldestSeen(seen, name)
	data, err := json.Marshal(seen)
	if err != nil {
		util.LogEvent("jobs", "error", "artifact-seen-write", fmt.Sprintf("%s: %v", key, err))
		return
	}
	if err := st.SetKV(key, string(data)); err != nil {
		util.LogEvent("jobs", "error", "artifact-seen-write", err.Error())
	}
}

// readArtifactSeen reads one row; an absent row is an empty record.
func readArtifactSeen(st *store.Store, key string) (ArtifactSeen, error) {
	raw, err := st.GetKV(key)
	if errors.Is(err, store.ErrNotFound) || (err == nil && raw == "") {
		return ArtifactSeen{}, nil
	}
	if err != nil {
		return ArtifactSeen{}, err
	}
	return parseArtifactSeen(key, raw)
}

func parseArtifactSeen(key, raw string) (ArtifactSeen, error) {
	var out ArtifactSeen
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ArtifactSeen{}, fmt.Errorf("parse %s: %v", key, err)
	}
	if out == nil {
		out = ArtifactSeen{}
	}
	return out, nil
}

// evictOldestSeen drops the names read longest ago (ties by name, so the
// choice is stable), never the one just marked, until at most
// artifactsSeenMax remain.
func evictOldestSeen(seen ArtifactSeen, keep string) {
	over := len(seen) - artifactsSeenMax
	if over <= 0 {
		return
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		if n != keep {
			names = append(names, n)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := seen[names[i]], seen[names[j]]
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	for _, n := range names[:over] {
		delete(seen, n)
	}
}

// ArtifactSeenIndex reads every job's record in ONE query, keyed by job id,
// for the board build: a payload rebuilt every ~2s must not run one GetKV
// per card. A row that does not parse is skipped and logged; nil store means
// an empty index, under which every artifact with an mtime reads as new.
func ArtifactSeenIndex(st *store.Store) map[int64]ArtifactSeen {
	out := map[int64]ArtifactSeen{}
	if st == nil {
		return out
	}
	rows, err := st.ListKVPrefix(artifactsSeenPrefix)
	if err != nil {
		util.LogEvent("jobs", "error", "artifact-seen-index", err.Error())
		return out
	}
	for key, raw := range rows {
		id, err := strconv.ParseInt(strings.TrimPrefix(key, artifactsSeenPrefix), 10, 64)
		if err != nil || id == 0 {
			continue
		}
		seen, err := parseArtifactSeen(key, raw)
		if err != nil {
			util.LogEvent("jobs", "error", "artifact-seen-index", err.Error())
			continue
		}
		out[id] = seen
	}
	return out
}

// ArtifactIsNew says whether an artifact with the given mtime (unix millis)
// is unread: changed after the operator last opened it, or never opened. An
// artifact with no known mtime (0 — a row imported before mtimes were
// stamped, or a file that could not be stat'd) is never new: a claim the
// server cannot back is noise on the map, not a prompt.
func ArtifactIsNew(seen ArtifactSeen, name string, mtime int64) bool {
	if mtime <= 0 {
		return false
	}
	return mtime > seen[name]
}
