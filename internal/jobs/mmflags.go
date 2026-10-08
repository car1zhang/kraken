package jobs

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// mmflags.go — contradiction flags: an agent noticing that a mental-model item
// disagrees with another item, or with what it is looking at, says so with
// `kraken mm flag`, and the job's LEAD decides whether carl hears about it.
//
// Three states. A flag from any session but the lead's is created PENDING and
// reaches zeus through the recall notes block (recallnotes.go) or a
// teammate's report; zeus verifies it and RAISES it (carl is notified, the
// board shows it with drop/keep) or DISMISSES it. A flag from the lead itself
// is raised at once — the lead is the one who would have raised it. Carl's
// drop/keep is the resolution, and drop invalidates the flagged item.
//
// The lead gate exists so carl's attention is spent by one agent's judgment
// rather than by every teammate's first impression: the only session that may
// raise or dismiss a job's flag is the one stamped on the job as zeus' (a
// job-less flag, JobID 0, has no lead and accepts any non-empty session).

// The flag refusals the API layer maps: ErrFlagInvalid → 400, ErrFlagNoItem
// (a named item, or the flag itself, does not exist) → 404, ErrFlagNoJob → 404,
// ErrNotLead → 403, ErrFlagState (the flag is not in the state the verb
// needs) → 409.
var (
	ErrFlagInvalid = errors.New("invalid mm flag")
	ErrFlagNoItem  = errors.New("no such mm item or flag")
	ErrFlagNoJob   = errors.New("no such job")
	ErrNotLead     = errors.New("not the lead session")
	ErrFlagState   = errors.New("mm flag is not in a state that allows this")
)

// MMFlagRequest is `kraken mm flag`'s body (the session rides beside it).
type MMFlagRequest struct {
	Slug      string `json:"slug"`
	ItemID    int64  `json:"item_id,omitempty"`
	AgainstID int64  `json:"against_id,omitempty"`
	Note      string `json:"note"`
}

// FlagMMItem records one flag. The note is required; named items must exist;
// the job is resolved by slug (no slug is accepted as JobID 0 — a
// contradiction noticed outside any job). Created raised when session is the
// job's stamped lead session, pending otherwise.
func FlagMMItem(st *store.Store, req MMFlagRequest, session string) (store.MMFlag, error) {
	if st == nil {
		return store.MMFlag{}, errors.New("no store")
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		return store.MMFlag{}, fmt.Errorf("%w: a flag needs a note", ErrFlagInvalid)
	}
	for _, id := range []int64{req.ItemID, req.AgainstID} {
		if id == 0 {
			continue
		}
		if _, err := st.GetMMItem(id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return store.MMFlag{}, fmt.Errorf("%w: mm item %d", ErrFlagNoItem, id)
			}
			return store.MMFlag{}, err
		}
	}
	var job *store.Job
	if slug := strings.TrimSpace(req.Slug); slug != "" {
		sr, err := st.GetJobBySlug(slug)
		if errors.Is(err, store.ErrNotFound) || (err == nil && sr == nil) {
			return store.MMFlag{}, fmt.Errorf("%w: %s", ErrFlagNoJob, slug)
		}
		if err != nil {
			return store.MMFlag{}, err
		}
		job = sr
	}
	// session is compared EXACTLY, never trimmed (recallNotesTarget's rule): a
	// near-miss identity is not the lead
	now := time.Now().UnixMilli()
	f := store.MMFlag{
		ItemID:    req.ItemID,
		AgainstID: req.AgainstID,
		Session:   session,
		Note:      note,
		State:     "pending",
		CreatedAt: now,
	}
	if job != nil {
		f.JobID, f.Slug = job.ID, job.Slug
		if session != "" && session == job.Session {
			f.State, f.RaisedBy, f.RaisedAt = "raised", session, now
		}
	}
	id, err := st.AddMMFlag(f)
	if err != nil {
		return store.MMFlag{}, err
	}
	f.ID = id
	if got, err := st.GetMMFlag(id); err == nil {
		f = got
	}
	dlogEvent("info", "mm-flag", "mm flag #%d (%s) on %s: %s", f.ID, f.State, mmFlagItems(f), core.TruncateOneLine(f.Note, 160))
	if f.State == "raised" {
		mmFlagRaisedSignal(f)
	}
	return f, nil
}

// RaiseMMFlag moves a pending flag to raised — the lead's verdict that carl
// should see it — and notifies carl once.
func RaiseMMFlag(st *store.Store, id int64, session, note string) error {
	f, err := mmPendingFlagForLead(st, id, session)
	if err != nil {
		return err
	}
	if err := st.RaiseMMFlag(id, time.Now().UnixMilli(), session, strings.TrimSpace(note)); err != nil {
		return err
	}
	if got, err := st.GetMMFlag(id); err == nil {
		f = got
	}
	mmFlagRaisedSignal(f)
	return nil
}

