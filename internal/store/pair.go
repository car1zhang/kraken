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

// pair.go — paired devices (schema v34's pair_devices table): one row per
// phone or browser that proved the pairing password. The bearer token itself
// is never stored; TokenHash is its lowercase hex SHA-256, which is all the
// request gate needs to recognise it and all a stolen kraken.db would yield.

// PairDevice is one paired device. Times are unix millis.
type PairDevice struct {
	ID                            int64
	Label, TailnetName, TokenHash string
	CreatedAt, LastSeenAt         int64
	LastIP                        string
}

const pairColumns = `id, COALESCE(label,''), COALESCE(tailnet_name,''), token_hash,
	COALESCE(created_at,0), COALESCE(last_seen_at,0), COALESCE(last_ip,'')`

func scanPairDevice(row interface{ Scan(...any) error }) (*PairDevice, error) {
	var d PairDevice
	if err := row.Scan(&d.ID, &d.Label, &d.TailnetName, &d.TokenHash,
		&d.CreatedAt, &d.LastSeenAt, &d.LastIP); err != nil {
		return nil, err
	}
	return &d, nil
}

// AddPairDevice inserts a device and returns its id. A duplicate TokenHash is
// the UNIQUE constraint's error — two rows one token could match is never a
// state this table holds.
func (s *Store) AddPairDevice(d *PairDevice) (int64, error) {
	if d == nil || d.TokenHash == "" {
		return 0, fmt.Errorf("add pair device: empty token hash")
	}
	res, err := s.db.Exec(
		`INSERT INTO pair_devices (label, tailnet_name, token_hash, created_at, last_seen_at, last_ip)
		VALUES (?, ?, ?, ?, ?, ?)`,
		d.Label, d.TailnetName, d.TokenHash, d.CreatedAt, d.LastSeenAt, d.LastIP)
	if err != nil {
		// the label, never the hash: an error string reaches logs
		return 0, fmt.Errorf("add pair device %q: %w", d.Label, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("add pair device %q: %w", d.Label, err)
	}
	d.ID = id
	return id, nil
}

// GetPairDeviceByHash returns the device holding hash, or ErrNotFound.
func (s *Store) GetPairDeviceByHash(hash string) (*PairDevice, error) {
	d, err := scanPairDevice(s.db.QueryRow(`SELECT `+pairColumns+` FROM pair_devices WHERE token_hash = ?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get pair device: %w", err)
	}
	return d, nil
}

// ListPairDevices returns every device, oldest first. Never nil.
func (s *Store) ListPairDevices() ([]*PairDevice, error) {
	rows, err := s.db.Query(`SELECT ` + pairColumns + ` FROM pair_devices ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list pair devices: %w", err)
	}
	defer rows.Close()
	out := []*PairDevice{}
	for rows.Next() {
		d, err := scanPairDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("list pair devices: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeletePairDevice removes one device; ErrNotFound when no row had that id.
func (s *Store) DeletePairDevice(id int64) error {
	res, err := s.db.Exec(`DELETE FROM pair_devices WHERE id = ?`, id)
	return oneRow(res, err, fmt.Sprintf("delete pair device %d", id))
}

// DeleteAllPairDevices removes every device — a password change or clear,
// which logs every phone out — and returns how many there were.
func (s *Store) DeleteAllPairDevices() (int, error) {
	res, err := s.db.Exec(`DELETE FROM pair_devices`)
	if err != nil {
		return 0, fmt.Errorf("delete all pair devices: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete all pair devices: %w", err)
	}
	return int(n), nil
}

// TouchPairDevice stamps when and from where a device was last seen.
func (s *Store) TouchPairDevice(id int64, at int64, ip string) error {
	res, err := s.db.Exec(`UPDATE pair_devices SET last_seen_at = ?, last_ip = ? WHERE id = ?`, at, ip, id)
	return oneRow(res, err, fmt.Sprintf("touch pair device %d", id))
}
