package jobs

import (
	"fmt"

	"kraken/internal/store"
)

// jobload.go — the jobs-level writer for a job's triage estimate (zeus'
// unblock/scope pair, store.JobLoad). One function, because there is exactly
// one thing to do: hand it to the store and mirror it into the in-memory Job
// the caller is holding.

// SetJobLoad writes j's estimate and updates j.Load on success.
//
// It deliberately does NOT go through WriteJobFields, the way every other
// kraken-owned field does. WriteJobFields' store path is UpdateJobFields,
// which bumps `rev` and stamps `updated_at` on every call — and zeus rewrites
// this estimate at the end of every turn, so routing it there would reset
// every blocked card's age on the board (updated_at is what the card renders
// as its age) and 409 an open editor save (rev is its optimistic lock) over a
// number the editor neither shows nor sends. store.SetJobLoad exists to skip
// both, exactly as SetPRWatermark does for the daemon's PR bookkeeping.
//
// Store-backed ONLY, and the refusal says so rather than degrading: there is
// no `load:` frontmatter field and there is deliberately not going to be one
// — the estimate is bookkeeping about the job, not part of the authored spec
// — so a file-mode job has nowhere to put it, and a silent no-op would leave
// a caller (a CLI verb whose whole output is a confirmation line) reporting a
// write that never happened.
func SetJobLoad(j *Job, l store.JobLoad) error {
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("set load for %s: no store is wired — load estimates are store-backed only (there is no `load:` job-file field)", j.Slug)
	}
	if err := st.SetJobLoad(j.ID, l); err != nil {
		return fmt.Errorf("set load for %s: %w", j.Slug, err)
	}
	j.Load = l
	return nil
}