// DismissMMFlag moves a pending flag to dismissed — the lead's verdict that it
// was not a real contradiction. Carl is not told.
func DismissMMFlag(st *store.Store, id int64, session, note string) error {
	f, err := mmPendingFlagForLead(st, id, session)
	if err != nil {
		return err
	}
	if err := st.DismissMMFlag(id, time.Now().UnixMilli(), session, strings.TrimSpace(note)); err != nil {
		return err
	}
	dlogEvent("info", "mm-flag-dismissed", "mm flag #%d on %s dismissed: %s", f.ID, mmFlagItems(f), core.TruncateOneLine(note, 160))
	return nil
}

// ResolveMMFlag is carl's verdict on a raised flag: keep (the item stands) or
// drop (the item is invalidated as contradicted). No session — this is the
// board's verb, not an agent's.
func ResolveMMFlag(st *store.Store, id int64, resolution string) error {
	if st == nil {
		return errors.New("no store")
	}
	if resolution != "drop" && resolution != "keep" {
		return fmt.Errorf("%w: resolution %q: want drop or keep", ErrFlagInvalid, resolution)
	}
	f, err := mmGetFlag(st, id)
	if err != nil {
		return err
	}
	if f.State != "raised" || f.Resolution != "" {
		return fmt.Errorf("%w: flag %d is %s%s", ErrFlagState, id, f.State, mmResolvedSuffix(f))
	}
	at := time.Now().UnixMilli()
	if err := st.ResolveMMFlag(id, at, resolution); err != nil {
		return err
	}
	if resolution == "drop" && f.ItemID != 0 {
		it, err := st.GetMMItem(f.ItemID)
		if err == nil && it.InvalidatedAt == 0 {
			detail := fmt.Sprintf("flag #%d: %s", f.ID, f.Note)
			if err := st.InvalidateMMItem(it.ID, at, "contradicted", detail); err != nil {
				return fmt.Errorf("flag %d resolved but invalidating item %d failed: %w", id, it.ID, err)
			}
			dlogEvent("info", "mm-invalidated", "mm item %d (%s/%s) invalidated: contradicted — %s", it.ID, it.Kind, it.Source, detail)
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("flag %d resolved but reading item %d failed: %w", id, f.ItemID, err)
		}
	}
	dlogEvent("info", "mm-flag-resolved", "mm flag #%d on %s resolved: %s", f.ID, mmFlagItems(f), resolution)
	return nil
}

// mmPendingFlagForLead is raise/dismiss's shared gate: the flag exists, the
// session is its job's lead (any non-empty session for a job-less flag), and
// it is still pending.
func mmPendingFlagForLead(st *store.Store, id int64, session string) (store.MMFlag, error) {
	if st == nil {
		return store.MMFlag{}, errors.New("no store")
	}
	f, err := mmGetFlag(st, id)
	if err != nil {
		return store.MMFlag{}, err
	}
	// exact equality, no trimming of either side — FlagMMItem's rule
	if session == "" {
		return store.MMFlag{}, ErrNotLead
	}
	if f.JobID != 0 {
		sr, err := st.GetJobByID(f.JobID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return store.MMFlag{}, err
		}
		if sr == nil || err != nil || sr.Session == "" || sr.Session != session {
			return store.MMFlag{}, ErrNotLead
		}
	}
	if f.State != "pending" {
		return store.MMFlag{}, fmt.Errorf("%w: flag %d is already %s", ErrFlagState, id, f.State)
	}
	return f, nil
}

// mmGetFlag reads one flag, mapping an unknown id to ErrFlagNoItem.
func mmGetFlag(st *store.Store, id int64) (store.MMFlag, error) {
	f, err := st.GetMMFlag(id)
	if errors.Is(err, store.ErrNotFound) {
		return store.MMFlag{}, fmt.Errorf("%w: flag %d", ErrFlagNoItem, id)
	}
	return f, err
}

// mmFlagRaisedSignal is what a raise costs carl: one notification and the
// mm-flag-raised event.
func mmFlagRaisedSignal(f store.MMFlag) {
	dlogEvent("info", "mm-flag-raised", "mm flag #%d on %s raised by %s: %s", f.ID, mmFlagItems(f), f.RaisedBy, core.TruncateOneLine(f.Note, 160))
	where := f.Slug
	if where == "" {
		where = "mental model"
	}
	// through the notifyFn seam (rotation.go), never notify directly, so a
	// test can count the one notification instead of firing a real one
	notifyFn("kraken — memory contradiction", fmt.Sprintf("%s: %s — %s", where, mmFlagItems(f), core.TruncateOneLine(f.Note, 120)))
}

// mmFlagItems names what a flag is about, for events and notifications.
func mmFlagItems(f store.MMFlag) string {
	switch {
	case f.ItemID != 0 && f.AgainstID != 0:
		return fmt.Sprintf("mm #%d vs #%d", f.ItemID, f.AgainstID)
	case f.ItemID != 0:
		return fmt.Sprintf("mm #%d", f.ItemID)
	case f.AgainstID != 0:
		return fmt.Sprintf("against mm #%d", f.AgainstID)
	}
	return "no item"
}

func mmResolvedSuffix(f store.MMFlag) string {
	if f.Resolution != "" {
		return " and already resolved " + f.Resolution
	}
	return ""
}
