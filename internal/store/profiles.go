package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/model"
)

func (s *Store) ListProfiles(ctx context.Context) ([]model.ConnectionProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, data, created_at, updated_at FROM connection_profiles ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ConnectionProfile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, data, created_at, updated_at FROM connection_profiles WHERE id = ?`, id)
	p, err := scanProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ConnectionProfile{}, apperr.New(apperr.NotFound, "La conexión no existe")
	}
	return p, err
}

// SaveProfile inserta (ID vacío → se genera) o actualiza. Nombre duplicado → VALIDATION.
func (s *Store) SaveProfile(ctx context.Context, p model.ConnectionProfile) (model.ConnectionProfile, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	if p.ID == "" {
		p.ID = uuid.NewString()
		p.CreatedAt = now
	} else {
		existing, err := s.GetProfile(ctx, p.ID)
		if err != nil {
			return model.ConnectionProfile{}, err
		}
		p.CreatedAt = existing.CreatedAt
	}
	p.UpdatedAt = now

	data, err := json.Marshal(p)
	if err != nil {
		return model.ConnectionProfile{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO connection_profiles (id, name, driver, data, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, driver=excluded.driver, data=excluded.data, updated_at=excluded.updated_at`,
		p.ID, p.Name, p.Driver, string(data), p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: connection_profiles.name") {
			return model.ConnectionProfile{}, apperr.New(apperr.Validation, "Ya existe una conexión con ese nombre").WithDetail("name")
		}
		return model.ConnectionProfile{}, err
	}
	return p, nil
}

func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM connection_profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.New(apperr.NotFound, "La conexión no existe")
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanProfile(r scanner) (model.ConnectionProfile, error) {
	var (
		id, name, data   string
		created, updated int64
	)
	if err := r.Scan(&id, &name, &data, &created, &updated); err != nil {
		return model.ConnectionProfile{}, err
	}
	var p model.ConnectionProfile
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return model.ConnectionProfile{}, err
	}
	p.ID, p.Name = id, name
	p.CreatedAt, p.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return p, nil
}
