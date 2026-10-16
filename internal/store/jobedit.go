package store

import "fmt"

// jobedit.go — SaveJobEdit (L10): the editor's whole "save" operation
// (api/jobedit.go's webJobSave/webJobSaveForm) collapsed into ONE
// transaction. Before this, a single editor save spanned MULTIPLE separate
// transactions — UpdateJobFields, then one UpsertPR/RemovePR call per PR
// named in the content — each committing on its own. A failure partway
// through (a slug collision on the field update, or a constraint error on the
// very last PR upsert) could leave the row's frontmatter fields updated while
// its PRs stayed stale — a partial-save state no caller intended but was
// reachable on disk. SaveJobEdit runs the field update and an optional PR
// sync inside one BEGIN/COMMIT, using the same
// updateJobFieldsTx/listPRsTx/upsertPRTx/removePRTx building blocks their
// standalone single-purpose methods (UpdateJobFields, UpsertPR, RemovePR)
// already use — one implementation of each either way.

// SaveJobEdit atomically applies id's full editor save. fields/rev follow
// UpdateJobFields' exact contract (rev-gated optimistic lock, ErrStaleRev
// on mismatch, ErrSlugTaken on a slug collision). prs is synced (every URL
// upserted, any existing PR row not named removed) only when syncPRs is true
// — false for an unparseable raw save (only body/err change, PRs are left
// exactly as they were, since a garbled parse's own PRs field is
// untrustworthy) and for a form save (which can't
// touch PRs by construction); true for a clean-parsing raw save.
func (s *Store) SaveJobEdit(id, rev int64, fields map[string]any, prs []PR, syncPRs bool) (newRev int64, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("save job edit %d: %w", id, err)
	}
	defer tx.Rollback()

	if _, err = updateJobFieldsTx(tx, id, rev, fields); err != nil {
		return 0, err
	}
	if syncPRs {
		existing, err := listPRsTx(tx, id)
		if err != nil {
			return 0, err
		}
		keep := make(map[string]bool, len(prs))
		for _, p := range prs {
			keep[p.URL] = true
			if err := upsertPRTx(tx, id, p.URL, p.State); err != nil {
				return 0, err
			}
		}
		for _, p := range existing {
			if !keep[p.URL] {
				if err := removePRTx(tx, id, p.URL); err != nil {
					return 0, err
				}
			}
		}
	}
	// The field update's own return value isn't necessarily the row's
	// FINAL rev: a PR upsert/remove above bumps rev again (L2) whenever it
	// actually changes something, so re-query the committed value rather
	// than return a stale one that would make the editor's NEXT save think
	// it's working against an already-outdated rev.
	if err := tx.QueryRow(`SELECT rev FROM jobs WHERE id = ?`, id).Scan(&newRev); err != nil {
		return 0, fmt.Errorf("save job edit %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("save job edit %d: %w", id, err)
	}
	return newRev, nil
}
