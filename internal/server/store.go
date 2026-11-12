package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jumpinjet22/doxacast/core"
	_ "modernc.org/sqlite" // pure-Go SQLite, no cgo, cross-compiles cleanly
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

// Profile is a reusable assignment: which sources, which photos.
type Profile struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Sources     []core.Source `json:"sources"`
	Album       string        `json:"album"`
	Interval    int           `json:"interval_seconds"`
	Shuffle     bool          `json:"shuffle"`
	PollSeconds int           `json:"poll_seconds"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Device is a registered screen.
type Device struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Hostname  string           `json:"hostname"`
	Platform  string           `json:"platform"`
	ProfileID string           `json:"profile_id"`
	Display   core.DisplaySpec `json:"display"`
	CreatedAt time.Time        `json:"created_at"`
	LastSeen  *time.Time       `json:"last_seen,omitempty"`
	Heartbeat *core.Heartbeat  `json:"heartbeat,omitempty"`
}

// Store persists everything in SQLite.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
INSERT OR IGNORE INTO meta (k, v) VALUES ('revision', '1');
CREATE TABLE IF NOT EXISTS profiles (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	body TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	hostname TEXT NOT NULL DEFAULT '',
	platform TEXT NOT NULL DEFAULT '',
	profile_id TEXT NOT NULL DEFAULT '',
	display TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL,
	last_seen INTEGER,
	heartbeat TEXT
);
`

// OpenStore opens (and migrates) the database at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: one writer; keeps things simple and safe
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Revision is bumped on every admin change. Devices compare it to decide
// whether to refetch. Coarse, but trivially correct and cheap at our scale.
func (s *Store) Revision(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx, `SELECT CAST(v AS INTEGER) FROM meta WHERE k='revision'`).Scan(&v)
	return v, err
}

func (s *Store) bump(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE meta SET v = CAST(v AS INTEGER) + 1 WHERE k='revision'`)
	return err
}

func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := s.bump(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- profiles ----

// SaveProfile creates or replaces a profile.
func (s *Store) SaveProfile(ctx context.Context, p *Profile) error {
	if p.ID == "" {
		p.ID = newID("p")
	}
	p.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO profiles (id, name, body, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, body=excluded.body, updated_at=excluded.updated_at`,
			p.ID, p.Name, string(body), p.UpdatedAt.Unix())
		return err
	})
}

// GetProfile loads one profile.
func (s *Store) GetProfile(ctx context.Context, id string) (*Profile, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT body FROM profiles WHERE id=?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var p Profile
	return &p, json.Unmarshal([]byte(body), &p)
}

// ListProfiles returns all profiles.
func (s *Store) ListProfiles(ctx context.Context) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var p Profile
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProfile removes a profile and unassigns devices using it.
func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM profiles WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `UPDATE devices SET profile_id='' WHERE profile_id=?`, id)
		return err
	})
}

// ---- devices ----

// CreateDevice registers a device and returns its plaintext token (shown once).
func (s *Store) CreateDevice(ctx context.Context, hostname, platform string) (*Device, string, error) {
	token, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	d := &Device{
		ID:        newID("d"),
		Name:      hostname,
		Hostname:  hostname,
		Platform:  platform,
		Display:   core.DisplaySpec{Output: "hdmi"},
		CreatedAt: time.Now().UTC(),
	}
	disp, _ := json.Marshal(d.Display)
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO devices (id, name, token_hash, hostname, platform, display, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, d.ID, d.Name, hashToken(token), hostname, platform, string(disp), d.CreatedAt.Unix())
		return err
	})
	return d, token, err
}

// DeviceByToken authenticates a device.
func (s *Store) DeviceByToken(ctx context.Context, token string) (*Device, error) {
	return s.getDevice(ctx, `token_hash=?`, hashToken(token))
}

// GetDevice loads a device by ID.
func (s *Store) GetDevice(ctx context.Context, id string) (*Device, error) {
	return s.getDevice(ctx, `id=?`, id)
}

const deviceCols = `id, name, hostname, platform, profile_id, display, created_at, last_seen, heartbeat`

func (s *Store) getDevice(ctx context.Context, where string, arg any) (*Device, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE `+where, arg)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

type scanner interface{ Scan(...any) error }

func scanDevice(r scanner) (*Device, error) {
	var (
		d        Device
		display  string
		created  int64
		lastSeen sql.NullInt64
		hb       sql.NullString
	)
	if err := r.Scan(&d.ID, &d.Name, &d.Hostname, &d.Platform, &d.ProfileID, &display, &created, &lastSeen, &hb); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(display), &d.Display)
	d.CreatedAt = time.Unix(created, 0).UTC()
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0).UTC()
		d.LastSeen = &t
	}
	if hb.Valid {
		var h core.Heartbeat
		if json.Unmarshal([]byte(hb.String), &h) == nil {
			d.Heartbeat = &h
		}
	}
	return &d, nil
}

// ListDevices returns all devices.
func (s *Store) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// DevicePatch holds editable device fields; nil means unchanged.
type DevicePatch struct {
	Name      *string           `json:"name"`
	ProfileID *string           `json:"profile_id"`
	Display   *core.DisplaySpec `json:"display"`
}

// UpdateDevice applies a patch.
func (s *Store) UpdateDevice(ctx context.Context, id string, p DevicePatch) (*Device, error) {
	d, err := s.GetDevice(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		d.Name = *p.Name
	}
	if p.ProfileID != nil {
		if *p.ProfileID != "" {
			if _, err := s.GetProfile(ctx, *p.ProfileID); err != nil {
				return nil, fmt.Errorf("profile %q: %w", *p.ProfileID, err)
			}
		}
		d.ProfileID = *p.ProfileID
	}
	if p.Display != nil {
		d.Display = *p.Display
	}
	disp, _ := json.Marshal(d.Display)
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE devices SET name=?, profile_id=?, display=? WHERE id=?`,
			d.Name, d.ProfileID, string(disp), id)
		return err
	})
	return d, err
}

// DeleteDevice removes a device; its token stops working immediately.
func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM devices WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// RecordHeartbeat stores the latest status. It does not bump the revision.
func (s *Store) RecordHeartbeat(ctx context.Context, id string, hb core.Heartbeat) error {
	b, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE devices SET last_seen=?, heartbeat=? WHERE id=?`,
		time.Now().Unix(), string(b), id)
	return err
}

// ---- helpers ----

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
