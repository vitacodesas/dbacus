package services

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/model"
	"tabulariswails/internal/results"
	"tabulariswails/internal/session"
	"tabulariswails/internal/sqlfmt"
	"tabulariswails/internal/sqlsplit"
)

const (
	defaultMaxRows   = 100_000
	maxWindow        = 1000
	progressEvery    = 100 * time.Millisecond
	cancelGrace      = 2 * time.Second // si KILL QUERY no basta, se cancela el ctx
	killTimeout      = 3 * time.Second
	maxTabsPersisted = 100
	maxTabSQLBytes   = 5 << 20
)

type ExecuteRequest struct {
	SessionID string `json:"sessionId"`
	TabID     string `json:"tabId"`
	SQL       string `json:"sql"`
	Database  string `json:"database"`  // base a usar (USE) antes de ejecutar; "" = la actual de la pestaña
	MaxRows   int    `json:"maxRows"`   // 0 = ajuste del usuario
	AutoLimit int    `json:"autoLimit"` // >0 añade LIMIT n a SELECT simples sin LIMIT
}

type ExecuteResponse struct {
	QueryID string `json:"queryId"`
}

// StatementResult describe una sentencia ejecutada; las filas se piden aparte con GetRows (ADR-002).
type StatementResult struct {
	Index        int                `json:"index"`
	Kind         string             `json:"kind"` // "rows" | "exec"
	Columns      []model.ColumnMeta `json:"columns"`
	RowCount     int64              `json:"rowCount"`
	Truncated    bool               `json:"truncated"`
	AffectedRows int64              `json:"affectedRows"`
	LastInsertID int64              `json:"lastInsertId"`
	Warnings     int                `json:"warnings"`
	ElapsedMs    int64              `json:"elapsedMs"`
	SQL          string             `json:"sql"`
	LimitApplied int                `json:"limitApplied"` // LIMIT automático añadido (0 = ninguno)
	Start        int                `json:"start"`        // posición en el SQL enviado (UTF-16)
	End          int                `json:"end"`
}

type RowWindow struct {
	Offset   int              `json:"offset"`
	Rows     [][]results.Cell `json:"rows"`
	Total    int64            `json:"total"`
	Complete bool             `json:"complete"`
}

// FullCell es el valor completo de una celda truncada.
type FullCell struct {
	V any    `json:"v"`
	T string `json:"t"`
}

