package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	if err != nil {
		return edit.Plan{}, err
	}
	t.plan.Info = t.info()
	return t.plan, nil
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
	return target{run: run, buf: buf, sess: sess, plan: plan}, nil
}

// info devuelve el veredicto con los contadores al día (celdas editadas + filas nuevas + filas a borrar).
func (t target) info() edit.Info {
	i := t.plan.Info
	i.PendingInserts, i.PendingDeletes = t.buf.InsertCount(), t.buf.DeleteCount()
	i.Pending = t.buf.PendingCount() + i.PendingInserts + i.PendingDeletes
	return i
}

// MaxPending es el tope de cambios sin aplicar por resultado: celdas editadas + filas nuevas + filas a borrar (spec 13 y 14).
const MaxPending = 1000

func (t target) total() int { return t.buf.PendingCount() + t.buf.InsertCount() + t.buf.DeleteCount() }

// Stage valida el valor escrito en una celda y lo deja como cambio pendiente (no toca el servidor). Si equivale al
// original, retira el cambio que hubiera. El valor nuevo se ve en `GetRows` con `Cell.Edited`. Si `row` es una fila nueva
// (índice tras la última real), da valor a esa celda de la fila nueva.
func (s *EditService) Stage(ctx context.Context, queryID string, stmtIndex, row, col int, v edit.Value) (edit.Info, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.Info{}, err
	}
	p := t.plan
	if !p.Editable {
		return t.info(), notEditable("No se puede editar este resultado", p.Reason)
	}
	if col < 0 || col >= len(p.Columns) {
		return t.info(), invalid("col", "La columna no existe")
	}
	if row >= t.buf.Len() {
		return s.stageNew(t, row, col, v)
	}
	if !p.Columns[col].Editable {
		return t.info(), notEditable("Esta columna no se puede editar", p.Columns[col].Reason)
	}
	if t.buf.IsDeleted(row) {
		return t.info(), notEditable("La fila está marcada para borrar", edit.ReasonRowDeleted)
	}
	orig, err := t.buf.Cell(row, col)
	if err != nil {
		return t.info(), invalid("row", "La celda no existe")
	}
	if orig.Trunc {
		return t.info(), notEditable("El valor es demasiado largo para editarlo aquí", "truncated")
	}
	meta := p.Meta[col]
	if edit.Same(meta, v, orig) {
		t.buf.Unstage(row, col)
		return t.info(), nil
	}
	arg, err := edit.Convert(meta, v)
	if err != nil {
		return t.info(), toAppErr(err)
	}
	if t.total() >= MaxPending && !hasPending(t.buf, row, col) {
		return t.info(), invalid("pending", "Hay demasiados cambios sin aplicar; aplícalos o descártalos antes de seguir")
	}
	origArg, origNull, err := edit.OriginalArg(meta, orig)
	if err != nil {
		return t.info(), toAppErr(err)
	}
	key, err := keyValues(t, row)
	if err != nil {
		return t.info(), err
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
		return t.info(), toAppErr(err)
	}
	return t.info(), nil
}

// keyValues devuelve los valores de clave de una fila real, tal como están en el buffer.
func keyValues(t target, row int) ([]any, error) {
	p := t.plan
	key := make([]any, len(p.KeyColumns))
	for i, kc := range p.KeyColumns {
		cell, err := t.buf.Cell(row, kc)
		if err != nil {
			return nil, invalid("row", "La fila no existe")
		}
		k, null, err := edit.OriginalArg(p.Meta[kc], cell)
		if err != nil || null {
			return nil, notEditable("La fila no tiene un valor de clave utilizable", "key_value")
		}
		key[i] = k
	}
	return key, nil
}

// cellTag da a la celda de una fila nueva el tipo de presentación que tendría si viniera del servidor.
func cellTag(dataType string) string {
	switch strings.ToLower(dataType) {
	case "json":
		return results.TJSON
	case "decimal", "numeric":
		return results.TDecimal
	case "date", "datetime", "timestamp", "time", "year":
		return results.TDatetime
	}
	return ""
}

