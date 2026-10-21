package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// GetKV fetches key's value, or ErrNotFound if unset. Intended for small
// singleton state (e.g. a usage snapshot) that doesn't warrant its own
// table.
func (s *Store) GetKV(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT COALESCE(value,'') FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get kv %s: %w", key, err)
	}
	return v, nil
}

// SetKV upserts key's value.
func (s *Store) SetKV(key, value string) error {
	if _, err := s.db.Exec(
		`INSERT INTO kv (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	); err != nil {
		return fmt.Errorf("set kv %s: %w", key, err)
	}
	return nil
}

// DeleteKV removes key. Deleting a key that is not set is not an error: the
// caller wanted it gone, and it is.
func (s *Store) DeleteKV(key string) error {
	if _, err := s.db.Exec(`DELETE FROM kv WHERE key = ?`, key); err != nil {
		return fmt.Errorf("delete kv %s: %w", key, err)
	}
	return nil
}

// ListKVPrefix returns every row whose key starts with prefix, keyed by the
// full key, in ONE query. It exists for the board build: a family of per-job
// rows (`cell_ctx:<id>:<session>`, `auto_handoff:<id>`) read per job would be
// one GetKV per active job inside a payload rebuilt every ~2s — the same
// argument ListJobTokens makes for the token rows.
//
// The prefix is matched LITERALLY: `%` and `_` in it are escaped before the
// LIKE, so a caller never gets wildcard matching it did not ask for. No key
// in this family carries either character today, but a helper that quietly
// treated them as wildcards would be a trap for the next caller.
func (s *Store) ListKVPrefix(prefix string) (map[string]string, error) {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
	rows, err := s.db.Query(`SELECT key, COALESCE(value,'') FROM kv WHERE key LIKE ? ESCAPE '\'`, escaped+"%")
	if err != nil {
		return nil, fmt.Errorf("list kv %s*: %w", prefix, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("list kv %s*: %w", prefix, err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list kv %s*: %w", prefix, err)
	}
	return out, nil
}
