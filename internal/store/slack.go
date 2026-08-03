package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// slack.go — schema v17's slack_inbox and slack_marks tables (schema.go): the
// Slack intake bot's durable memory of every message it has seen, and the
// per-conversation watermarks that keep polling incremental.
//
// The pipeline this backs is AT-LEAST-ONCE by construction, and every verb
// here is written to that contract rather than to a stricter one:
//
//   - Slack's `oldest` is EXCLUSIVE and the mention search's `after:` is only
//     day-grained, so the poller deliberately re-fetches overlapping spans. A
//     message it has already stored is therefore normal traffic, not an error —
//     AddSlackBatch skips it (INSERT OR IGNORE) and says how many were new.
//   - Rows and the watermarks that cover them commit TOGETHER, in one
//     transaction. A pass that dies between the two would otherwise advance a
//     mark past messages it never stored, and nothing would ever fetch them
//     again — the one failure this design cannot recover from.
//   - Delivery is send-then-watermark: the pane bump happens first and
//     MarkSlackDelivered stamps only after it is confirmed, so a crash costs a
//     duplicate bump rather than a silently dropped batch.
//
// The exception is the ACK, which is the one strict verb here: it is iris
// asserting it has acted on specific rows, so an id it names that does not
// exist (or that has already been acked) fails the WHOLE call and applies
// nothing. See AckSlackMessages.
//
// Ownership is split exactly two ways and nothing writes both halves: the
// daemon's poll pass writes rows and marks, the delivery pass writes
// delivered_at, and iris — through the API — writes acked_at + disposition.
// No column here belongs to a job, so nothing in this file touches, or may
// touch, a `jobs` row: an inbox write must never bump a job's rev or
// updated_at, the same rule SetJobLoad and SetPRWatermark follow.

// SlackInboxMsg is one seen Slack message. Deliberately tag-free like every
// other type in this package — the API layer owns its own DTOs and the wire
// shape that goes with them (internal/api).
//
// TS and ThreadTS are Slack's "seconds.micros" timestamps and are STRINGS the
// whole way through, never parsed into a float: the fractional part is what
// distinguishes two messages posted in the same second, and float64 loses it.
// Comparison is core.SlackTSLess' job, which splits on the dot and compares
// the integer halves.
type SlackInboxMsg struct {
	ID          int64
	ChannelID   string
	ChannelName string // filled only where Slack supplies it (search results); empty otherwise
	TS          string
	ThreadTS    string // the parent's ts when this is a thread reply; empty otherwise
	AuthorID    string
	AuthorName  string
	Source      string // 'dm' | 'mpim' | 'mention' — which polling loop found it
	Text        string
	SeenAt      int64 // unix millis: the poller stored it
	DeliveredAt int64 // unix millis: iris' pane was bumped about it; 0 = still pending
	AckedAt     int64 // unix millis: iris acted on it; 0 = still unacked
	Disposition string
}

// ErrSlackAckRefused is what AckSlackMessages wraps when the ids it was handed
// do not all name a still-unacked row — an unknown id or an already-acked one.
// It lives here rather than in errors.go only because this file is the whole
// Slack slice of the store; it is the same idea as ErrThreadIDTaken and exists
// for the same reason: the API layer has to tell a caller mistake (409, with
// the sentence below naming the id) from a real database failure (500) without
// substring-matching an error string.
var ErrSlackAckRefused = errors.New("store: slack ack refused")

// validSlackSource mirrors slack_inbox's CHECK constraint. Validating in Go
// too is the questions.go/threads.go idiom: the caller gets a sentence naming
// the bad value instead of SQLite's bare "CHECK constraint failed", and it
// fails before the transaction opens rather than aborting a whole batch of
// good rows partway through.
var validSlackSource = map[string]bool{"dm": true, "mpim": true, "mention": true}

