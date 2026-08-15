package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// orch.go — schema v24's orch_inbox table (schema.go): kronos' durable inbox.
//
// This is slack.go's shape, one layer up. Where slack_inbox holds messages a
// human wrote and iris triages, orch_inbox holds TRANSITIONS the daemon already
// noticed — a job went blocked, a cell died, a PR merged — and the row moves
// through the same three stamps: seen (enqueued), delivered (kronos' pane was
// bumped with a count), acked (kronos said what it did). Reusing that
// lifecycle rather than inventing one is the point: the delivery pass, the
// inbox route and the ack route are then the same three shapes iris' already
// are, and a reader who knows one knows the other.
//
// Two verbs part company with slack's, and both are deliberate:
//
//   - MarkOrchDelivered REFUSES an unknown id where MarkSlackDelivered ignores
//     one. The stance is the contract's, and it fits what this table is: an
//     orch row is minted by kraken itself at a transition it can name, so an id
//     nobody can find is a caller bug rather than the ordinary raciness of an
//     external feed. Re-stamping an ALREADY-delivered row stays legal, because
//     a bump whose stamp failed must be able to run again.
//   - AddOrchItem never dedupes. The daemon's existing Notified-key pattern is
//     what makes a persisting condition enqueue once per streak, and it decides
//     that with knowledge this layer does not have (which streak a condition is
//     in); a UNIQUE constraint here could only collapse rows that are honestly
//     distinct — the same job blocking twice a day is two things to tell kronos
//     about.
//
// Ownership splits exactly as slack's does and nothing writes both halves: the
// daemon writes rows (EnqueueOrch) and delivered_at, kronos — through the API —
// writes acked_at + disposition. No column here belongs to a job, so nothing in
// this file touches, or may touch, a `jobs` row: an inbox write must never bump
// a job's rev or updated_at, the rule SetJobLoad and SetPRWatermark follow.

// OrchItem is one row of kronos' inbox. Tag-free like every other type in this
// package — the API layer owns its own DTOs and the wire shape that goes with
// them, which is what lets it withhold the delivery/ack columns from the
// inbox route while this type keeps carrying them.
type OrchItem struct {
	ID          int64
	Kind        string // which daemon transition raised it: job-blocked, cell-gone, …
	Slug        string // the job it is about; empty for a row belonging to no job
	Summary     string
	SeenAt      int64 // unix millis: the daemon enqueued it
	DeliveredAt int64 // unix millis: kronos' pane was bumped about it; 0 = still pending
	AckedAt     int64 // unix millis: kronos acted on it; 0 = still unacked
	Disposition string
}

// ErrOrchAckRefused is what AckOrchItems wraps when the ids it was handed do
// not all name a still-unacked row — an unknown id or an already-acked one.
// Same idea as ErrSlackAckRefused, and it exists for the same reason: the API
// layer has to tell a caller mistake (409, with the sentence below naming the
// id) from a real database failure without substring-matching an error string.
//
// It wraps ONLY the two state refusals, deliberately not the empty-ids and
// empty-disposition ones. Those two are malformed REQUESTS rather than a
// conflict with the rows' current state, and the route answers them 400 while
// answering this sentinel 409 — so keeping them out of it leaves the sentinel
// meaning exactly one thing.
var ErrOrchAckRefused = errors.New("store: orch ack refused")

// orchInboxColumns COALESCEs every scanned column defensively, the
// slackInboxColumns/jobColumns idiom: the schema already declares all of these
// NOT NULL with a same-typed default, but a NULL that sneaks in anyway (schema
// drift, a hand-edited row) must degrade to a blank field rather than error the
// whole query.
const orchInboxColumns = `id, COALESCE(kind,''), COALESCE(slug,''), COALESCE(summary,''),
	COALESCE(seen_at,0), COALESCE(delivered_at,0), COALESCE(acked_at,0),
	COALESCE(disposition,'')`

func scanOrchItem(row interface{ Scan(...any) error }) (*OrchItem, error) {
	var it OrchItem
	if err := row.Scan(&it.ID, &it.Kind, &it.Slug, &it.Summary,
		&it.SeenAt, &it.DeliveredAt, &it.AckedAt, &it.Disposition); err != nil {
		return nil, err
	}
	return &it, nil
}