// stageNew da valor a una celda de una fila nueva.
func (s *EditService) stageNew(t target, row, col int, v edit.Value) (edit.Info, error) {
	p := t.plan
	if !p.CanInsert {
		return t.info(), notEditable("No se pueden añadir filas a este resultado", p.InsertReason)
	}
	if !p.Columns[col].Insertable {
		return t.info(), notEditable("Esta columna no se puede escribir en una fila nueva", edit.ReasonNotInsertable)
	}
	arg, err := edit.Convert(p.Meta[col], v)
	if err != nil {
		return t.info(), toAppErr(err)
	}
	shown := results.Cell{T: cellTag(p.Meta[col].DataType)}
	if !v.Null {
		shown.V = fmt.Sprint(arg)
	}
	if err := t.buf.SetNewCell(row, col, shown, edit.NewCell{Column: p.TableCols[col], Value: v, Arg: arg}); err != nil {
		return t.info(), invalid("row", "La fila nueva no existe")
	}
	return t.info(), nil
}

func hasPending(buf *results.Buffer, row, col int) bool {
	for _, p := range buf.PendingList() {
		if p.Row == row && p.Col == col {
			return true
		}
	}
	return false
}

// Unstage retira el cambio pendiente de una celda; en una fila nueva la devuelve a «DEFAULT».
func (s *EditService) Unstage(ctx context.Context, queryID string, stmtIndex, row, col int) (edit.Info, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.Info{}, err
	}
	if row >= t.buf.Len() {
		t.buf.UnsetNewCell(row, col)
	} else {
		t.buf.Unstage(row, col)
	}
	return t.info(), nil
}

// Discard descarta todos los cambios pendientes del resultado: ediciones, filas nuevas y marcas de borrado.
func (s *EditService) Discard(ctx context.Context, queryID string, stmtIndex int) (edit.Info, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.Info{}, err
	}
	t.buf.ClearPending()
	return t.info(), nil
}

// AddRowResult es la fila nueva creada y el veredicto actualizado.
type AddRowResult struct {
	Rows []int     `json:"rows"` // índices absolutos de las filas nuevas
	Info edit.Info `json:"info"`
}

func (s *EditService) canInsert(t target) error {
	if !t.plan.Editable {
		return notEditable("No se puede editar este resultado", t.plan.Reason)
	}
	if !t.plan.CanInsert {
		return notEditable("No se pueden añadir filas a este resultado", t.plan.InsertReason)
	}
	return nil
}

// AddRow añade una fila nueva vacía (todas las celdas en «DEFAULT») al final del resultado.
func (s *EditService) AddRow(ctx context.Context, queryID string, stmtIndex int) (AddRowResult, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return AddRowResult{}, err
	}
	if err := s.canInsert(t); err != nil {
		return AddRowResult{Info: t.info()}, err
	}
	if t.total()+1 > MaxPending {
		return AddRowResult{Info: t.info()}, invalid("pending", "Hay demasiados cambios sin aplicar; aplícalos o descártalos antes de seguir")
	}
	row, err := t.buf.AddNewRow(len(t.plan.Columns))
	if err != nil {
		return AddRowResult{Info: t.info()}, invalid("row", "El resultado aún se está leyendo")
	}
	return AddRowResult{Rows: []int{row}, Info: t.info()}, nil
}

// DuplicateRows crea una fila nueva por cada fila indicada, con los valores que se ven (D8 del spec 14): no se copian las
// columnas autoincrementales, generadas, binarias ni las de una clave no autoincremental (quedan vacías; las obligatorias
// se marcan en el grid). Sirve de origen tanto una fila existente como una fila nueva.
func (s *EditService) DuplicateRows(ctx context.Context, queryID string, stmtIndex int, rows []int) (AddRowResult, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return AddRowResult{}, err
	}
	if err := s.canInsert(t); err != nil {
		return AddRowResult{Info: t.info()}, err
	}
	if len(rows) == 0 {
		return AddRowResult{Info: t.info()}, invalid("rows", "No hay filas que copiar")
	}
	if t.total()+len(rows) > MaxPending {
		return AddRowResult{Info: t.info()}, invalid("pending", "Hay demasiados cambios sin aplicar; aplícalos o descártalos antes de seguir")
	}
	isKey := map[int]bool{}
	for _, k := range t.plan.KeyColumns {
		isKey[k] = true
	}
	var created []int
	rollback := func() {
		for i := len(created) - 1; i >= 0; i-- {
			t.buf.RemoveNewRow(created[i])
		}
	}
	for _, src := range rows {
		values, err := s.sourceValues(t, src)
		if err != nil {
			rollback()
			return AddRowResult{Info: t.info()}, err
		}
		dst, err := t.buf.AddNewRow(len(t.plan.Columns))
		if err != nil {
			rollback()
			return AddRowResult{Info: t.info()}, invalid("row", "El resultado aún se está leyendo")
		}
		created = append(created, dst)
		for c, v := range values {
			if !t.plan.Columns[c].Insertable || isKey[c] {
				continue
			}
			arg, err := edit.Convert(t.plan.Meta[c], v)
			if err != nil {
				rollback()
				return AddRowResult{Info: t.info()}, invalid("cell", fmt.Sprintf("No se pudo copiar el valor de «%s»: %s", t.plan.Meta[c].Name, apperr.From(err).Message))
			}
			shown := results.Cell{T: cellTag(t.plan.Meta[c].DataType)}
			if !v.Null {
				shown.V = fmt.Sprint(arg)
			}
			if err := t.buf.SetNewCell(dst, c, shown, edit.NewCell{Column: t.plan.TableCols[c], Value: v, Arg: arg}); err != nil {
				rollback()
				return AddRowResult{Info: t.info()}, toAppErr(err)
			}
		}
	}
	return AddRowResult{Rows: created, Info: t.info()}, nil
}

