package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"tabulariswails/internal/apperr"

	"tabulariswails/internal/driver"
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
	History  func(model.HistoryEntry)    // opcional: registra lo aplicado en el historial (spec 07)
	Emit     func(name string, data any) // opcional
}

// EditApplied avisa de que se aplicaron cambios en un resultado (spec 13): el grid vuelve a pedir sus ventanas.
type EditApplied struct {
	QueryID   string `json:"queryId"`
	StmtIndex int    `json:"stmtIndex"`
	Rows      int    `json:"rows"`
}

func init() { application.RegisterEvent[EditApplied]("edit:applied") }

// EditService expone la edición de celdas (spec 13). Go decide qué es editable, valida y genera el SQL; Vue solo pinta.
type EditService struct {
	d EditDeps

	mu       sync.Mutex
	applying map[string]bool // queryID:stmt con un Apply en curso
}

func NewEditService(d EditDeps) *EditService { return &EditService{d: d, applying: map[string]bool{}} }

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

// Apply aplica los cambios pendientes en una transacción (spec 13): un UPDATE por fila con control optimista. Si alguna
// fila no coincide (otra sesión la cambió o la borró) o el servidor rechaza algo, se revierte todo y no se aplica nada.
// Tras el commit se releen las filas tocadas, porque triggers, valores por defecto y `ON UPDATE` pueden haberlas cambiado.
func (s *EditService) Apply(ctx context.Context, queryID string, stmtIndex int) (edit.ApplyResult, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.ApplyResult{}, err
	}
	if !t.plan.Editable {
		if t.plan.Reason == edit.ReasonReadOnly {
			return edit.ApplyResult{}, apperr.New(apperr.QueryFailed, "Esta conexión es de solo lectura: no se pueden aplicar cambios").WithDetail("readonly")
		}
		return edit.ApplyResult{}, notEditable("No se puede editar este resultado", t.plan.Reason)
	}
	key := fmt.Sprintf("%s:%d", queryID, stmtIndex)
	s.mu.Lock()
	if s.applying[key] {
		s.mu.Unlock()
		return edit.ApplyResult{}, apperr.New(apperr.QueryFailed, "Ya se están aplicando los cambios de este resultado").WithDetail("busy")
	}
	s.applying[key] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.applying, key)
		s.mu.Unlock()
	}()

	stmts := s.statements(t)
	if len(stmts) == 0 {
		return edit.ApplyResult{}, nil
	}

	conn, err := t.sess.Pool.AcquireEdit(ctx)
	if err != nil {
		return edit.ApplyResult{}, toAppErr(err)
	}
	defer conn.Release()
	// Sin modo estricto el servidor recortaría o redondearía valores en silencio; aquí un valor que no cabe debe fallar.
	if _, err := conn.Exec(ctx, "SET SESSION sql_mode = CONCAT(@@sql_mode, ',STRICT_ALL_TABLES')"); err != nil {
		return edit.ApplyResult{}, toAppErr(err)
	}
	if err := conn.Begin(ctx); err != nil {
		return edit.ApplyResult{}, toAppErr(err)
	}

	started := time.Now()
	for _, st := range stmts {
		n, err := conn.Exec(ctx, st.SQL, st.Args...)
		if err != nil {
			_ = conn.Rollback(ctx)
			s.record(t, st, started, err)
			ae := apperr.From(err)
			return edit.ApplyResult{}, apperr.Wrap(ae.Code, fmt.Sprintf("Fila %d: %s", st.Row+1, ae.Message), err).WithDetail(ae.Detail)
		}
		if n == 0 {
			_ = conn.Rollback(ctx)
			c := &edit.Conflict{Row: st.Row, Reason: edit.ConflictChanged}
			if !s.rowExists(ctx, conn, t, st.Row) {
				c.Reason = edit.ConflictMissing
			}
			return edit.ApplyResult{Conflict: c}, nil
		}
	}
	if err := conn.Commit(ctx); err != nil {
		return edit.ApplyResult{}, toAppErr(err)
	}
	for _, st := range stmts {
		s.record(t, st, started, nil)
	}

	rows := rowsOf(stmts)
	t.buf.CommitPending() // el valor mostrado pasa a ser el real
	s.reread(ctx, conn, t, rows)
	if s.d.Emit != nil {
		s.d.Emit("edit:applied", EditApplied{QueryID: queryID, StmtIndex: stmtIndex, Rows: len(stmts)})
	}
	return edit.ApplyResult{Applied: len(stmts)}, nil
}

func rowsOf(stmts []edit.Statement) []int {
	out := make([]int, len(stmts))
	for i, st := range stmts {
		out[i] = st.Row
	}
	return out
}

// keyOf devuelve los valores de clave de una fila tal como se capturaron al editarla.
func keyOf(t target, row int) []any {
	for _, p := range t.buf.PendingList() {
		if c, ok := p.Data.(edit.Change); ok && c.Row == row {
			return c.Key
		}
	}
	return nil
}

// rowExists comprueba (fuera de la transacción, ya revertida) si la fila con esa clave sigue existiendo.
func (s *EditService) rowExists(ctx context.Context, conn driver.Conn, t target, row int) bool {
	key := keyOf(t, row)
	if key == nil {
		return true
	}
	res, err := conn.Run(ctx, edit.RowSelect(t.sess.Driver, t.plan.Database, t.plan.Table, nil, t.plan.KeyTableCols, key))
	if err != nil {
		return true // sin poder comprobarlo, se informa como «cambió»
	}
	defer res.Close()
	return res.Next()
}

// reread vuelve a leer las filas editadas y sustituye las del buffer. Es un refinamiento: si algo falla, el buffer ya
// tiene los valores aplicados (CommitPending) y la fila se queda como está.
func (s *EditService) reread(ctx context.Context, conn driver.Conn, t target, rows []int) {
	p := t.plan
	for _, c := range p.TableCols {
		if c == "" { // hay una columna del resultado que no es de la tabla (expresión): no se puede releer por nombre
			return
		}
	}
	for _, row := range rows {
		key := make([]any, len(p.KeyColumns))
		for i, kc := range p.KeyColumns {
			cell, err := t.buf.Cell(row, kc)
			if err != nil {
				return
			}
			k, _, err := edit.OriginalArg(p.Meta[kc], cell)
			if err != nil {
				return
			}
			key[i] = k
		}
		res, err := conn.Run(ctx, edit.RowSelect(t.sess.Driver, p.Database, p.Table, p.TableCols, p.KeyTableCols, key))
		if err != nil {
			continue
		}
		cols := res.Columns()
		if res.Next() && len(cols) == len(p.TableCols) {
			vals := res.Row()
			cells := make([]results.Cell, len(vals))
			for i, v := range vals {
				cells[i] = results.Convert(v, cols[i].DataType)
			}
			_ = t.buf.ReplaceRow(row, cells)
		}
		_ = res.Close()
	}
}

func (s *EditService) record(t target, st edit.Statement, started time.Time, err error) {
	if s.d.History == nil {
		return
	}
	e := model.HistoryEntry{
		ProfileID: t.sess.ProfileID, SQL: st.Review, Database: t.plan.Database, ExecutedAt: started.UTC(),
		ElapsedMs: time.Since(started).Milliseconds(), OK: err == nil,
	}
	if err == nil {
		e.AffectedRows = 1
	} else {
		e.ErrorMessage = apperr.From(err).Message
	}
	s.d.History(e)
}
