package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// periodic.go — schema v42's periodic_jobs table: one row per periodic job,
// a stored job template that the daemon tick (internal/jobs/periodic.go)
// turns into a job on each of its cadence days or every fixed interval. The
// dream pass's row lives here too (name `dream`). The store holds the row and
// its last-firing watermark; every cadence and validation rule lives in jobs.

// PeriodicJob is one row. Tag-free like every type in this package: the API
// layer owns the wire shape (internal/api/periodic.go's webPeriodic).
type PeriodicJob struct {
	ID       int64
	Name     string
	Prefix   string
	Template string
	AtMinute int // minutes after local midnight, 0..1439
	Days     int // weekday bitmask, bit 1<<time.Weekday (Sunday is bit 0); 0 means every day
	// EveryMinutes > 0 makes the row an interval row: it fires that many
	// minutes after its last firing (or its arming), and AtMinute and Days
	// are unused. 0 is a calendar row.
	EveryMinutes int
	Enabled      bool
	ArmedAt      int64  // unix millis: when the row was created or last resumed
	LastDate     string // local date of the last scheduled firing attempt, "" when never
	LastSlug     string // slug that attempt created, "" when it created none
	LastNote     string // why that attempt created or started nothing, "" when it went fine
	LastAt       int64  // unix millis of that attempt, 0 when never
	CreatedAt    int64  // unix millis
}

// ErrPeriodicTaken is CreatePeriodicJob's refusal when another row already
// holds the name or the prefix. errors.Is-comparable so the API layer can
// answer 409 without matching on text.
var ErrPeriodicTaken = errors.New("store: periodic job name or prefix taken")

const periodicColumns = `id, name, prefix, template, at_minute, days, every_minutes, enabled, armed_at, last_date, last_slug, last_note, last_at, created_at`

func scanPeriodic(sc interface{ Scan(...any) error }) (PeriodicJob, error) {
	var p PeriodicJob
	err := sc.Scan(&p.ID, &p.Name, &p.Prefix, &p.Template, &p.AtMinute, &p.Days, &p.EveryMinutes, &p.Enabled,
		&p.ArmedAt, &p.LastDate, &p.LastSlug, &p.LastNote, &p.LastAt, &p.CreatedAt)
	return p, err
}

// CreatePeriodicJob inserts p and returns the new id. The Last* fields are
// not written: a new row has never fired, whatever the caller's struct says.
func (s *Store) CreatePeriodicJob(p *PeriodicJob) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO periodic_jobs (name, prefix, template, at_minute, days, every_minutes, enabled, armed_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Prefix, p.Template, p.AtMinute, p.Days, p.EveryMinutes, p.Enabled, p.ArmedAt, p.CreatedAt,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return 0, fmt.Errorf("periodic job %q (prefix %q): %w", p.Name, p.Prefix, ErrPeriodicTaken)
		}
		return 0, fmt.Errorf("create periodic job %q: %w", p.Name, err)
	}
	return res.LastInsertId()
}

// ListPeriodicJobs returns every row in creation order (id order: the id is
// AUTOINCREMENT, so it never reuses a deleted row's place).
func (s *Store) ListPeriodicJobs() ([]PeriodicJob, error) {
	rows, err := s.db.Query(`SELECT ` + periodicColumns + ` FROM periodic_jobs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list periodic jobs: %w", err)
	}
	defer rows.Close()
	out := []PeriodicJob{}
	for rows.Next() {
		p, err := scanPeriodic(rows)
		if err != nil {
			return nil, fmt.Errorf("scan periodic job: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPeriodicJob returns the row named name exactly, or ErrNotFound.
func (s *Store) GetPeriodicJob(name string) (*PeriodicJob, error) {
	p, err := scanPeriodic(s.db.QueryRow(`SELECT `+periodicColumns+` FROM periodic_jobs WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get periodic job %q: %w", name, err)
	}
	return &p, nil
}

// SetPeriodicEnabled pauses or resumes a row. Resuming re-arms it at `at`, so
// a day whose occurrence passed while the row was paused does not fire the
// moment it comes back; pausing leaves armed_at alone.
func (s *Store) SetPeriodicEnabled(name string, enabled bool, at int64) error {
	var res sql.Result
	var err error
	if enabled {
		res, err = s.db.Exec(`UPDATE periodic_jobs SET enabled = 1, armed_at = ? WHERE name = ?`, at, name)
	} else {
		res, err = s.db.Exec(`UPDATE periodic_jobs SET enabled = 0 WHERE name = ?`, name)
	}
	if err != nil {
		return fmt.Errorf("set periodic job %q enabled=%v: %w", name, enabled, err)
	}
	return affectedOrNotFound(res)
}

// StampPeriodicFiring records one scheduled firing attempt and nothing else.
func (s *Store) StampPeriodicFiring(id int64, date, slug, note string, at int64) error {
	res, err := s.db.Exec(
		`UPDATE periodic_jobs SET last_date = ?, last_slug = ?, last_note = ?, last_at = ? WHERE id = ?`,
		date, slug, note, at, id,
	)
	if err != nil {
		return fmt.Errorf("stamp periodic job %d: %w", id, err)
	}
	return affectedOrNotFound(res)
}

// DeletePeriodicJob removes the row. The jobs it fired are not touched.
func (s *Store) DeletePeriodicJob(name string) error {
	res, err := s.db.Exec(`DELETE FROM periodic_jobs WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete periodic job %q: %w", name, err)
	}
	return affectedOrNotFound(res)
}

func affectedOrNotFound(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