// sourceValues devuelve, por columna del resultado, el valor que se ve en la fila origen (con sus ediciones pendientes).
func (s *EditService) sourceValues(t target, row int) (map[int]edit.Value, error) {
	out := map[int]edit.Value{}
	real := t.buf.Len()
	switch {
	case row >= real: // otra fila nueva
		for _, n := range t.buf.NewRows() {
			if n.Row != row {
				continue
			}
			for c, set := range n.Set {
				if nc, ok := n.Data[c].(edit.NewCell); ok && set {
					out[c] = nc.Value
				}
			}
			return out, nil
		}
		return nil, invalid("row", "La fila no existe")
	case row < 0:
		return nil, invalid("row", "La fila no existe")
	}
	pending := map[int]edit.Value{}
	for _, p := range t.buf.PendingList() {
		if ch, ok := p.Data.(edit.Change); ok && p.Row == row {
			pending[p.Col] = ch.Value
		}
	}
	for c := range t.plan.Columns {
		if v, ok := pending[c]; ok {
			out[c] = v
			continue
		}
		cell, err := t.buf.Cell(row, c)
		if err != nil {
			return nil, invalid("row", "La fila no existe")
		}
		if cell.V == nil {
			out[c] = edit.Value{Null: true}
		} else {
			out[c] = edit.Value{Text: fmt.Sprint(cell.Full())}
		}
	}
	return out, nil
}

// MarkDelete marca filas para borrar. En una fila nueva, «borrar» la descarta. Las ediciones pendientes de una fila
// marcada se pierden. Se captura la clave y los valores verificables para detectar, al aplicar, que otra sesión la cambió.
func (s *EditService) MarkDelete(ctx context.Context, queryID string, stmtIndex int, rows []int) (edit.Info, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.Info{}, err
	}
	if !t.plan.Editable {
		return t.info(), notEditable("No se puede editar este resultado", t.plan.Reason)
	}
	real := t.buf.Len()
	// Las nuevas se descartan de la última a la primera para que los índices pendientes no se desplacen.
	sorted := append([]int(nil), rows...)
	sort.Sort(sort.Reverse(sort.IntSlice(sorted)))
	for _, row := range sorted {
		if row >= real {
			t.buf.RemoveNewRow(row)
			continue
		}
		change, err := s.deleteChange(t, row)
		if err != nil {
			return t.info(), err
		}
		if err := t.buf.MarkDelete(row, change); err != nil {
			return t.info(), invalid("row", "La fila no existe")
		}
	}
	return t.info(), nil
}

// UnmarkDelete quita la marca de borrado de las filas indicadas.
func (s *EditService) UnmarkDelete(ctx context.Context, queryID string, stmtIndex int, rows []int) (edit.Info, error) {
	t, err := s.load(ctx, queryID, stmtIndex)
	if err != nil {
		return edit.Info{}, err
	}
	for _, r := range rows {
		t.buf.UnmarkDelete(r)
	}
	return t.info(), nil
}

// maxVerifyBytes es lo más largo que puede ser un valor original para incluirse en la condición del DELETE.
const maxVerifyBytes = 256

