package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jumpinjet22/doxaboard/internal/live"
)

const liveSchema = `
CREATE TABLE IF NOT EXISTS broadcasts (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	outputs TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);
`

// StoredOutput is an output as stored: the stream key is sealed.
type StoredOutput struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	URL       string `json:"url"`
	SealedKey string `json:"sealed_key,omitempty"`
}

// BroadcastRow is a stored broadcast.
type BroadcastRow struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Outputs   []StoredOutput `json:"outputs"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// LiveSchedule is the weekly schedule plus dated specials.
type LiveSchedule struct {
	Slots    []live.Slot    `json:"slots"`
	Specials []live.Special `json:"specials"`
}

// SaveBroadcast creates or replaces a broadcast.
func (s *Store) SaveBroadcast(ctx context.Context, b *BroadcastRow) error {
	now := time.Now().UTC()
	if b.ID == "" {
		b.ID = newID("b")
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	outs, err := json.Marshal(b.Outputs)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO broadcasts (id, name, outputs, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, outputs=excluded.outputs, updated_at=excluded.updated_at`,
			b.ID, b.Name, string(outs), b.CreatedAt.Unix(), b.UpdatedAt.Unix())
		return err
	})
}

// GetBroadcast loads one broadcast.
func (s *Store) GetBroadcast(ctx context.Context, id string) (*BroadcastRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, outputs, created_at, updated_at FROM broadcasts WHERE id=?`, id)
	b, err := scanBroadcast(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// ListBroadcasts returns all broadcasts by name.
func (s *Store) ListBroadcasts(ctx context.Context) ([]BroadcastRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, outputs, created_at, updated_at FROM broadcasts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BroadcastRow{}
	for rows.Next() {
		b, err := scanBroadcast(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

func scanBroadcast(r scanner) (*BroadcastRow, error) {
	var (
		b                BroadcastRow
		outs             string
		created, updated int64
	)
	if err := r.Scan(&b.ID, &b.Name, &outs, &created, &updated); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(outs), &b.Outputs); err != nil {
		return nil, err
	}
	if b.Outputs == nil {
		b.Outputs = []StoredOutput{}
	}
	b.CreatedAt, b.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return &b, nil
}

// DeleteBroadcast removes a broadcast and its schedule entries.
func (s *Store) DeleteBroadcast(ctx context.Context, id string) error {
	sched, err := s.LiveSchedule(ctx)
	if err != nil {
		return err
	}
	keepSlots := sched.Slots[:0]
	for _, sl := range sched.Slots {
		if sl.BroadcastID != id {
			keepSlots = append(keepSlots, sl)
		}
	}
	keepSpecials := sched.Specials[:0]
	for _, sp := range sched.Specials {
		if sp.BroadcastID != id {
			keepSpecials = append(keepSpecials, sp)
		}
	}
	sched.Slots, sched.Specials = keepSlots, keepSpecials
	body, err := json.Marshal(sched)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM broadcasts WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return putKV(ctx, tx, "live_schedule", string(body))
	})
}

// LiveSchedule loads the schedule (empty if never saved).
func (s *Store) LiveSchedule(ctx context.Context) (LiveSchedule, error) {
	sched := LiveSchedule{Slots: []live.Slot{}, Specials: []live.Special{}}
	v, err := s.getKV(ctx, "live_schedule")
	if err != nil || v == "" {
		return sched, err
	}
	if err := json.Unmarshal([]byte(v), &sched); err != nil {
		return sched, err
	}
	if sched.Slots == nil {
		sched.Slots = []live.Slot{}
	}
	if sched.Specials == nil {
		sched.Specials = []live.Special{}
	}
	return sched, nil
}

// SaveLiveSchedule replaces the schedule.
func (s *Store) SaveLiveSchedule(ctx context.Context, sched LiveSchedule) error {
	body, err := json.Marshal(sched)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error { return putKV(ctx, tx, "live_schedule", string(body)) })
}

// GetLiveState returns the persisted live state.
func (s *Store) GetLiveState(ctx context.Context) (live.State, error) {
	var st live.State
	v, err := s.getKV(ctx, "live_state")
	if err != nil || v == "" {
		return st, err
	}
	return st, json.Unmarshal([]byte(v), &st)
}

// PutLiveState persists the live state. It doesn't bump the screen
// revision: screens don't care what's on Vimeo.
func (s *Store) PutLiveState(ctx context.Context, st live.State) error {
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO kv (k, v) VALUES ('live_state', ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, string(body))
	return err
}

func (s *Store) getKV(ctx context.Context, k string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, k).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func putKV(ctx context.Context, tx *sql.Tx, k, v string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO kv (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}
