package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

func (s *Store) ListGroups(ctx context.Context) ([]model.ConnectionGroup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, color, created_at, updated_at FROM connection_groups ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ConnectionGroup{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GetGroup(ctx context.Context, id string) (model.ConnectionGroup, error) {
	g, err := scanGroup(s.db.QueryRowContext(ctx, `SELECT id, name, color, created_at, updated_at FROM connection_groups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.ConnectionGroup{}, apperr.New(apperr.NotFound, "El grupo no existe")
	}
	return g, err
}

// SaveGroup inserta (ID vacío → se genera) o actualiza. Nombre duplicado → VALIDATION.
func (s *Store) SaveGroup(ctx context.Context, g model.ConnectionGroup) (model.ConnectionGroup, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	if g.ID == "" {
		g.ID = uuid.NewString()
		g.CreatedAt = now
	} else {
		existing, err := s.GetGroup(ctx, g.ID)
		if err != nil {
			return model.ConnectionGroup{}, err
		}
		g.CreatedAt = existing.CreatedAt
	}
	g.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO connection_groups (id, name, color, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, color=excluded.color, updated_at=excluded.updated_at`,
		g.ID, g.Name, g.Color, g.CreatedAt.UnixMilli(), g.UpdatedAt.UnixMilli())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: connection_groups.name") {
			return model.ConnectionGroup{}, apperr.New(apperr.Validation, "Ya existe un grupo con ese nombre").WithDetail("name")
		}
		return model.ConnectionGroup{}, err
	}
	return g, nil
}

// DeleteGroup borra el grupo y deja sus conexiones en «Sin grupo», todo en una transacción: no hay clave foránea
// porque el grupo del perfil vive dentro de su JSON. Es idempotente. No toca secretos ni historial.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE connection_profiles SET data = json_set(data, '$.groupId', '') WHERE json_extract(data, '$.groupId') = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM connection_groups WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func scanGroup(r scanner) (model.ConnectionGroup, error) {
	var (
		g                model.ConnectionGroup
		created, updated int64
	)
	if err := r.Scan(&g.ID, &g.Name, &g.Color, &created, &updated); err != nil {
		return model.ConnectionGroup{}, err
	}
	g.CreatedAt, g.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return g, nil
}