// slackInboxColumns COALESCEs every scanned column defensively, same
// belt-and-suspenders reasoning as jobColumns/threadColumns: the schema already
// declares all of these NOT NULL with a same-typed default, but a NULL that
// sneaks in anyway (schema drift, a hand-edited row) must degrade to a blank
// field rather than error the whole query.
const slackInboxColumns = `id, COALESCE(channel_id,''), COALESCE(channel_name,''),
	COALESCE(ts,''), COALESCE(thread_ts,''), COALESCE(author_id,''),
	COALESCE(author_name,''), COALESCE(source,''), COALESCE(text,''),
	COALESCE(seen_at,0), COALESCE(delivered_at,0), COALESCE(acked_at,0),
	COALESCE(disposition,'')`

func scanSlackInboxMsg(row interface{ Scan(...any) error }) (*SlackInboxMsg, error) {
	var m SlackInboxMsg
	if err := row.Scan(&m.ID, &m.ChannelID, &m.ChannelName, &m.TS, &m.ThreadTS,
		&m.AuthorID, &m.AuthorName, &m.Source, &m.Text,
		&m.SeenAt, &m.DeliveredAt, &m.AckedAt, &m.Disposition); err != nil {
		return nil, err
	}
	return &m, nil
}

// AddSlackBatch commits one poll pass' result — new messages and the advanced
// watermarks that cover them — in ONE transaction, and returns how many rows
// were actually new.
//
// THE TRANSACTION IS THE POINT, not tidiness. A mark says "everything up to
// here is already stored", so advancing one in a separate statement from the
// insert it describes opens a window where a crash leaves the mark ahead of
// the rows: the next pass asks Slack for messages after the mark, Slack
// answers with the ones it already returned, and the messages in the gap are
// never fetched by anything, ever. Committing both together makes the only
// possible failure a REPEAT — the pass re-fetches a span it had already
// stored — which the UNIQUE(channel_id, ts) dedupe below absorbs silently.
//
// INSERT OR IGNORE rather than a pre-read: a duplicate (channel_id, ts) is
// EXPECTED traffic, because Slack's `oldest` is exclusive and the mention
// search's `after:` is day-grained, so every pass deliberately re-reads an
// overlap. It is not an error and does not abort the batch — the returned
// count is inserted rows, so a pass that found nothing genuinely new returns
// 0 with a nil error, and running the same batch twice leaves the identical
// rows and marks behind.
//
// Marks are SET to what the caller computed, not maxed against what is stored:
// the poller owns the watermark arithmetic (it knows which of the messages it
// fetched it actually committed), and a store-side max would silently paper
// over a caller that computed one wrong.
//
// SeenAt is stamped to now when the caller left it zero, the AddThread /
// AddFeedback convenience. That stamp is invisible on a duplicate, since the
// row is skipped rather than updated — the first sighting's time is the honest
// one to keep.
//
// Empty input is a no-op returning (0, nil) and opens no transaction: a tick
// with nothing new is the common case, and it must not take the write lock.
func (s *Store) AddSlackBatch(msgs []SlackInboxMsg, marks map[string]string) (int, error) {
	if len(msgs) == 0 && len(marks) == 0 {
		return 0, nil
	}
	for i, m := range msgs {
		if !validSlackSource[m.Source] {
			return 0, fmt.Errorf("add slack batch: message %d (%s/%s) has bad source %q: want dm, mpim or mention",
				i, m.ChannelID, m.TS, m.Source)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("add slack batch: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	inserted := 0
	for _, m := range msgs {
		seenAt := m.SeenAt
		if seenAt == 0 {
			seenAt = now
		}
		res, err := tx.Exec(
			`INSERT OR IGNORE INTO slack_inbox
				(channel_id, channel_name, ts, thread_ts, author_id, author_name,
				 source, text, seen_at, delivered_at, acked_at, disposition)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ChannelID, m.ChannelName, m.TS, m.ThreadTS, m.AuthorID, m.AuthorName,
			m.Source, m.Text, seenAt, m.DeliveredAt, m.AckedAt, m.Disposition,
		)
		if err != nil {
			return 0, fmt.Errorf("add slack message %s/%s: %w", m.ChannelID, m.TS, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("add slack message %s/%s: %w", m.ChannelID, m.TS, err)
		}
		inserted += int(n)
	}

	for key, ts := range marks {
		if _, err := tx.Exec(
			`INSERT INTO slack_marks (key, ts) VALUES (?, ?)
				ON CONFLICT(key) DO UPDATE SET ts = excluded.ts`,
			key, ts,
		); err != nil {
			return 0, fmt.Errorf("set slack mark %s: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("add slack batch: %w", err)
	}
	return inserted, nil
}

// listSlackInbox is the shared body behind the two pending-row listings: same
// columns, same id ordering, only the WHERE clause differs.
//
// ORDER BY id rather than by ts or seen_at, for ListThreads' reason: id is
// AUTOINCREMENT, so it is insertion order BY CONSTRUCTION. A ts sort would
// interleave conversations (Slack timestamps are per-channel and the poller
// sweeps channels in rotation), and seen_at is a wall clock that NTP can step
// backwards. Insertion order is also the order iris should read a batch in.
func (s *Store) listSlackInbox(where, label string) ([]SlackInboxMsg, error) {
	rows, err := s.db.Query(`SELECT ` + slackInboxColumns + ` FROM slack_inbox WHERE ` + where + ` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	defer rows.Close()
	var out []SlackInboxMsg
	for rows.Next() {
		m, err := scanSlackInboxMsg(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// UnackedSlackInbox returns every message iris has not yet acted on, oldest
// first. This is what GET /api/slack/inbox serves — deliberately including
// rows already DELIVERED, since a bump iris saw but never got through (its
// pane was busy, it was mid-compaction, the daemon restarted) must still be
// waiting for it on the next read. The ack, not the delivery, is what retires
// a row.
func (s *Store) UnackedSlackInbox() ([]SlackInboxMsg, error) {
	return s.listSlackInbox(`acked_at = 0`, "list unacked slack inbox")
}

// UndeliveredSlackInbox returns the delivery pass' worklist: messages iris has
// neither been bumped about nor acked, oldest first.
//
// The acked_at half of the filter is not redundant with delivered_at. Iris can
// ack a row it was never bumped about — it reads the whole unacked list on
// every wake, so a row that arrived while a bump was refused is handled on the
// next pass through — and bumping it afterwards would announce work that is
// already done.
func (s *Store) UndeliveredSlackInbox() ([]SlackInboxMsg, error) {
	return s.listSlackInbox(`delivered_at = 0 AND acked_at = 0`, "list undelivered slack inbox")
}

// MarkSlackDelivered stamps delivered_at on the named rows and nothing else.
//
// Best-effort BY DESIGN, which is why an unknown id is ignored rather than
// refused: this is bookkeeping about a tmux pane bump that has ALREADY
// happened, so there is no action left to undo and refusing the call would
// only strand the ids that were fine. That is the opposite stance to
// AckSlackMessages, which refuses the whole call — and the difference is who
// is asserting what. Here the daemon is recording something it just did; there
// iris is asserting it handled specific rows, and a wrong id means it handled
// something else.
//
// `at` defaults to now when zero: a stamp of 0 is the column's own "not
// delivered" value, so writing it literally would leave the row on the
// delivery worklist forever and re-bump it every tick. Empty ids is a no-op.
func (s *Store) MarkSlackDelivered(ids []int64, at int64) error {
	if len(ids) == 0 {
		return nil
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("mark slack delivered: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE slack_inbox SET delivered_at = ? WHERE id = ?`, at, id); err != nil {
			return fmt.Errorf("mark slack message %d delivered: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mark slack delivered: %w", err)
	}
	return nil
}

// AckSlackMessages records iris' verdict on a batch: acked_at plus the
// one-line disposition, on every named row, in ONE transaction — and it is
// ALL OR NOTHING. An id that names no row, or that names a row already acked,
// fails the whole call with ErrSlackAckRefused and a sentence naming that id,
// and nothing is applied.
//
// The strictness is the point and it is the opposite of MarkSlackDelivered's
// stance above. An ack is iris ASSERTING it has dealt with specific messages —
// drafted a job from this one, edited a job from that one, ignored the third —
// so a wrong id means it dealt with something other than what it thinks. Under
// a lenient rule the mistake is invisible in both directions at once: the row
// it meant stays unacked and gets re-offered forever, while the row it named
// is retired unread. Refusing the batch surfaces that on the spot, and
// applying nothing means the retry is a plain repeat rather than a
// reconciliation.
//
// The `acked_at = 0` predicate makes each row's update its own atomic
// compare-and-set, so two concurrent acks of one row cannot both succeed — the
// loser is told the row is already acked, exactly as ClaimClosedThread tells a
// losing revive. Which of the two refusals it was is settled by a read-back,
// purely to name the case in the error; the UPDATE has already decided.
//
// Duplicate ids within one call are collapsed rather than refused: the second
// mention names the same row this same call just acked, which is a repeat, not
// the stale claim the already-acked refusal exists to catch.
//
// Empty ids is an error, not a no-op: an ack with nothing to ack is a caller
// bug (a mis-shaped request body), and answering it with success would tell
// iris its batch was recorded when nothing was.
//
// `at` defaults to now when zero, for MarkSlackDelivered's reason — 0 is the
// column's "unacked" value, so storing it literally would leave the row
// unacked while reporting success.
func (s *Store) AckSlackMessages(ids []int64, disposition string, at int64) error {
	if len(ids) == 0 {
		return fmt.Errorf("ack slack messages: no ids given: %w", ErrSlackAckRefused)
	}
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("ack slack messages: %w", err)
	}
	defer tx.Rollback()

	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		res, err := tx.Exec(
			`UPDATE slack_inbox SET acked_at = ?, disposition = ? WHERE id = ? AND acked_at = 0`,
			at, disposition, id,
		)
		if err != nil {
			return fmt.Errorf("ack slack message %d: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("ack slack message %d: %w", id, err)
		}
		if n == 1 {
			continue
		}
		// Zero rows means the id is unknown OR the row is already acked. The
		// read-back only picks which sentence to write — this caller has lost
		// either way, and the whole transaction is about to roll back.
		var acked int64
		qerr := tx.QueryRow(`SELECT COALESCE(acked_at,0) FROM slack_inbox WHERE id = ?`, id).Scan(&acked)
		if errors.Is(qerr, sql.ErrNoRows) {
			return fmt.Errorf("ack slack message %d: no such message: %w", id, ErrSlackAckRefused)
		}
		if qerr != nil {
			return fmt.Errorf("ack slack message %d: %w", id, qerr)
		}
		return fmt.Errorf("ack slack message %d: already acked at %d: %w", id, acked, ErrSlackAckRefused)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ack slack messages: %w", err)
	}
	return nil
}

// SlackMarks returns every watermark as key -> ts. A database with none yields
// an empty (non-nil) map, never an error: "nothing polled yet" is a normal
// state, and the empty map makes every conversation's first sweep run with an
// empty `oldest`, which is what a first poll should do.
func (s *Store) SlackMarks() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, COALESCE(ts,'') FROM slack_marks`)
	if err != nil {
		return nil, fmt.Errorf("list slack marks: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, ts string
		if err := rows.Scan(&key, &ts); err != nil {
			return nil, fmt.Errorf("list slack marks: %w", err)
		}
		out[key] = ts
	}
	return out, rows.Err()
}

// SlackInboxCounts returns the three numbers GET /api/slack/status reports:
// every row ever seen, the ones still waiting to be delivered, and the ones
// still unacked. undelivered uses the delivery pass' exact filter and unacked
// the inbox route's, so the status line and those two worklists can never
// disagree about how much is outstanding.
//
// One query rather than three: they are three views of one table and the
// status route asks for all of them together. SUM over an empty table is NULL,
// hence the COALESCE — a fresh database reports three honest zeros rather than
// erroring the scan.
func (s *Store) SlackInboxCounts() (total, undelivered, unacked int, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN delivered_at = 0 AND acked_at = 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN acked_at = 0 THEN 1 ELSE 0 END), 0)
		FROM slack_inbox`).Scan(&total, &undelivered, &unacked)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("slack inbox counts: %w", err)
	}
	return total, undelivered, unacked, nil
}
