package store

import "errors"

// ErrStaleRev is returned by any rev-checked update (UpdateJobFields today)
// when the WHERE id=? AND rev=? clause matches zero rows: either the row
// doesn't exist, or someone else updated it first. Callers compare with
// errors.Is; the board/API layer turns this into a 409 the same way the
// old mtime check did.
var ErrStaleRev = errors.New("store: stale rev")

// ErrSlugTaken is returned by UnarchiveJob when an active job already
// holds the slug being restored.
var ErrSlugTaken = errors.New("store: slug taken by an active job")

// ErrNotFound is returned by single-row lookups (GetBySlug, GetByID, ...)
// when no row matches.
var ErrNotFound = errors.New("store: not found")

// ErrInvalidExt is returned by SaveUpload when ext doesn't match the
// expected shape (letters/digits only) — a shape check, not an allowlist,
// same idiom as jobs.modelTokenRe.
var ErrInvalidExt = errors.New("store: invalid upload extension")