func (s *EditService) deleteChange(t target, row int) (edit.DeleteChange, error) {
	key, err := keyValues(t, row)
	if err != nil {
		return edit.DeleteChange{}, err
	}
	p := t.plan
	isKey := map[int]bool{}
	for _, k := range p.KeyColumns {
		isKey[k] = true
	}
	d := edit.DeleteChange{Row: row, Key: key}
	for c, name := range p.TableCols {
		meta := p.Meta[c]
		if name == "" || isKey[c] || meta.IsGenerated || !edit.Verifiable(meta.DataType) {
			continue
		}
		cell, err := t.buf.Cell(row, c)
		if err != nil || cell.Trunc {
			continue
		}
		if text, ok := cell.V.(string); ok && len(text) > maxVerifyBytes {
			continue
		}
		arg, null, err := edit.OriginalArg(meta, cell)
		if err != nil {
			continue
		}
		d.Verify = append(d.Verify, edit.VerifyCol{Column: name, Arg: arg, Null: null})
	}
	return d, nil
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
		out[i] = edit.Preview{Kind: st.Kind, Row: st.Row, Cols: st.Cols, SQL: st.Review}
	}
	return out, nil
}

// statements genera las sentencias de los cambios pendientes en el orden en que se aplican (D3 del spec 14): primero los
// UPDATE, luego los DELETE y al final los INSERT, así se puede borrar y volver a insertar la misma clave en un lote.
func (s *EditService) statements(t target) []edit.Statement {
	pend := t.buf.PendingList()
	changes := make([]edit.Change, 0, len(pend))
	for _, p := range pend {
		if c, ok := p.Data.(edit.Change); ok {
			changes = append(changes, c)
		}
	}
	out := edit.BuildUpdates(t.sess.Driver, t.plan.Database, t.plan.Table, t.plan.KeyTableCols, changes)

	var deletes []edit.DeleteChange
	for _, d := range t.buf.DeletedRows() {
		if c, ok := d.Data.(edit.DeleteChange); ok {
			deletes = append(deletes, c)
		}
	}
	out = append(out, edit.BuildDeletes(t.sess.Driver, t.plan.Database, t.plan.Table, t.plan.KeyTableCols, deletes)...)

	var inserts []edit.InsertRow
	for _, n := range t.buf.NewRows() {
		row := edit.InsertRow{Row: n.Row}
		for c, set := range n.Set {
			if nc, ok := n.Data[c].(edit.NewCell); ok && set {
				row.Cells = append(row.Cells, edit.InsertCell{Col: c, Column: nc.Column, Arg: nc.Arg})
			}
		}
		inserts = append(inserts, row)
	}
	return append(out, edit.BuildInserts(t.sess.Driver, t.plan.Database, t.plan.Table, inserts)...)
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

	if err := s.checkNewRows(t); err != nil {
		return edit.ApplyResult{}, err
	}
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
	if _, _, err := conn.Exec(ctx, "SET SESSION sql_mode = CONCAT(@@sql_mode, ',STRICT_ALL_TABLES')"); err != nil {
		return edit.ApplyResult{}, toAppErr(err)
	}
	if err := conn.Begin(ctx); err != nil {
		return edit.ApplyResult{}, toAppErr(err)
	}

	started := time.Now()
	insertedID := map[int]int64{} // fila nueva → ID autoincremental que le dio el servidor
	for _, st := range stmts {
		n, id, err := conn.Exec(ctx, st.SQL, st.Args...)
		if err != nil {
			_ = conn.Rollback(ctx)
			s.record(t, st, started, err)
			ae := apperr.From(err)
			return edit.ApplyResult{}, apperr.Wrap(ae.Code, fmt.Sprintf("%s: %s", rowLabel(t, st), ae.Message), err).WithDetail(ae.Detail)
		}
		if n == 0 && st.Kind != edit.KindInsert {
			_ = conn.Rollback(ctx)
			c := &edit.Conflict{Row: st.Row, Reason: edit.ConflictChanged}
			if !s.rowExists(ctx, conn, t, st) {
				c.Reason = edit.ConflictMissing
			}
			return edit.ApplyResult{Conflict: c}, nil
		}
		if st.Kind == edit.KindInsert {
			insertedID[st.Row] = id
		}
	}
	if err := conn.Commit(ctx); err != nil {
		return edit.ApplyResult{}, toAppErr(err)
	}
	var res edit.ApplyResult
	var updated, deleted []int
	for _, st := range stmts {
		s.record(t, st, started, nil)
		switch st.Kind {
		case edit.KindUpdate:
			updated = append(updated, st.Row)
		case edit.KindDelete:
			deleted = append(deleted, st.Row)
		case edit.KindInsert:
			res.Inserted++
		}
	}
	res.Applied, res.Deleted = len(updated), len(deleted)

	// El buffer pasa a reflejar el servidor: primero las filas editadas (con los índices de antes de borrar), luego se
	// quitan las borradas y al final se añaden las nuevas ya releídas.
	newRows := t.buf.NewRows()
	t.buf.CommitPending()
	s.reread(ctx, conn, t, updated)
	t.buf.RemoveRows(deleted)
	s.appendInserted(ctx, conn, t, newRows, insertedID)
	t.buf.ClearPending()

	if s.d.Emit != nil {
		s.d.Emit("edit:applied", EditApplied{QueryID: queryID, StmtIndex: stmtIndex, Rows: len(stmts)})
	}
	return res, nil
}

