package services

import (
	"context"
	"fmt"

	"tabulariswails/internal/apperr"

	"tabulariswails/internal/edit"
	"tabulariswails/internal/model"
	"tabulariswails/internal/results"
	"tabulariswails/internal/session"
)

// EditStore es lo que EditService necesita de la persistencia (lo implementa *store.Store).
type EditStore interface {
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
}

type EditDeps struct {
	Sessions *session.Manager
	Results  *results.Manager
	Schema   *SchemaService
	Store    EditStore
}

// EditService expone la edición de celdas (spec 13). Go decide qué es editable, valida y genera el SQL; Vue solo pinta.
type EditService struct {
	d EditDeps
}

func NewEditService(d EditDeps) *EditService { return &EditService{d: d} }

// GetEditInfo dice si el resultado `stmtIndex` de la ejecución `queryID` se puede editar y qué columnas.
func (s *EditService) GetEditInfo(ctx context.Context, queryID string, stmtIndex int) (edit.Info, error) {
	plan, err := s.plan(ctx, queryID, stmtIndex)
	return plan.Info, err
}

// target reúne lo necesario para actuar sobre un resultado.
type target struct {
	run  *results.Run
	buf  *results.Buffer
	sess *session.Session
	plan edit.Plan
}

func (s *EditService) plan(ctx context.Context, queryID string, stmtIndex int) (edit.Plan, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	return t.plan, err
}

func (s *EditService) load(ctx context.Context, queryID string, stmtIndex int) (target, error) {
	run, err := s.d.Results.Get(queryID)
	if err != nil {
		return target{}, toAppErr(err)
	}
	buf, err := run.Buffer(stmtIndex)
	if err != nil {
		return target{}, toAppErr(err)
	}
	sess, err := s.d.Sessions.Get(run.SessionID)
	if err != nil {
		return target{}, toAppErr(err)
	}
	// El modo solo lectura se lee del perfil guardado (como en QueryService.Execute): cambiarlo con la sesión abierta aplica ya.
	readOnly := sess.ReadOnly
	if p, err := s.d.Store.GetProfile(ctx, sess.ProfileID); err == nil && p.ReadOnly {
		readOnly = true
	}
	sql, db := buf.Source()
	plan := edit.Analyze(edit.Input{
		SQL: sql, DefaultDB: db, Cols: buf.Columns(), ReadOnly: readOnly,
		Describe: func(db, table string) (model.TableDetail, error) {
			return s.d.Schema.DescribeTable(ctx, run.SessionID, db, table)
		},
	})
	plan.Pending = buf.PendingCount()
	return target{run: run, buf: buf, sess: sess, plan: plan}, nil
}

// MaxPending es el tope de celdas con cambios sin aplicar por resultado (spec 13).
const MaxPending = 1000

// Stage valida el valor escrito en una celda y lo deja como cambio pendiente (no toca el servidor). Si equivale al
// original, retira el cambio que hubiera. El valor nuevo se ve en `GetRows` con `Cell.Edited`.
func (s *EditService) Stage(ctx context.Context, queryID string, stmtIndex, row, col int, v edit.Value) (edit.Info, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.Info{}, err
	}
	p := t.plan
	if !p.Editable {
		return p.Info, notEditable("No se puede editar este resultado", p.Reason)
	}
	if col < 0 || col >= len(p.Columns) {
		return p.Info, invalid("col", "La columna no existe")
	}
	if !p.Columns[col].Editable {
		return p.Info, notEditable("Esta columna no se puede editar", p.Columns[col].Reason)
	}
	orig, err := t.buf.Cell(row, col)
	if err != nil {
		return p.Info, invalid("row", "La celda no existe")
	}
	if orig.Trunc {
		return p.Info, notEditable("El valor es demasiado largo para editarlo aquí", "truncated")
	}
	meta := p.Meta[col]
	if edit.Same(meta, v, orig) {
		t.buf.Unstage(row, col)
		p.Pending = t.buf.PendingCount()
		return p.Info, nil
	}
	arg, err := edit.Convert(meta, v)
	if err != nil {
		return p.Info, toAppErr(err)
	}
	if t.buf.PendingCount() >= MaxPending && !hasPending(t.buf, row, col) {
		return p.Info, invalid("pending", "Hay demasiados cambios sin aplicar; aplícalos o descártalos antes de seguir")
	}
	origArg, origNull, err := edit.OriginalArg(meta, orig)
	if err != nil {
		return p.Info, toAppErr(err)
	}
	key := make([]any, len(p.KeyColumns))
	for i, kc := range p.KeyColumns {
		cell, err := t.buf.Cell(row, kc)
		if err != nil {
			return p.Info, invalid("row", "La fila no existe")
		}
		k, null, err := edit.OriginalArg(p.Meta[kc], cell)
		if err != nil || null {
			return p.Info, notEditable("La fila no tiene un valor de clave utilizable", "key_value")
		}
		key[i] = k
	}
	change := edit.Change{
		Row: row, Col: col, Column: p.TableCols[col], Value: v, Arg: arg,
		OrigArg: origArg, OrigNull: origNull, Verify: edit.Verifiable(meta.DataType), Key: key,
	}
	shown := results.Cell{T: orig.T}
	if !v.Null {
		shown.V = fmt.Sprint(arg)
	}
	if err := t.buf.Stage(row, col, shown, change); err != nil {
		return p.Info, toAppErr(err)
	}
	p.Pending = t.buf.PendingCount()
	return p.Info, nil
}

func hasPending(buf *results.Buffer, row, col int) bool {
	for _, p := range buf.PendingList() {
		if p.Row == row && p.Col == col {
			return true
		}
	}
	return false
}

// Unstage retira el cambio pendiente de una celda.
func (s *EditService) Unstage(ctx context.Context, queryID string, stmtIndex, row, col int) (edit.Info, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.Info{}, err
	}
	t.buf.Unstage(row, col)
	t.plan.Pending = t.buf.PendingCount()
	return t.plan.Info, nil
}

// Discard descarta todos los cambios pendientes del resultado.
func (s *EditService) Discard(ctx context.Context, queryID string, stmtIndex int) (edit.Info, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.Info{}, err
	}
	t.buf.ClearPending()
	t.plan.Pending = 0
	return t.plan.Info, nil
}

// Preview devuelve el SQL de cada fila modificada, con los valores a la vista, para que el usuario lo revise antes de aplicar.
func (s *EditService) Preview(ctx context.Context, queryID string, stmtIndex int) ([]edit.Preview, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return nil, err
	}
	stmts := s.statements(t)
	out := make([]edit.Preview, len(stmts))
	for i, st := range stmts {
		out[i] = edit.Preview{Row: st.Row, Cols: st.Cols, SQL: st.Review}
	}
	return out, nil
}

// statements genera los UPDATE de los cambios pendientes.
func (s *EditService) statements(t target) []edit.Statement {
	pend := t.buf.PendingList()
	changes := make([]edit.Change, 0, len(pend))
	for _, p := range pend {
		if c, ok := p.Data.(edit.Change); ok {
			changes = append(changes, c)
		}
	}
	return edit.BuildUpdates(t.sess.Driver, t.plan.Database, t.plan.Table, t.plan.KeyTableCols, changes)
}

func notEditable(msg, reason string) error {
	return apperr.New(apperr.Validation, msg).WithDetail(reason)
}
