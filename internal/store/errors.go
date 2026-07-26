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

// ErrThreadIDTaken is returned by AddThread when the (job_id, tid) pair it
// was handed already exists — the structurally identical case to
// ErrSlugTaken, and errors.Is-comparable for the same reason: the API layer
// has to tell a recoverable collision (409, renumber and retry) apart from a
// real database failure (500) without substring-matching an error string.
// AddThreadNext allocates inside a transaction and so can never raise it.
var ErrThreadIDTaken = errors.New("store: thread id taken")

// ErrThreadNotClosed is returned by ClaimClosedThread when the row it was
// handed exists but is already live — i.e. this caller LOST the race to
// revive it, and another one is already spawning (or has spawned) the claude
// that resumes that session. It is a distinct sentinel from ErrNotFound
// because the two demand opposite reactions: an unknown tid is a 404 and a
// caller bug, while a lost claim is the compare-and-set working exactly as
// designed — the loser must quietly spawn NOTHING, since a second
// `claude -r <same session id>` would put two writers on one transcript.
var ErrThreadNotClosed = errors.New("store: thread is not closed")

// ErrNotFound is returned by single-row lookups (GetBySlug, GetByID, ...)
// when no row matches.
var ErrNotFound = errors.New("store: not found")

// ErrInvalidExt is returned by SaveUpload when ext doesn't match the
// expected shape (letters/digits only) — a shape check, not an allowlist,
// same idiom as jobs.modelTokenRe.
var ErrInvalidExt = errors.New("store: invalid upload extension")
