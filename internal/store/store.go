// Package store owns kraken.db: the single SQLite database that (after the
// migration this package is the first slice of) replaces kraken's six file
// stores. It sits at the bottom of the dependency chain next to util —
// util ← store ← core ← jobs ← api ← web — and must stay pure storage: no
// HTTP, no tmux, no file-tree walking. Blobs in, blobs out.
//
// Store is the sole writer of kraken.db (today: the importer and, later,
// kraken serve). Concurrency inside one process is handled by capping the
// connection pool at one connection (see Open) rather than leaning on
// SQLITE_BUSY retries across many connections — simple, and sufficient at
// the scale this package targets (a few MB, tens of jobs).
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"

	"kraken/internal/util"
)

// Store wraps the single *sql.DB handle for kraken.db.
type Store struct {
	db *sql.DB
}

// execer is the subset of *sql.DB / *sql.Tx this package's per-table
// helpers (updateJobFieldsTx, upsertPRTx, removePRTx, insertThreadTx,
// nextThreadIDTx — jobs.go/prs.go/threads.go) run against. Writing them
// against this interface instead of *Store directly lets each one run either
// standalone (its own implicit transaction, via s.db) or composed inside a
// caller-owned transaction (SaveJobEdit, L10) — the exact same SQL either
// way, so there's only one implementation of each to keep correct.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// DBPath is where kraken.db lives: ~/.kraken/kraken.db, overridable with
// KRAKEN_DB_PATH for tests.
func DBPath() string {
	if p := os.Getenv("KRAKEN_DB_PATH"); p != "" {
		return p // tests point at a temp file
	}
	return filepath.Join(util.Home(), ".kraken", "kraken.db")
}

// Open opens (creating if absent) the SQLite database at path, applies the
// PRAGMAs the spec calls for (WAL, foreign_keys, busy_timeout, immediate
// transaction locking), and runs the embedded schema migration. The
// connection pool is capped at one connection: this process is meant to be
// the sole writer, and pinning to one connection means every statement
// this Store issues shares a single SQLite connection (and therefore a
// single busy_timeout setting) instead of some statements landing on a
// fresh pooled connection that never saw the PRAGMA. _txlock=immediate
// makes every Begin() issue BEGIN IMMEDIATE instead of mattn's default
// deferred BEGIN, so a transaction that reads then writes (UpdateJobFields'
// rev check, migrate's version check) grabs the write lock up front —
// subject to _busy_timeout — rather than risking SQLITE_BUSY when a later
// statement tries to upgrade a deferred read lock to a write lock.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	dsn := fmt.Sprintf(
		"file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate",
		path,
	)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	// Several processes (or, in tests, goroutines) racing to open
	// the same brand-new file each pay a one-time WAL-mode initialization
	// cost on their first real connection (Ping opens it) — that handshake
	// can itself trip a brief SQLITE_BUSY/LOCKED before this connection's own
	// _busy_timeout PRAGMA (a DSN param, applied as part of the same
	// handshake) has taken effect, so the DSN setting alone doesn't fully
	// cover it. retryBusy closes that gap with its own bounded backoff.
	if err := retryBusy(db.Ping); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := retryBusy(s.migrate); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

// openRetryBudget bounds how long retryBusy keeps retrying a SQLITE_BUSY/
// LOCKED error before giving up and returning it.
const openRetryBudget = 5 * time.Second

// isSQLiteBusy reports whether err is SQLITE_BUSY or SQLITE_LOCKED — the
// transient "someone else holds the lock right now" codes _busy_timeout is
// meant to absorb, as opposed to a genuine failure. Falls back to a
// substring match on the error text as a backstop in case the error ever
// reaches here already flattened to a string by an intermediate wrapper.
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	var serr sqlite3.Error
	if errors.As(err, &serr) {
		return serr.Code == sqlite3.ErrBusy || serr.Code == sqlite3.ErrLocked
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY")
}

// retryBusy runs fn, retrying with a short sleep while it fails with
// SQLITE_BUSY/LOCKED, up to openRetryBudget total — the actual fix for
// TestConcurrentOpenBothSucceed being flaky: _busy_timeout covers
// individual statements once a connection is fully up, not the brief window
// while WAL mode is first being initialized on a fresh file.
func retryBusy(fn func() error) error {
	deadline := time.Now().Add(openRetryBudget)
	for {
		err := fn()
		if err == nil || !isSQLiteBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB exposes the raw handle. Importer/debug only; bypasses rev checking —
// never call from serve paths.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Backup writes a consistent point-in-time copy of the database to
// destPath via VACUUM INTO — simpler than the SQLite online backup API and
// sufficient at this scale (nightly, <10MB). destPath must not already
// exist; VACUUM INTO refuses to overwrite.
func (s *Store) Backup(destPath string) error {
	if dir := filepath.Dir(destPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, destPath); err != nil {
		return fmt.Errorf("backup to %s: %w", destPath, err)
	}
	return nil
}
