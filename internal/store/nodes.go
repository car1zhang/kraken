// Protocol parity: this file is part of the MULTIPLAYER protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// nodes.go — the fleet registry (schema v34's nodes table): one row per
// sibling `kraken serve` this node has seen on the tailnet. The daemon's nodes
// pass is the only writer (plus the announce route, which upserts identity);
// the board, the CLI and the action proxy read it.

// NodeRow is one sibling node. Times are unix millis. ID is the device's
// tailscale stable id (the primary key). JobsJSON is the last successfully
// polled card list, verbatim — "" for a node never polled.
type NodeRow struct {
	ID, Label, Operator, DNSName, IP string
	Port                             int
	Build                            string
	Contract                         int
	FirstSeen, LastSeen, LastOK      int64
	LastErr                          string
	Online                           bool
	JobsJSON                         string
}

const nodeColumns = `id, COALESCE(label,''), COALESCE(operator,''), COALESCE(dns_name,''),
	COALESCE(ip,''), COALESCE(port,0), COALESCE(build,''), COALESCE(contract,0),
	COALESCE(first_seen,0), COALESCE(last_seen,0), COALESCE(last_ok,0),
	COALESCE(last_err,''), COALESCE(online,0), COALESCE(jobs_json,'')`

func scanNode(row interface{ Scan(...any) error }) (*NodeRow, error) {
	var r NodeRow
	if err := row.Scan(&r.ID, &r.Label, &r.Operator, &r.DNSName, &r.IP, &r.Port,
		&r.Build, &r.Contract, &r.FirstSeen, &r.LastSeen, &r.LastOK, &r.LastErr,
		&r.Online, &r.JobsJSON); err != nil {
		return nil, err
	}
	return &r, nil
}

// UpsertNode inserts or updates a node by ID. An update touches IDENTITY
// columns only — label, operator, dns_name, ip, port, build, contract,
// last_seen, online — and never first_seen, jobs_json, last_ok or last_err:
// an announce or a re-probe says who a node is, not what its last poll found,
// and letting it blank the card list would empty that node's cards on every
// board until the next poll landed.
func (s *Store) UpsertNode(r *NodeRow) error {
	if r == nil || r.ID == "" {
		return fmt.Errorf("upsert node: empty id")
	}
	first := r.FirstSeen
	if first == 0 {
		first = r.LastSeen
	}
	if _, err := s.db.Exec(
		`INSERT INTO nodes (id, label, operator, dns_name, ip, port, build, contract,
			first_seen, last_seen, last_ok, last_err, online, jobs_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			label = excluded.label, operator = excluded.operator,
			dns_name = excluded.dns_name, ip = excluded.ip, port = excluded.port,
			build = excluded.build, contract = excluded.contract,
			last_seen = excluded.last_seen, online = excluded.online`,
		r.ID, r.Label, r.Operator, r.DNSName, r.IP, r.Port, r.Build, r.Contract,
		first, r.LastSeen, r.LastOK, r.LastErr, r.Online, r.JobsJSON,
	); err != nil {
		return fmt.Errorf("upsert node %s: %w", r.ID, err)
	}
	return nil
}

// SetNodePoll records one poll's outcome. A success stores the card list,
// stamps last_ok and last_seen, clears last_err and marks the node online. A
// failure stamps last_err and last_seen only: the previous card list and
// last_ok stay, so a sibling that blips keeps its cards on the board, and
// online is the pass' call (three failures in a row), not one poll's.
func (s *Store) SetNodePoll(id string, ok bool, errText, jobsJSON string, at int64) error {
	var res sql.Result
	var err error
	if ok {
		res, err = s.db.Exec(
			`UPDATE nodes SET last_ok = ?, last_err = '', jobs_json = ?, online = 1, last_seen = ? WHERE id = ?`,
			at, jobsJSON, at, id)
	} else {
		res, err = s.db.Exec(`UPDATE nodes SET last_err = ?, last_seen = ? WHERE id = ?`, errText, at, id)
	}
	return oneRow(res, err, "set node poll "+id)
}

// SetNodeOnline flips online and nothing else.
func (s *Store) SetNodeOnline(id string, online bool) error {
	res, err := s.db.Exec(`UPDATE nodes SET online = ? WHERE id = ?`, online, id)
	return oneRow(res, err, "set node online "+id)
}

// oneRow turns an UPDATE result into nil, ErrNotFound (no row matched) or the
// error. SQLite counts a matched row as affected even when no value changed,
// so zero means exactly "no such row".
func oneRow(res sql.Result, err error, what string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}

// GetNode returns one node, or ErrNotFound.
func (s *Store) GetNode(id string) (*NodeRow, error) {
	r, err := scanNode(s.db.QueryRow(`SELECT `+nodeColumns+` FROM nodes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get node %s: %w", id, err)
	}
	return r, nil
}

// ListNodes returns every node, online first, then by label
// (case-insensitively), then id. Never nil.
func (s *Store) ListNodes() ([]*NodeRow, error) {
	rows, err := s.db.Query(`SELECT ` + nodeColumns + ` FROM nodes
		ORDER BY online DESC, label COLLATE NOCASE ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	defer rows.Close()
	out := []*NodeRow{}
	for rows.Next() {
		r, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("list nodes: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