type StatementRange struct {
	Text  string `json:"text"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Eventos Go → Vue (spec 02).
type QueryStarted struct {
	QueryID   string    `json:"queryId"`
	TabID     string    `json:"tabId"`
	SessionID string    `json:"sessionId"`
	StartedAt time.Time `json:"startedAt"`
}
type QueryStatement struct {
	QueryID string          `json:"queryId"`
	Result  StatementResult `json:"result"`
}
type QueryProgress struct {
	QueryID   string `json:"queryId"`
	StmtIndex int    `json:"stmtIndex"`
	RowCount  int64  `json:"rowCount"`
}
type QueryDone struct {
	QueryID   string            `json:"queryId"`
	Results   []StatementResult `json:"results"`
	ElapsedMs int64             `json:"elapsedMs"`
	Database  string            `json:"database"` // base vigente al terminar (cambia con USE)
}
type QueryError struct {
	QueryID   string           `json:"queryId"`
	StmtIndex int              `json:"stmtIndex"` // -1 si el error no es de una sentencia concreta
	Error     *apperr.AppError `json:"error"`
	Start     int              `json:"start"`
	End       int              `json:"end"`
}

func init() {
	application.RegisterEvent[QueryStarted]("query:started")
	application.RegisterEvent[QueryStatement]("query:statement")
	application.RegisterEvent[QueryProgress]("query:progress")
	application.RegisterEvent[QueryDone]("query:done")
	application.RegisterEvent[QueryError]("query:error")
}

// QueryStore es lo que QueryService necesita de la persistencia (lo implementa *store.Store).
type QueryStore interface {
	GetSettings(ctx context.Context) (model.Settings, error)
	SaveTabs(ctx context.Context, tabs []model.TabState) error
	LoadTabs(ctx context.Context) ([]model.TabState, error)
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
}

type QueryDeps struct {
	Sessions *session.Manager
	Results  *results.Manager
	Schema   *SchemaService // para invalidar la caché tras DDL; puede ser nil en tests
	Store    QueryStore
	History  func(model.HistoryEntry) // opcional; se llama fuera del camino crítico
	Emit     func(name string, data any)
}

// QueryService ejecuta SQL en goroutines, guarda los resultados en Go y avisa a Vue por eventos (spec 06, ADR-002).
type QueryService struct {
	d QueryDeps

	baseCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu       sync.Mutex
	byQuery  map[string]*activeRun
	byTab    map[string]*activeRun
	tabDB    map[string]tabDBState // base vigente en la conexión de cada pestaña
	tabOwner map[string]string     // pestaña → sesión
}

func NewQueryService(d QueryDeps) *QueryService {
	ctx, cancel := context.WithCancel(context.Background())
	return &QueryService{
		d: d, baseCtx: ctx, cancel: cancel,
		byQuery: map[string]*activeRun{}, byTab: map[string]*activeRun{}, tabDB: map[string]tabDBState{}, tabOwner: map[string]string{},
	}
}

// ServiceShutdown (hook de Wails, no se enlaza a Vue) cancela las ejecuciones en curso y espera a que terminen (se registra en el shutdown de la app).
func (s *QueryService) ServiceShutdown() error {
	s.cancel()
	s.wg.Wait()
	return nil
}

type activeRun struct {
	queryID, tabID, sessionID string
	cancel                    context.CancelFunc
	canceled                  atomic.Bool
	done                      chan struct{}

	mu   sync.Mutex
	conn driver.Conn
}

func (r *activeRun) setConn(c driver.Conn) {
	r.mu.Lock()
	r.conn = c
	r.mu.Unlock()
}
func (r *activeRun) getConn() driver.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn
}

func (s *QueryService) emit(name string, data any) {
	if s.d.Emit != nil {
		s.d.Emit(name, data)
	}
}

// Execute valida y lanza la ejecución; devuelve de inmediato y el trabajo sigue en una goroutine.
func (s *QueryService) Execute(ctx context.Context, req ExecuteRequest) (ExecuteResponse, error) {
	sess, err := s.d.Sessions.Get(req.SessionID)
	if err != nil {
		return ExecuteResponse{}, toAppErr(err)
	}
	if strings.TrimSpace(req.TabID) == "" {
		return ExecuteResponse{}, invalid("tabId", "Falta la pestaña")
	}
	stmts := sqlsplit.Split(req.SQL)
	if len(stmts) == 0 {
		return ExecuteResponse{}, invalid("sql", "No hay nada que ejecutar")
	}

	// El modo se lee del perfil guardado en cada ejecución: marcar «Solo lectura» con una sesión ya abierta protege al
	// instante (la sesión del servidor sigue en lectura-escritura hasta reconectar, pero Go ya no deja pasar escrituras).
	// El sentido contrario (quitar el modo) exige reconectar: la sesión conserva el flag con el que se abrió.
	readOnly := sess.ReadOnly
	if p, err := s.d.Store.GetProfile(ctx, sess.ProfileID); err == nil && p.ReadOnly {
		readOnly = true
	}
	if readOnly {
		for _, st := range stmts {
			if !sqlsplit.IsReadOnlySafe(st.Text) {
				return ExecuteResponse{}, apperr.New(apperr.QueryFailed, "Esta conexión es de solo lectura: solo se permiten SELECT, SHOW, DESCRIBE y EXPLAIN").WithDetail("readonly")
			}
		}
	}

	settings, _ := s.d.Store.GetSettings(ctx)
	if settings.MaxRows <= 0 {
		settings = model.DefaultSettings()
	}
	maxRows := req.MaxRows
	if maxRows <= 0 {
		maxRows = settings.MaxRows
	}
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	s.d.Results.SetLimit(int64(settings.MaxResultMemoryMiB) << 20)

	runCtx, cancel := context.WithCancel(sess.Context())
	stopAfter := context.AfterFunc(s.baseCtx, cancel) // el cierre del servicio también cancela las ejecuciones
	ar := &activeRun{queryID: uuid.NewString(), tabID: req.TabID, sessionID: req.SessionID, cancel: cancel, done: make(chan struct{})}

	s.mu.Lock()
	if s.byTab[req.TabID] != nil {
		s.mu.Unlock()
		stopAfter()
		cancel()
		return ExecuteResponse{}, apperr.New(apperr.QueryFailed, "Ya hay una ejecución en curso en esta pestaña").WithDetail("busy")
	}
	s.byTab[req.TabID], s.byQuery[ar.queryID], s.tabOwner[req.TabID] = ar, ar, req.SessionID
	s.mu.Unlock()

	// La ejecución anterior de la pestaña ya no se puede ver: libera su memoria.
	s.d.Results.ReleaseTab(req.TabID)
	run := s.d.Results.NewRun(ar.queryID, req.TabID)
	run.SessionID = req.SessionID

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			stopAfter()
			run.MarkDone()
			cancel()
			s.mu.Lock()
			delete(s.byQuery, ar.queryID)
			if s.byTab[req.TabID] == ar {
				delete(s.byTab, req.TabID)
			}
			s.mu.Unlock()
			close(ar.done)
		}()
		s.execute(runCtx, ar, sess, run, req, stmts, maxRows)
	}()
	return ExecuteResponse{QueryID: ar.queryID}, nil
}

func (s *QueryService) execute(ctx context.Context, ar *activeRun, sess *session.Session, run *results.Run, req ExecuteRequest, stmts []sqlsplit.Statement, maxRows int) {
	started := time.Now()
	s.emit("query:started", QueryStarted{QueryID: ar.queryID, TabID: ar.tabID, SessionID: ar.sessionID, StartedAt: started})

	fail := func(idx int, err error, st *sqlsplit.Statement) {
		ae := apperr.From(err)
		if ar.canceled.Load() {
			ae = apperr.Wrap(apperr.QueryCanceled, "Consulta cancelada", err)
		}
		ev := QueryError{QueryID: ar.queryID, StmtIndex: idx, Error: ae}
		if st != nil {
			ev.Start, ev.End = st.Start, st.End
		}
		s.emit("query:error", ev)
	}

	conn, err := sess.AcquireConn(ctx, ar.tabID)
	if err != nil {
		fail(-1, err, nil)
		return
	}
	ar.setConn(conn)

	s.mu.Lock()
	prev, known := s.tabDB[ar.tabID]
	s.mu.Unlock()
	// La base recordada solo vale para la conexión del servidor en la que se hizo el USE: tras reconectar la pestaña
	// recibe otra conexión (otra sesión y otro ID) que arranca con la base por defecto del perfil.
	curDB := sess.CurrentDatabase
	if known && prev.sessionID == sess.ID && prev.connID == conn.ID() {
		curDB = prev.db
	}
	if req.Database != "" && req.Database != curDB {
		if err := conn.Use(ctx, req.Database); err != nil {
			fail(-1, err, nil)
			return
		}
		curDB = req.Database
	}
	s.setTabDB(ar.tabID, sess.ID, conn.ID(), curDB)

	var all []StatementResult
	for i := range stmts {
		st := &stmts[i]
		res, err := s.runStatement(ctx, ar, conn, sess, run, req, st, i, maxRows, curDB)
		if err != nil {
			fail(i, err, st)
			return
		}
		all = append(all, res)
		if isUse(st.Text) {
			if db := s.currentDatabase(ctx, conn); db != "" {
				curDB = db
				s.setTabDB(ar.tabID, sess.ID, conn.ID(), db)
			}
		}
		// Un SLEEP() interrumpido por KILL no devuelve error: se detecta por la marca de cancelación.
		if ar.canceled.Load() {
			fail(i, context.Canceled, st)
			return
		}
	}
	s.emit("query:done", QueryDone{QueryID: ar.queryID, Results: all, ElapsedMs: time.Since(started).Milliseconds(), Database: curDB})
}

func (s *QueryService) runStatement(ctx context.Context, ar *activeRun, conn driver.Conn, sess *session.Session, run *results.Run,
	req ExecuteRequest, st *sqlsplit.Statement, idx, maxRows int, db string) (StatementResult, error) {

	text, applied := st.Text, 0
	if req.AutoLimit > 0 {
		if t, ok := sqlsplit.InjectLimit(text, req.AutoLimit); ok {
			text, applied = t, req.AutoLimit
		}
	}
	started := time.Now()
	out := StatementResult{Index: idx, SQL: st.Text, LimitApplied: applied, Start: st.Start, End: st.End}

	record := func(err error) {
		if s.d.History == nil {
			return
		}
		e := model.HistoryEntry{
			ProfileID: sess.ProfileID, SQL: st.Text, Database: db, ExecutedAt: started.UTC(), ElapsedMs: time.Since(started).Milliseconds(),
			OK: err == nil, RowCount: out.RowCount, AffectedRows: out.AffectedRows,
		}
		if err != nil {
			e.ErrorMessage = apperr.From(err).Message
		}
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.d.History(e) }()
	}

	res, err := conn.Run(ctx, text)
	if err != nil {
		record(err)
		return out, err
	}
	defer res.Close()

	cols := res.Columns()
	if len(cols) == 0 { // INSERT/UPDATE/DDL (o CALL sin resultado)
		rows, last, warns := res.Affected()
		out.Kind, out.AffectedRows, out.LastInsertID, out.Warnings = "exec", rows, last, warns
		out.ElapsedMs = time.Since(started).Milliseconds()
		run.NewBuffer(nil).Finish(false) // mantiene alineados los índices de sentencia con el resultado
		s.emit("query:statement", QueryStatement{QueryID: ar.queryID, Result: out})
		s.afterStatement(sess, st.Text)
		record(nil)
		return out, nil
	}

	buf := run.NewBuffer(cols)
	buf.SetSource(st.Text, db)
	out.Kind, out.Columns = "rows", cols
	s.emit("query:statement", QueryStatement{QueryID: ar.queryID, Result: out})

	var n int64
	truncated := false
	lastProgress := time.Now()
	for res.Next() {
		row := res.Row()
		cells := make([]results.Cell, len(row))
		for j, v := range row {
			cells[j] = results.Convert(v, cols[j].DataType)
		}
		if !buf.Append(cells) {
			truncated = true
			break
		}
		n++
		if now := time.Now(); now.Sub(lastProgress) >= progressEvery {
			lastProgress = now
			s.emit("query:progress", QueryProgress{QueryID: ar.queryID, StmtIndex: idx, RowCount: n})
		}
		if n >= int64(maxRows) {
			// ¿Había más filas? Se intenta leer una más para no marcar como truncado un resultado que justo cabía.
			if res.Next() {
				truncated = true
			}
			break
		}
	}
	if truncated {
		// Cerrar un cursor con filas pendientes obliga al driver a leerlas todas: se aborta la consulta antes.
		kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), killTimeout)
		_ = conn.Kill(kctx)
		cancel()
	} else if err := res.Err(); err != nil {
		buf.Finish(false)
		out.RowCount = int64(buf.Len())
		record(err)
		return out, err
	}
	buf.Finish(truncated)
	out.RowCount, out.Truncated = int64(buf.Len()), buf.Truncated()
	out.ElapsedMs = time.Since(started).Milliseconds()
	s.emit("query:progress", QueryProgress{QueryID: ar.queryID, StmtIndex: idx, RowCount: out.RowCount})
	s.afterStatement(sess, st.Text)
	record(nil)
	return out, nil
}

// afterStatement invalida la caché de esquema tras DDL (spec 05).
func (s *QueryService) afterStatement(sess *session.Session, text string) {
	if s.d.Schema != nil && sqlsplit.IsDDL(sqlsplit.FirstKeyword(text)) {
		_ = s.d.Schema.invalidate(sess.ID, "")
	}
}

func isUse(text string) bool { return sqlsplit.FirstKeyword(text) == "USE" }

func (s *QueryService) currentDatabase(ctx context.Context, conn driver.Conn) string {
	res, err := conn.Run(ctx, "SELECT DATABASE()")
	if err != nil {
		return ""
	}
	defer res.Close()
	if res.Next() {
		if row := res.Row(); len(row) == 1 {
			switch v := row[0].(type) {
			case string:
				return v
			case []byte:
				return string(v)
			}
		}
	}
	return ""
}

// tabDBState es la base vigente de una pestaña y la conexión (sesión + CONNECTION_ID del servidor) en la que está.
type tabDBState struct{ sessionID, connID, db string }

func (s *QueryService) setTabDB(tabID, sessionID, connID, db string) {
	s.mu.Lock()
	s.tabDB[tabID] = tabDBState{sessionID, connID, db}
	s.mu.Unlock()
}

// Cancel aborta una ejecución. Primero KILL QUERY (conserva la conexión y sus variables de sesión, ADR-004); solo si no
// basta se cancela el contexto, que el driver resuelve cerrando la conexión. Es idempotente.
func (s *QueryService) Cancel(queryID string) error {
	s.mu.Lock()
	ar := s.byQuery[queryID]
	s.mu.Unlock()
	if ar == nil {
		return nil
	}
	ar.canceled.Store(true)
	if conn := ar.getConn(); conn != nil {
		kctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), killTimeout)
		go func() {
			defer cancel()
			if err := conn.Kill(kctx); err != nil {
				slog.Warn("KILL QUERY falló; se cancela el contexto", "err", err)
				ar.cancel()
			}
		}()
	} else {
		ar.cancel() // aún no hay conexión: nada que matar en el servidor
	}
	time.AfterFunc(cancelGrace, func() {
		select {
		case <-ar.done:
		default:
			ar.cancel()
		}
	})
	return nil
}

func (s *QueryService) GetRows(queryID string, stmtIndex, offset, limit int) (RowWindow, error) {
	buf, err := s.buffer(queryID, stmtIndex)
	if err != nil {
		return RowWindow{}, err
	}
	if limit <= 0 || limit > maxWindow {
		limit = maxWindow
	}
	rows, total, complete, err := buf.Window(offset, limit)
	if err != nil {
		return RowWindow{}, toAppErr(err)
	}
	if offset < 0 {
		offset = 0
	}
	return RowWindow{Offset: offset, Rows: nonNilRows(rows), Total: int64(total), Complete: complete}, nil
}

func nonNilRows(r [][]results.Cell) [][]results.Cell {
	if r == nil {
		return [][]results.Cell{}
	}
	return r
}

// GetCell devuelve el valor completo de una celda (textos/binarios truncados en la ventana).
func (s *QueryService) GetCell(queryID string, stmtIndex, row, col int) (FullCell, error) {
	buf, err := s.buffer(queryID, stmtIndex)
	if err != nil {
		return FullCell{}, err
	}
	c, err := buf.Cell(row, col)
	if err != nil {
		if errors.Is(err, results.ErrExpired) {
			return FullCell{}, toAppErr(err)
		}
		return FullCell{}, invalid("cell", "La celda no existe")
	}
	return FullCell{V: c.Full(), T: c.T}, nil
}

func (s *QueryService) buffer(queryID string, stmtIndex int) (*results.Buffer, error) {
	run, err := s.d.Results.Get(queryID)
	if err != nil {
		return nil, toAppErr(err)
	}
	buf, err := run.Buffer(stmtIndex)
	return buf, toAppErr(err)
}

// ReleaseResult libera la memoria de una ejecución que la UI ya no muestra.
func (s *QueryService) ReleaseResult(queryID string) { s.d.Results.Release(queryID) }

// CloseTab cancela lo que la pestaña esté ejecutando, libera sus resultados y devuelve su conexión al pool.
func (s *QueryService) CloseTab(tabID string) error {
	s.mu.Lock()
	ar := s.byTab[tabID]
	sessionID := s.tabOwner[tabID]
	delete(s.tabDB, tabID)
	delete(s.tabOwner, tabID)
	s.mu.Unlock()

	if ar != nil {
		_ = s.Cancel(ar.queryID)
	}
	s.d.Results.ReleaseTab(tabID)

	sess, err := s.d.Sessions.Get(sessionID)
	if err != nil { // sin sesión no hay conexión que liberar
		return nil
	}
	if ar == nil {
		return toAppErr(sess.CloseTab(tabID))
	}
	// La conexión no se puede devolver mientras corre la consulta: se espera a que termine (acotado).
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case <-ar.done:
		case <-time.After(killTimeout + cancelGrace):
		}
		_ = sess.CloseTab(tabID)
	}()
	return nil
}

// StatementAt devuelve la sentencia bajo el cursor (offset en UTF-16, como Monaco).
func (s *QueryService) StatementAt(sql string, offset int) (StatementRange, error) {
	st, ok := sqlsplit.At(sqlsplit.Split(sql), offset)
	if !ok {
		return StatementRange{}, invalid("sql", "No hay ninguna sentencia")
	}
	return StatementRange{Text: st.Text, Start: st.Start, End: st.End}, nil
}

// FormatSQL formatea el script (cambia espacios y mayúsculas de palabras reservadas, nada más).
func (s *QueryService) FormatSQL(sql string) string { return sqlfmt.Format(sql) }

// SaveTabs / LoadTabs persisten las pestañas abiertas (spec 06).
func (s *QueryService) SaveTabs(ctx context.Context, tabs []model.TabState) error {
	if len(tabs) > maxTabsPersisted {
		return invalid("tabs", "Hay demasiadas pestañas")
	}
	for i := range tabs {
		if len(tabs[i].SQL) > maxTabSQLBytes {
			return invalid("tabs", "El SQL de una pestaña es demasiado grande para guardarlo")
		}
	}
	return toAppErr(s.d.Store.SaveTabs(ctx, tabs))
}

func (s *QueryService) LoadTabs(ctx context.Context) ([]model.TabState, error) {
	tabs, err := s.d.Store.LoadTabs(ctx)
	return tabs, toAppErr(err)
}
