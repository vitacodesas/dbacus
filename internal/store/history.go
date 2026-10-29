package store

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vitacodesas/dbacus/internal/model"
)

// MaxHistorySQLBytes limita el SQL guardado por entrada (spec 07).
const MaxHistorySQLBytes = 100 * 1024

type HistoryQuery struct {
	ProfileID string
	Search    string
	Status    string // "" | "ok" | "error"
	Limit     int    // default 50, máx 500
	Cursor    string // seq de la última entrada de la página anterior
}

// AddHistory inserta la entrada y purga las más antiguas del perfil por encima de retention (0 = sin límite).
func (s *Store) AddHistory(ctx context.Context, e model.HistoryEntry, retention int) (model.HistoryEntry, error) {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.ExecutedAt.IsZero() {
		e.ExecutedAt = time.Now().UTC()
	}
	if len(e.SQL) > MaxHistorySQLBytes {
		e.SQL = strings.ToValidUTF8(e.SQL[:MaxHistorySQLBytes], "")
	}
	ok := 0
	if e.OK {
		ok = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return e, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO query_history (id, profile_id, sql, database, executed_at, elapsed_ms, ok, row_count, affected_rows, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.ProfileID, e.SQL, e.Database, e.ExecutedAt.UnixMilli(), e.ElapsedMs, ok, e.RowCount, e.AffectedRows, e.ErrorMessage); err != nil {
		return e, err
	}
	if retention > 0 {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM query_history WHERE profile_id = ? AND seq NOT IN
			  (SELECT seq FROM query_history WHERE profile_id = ? ORDER BY seq DESC LIMIT ?)`,
			e.ProfileID, e.ProfileID, retention); err != nil {
			return e, err
		}
	}
	return e, tx.Commit()
}

// ListHistory devuelve entradas más recientes primero y el cursor de la siguiente página ("" si no hay más).
func (s *Store) ListHistory(ctx context.Context, q HistoryQuery) ([]model.HistoryEntry, string, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	where := []string{"profile_id = ?"}
	args := []any{q.ProfileID}
	if q.Search != "" {
		where = append(where, `sql LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(q.Search)+"%")
	}
	switch q.Status {
	case "ok":
		where = append(where, "ok = 1")
	case "error":
		where = append(where, "ok = 0")
	}
	if q.Cursor != "" {
		seq, err := strconv.ParseInt(q.Cursor, 10, 64)
		if err == nil {
			where = append(where, "seq < ?")
			args = append(args, seq)
		}
	}
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, id, profile_id, sql, database, executed_at, elapsed_ms, ok, row_count, affected_rows, error_message
		FROM query_history WHERE `+strings.Join(where, " AND ")+` ORDER BY seq DESC LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	out := []model.HistoryEntry{}
	var seqs []int64
	for rows.Next() {
		var (
			seq, executed int64
			ok            int
			e             model.HistoryEntry
		)
		if err := rows.Scan(&seq, &e.ID, &e.ProfileID, &e.SQL, &e.Database, &executed, &e.ElapsedMs, &ok, &e.RowCount, &e.AffectedRows, &e.ErrorMessage); err != nil {
			return nil, "", err
		}
		e.ExecutedAt, e.OK = time.UnixMilli(executed).UTC(), ok == 1
		out, seqs = append(out, e), append(seqs, seq)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(seqs[limit-1], 10)
	}
	return out, next, nil
}

func (s *Store) DeleteHistory(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM query_history WHERE id = ?`, id)
	return err
}

func (s *Store) ClearHistory(ctx context.Context, profileID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM query_history WHERE profile_id = ?`, profileID)
	return err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
