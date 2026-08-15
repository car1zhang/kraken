package store

import (
	"fmt"
	"strings"
)

// recall.go — the kg_recall table (schemaV19): which graph facts have already
// been injected into which claude conversation.
//
// This is the durable half of "a memory is injected at most once per session".
// The recall hook retrieves facts on every user prompt, so without a record of
// what it already said, the same fact would be re-injected on every turn it
// stays relevant for — which is every turn, since relevance is computed against
// the same conversation that fact is now part of. The record is a TABLE rather
// than a process-local set for the reason schemaV19's doc comment states: it
// has to survive `kraken serve` restarting mid-conversation.
//
// Three accessors, and they are the whole surface: RecalledEdgeIDs is the skip
// set a retrieval subtracts, MarkRecalled is the commit, ClearRecalled is the
// one erasure. Nothing UPDATES a row — a delivery is a fact about the past, and
// the only thing that can stop it being true is the context it was delivered
// INTO going away, which is what ClearRecalled is called for.
//
// The session id is treated as an OPAQUE key: it is stored and matched exactly
// as the caller gave it, and the only inspection either function makes is
// whether it is blank (see MarkRecalled). Normalising it in one function and
// not the other would silently split the dedup — a mark under a trimmed key
// that a read under the raw key never sees — so neither does.

// RecalledEdgeIDs returns the set of edge ids already injected into sessionID,
// as a NON-NIL map so callers can index it without a nil check (an empty skip
// set and "no dedup knowledge" must not look alike at the call site — the
// caller's failure path is to inject nothing, and it keys that off the error).
//
// An unknown session — including the empty string — is not an error: nothing
// was ever marked for it, which is exactly an empty set.
func (s *Store) RecalledEdgeIDs(sessionID string) (map[int64]bool, error) {
	out := map[int64]bool{}
	if sessionID == "" {
		// Short-circuit rather than query: no row can carry an empty key,
		// since MarkRecalled refuses to write one.
		return out, nil
	}
	rows, err := s.db.Query(`SELECT edge_id FROM kg_recall WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("recalled edge ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("recalled edge ids scan: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recalled edge ids: %w", err)
	}
	return out, nil
}

// MarkRecalled records that every edge in edgeIDs has now been injected into
// sessionID, stamped at ts (unix millis).
//
// A blank session id is an ERROR rather than a silent no-op, and that asymmetry
// with RecalledEdgeIDs is deliberate: a read of an unknown session is a real answer
// ("nothing yet"), while a write with no key to file it under is a delivery that
// can never be deduplicated — the caller's cue to inject nothing rather than to
// inject something it will repeat forever. Whitespace-only counts as blank; the
// key is otherwise stored verbatim, per this file's header.
//
// An empty id list is nothing to record, not a failure — a retrieval that kept
// no facts simply has nothing to commit.
//
// INSERT OR IGNORE against the (session_id, edge_id) primary key makes a repeat
// mark a skip: the row keeps its ORIGINAL ts, so the stamp says when a fact was
// first delivered rather than when it was last considered. All ids land in one
// transaction, so a caller that fails partway through can never leave a session
// believing it was told half a block it never saw.
func (s *Store) MarkRecalled(sessionID string, edgeIDs []int64, ts int64) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("mark recalled: empty session id")
	}
	if len(edgeIDs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("mark recalled: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO kg_recall (session_id, edge_id, ts) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("mark recalled prepare: %w", err)
	}
	defer stmt.Close()
	for _, id := range edgeIDs {
		if _, err := stmt.Exec(sessionID, id, ts); err != nil {
			return fmt.Errorf("mark recalled edge %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// ClearRecalled deletes every recall row for sessionID and returns how many
// went, re-arming that session to be told its facts again.
//
// It is the only deletion in this file, and the exception is narrow enough to
// state exactly: a recall row means "this fact is already in this
// conversation's context", and a COMPACTION replaces that context with a
// summary — so after one, the premise the row rests on is no longer true and
// the session's best facts would stay suppressed forever. Clearing the rows is
// what puts them back in play; nothing else may call this, and no pass ages a
// row out on its own.
//
// The whole session goes at once rather than row by row. Compaction is not
// selective — it does not keep some injected facts and drop others — so
// deleting a subset would encode a distinction the event does not make.
//
// A blank session id is an ERROR that deletes nothing, MarkRecalled's rule and
// for a sharper version of its reason: the write it guards there is a row that
// cannot be deduplicated, while here an unguarded blank key would match on the
// empty string and answer zero to a caller that thinks it cleared its session's
// rows — a re-arm that silently did nothing. Rows of every OTHER session are
// untouched in both cases — the delete is keyed, never a sweep.
// Whitespace-only counts as blank, since MarkRecalled stores keys verbatim
// and refuses those too, so no row can carry one.
//
// A session with no rows is not an error: it returns 0, which is the honest
// answer to "how many deliveries did this discard" for a conversation that was
// never told anything.
func (s *Store) ClearRecalled(sessionID string) (int64, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, fmt.Errorf("clear recalled: empty session id")
	}
	res, err := s.db.Exec(`DELETE FROM kg_recall WHERE session_id = ?`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("clear recalled: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("clear recalled: %w", err)
	}
	return n, nil
}