// rowLabel dice a qué fila se refiere un error: «Fila 3» para una existente, «Fila nueva 2» para una inserción.
func rowLabel(t target, st edit.Statement) string {
	if st.Kind == edit.KindInsert {
		return fmt.Sprintf("Fila nueva %d", st.Row-t.buf.Len()+1)
	}
	return fmt.Sprintf("Fila %d", st.Row+1)
}

// checkNewRows comprueba, antes de abrir ninguna conexión, que las filas nuevas tienen valor en las columnas obligatorias.
func (s *EditService) checkNewRows(t target) error {
	for i, n := range t.buf.NewRows() {
		for c, col := range t.plan.Columns {
			if col.Required && !n.Set[c] {
				return invalid("required", fmt.Sprintf("Fila nueva %d: falta un valor para «%s»", i+1, t.plan.Meta[c].Name))
			}
		}
	}
	return nil
}

// appendInserted relee cada fila insertada por su clave y la añade al buffer. Si no se puede (clave desconocida o la
// consulta falla), se añade con lo que escribió el usuario: la fila existe en el servidor y debe verse.
func (s *EditService) appendInserted(ctx context.Context, conn driver.Conn, t target, rows []results.NewRowView, ids map[int]int64) {
	p := t.plan
	complete := true
	for _, c := range p.TableCols {
		if c == "" {
			complete = false
		}
	}
	for _, n := range rows {
		cells := s.rereadNew(ctx, conn, t, n, ids[n.Row], complete)
		if cells == nil {
			cells = make([]results.Cell, len(n.Cells))
			for c, cell := range n.Cells {
				if n.Set[c] {
					cell.Edited = false
					cells[c] = cell
				}
			}
		}
		t.buf.AppendRow(cells)
	}
}

func (s *EditService) rereadNew(ctx context.Context, conn driver.Conn, t target, n results.NewRowView, id int64, complete bool) []results.Cell {
	p := t.plan
	if !complete {
		return nil
	}
	key := make([]any, len(p.KeyTableCols))
	for i, name := range p.KeyTableCols {
		found := false
		for c, tc := range p.TableCols {
			if !strings.EqualFold(tc, name) {
				continue
			}
			switch {
			case n.Set[c]:
				if nc, ok := n.Data[c].(edit.NewCell); ok {
					key[i], found = nc.Arg, true
				}
			case p.Meta[c].IsAutoInc && id > 0:
				key[i], found = id, true
			}
		}
		if !found {
			return nil
		}
	}
	res, err := conn.Run(ctx, edit.RowSelect(t.sess.Driver, p.Database, p.Table, p.TableCols, p.KeyTableCols, key))
	if err != nil {
		return nil
	}
	defer res.Close()
	cols := res.Columns()
	if !res.Next() || len(cols) != len(p.TableCols) {
		return nil
	}
	vals := res.Row()
	cells := make([]results.Cell, len(vals))
	for i, v := range vals {
		cells[i] = results.Convert(v, cols[i].DataType)
	}
	return cells
}

// rowExists comprueba (fuera de la transacción, ya revertida) si la fila con esa clave sigue existiendo.
func (s *EditService) rowExists(ctx context.Context, conn driver.Conn, t target, st edit.Statement) bool {
	if st.Key == nil {
		return true
	}
	res, err := conn.Run(ctx, edit.RowSelect(t.sess.Driver, t.plan.Database, t.plan.Table, nil, t.plan.KeyTableCols, st.Key))
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

// FormatJSON da formato (sangría) a un texto JSON para el editor ampliado de celdas. No toca el servidor.
func (s *EditService) FormatJSON(_ context.Context, text string) (string, error) {
	out, err := edit.FormatJSON(text)
	return out, toAppErr(err)
}