// AddOrchItem records one thing kronos should know about, stamped seen at now,
// and returns the new row's id.
//
// An empty kind is refused because kind is the only column that says WHAT
// happened: slug can be legitimately empty (a row may belong to no job) and
// so can summary, but a row nobody can
// classify is one the inbox can only render as noise. Whitespace-only counts as
// empty, MarkRecalled's rule — a kind of " " would pass a bare != "" check and
// then read as blank everywhere it is shown.
//
// `now` is stored EXACTLY as given, including zero, which is the one place this
// file does not take slack.go's default-to-now convenience. seen_at is not a
// sentinel for anything — no query filters on it, unlike delivered_at and
// acked_at, whose zero MEANS "not yet" — so there is nothing for a default to
// protect, and the caller's stamp is the honest record of when the daemon tick
// that noticed the transition ran.
func (s *Store) AddOrchItem(kind, slug, summary string, now int64) (int64, error) {
	if strings.TrimSpace(kind) == "" {
		return 0, fmt.Errorf("add orch item: empty kind")
	}
	res, err := s.db.Exec(
		`INSERT INTO orch_inbox (kind, slug, summary, seen_at, delivered_at, acked_at, disposition)
			VALUES (?, ?, ?, ?, 0, 0, '')`,
		kind, slug, summary, now,
	)
	if err != nil {
		return 0, fmt.Errorf("add orch item %s/%s: %w", kind, slug, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("add orch item %s/%s: %w", kind, slug, err)
	}
	return id, nil
}

// listOrch is the shared body behind the two pending-row listings: same
// columns, same id ordering, only the WHERE clause differs.
//
// ORDER BY id rather than by seen_at, listSlackInbox's reason: id is
// AUTOINCREMENT, so it is insertion order BY CONSTRUCTION, while seen_at is a
// wall clock NTP can step backwards. Insertion order is also the order kronos
// should read a batch in.
func (s *Store) listOrch(where, label string) ([]OrchItem, error) {
	rows, err := s.db.Query(`SELECT ` + orchInboxColumns + ` FROM orch_inbox WHERE ` + where + ` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	defer rows.Close()
	var out []OrchItem
	for rows.Next() {
		it, err := scanOrchItem(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

// UndeliveredOrch returns the delivery pass' worklist: rows kronos has neither
// been bumped about nor acked, oldest first.
//
// The acked_at half of the filter is not redundant with delivered_at, for
// UndeliveredSlackInbox's reason: kronos reads the whole unacked list on every
// wake, so it can ack a row it was never bumped about, and bumping it
// afterwards would announce work that is already done.
func (s *Store) UndeliveredOrch() ([]OrchItem, error) {
	return s.listOrch(`delivered_at = 0 AND acked_at = 0`, "list undelivered orch inbox")
}

// UnackedOrch returns everything kronos has not yet acted on, oldest first,
// DELIVERED OR NOT — which is the whole reason it is a second query rather than
// the one above. A bump can fail (the pane was busy, the daemon restarted
// between send and stamp) and a row must never become unreachable because of
// it: the ack, not the delivery, is what retires a row. This is what
// GET /api/orch/inbox serves.
func (s *Store) UnackedOrch() ([]OrchItem, error) {
	return s.listOrch(`acked_at = 0`, "list unacked orch inbox")
}

// MarkOrchDelivered stamps delivered_at on exactly the named rows and nothing
// else, in one transaction.
//
// An unknown id fails the WHOLE call and applies nothing — the opposite of
// MarkSlackDelivered, which ignores one, and the difference is who minted the
// id. A Slack row's id comes back from an external feed the poller re-reads;
// an orch row's is minted by kraken at a transition it named itself, so an id
// that resolves to nothing means the caller is stamping something other than
// what it delivered, and stamping the rest would hide that in both directions
// at once.
//
// An ALREADY-delivered row is not a refusal: this call is bookkeeping about a
// bump that just happened, and a bump whose stamp failed the first time has to
// be able to run again. The row simply takes the newer stamp.
//
// A zero RowsAffected is checked against a read-back rather than trusted to
// mean "no such row": re-stamping a row with the value it already holds is
// legal here, and this way the unknown-id refusal never depends on how SQLite
// counts an update that changed no bytes.
//
// `at` defaults to now when zero, MarkSlackDelivered's rule: 0 is the column's
// own "not delivered" value, so writing it literally would leave the row on the
// delivery worklist forever and re-bump it every tick. Empty ids is a no-op —
// a tick with nothing to stamp is the common case and must not take the write
// lock.
func (s *Store) MarkOrchDelivered(ids []int64, at int64) error {
	if len(ids) == 0 {
		return nil
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("mark orch delivered: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		res, err := tx.Exec(`UPDATE orch_inbox SET delivered_at = ? WHERE id = ?`, at, id)
		if err != nil {
			return fmt.Errorf("mark orch item %d delivered: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("mark orch item %d delivered: %w", id, err)
		}
		if n > 0 {
			continue
		}
		var exists int64
		qerr := tx.QueryRow(`SELECT 1 FROM orch_inbox WHERE id = ?`, id).Scan(&exists)
		if errors.Is(qerr, sql.ErrNoRows) {
			return fmt.Errorf("mark orch item %d delivered: no such item", id)
		}
		if qerr != nil {
			return fmt.Errorf("mark orch item %d delivered: %w", id, qerr)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mark orch delivered: %w", err)
	}
	return nil
}

// AckOrchItems records kronos' verdict on a batch: acked_at plus the one-line
// disposition, on every named row, in ONE transaction — and it is ALL OR
// NOTHING. An id that names no row, or that names a row already acked, fails
// the whole call with ErrOrchAckRefused and a sentence naming that id, and
// nothing is applied.
//
// The strictness is AckSlackMessages' and it is here for the same reason. An
// ack is kronos ASSERTING it has dealt with specific rows — steered this cell,
// answered that question, ignored the third — so a wrong id means it dealt with
// something other than what it thinks. Under a lenient rule the mistake is
// invisible in both directions: the row it meant stays unacked and is re-offered
// forever, while the row it named is retired unread. Applying nothing means the
// retry is a plain repeat rather than a reconciliation.
//
// The `acked_at = 0` predicate makes each row's update its own atomic
// compare-and-set, so two concurrent acks of one row cannot both succeed — the
// loser is told the row is already acked. Which of the two refusals it was is
// settled by a read-back purely to name the case in the error; the UPDATE has
// already decided.
//
// Duplicate ids within one call are collapsed rather than refused: the second
// mention names the row this same call just acked, which is a repeat, not the
// stale claim the already-acked refusal exists to catch.
//
// Empty ids and an empty disposition are both errors, not defaults. An ack with
// nothing to ack would report success for a batch that recorded nothing, and a
// blank disposition is "acked, reason unknown" — indistinguishable from a bug
// that acks everything, which is the one failure the disposition column exists
// to make visible. Neither wraps ErrOrchAckRefused; see that var's comment.
//
// `at` defaults to now when zero, for MarkOrchDelivered's reason — 0 is the
// column's "unacked" value, so storing it literally would leave the row unacked
// while reporting success.
func (s *Store) AckOrchItems(ids []int64, disposition string, at int64) error {
	if len(ids) == 0 {
		return fmt.Errorf("ack orch items: no ids given")
	}
	if strings.TrimSpace(disposition) == "" {
		return fmt.Errorf("ack orch items: empty disposition")
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("ack orch items: %w", err)
	}
	defer tx.Rollback()

	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		res, err := tx.Exec(
			`UPDATE orch_inbox SET acked_at = ?, disposition = ? WHERE id = ? AND acked_at = 0`,
			at, disposition, id,
		)
		if err != nil {
			return fmt.Errorf("ack orch item %d: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("ack orch item %d: %w", id, err)
		}
		if n == 1 {
			continue
		}
		// Zero rows means the id is unknown OR the row is already acked. The
		// read-back only picks which sentence to write — this caller has lost
		// either way, and the whole transaction is about to roll back.
		var acked int64
		qerr := tx.QueryRow(`SELECT COALESCE(acked_at,0) FROM orch_inbox WHERE id = ?`, id).Scan(&acked)
		if errors.Is(qerr, sql.ErrNoRows) {
			return fmt.Errorf("ack orch item %d: no such item: %w", id, ErrOrchAckRefused)
		}
		if qerr != nil {
			return fmt.Errorf("ack orch item %d: %w", id, qerr)
		}
		return fmt.Errorf("ack orch item %d: already acked at %d: %w", id, acked, ErrOrchAckRefused)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ack orch items: %w", err)
	}
	return nil
}
