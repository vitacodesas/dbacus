package mysql

import (
	"database/sql"

	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

type rowsResult struct {
	rows *sql.Rows
	cols []model.ColumnMeta
	vals []any
	ptrs []any
	err  error
}

func newRowsResult(rows *sql.Rows) (*rowsResult, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		_ = rows.Close()
		return nil, mapError(err)
	}
	r := &rowsResult{rows: rows, vals: make([]any, len(types)), ptrs: make([]any, len(types))}
	for i, t := range types {
		m := model.ColumnMeta{Name: t.Name(), DataType: t.DatabaseTypeName()}
		m.Nullable, _ = t.Nullable()
		if p, s, ok := t.DecimalSize(); ok {
			pi, si := int(p), int(s)
			m.Precision, m.Scale = &pi, &si
		}
		r.cols = append(r.cols, m)
		r.ptrs[i] = &r.vals[i]
	}
	return r, nil
}

func (r *rowsResult) Columns() []model.ColumnMeta { return r.cols }

func (r *rowsResult) Next() bool {
	if r.err != nil || !r.rows.Next() {
		return false
	}
	if err := r.rows.Scan(r.ptrs...); err != nil {
		r.err = mapError(err)
		return false
	}
	return true
}

// Row copia el slice: los valores []byte ya los clona database/sql al escanear en *any.
func (r *rowsResult) Row() []any { return append([]any(nil), r.vals...) }

func (r *rowsResult) Err() error {
	if r.err != nil {
		return r.err
	}
	return mapError(r.rows.Err())
}

func (r *rowsResult) Affected() (int64, int64, int) { return 0, 0, 0 }
func (r *rowsResult) Close() error                  { return r.rows.Close() }

type execResult struct {
	rows, lastID int64
	warnings     int
}

func (*execResult) Columns() []model.ColumnMeta     { return nil }
func (*execResult) Next() bool                      { return false }
func (*execResult) Row() []any                      { return nil }
func (*execResult) Err() error                      { return nil }
func (r *execResult) Affected() (int64, int64, int) { return r.rows, r.lastID, r.warnings }
func (*execResult) Close() error                    { return nil }

var (
	_ driver.RunResult = (*rowsResult)(nil)
	_ driver.RunResult = (*execResult)(nil)
	_ driver.Conn      = (*conn)(nil)
	_ driver.Pool      = (*pool)(nil)
	_ driver.Driver    = (*Driver)(nil)
)
