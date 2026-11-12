package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

const sourcesSchema = `
CREATE TABLE IF NOT EXISTS sources (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	connector TEXT NOT NULL,
	config TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
`

// SourceRow is a stored content source. Secret config values are sealed
// (encrypted) before they reach the store; the store never sees plaintext.
type SourceRow struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Connector string            `json:"connector"`
	Config    map[string]string `json:"config"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// SaveSource creates or replaces a source.
func (s *Store) SaveSource(ctx context.Context, src *SourceRow) error {
	now := time.Now().UTC()
	if src.ID == "" {
		src.ID = newID("s")
		src.CreatedAt = now
	}
	src.UpdatedAt = now
	cfg, err := json.Marshal(src.Config)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sources (id, name, connector, config, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, config=excluded.config, updated_at=excluded.updated_at`,
			src.ID, src.Name, src.Connector, string(cfg), src.CreatedAt.Unix(), src.UpdatedAt.Unix())
		return err
	})
}

// GetSource loads one source.
func (s *Store) GetSource(ctx context.Context, id string) (*SourceRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, connector, config, created_at, updated_at FROM sources WHERE id=?`, id)
	src, err := scanSource(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return src, err
}

// ListSources returns all sources, sorted by name.
func (s *Store) ListSources(ctx context.Context) ([]SourceRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, connector, config, created_at, updated_at FROM sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SourceRow{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *src)
	}
	return out, rows.Err()
}

func scanSource(r scanner) (*SourceRow, error) {
	var (
		src              SourceRow
		cfg              string
		created, updated int64
	)
	if err := r.Scan(&src.ID, &src.Name, &src.Connector, &cfg, &created, &updated); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(cfg), &src.Config); err != nil {
		return nil, err
	}
	if src.Config == nil {
		src.Config = map[string]string{}
	}
	src.CreatedAt, src.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return &src, nil
}

// DeleteSource removes a source and takes it out of every profile.
func (s *Store) DeleteSource(ctx context.Context, id string) error {
	profiles, err := s.ListProfiles(ctx)
	if err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM sources WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		for _, p := range profiles {
			if !slices.Contains(p.SourceIDs, id) {
				continue
			}
			p.SourceIDs = slices.DeleteFunc(p.SourceIDs, func(x string) bool { return x == id })
			body, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE profiles SET body=? WHERE id=?`, string(body), p.ID); err != nil {
				return err
			}
		}
		return nil
	})
}
