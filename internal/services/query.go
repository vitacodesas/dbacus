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

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/paging"
	"github.com/vitacodesas/dbacus/internal/results"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/sqlfmt"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
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
	LimitApplied int                `json:"limitApplied"` // LIMIT automático añadido (0 = ninguno); no se usa si hay paginación por cursor
	Paging       *PagingInfo        `json:"paging"`       // paginación por cursor (spec 18); nil si no es un SELECT o AutoLimit es 0
	Start        int                `json:"start"`        // posición en el SQL enviado (UTF-16)
	End          int                `json:"end"`
}

type RowWindow struct {
	Offset   int              `json:"offset"`
	Rows     [][]results.Cell `json:"rows"`
	Total    int64            `json:"total"`
	Complete bool             `json:"complete"`
	// Edición de filas (spec 14): índices absolutos de las filas de esta ventana marcadas para borrar, y el índice de la
	// primera fila nueva sin aplicar (-1 si no hay). El Total ya incluye las filas nuevas.
	Deleted []int `json:"deleted"`
	NewFrom int   `json:"newFrom"`
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
		// Si el contexto se canceló a la fuerza (el servidor no respondió al KILL), el driver cierra la conexión: se descarta
		// para que la siguiente ejecución de la pestaña abra una nueva en vez de reutilizar una muerta.
		if ctx.Err() != nil {
			_ = sess.CloseTab(ar.tabID)
		}
	}

	// prepare adquiere la conexión de la pestaña y deja puesta la base elegida. Si la conexión cacheada murió (p. ej. tras
	// cancelar a la fuerza una consulta que no respondió al KILL, o por un corte de red), se descarta y se pide otra una vez.
	prepare := func(retry bool) (driver.Conn, string, error) {
		conn, err := sess.AcquireConn(ctx, ar.tabID)
		if err != nil {
			return nil, "", err
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
				if retry && connLost(err) {
					_ = sess.CloseTab(ar.tabID)
					return nil, "", errRetry
				}
				return nil, "", err
			}
			curDB = req.Database
		}
		s.setTabDB(ar.tabID, sess.ID, conn.ID(), curDB)
		return conn, curDB, nil
	}
	conn, curDB, err := prepare(true)
	if errors.Is(err, errRetry) {
		conn, curDB, err = prepare(false)
	}
	if err != nil {
		fail(-1, err, nil)
		return
	}

	var all []StatementResult
	retried := false
	for i := 0; i < len(stmts); i++ {
		st := &stmts[i]
		res, err := s.runStatement(ctx, ar, conn, sess, run, req, st, i, maxRows, curDB, !retried && i == 0)
		if err != nil {
			if !retried && i == 0 && connLost(err) && ctx.Err() == nil {
				retried = true
				slog.Info("la conexión de la pestaña estaba cerrada; se abre otra", "tab", ar.tabID)
				_ = sess.CloseTab(ar.tabID)
				if conn, curDB, err = prepare(false); err != nil {
					fail(-1, err, nil)
					return
				}
				i--
				continue
			}
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

// errRetry indica que la conexión de la pestaña estaba muerta y se descartó: hay que pedir otra.
var errRetry = errors.New("conexión descartada")

// connLost dice si el error significa que la conexión ya no sirve (cerrada, cortada), no que la sentencia falló.
func connLost(err error) bool { return apperr.From(err).Code == apperr.SessionClosed }

// runStatement ejecuta una sentencia. Con allowRetry, un fallo por conexión perdida no se registra en el historial: el
// llamador volverá a intentarlo con otra conexión y lo que consta es el resultado final.
func (s *QueryService) runStatement(ctx context.Context, ar *activeRun, conn driver.Conn, sess *session.Session, run *results.Run,
	req ExecuteRequest, st *sqlsplit.Statement, idx, maxRows int, db string, allowRetry bool) (StatementResult, error) {

	text, applied := st.Text, 0
	started := time.Now()
	out := StatementResult{Index: idx, SQL: st.Text, Start: st.Start, End: st.End}

	// Paginación por cursor (spec 18): si la sentencia se puede paginar, se ejecuta su primera página con el SQL reescrito.
	// Si no, o si no se puede saber, se mantiene el LIMIT automático de siempre y se explica por qué no se pagina.
	var pstate *pageState
	if req.AutoLimit > 0 {
		size := pageSizeFor(req.AutoLimit, maxRows)
		dec, detail, known := s.planPaging(ctx, sess, st.Text, db, size)
		if known && dec.Mode == paging.ModeKeyset {
			n, probe := dec.Plan.Window(0)
			if probe {
				n++ // la fila de sondeo; con un LIMIT propio la última página llega justo hasta el tope y no lleva
			}
			if q, err := dec.Plan.BuildN(paging.First, nil, sess.Driver.QuoteIdent, n, 0); err == nil {
				text, pstate = q, newPageState(dec.Plan, detail, out)
			}
		} else if known {
			out.Paging = &PagingInfo{Mode: paging.ModeNone, Reason: dec.Reason, PageSize: size}
			// El analizador de una sola tabla se rinde en lo primero que no entiende (un alias, un JOIN); el LIMIT propio o
			// el INTO/FOR UPDATE que vengan después son el motivo que le sirve a quien lo lee.
			if dec.Reason != sqlsplit.ReasonNotSelect {
				if _, r := sqlsplit.Wrap(st.Text); r == sqlsplit.ReasonUserLimit || r == sqlsplit.ReasonIntoLock {
					out.Paging.Reason = r
				}
			}
		}
		if pstate == nil {
			if t, ok := sqlsplit.InjectLimit(text, req.AutoLimit); ok {
				text, applied = t, req.AutoLimit
			}
		}
	}
	out.LimitApplied = applied

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

	if pstate != nil && explainUsesFilesort(ctx, conn, text) {
		pstate.warning = warnNoIndex
	}
	res, err := conn.Run(ctx, text)
	if err != nil {
		if !(allowRetry && connLost(err)) {
			record(err)
		}
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
		out.Paging = nil
		return out, nil
	}

	buf := run.NewBuffer(cols)
	buf.SetSource(st.Text, db)
	out.Kind, out.Columns = "rows", cols
	s.emit("query:statement", QueryStatement{QueryID: ar.queryID, Result: out})

	if pstate != nil {
		r, err := s.firstPage(res, cols, buf, pstate, out, started)
		if err != nil {
			out.RowCount = int64(buf.Len())
			if !(allowRetry && connLost(err)) {
				record(err)
			}
			return out, err
		}
		out = r // el historial lee out.RowCount: va después de asignarlo
		s.afterStatement(sess, st.Text)
		record(nil)
		return out, nil
	}

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
	// Sin paginación por cursor pero con el resultado al límite (puede haber más filas): se ofrece el modo aproximado.
	if out.Paging != nil && applied > 0 && out.RowCount >= int64(applied) {
		if names := offerApprox(out.Paging, st.Text, cols); names != nil {
			buf.SetExt(&approxOffer{base: out, size: out.Paging.PageSize, candidates: names})
		}
	}
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
	v, err := buf.WindowView(offset, limit)
	if err != nil {
		return RowWindow{}, toAppErr(err)
	}
	if offset < 0 {
		offset = 0
	}
	deleted := v.Deleted
	if deleted == nil {
		deleted = []int{}
	}
	return RowWindow{Offset: offset, Rows: nonNilRows(v.Rows), Total: int64(v.Total), Complete: v.Complete, Deleted: deleted, NewFrom: v.NewFrom}, nil
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
