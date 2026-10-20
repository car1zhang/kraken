package store

import (
	"database/sql"
	"errors"
	"fmt"
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
