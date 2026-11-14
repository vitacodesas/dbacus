package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
)

const (
	maxAttempts   = 3
	progressEvery = 250 * time.Millisecond // como mucho 4 eventos por segundo (spec 23)
	batchBytes    = 2 << 20                // tope de un lote de la ruta por Go
	pipeDepth     = 2                      // lotes en vuelo por worker: acota la memoria (spec 23)
	planners      = 2                      // conexiones de planificación (⚑D4)
)

// Callbacks son los avisos del job; todos son opcionales y se llaman desde goroutines del job, nunca con su cerrojo tomado.
type Callbacks struct {
	OnProgress func(model.MigrationJob) // como mucho 4 por segundo
	OnState    func(model.MigrationJob) // cambios de estado y de fase
	OnDone     func(model.MigrationResult)
}

type tblState struct {
	t       model.MigrationTable
	state   string
	rows    int64
	done    int
	all     int
	planned bool
	err     string
	created bool
	skip    bool
	empty   bool            // solo estructura: se crea y no se copian filas (⚑D20)
	dirty   bool            // pudo quedar con filas a medias: se vacía antes de volver a copiarla
	fkDone  map[string]bool // claves foráneas ya añadidas (un Retry no las repite)
}

// Job es una migración en curso. Es dueño de todas sus goroutines: Wait no vuelve hasta que han terminado todas.
type Job struct {
	ID string
	p  *Prepared
	cb Callbacks

	sleep func(ctx context.Context, d time.Duration) error
	now   func() time.Time

	mu       sync.Mutex
	state    string
	phase    string
	started  time.Time
	ended    time.Time
	tables   map[string]*tblState
	order    []string
	objects  []*model.MigrationObjectState
	failed   []model.MigrationFailure
	created  []model.MigrationCreated
	rows     int64
	bytes    int64
	fatal    string
	verif    *model.MigrationVerification
	userStop bool
	cancel   context.CancelFunc
	active   map[driver.MigrationConn]struct{}
	ver      atomic.Uint64
	done     chan struct{}
	running  bool
}

// NewJob prepara un job a partir de un plan. No toca el servidor hasta Start.
func NewJob(id string, p *Prepared, cb Callbacks) *Job {
	j := &Job{
		ID: id, p: p, cb: cb, state: model.MigPlanning, tables: map[string]*tblState{}, active: map[driver.MigrationConn]struct{}{},
		sleep: sleepCtx, now: time.Now, done: make(chan struct{}),
	}
	for _, t := range p.Tables {
		j.tables[t.Name] = &tblState{t: t, state: model.TblPending, empty: p.StructureOnly[t.Name]}
		j.order = append(j.order, t.Name)
	}
	for _, n := range p.SkippedTables {
		j.tables[n] = &tblState{t: model.MigrationTable{Name: n}, state: model.TblSkipped, skip: true}
		j.order = append(j.order, n)
	}
	for _, o := range p.Objects() {
		j.objects = append(j.objects, &model.MigrationObjectState{Name: o.Name, Kind: o.Kind, State: "pending"})
	}
	return j
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetClock sustituye el reloj y la espera de los reintentos (para pruebas).
func (j *Job) SetClock(now func() time.Time, sleep func(context.Context, time.Duration) error) {
	if now != nil {
		j.now = now
	}
	if sleep != nil {
		j.sleep = sleep
	}
}

// Start lanza la migración en segundo plano. El contexto padre es el de la aplicación o el de la sesión de origen.
func (j *Job) Start(parent context.Context) error {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return apperr.New(apperr.QueryFailed, "La migración ya está en curso").WithDetail("busy")
	}
	j.running = true
	j.state = model.MigPlanning // tras un Retry el estado anterior (partial/failed) no debe verse como «terminada» antes de que arranque la goroutine
	j.done = make(chan struct{})
	ctx, cancel := context.WithCancel(parent)
	j.cancel = cancel
	if j.started.IsZero() {
		j.started = j.now()
	}
	j.mu.Unlock()
	go func() {
		defer close(j.done)
		defer cancel()
		j.execute(ctx)
	}()
	return nil
}

// Wait bloquea hasta que el job termina.
func (j *Job) Wait() { <-j.done }

// Cancel detiene la migración: primero KILL QUERY de las sentencias en curso (un INSERT … SELECT no notaría que el cliente
// se fue) y después cancela el contexto. El trozo en curso se revierte; el destino se queda como está.
func (j *Job) Cancel() {
	j.mu.Lock()
	if j.state == model.MigRunning || j.state == model.MigPlanning {
		j.state = model.MigCancelling
		j.userStop = true
	}
	conns := make([]driver.MigrationConn, 0, len(j.active))
	for c := range j.active {
		conns = append(conns, c)
	}
	cancel := j.cancel
	j.mu.Unlock()
	j.touch()
	killCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Add(1)
		go func(c driver.MigrationConn) { // solo los hilos de este job (spec 23 ⚑D19.5)
			defer wg.Done()
			_ = c.Kill(killCtx)
		}(c)
	}
	wg.Wait()
	if cancel != nil {
		cancel()
	}
}

func (j *Job) touch() { j.ver.Add(1) }

func (j *Job) setPhase(phase string) {
	j.mu.Lock()
	j.phase = phase
	j.mu.Unlock()
	j.touch()
	if j.cb.OnState != nil {
		j.cb.OnState(j.Snapshot())
	}
}

func (j *Job) setState(state string) {
	j.mu.Lock()
	j.state = state
	j.mu.Unlock()
	j.touch()
	if j.cb.OnState != nil {
		j.cb.OnState(j.Snapshot())
	}
}

// Snapshot es una copia consistente del estado.
func (j *Job) Snapshot() model.MigrationJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

func (j *Job) snapshotLocked() model.MigrationJob {
	end := j.ended
	if end.IsZero() {
		end = j.now()
	}
	out := model.MigrationJob{
		ID: j.ID, State: j.state, Phase: j.phase, Route: j.p.Plan.Route, StartedAt: j.started, Rows: j.rows, Bytes: j.bytes,
		TablesAll: 0, Tables: make([]model.MigrationTableState, 0, len(j.order)), Objects: make([]model.MigrationObjectState, 0, len(j.objects)), Error: j.fatal,
	}
	if !j.started.IsZero() {
		out.ElapsedMs = end.Sub(j.started).Milliseconds()
	}
	for _, n := range j.order {
		ts := j.tables[n]
		if ts.state == model.TblSkipped {
			out.Tables = append(out.Tables, model.MigrationTableState{Name: n, State: ts.state})
			continue
		}
		out.TablesAll++
		if ts.state == model.TblDone {
			out.TablesDone++
		}
		est := ts.t.EstRows
		if ts.empty {
			est = 0
		}
		out.EstRows += est
		out.Tables = append(out.Tables, model.MigrationTableState{
			Name: n, State: ts.state, StructureOnly: ts.empty, RowsDone: ts.rows, RowsEst: est, ChunksDone: ts.done, ChunksAll: ts.all, Error: ts.err,
		})
	}
	for _, o := range j.objects {
		out.Objects = append(out.Objects, *o)
	}
	return out
}

// Result es el estado final con los fallos, lo creado (para limpiar) y la verificación si la hubo.
func (j *Job) Result() model.MigrationResult {
	j.mu.Lock()
	defer j.mu.Unlock()
	return model.MigrationResult{
		Job: j.snapshotLocked(), Failed: append([]model.MigrationFailure{}, j.failed...), Created: append([]model.MigrationCreated{}, j.created...),
		Warnings: append([]model.MigrationWarning{}, j.p.Plan.Warnings...), Verification: j.verif,
	}
}

func (j *Job) fail(object, kind string, err error) {
	msg := errMessage(err)
	j.mu.Lock()
	j.failed = append(j.failed, model.MigrationFailure{Object: object, Kind: kind, Error: msg})
	j.mu.Unlock()
	j.touch()
}

func errMessage(err error) string {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		if ae.Detail != "" {
			return ae.Message + " (" + ae.Detail + ")"
		}
		return ae.Message
	}
	return err.Error()
}

func (j *Job) failTable(name string, err error) {
	j.mu.Lock()
	ts := j.tables[name]
	first := ts != nil && ts.state != model.TblFailed
	if ts != nil {
		ts.dirty = ts.dirty || ts.state == model.TblCopying
		ts.state = model.TblFailed
		ts.err = errMessage(err)
	}
	j.mu.Unlock()
	if first {
		j.fail(name, "table", err)
	}
	j.touch()
}

func retryable(err error) bool {
	var ae *apperr.AppError
	return errors.As(err, &ae) && ae.Retryable
}

// ---- registro de conexiones activas ----

func (j *Job) register(c driver.MigrationConn) {
	j.mu.Lock()
	j.active[c] = struct{}{}
	j.mu.Unlock()
}

func (j *Job) unregister(c driver.MigrationConn) {
	j.mu.Lock()
	delete(j.active, c)
	j.mu.Unlock()
	_ = c.Release()
}

func (j *Job) acquire(ctx context.Context, s driver.MigrationSession) (driver.MigrationConn, error) {
	c, err := s.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	j.register(c)
	return c, nil
}

type sess struct {
	server, source, target driver.MigrationSession // target = server en la ruta del servidor
	route                  string
}

func (j *Job) open(ctx context.Context, withData bool) (*sess, error) {
	p := j.p
	ss := &sess{route: p.Plan.Route}
	if p.Plan.Route == model.RouteServer {
		s, err := p.Target.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleServer, Database: p.TargetDB, Source: p.SourceDB, Size: p.Workers + planners, SQLMode: p.Src.SQLMode})
		if err != nil {
			return nil, err
		}
		ss.server, ss.target = s, s
		return ss, nil
	}
	dst, err := p.Target.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleTarget, Database: p.TargetDB, Size: p.Workers + 1, SQLMode: p.Src.SQLMode})
	if err != nil {
		return nil, err
	}
	src, err := p.Source.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleSource, Database: p.SourceDB, Size: p.Workers + planners, SQLMode: p.Src.SQLMode})
	if err != nil {
		_ = dst.Close()
		return nil, err
	}
	ss.source, ss.target = src, dst
	return ss, nil
}

func (s *sess) close() {
	if s == nil {
		return
	}
	if s.server != nil { // ruta en el servidor: server y target son la misma sesión
		_ = s.server.Close()
		return
	}
	if s.source != nil {
		_ = s.source.Close()
	}
	if s.target != nil {
		_ = s.target.Close()
	}
}

// reader devuelve la sesión que lee el origen.
func (s *sess) reader() driver.MigrationSession {
	if s.route == model.RouteServer {
		return s.server
	}
	return s.source
}

// ---- ejecución ----

func (j *Job) execute(ctx context.Context) {
	defer j.finish()
	j.setState(model.MigRunning)
	if err := j.run(ctx); err != nil && ctx.Err() == nil {
		j.mu.Lock()
		j.fatal = errMessage(err)
		j.mu.Unlock()
		j.fail("", "schema", err)
	}
	if ctx.Err() != nil {
		j.mu.Lock()
		if !j.userStop {
			j.fatal = "La migración se interrumpió: se cerró la sesión o la aplicación"
		}
		j.mu.Unlock()
	}
	j.touch()
}

func (j *Job) run(ctx context.Context) error {
	p := j.p
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // agregador de progreso: como mucho 4 eventos por segundo
		defer wg.Done()
		tk := time.NewTicker(progressEvery)
		defer tk.Stop()
		var last uint64
		for {
			select {
			case <-tk.C:
				if v := j.ver.Load(); v != last {
					last = v
					if j.cb.OnProgress != nil {
						j.cb.OnProgress(j.Snapshot())
					}
				}
			case <-stop:
				return
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	// Fase A: schema destino.
	j.setPhase(model.PhaseSchema)
	if p.TargetNew && !j.hasCreated("database", p.TargetDB) {
		created, err := p.Target.MigrationEnsureDatabase(ctx, p.TargetDB, p.Src.Charset, p.Src.Collation)
		if err != nil {
			return err
		}
		if created {
			j.addCreated("database", p.TargetDB)
		}
	}
	ss, err := j.open(ctx, true)
	if err != nil {
		return err
	}
	defer ss.close()

	if err := j.phaseTables(ctx, ss); err != nil {
		return err
	}
	if err := j.phaseData(ctx, ss); err != nil {
		return err
	}
	if err := j.phaseKeys(ctx, ss); err != nil {
		return err
	}
	if err := j.phaseObjects(ctx, ss); err != nil {
		return err
	}
	if p.Req.Options.VerifyForeignKeys || p.Req.Options.VerifyRowCounts {
		j.setPhase(model.PhaseVerify)
		v, err := j.verify(ctx, ss, model.VerifyOptions{ForeignKeys: p.Req.Options.VerifyForeignKeys, RowCounts: p.Req.Options.VerifyRowCounts})
		if err != nil {
			return err
		}
		j.mu.Lock()
		j.verif = v
		j.mu.Unlock()
	}
	return nil
}

func (j *Job) finish() {
	j.mu.Lock()
	j.ended = j.now()
	switch {
	case j.userStop:
		j.state = model.MigCanceled
	case len(j.failed) == 0 && j.fatal == "":
		j.state = model.MigDone
	default:
		anyDone := false
		for _, ts := range j.tables {
			anyDone = anyDone || ts.state == model.TblDone
		}
		if anyDone {
			j.state = model.MigPartial
		} else {
			j.state = model.MigFailed
		}
	}
	j.running = false
	j.mu.Unlock()
	j.touch()
	snap := j.Snapshot()
	if j.cb.OnProgress != nil {
		j.cb.OnProgress(snap)
	}
	if j.cb.OnState != nil {
		j.cb.OnState(snap)
	}
	if j.cb.OnDone != nil {
		j.cb.OnDone(j.Result())
	}
}

func (j *Job) hasCreated(kind, name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range j.created {
		if c.Kind == kind && c.Name == name {
			return true
		}
	}
	return false
}

func (j *Job) addCreated(kind, name string) {
	j.mu.Lock()
	j.created = append(j.created, model.MigrationCreated{Name: name, Kind: kind})
	j.mu.Unlock()
}

// pending devuelve las tablas que hay que procesar: todas la primera vez; en Retry solo las que fallaron.
func (j *Job) pendingTables(states ...string) []*tblState {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []*tblState
	for _, n := range j.order {
		ts := j.tables[n]
		for _, s := range states {
			if ts.state == s {
				out = append(out, ts)
				break
			}
		}
	}
	return out
}

// phaseTables crea las tablas sin claves foráneas ni triggers, en paralelo (fase A).
func (j *Job) phaseTables(ctx context.Context, ss *sess) error {
	j.setPhase(model.PhaseTables)
	todo := j.pendingTables(model.TblPending)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(j.p.Workers)
	for _, ts := range todo {
		ts := ts
		if ts.created {
			continue
		}
		g.Go(func() error {
			j.mu.Lock()
			ts.state = model.TblCreating
			j.mu.Unlock()
			conn, err := j.acquire(gctx, ss.target)
			if err != nil {
				return err
			}
			defer j.unregister(conn)
			if err := conn.Exec(gctx, ts.t.CreateSQL); err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				j.failTable(ts.t.Name, err)
				return nil
			}
			j.mu.Lock()
			ts.created, ts.state = true, model.TblPending
			if ts.empty {
				ts.state = model.TblDone // no hay datos que copiar: la fase de datos no la ve
			}
			j.mu.Unlock()
			j.addCreated("table", ts.t.Name)
			j.touch()
			return nil
		})
	}
	return g.Wait()
}

type unit struct {
	ts *tblState
	c  driver.MigrationChunk
}

// phaseData copia los datos: planificadores perezosos → cola acotada → workers (fase B).
func (j *Job) phaseData(ctx context.Context, ss *sess) error {
	j.setPhase(model.PhaseData)
	todo := j.pendingTables(model.TblPending)
	if len(todo) == 0 {
		return nil
	}
	// Las tablas más grandes primero (minimiza el tiempo total); las que no se pueden partir van al principio: su único trozo largo no debe quedar de cola.
	sort.SliceStable(todo, func(a, b int) bool {
		na, nb := todo[a].t.Key.Kind == model.ChunkNone, todo[b].t.Key.Kind == model.ChunkNone
		if na != nb {
			return na
		}
		return todo[a].t.EstBytes+todo[a].t.EstRows > todo[b].t.EstBytes+todo[b].t.EstRows
	})
	if err := j.clearDirty(ctx, ss, todo); err != nil {
		return err
	}
	for _, ts := range todo {
		j.mu.Lock()
		ts.state, ts.rows, ts.done, ts.all, ts.planned = model.TblCopying, 0, 0, 0, false
		j.mu.Unlock()
	}
	j.touch()

	tablesQ := make(chan *tblState, len(todo))
	for _, ts := range todo {
		tablesQ <- ts
	}
	close(tablesQ)
	work := make(chan unit, j.p.Workers*2)

	g, gctx := errgroup.WithContext(ctx)
	gp, pctx := errgroup.WithContext(gctx)
	for i := 0; i < min(planners, len(todo)); i++ {
		gp.Go(func() error {
			conn, err := j.acquire(pctx, ss.reader())
			if err != nil {
				return err
			}
			defer j.unregister(conn)
			for ts := range tablesQ {
				if err := j.planTable(pctx, conn, ts, work); err != nil {
					return err
				}
			}
			return nil
		})
	}
	g.Go(func() error { // el cierre de la cola tiene dueño: espera a los planificadores
		err := gp.Wait()
		close(work)
		return err
	})
	for w := 0; w < j.p.Workers; w++ {
		g.Go(func() error {
			for u := range work {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				j.runChunk(gctx, ss, u)
			}
			return nil
		})
	}
	return g.Wait()
}

// clearDirty vacía las tablas que quedaron a medias en un intento anterior (Retry), antes de volver a copiarlas.
func (j *Job) clearDirty(ctx context.Context, ss *sess, todo []*tblState) error {
	for _, ts := range todo {
		j.mu.Lock()
		dirty := ts.dirty
		j.mu.Unlock()
		if !dirty {
			continue
		}
		conn, err := j.acquire(ctx, ss.target)
		if err != nil {
			return err
		}
		err = conn.ClearRange(ctx, ts.t, driver.MigrationChunk{Whole: true})
		j.unregister(conn)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			j.failTable(ts.t.Name, err)
			continue
		}
		j.mu.Lock()
		ts.dirty = false
		j.mu.Unlock()
	}
	return nil
}

func (j *Job) chunkRows(t model.MigrationTable) int {
	if n, ok := j.p.ChunkRows[t.Name]; ok {
		return n
	}
	return 100_000
}

// planTable emite los trozos de una tabla a la cola; el envío bloquea si los workers no dan abasto (contrapresión).
func (j *Job) planTable(ctx context.Context, conn driver.MigrationConn, ts *tblState, work chan<- unit) error {
	emit := func(c driver.MigrationChunk) error {
		j.mu.Lock()
		ts.all++
		j.mu.Unlock()
		select {
		case work <- unit{ts: ts, c: c}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var err error
	switch ts.t.Key.Kind {
	case model.ChunkNone:
		err = emit(driver.MigrationChunk{Seq: 0, Whole: true})
	case model.ChunkPartition:
		for i, part := range ts.t.Key.Partitions {
			if err = emit(driver.MigrationChunk{Seq: i, Partition: part, Whole: true}); err != nil {
				break
			}
		}
	default:
		err = conn.Chunks(ctx, ts.t, j.chunkRows(ts.t), emit)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		j.failTable(ts.t.Name, err)
		return nil
	}
	j.mu.Lock()
	ts.planned = true
	finished := ts.done >= ts.all && ts.state == model.TblCopying
	if finished {
		ts.state = model.TblDone
	}
	j.mu.Unlock()
	j.touch()
	return nil
}

func (j *Job) runChunk(ctx context.Context, ss *sess, u unit) {
	j.mu.Lock()
	failed := u.ts.state == model.TblFailed
	j.mu.Unlock()
	if failed { // una tabla que ya falló no gasta más trozos
		return
	}
	for attempt := 1; ; attempt++ {
		rows, bytes, err := j.copyOnce(ctx, ss, u, attempt)
		if err == nil {
			j.mu.Lock()
			u.ts.rows += rows
			u.ts.done++
			j.rows += rows
			j.bytes += bytes
			if u.ts.planned && u.ts.done >= u.ts.all && u.ts.state == model.TblCopying {
				u.ts.state = model.TblDone
			}
			j.mu.Unlock()
			j.touch()
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !retryable(err) || attempt >= maxAttempts {
			j.failTable(u.ts.t.Name, err)
			return
		}
		// Solo el código y el motivo del servidor (un bloqueo o una conexión perdida): nunca SQL ni valores (spec 10).
		slog.Warn("reintento de un trozo de la migración", "job", j.ID, "attempt", attempt, "chunk", u.c.Seq, "reason", errMessage(err))
		if j.sleep(ctx, (200*time.Millisecond)<<(attempt-1)) != nil {
			return
		}
	}
}

// copyOnce hace un intento de un trozo: una transacción; antes de repetirlo se borra su rango en el destino (⚑D9).
func (j *Job) copyOnce(ctx context.Context, ss *sess, u unit, attempt int) (rows, bytes int64, err error) {
	t := u.ts.t
	if ss.route == model.RouteServer {
		conn, err := j.acquire(ctx, ss.server)
		if err != nil {
			return 0, 0, err
		}
		defer j.unregister(conn)
		if attempt > 1 {
			if err := conn.ClearRange(ctx, t, u.c); err != nil {
				return 0, 0, err
			}
		}
		if err := conn.Begin(ctx); err != nil {
			return 0, 0, err
		}
		n, err := conn.CopyChunk(ctx, t, u.c)
		if err != nil {
			_ = conn.Rollback(context.Background())
			return 0, 0, err
		}
		if err := conn.Commit(ctx); err != nil {
			return 0, 0, err
		}
		return n, n * max(t.AvgRowLen, 1), nil
	}

	src, err := j.acquire(ctx, ss.source)
	if err != nil {
		return 0, 0, err
	}
	defer j.unregister(src)
	dst, err := j.acquire(ctx, ss.target)
	if err != nil {
		return 0, 0, err
	}
	defer j.unregister(dst)
	if attempt > 1 {
		if err := dst.ClearRange(ctx, t, u.c); err != nil {
			return 0, 0, err
		}
	}
	if err := dst.Begin(ctx); err != nil {
		return 0, 0, err
	}
	ch := make(chan driver.RowBatch, pipeDepth) // contrapresión: si el escritor se atrasa, el lector espera
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer close(ch)
		return src.Read(gctx, t, u.c, batchBytes, func(b driver.RowBatch) error {
			select {
			case ch <- b:
				return nil
			case <-gctx.Done():
				return gctx.Err()
			}
		})
	})
	g.Go(func() error {
		for b := range ch {
			n, err := dst.Insert(gctx, t, u.c, b)
			if err != nil {
				return err
			}
			rows += n
			bytes += int64(b.Bytes)
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		_ = dst.Rollback(context.Background())
		return 0, 0, err
	}
	if err := dst.Commit(ctx); err != nil {
		return 0, 0, err
	}
	return rows, bytes, nil
}

// retryOp repite una operación corta reintentable (añadir una clave foránea, crear un objeto).
func (j *Job) retryOp(ctx context.Context, op func() error) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err = op(); err == nil || ctx.Err() != nil || !retryable(err) {
			return err
		}
		if j.sleep(ctx, (200*time.Millisecond)<<(attempt-1)) != nil {
			return ctx.Err()
		}
	}
	return err
}

// phaseKeys fija los AUTO_INCREMENT y añade las claves foráneas, sin validar filas (fase C).
func (j *Job) phaseKeys(ctx context.Context, ss *sess) error {
	j.setPhase(model.PhaseKeys)
	var done []*tblState
	for _, ts := range j.pendingTables(model.TblDone) {
		done = append(done, ts)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(j.p.Workers)
	for _, ts := range done {
		ts := ts
		g.Go(func() error {
			conn, err := j.acquire(gctx, ss.target)
			if err != nil {
				return err
			}
			defer j.unregister(conn)
			if ts.t.AutoIncColumn != "" && ts.t.AutoIncrement > 0 {
				if err := conn.FixAutoIncrement(gctx, ts.t.Name, ts.t.AutoIncColumn, ts.t.AutoIncrement); err != nil {
					if gctx.Err() != nil {
						return gctx.Err()
					}
					j.fail(ts.t.Name, "table", fmt.Errorf("AUTO_INCREMENT: %w", err))
				}
			}
			for _, fk := range ts.t.ForeignKeys {
				fk := fk
				j.mu.Lock()
				already := ts.fkDone[fk.Name]
				j.mu.Unlock()
				if already {
					continue
				}
				if fk.RefSchema == j.p.SourceDB {
					fk.RefSchema = j.p.TargetDB
				}
				if err := j.retryOp(gctx, func() error { return conn.AddForeignKey(gctx, ts.t.Name, fk) }); err != nil {
					if gctx.Err() != nil {
						return gctx.Err()
					}
					j.fail(ts.t.Name+"."+fk.Name, "foreignKey", err)
					continue
				}
				j.mu.Lock()
				if ts.fkDone == nil {
					ts.fkDone = map[string]bool{}
				}
				ts.fkDone[fk.Name] = true
				j.mu.Unlock()
			}
			return nil
		})
	}
	return g.Wait()
}

func (j *Job) objState(kind, name string) *model.MigrationObjectState {
	for _, o := range j.objects {
		if o.Kind == kind && o.Name == name {
			return o
		}
	}
	return nil
}

func (j *Job) setObj(kind, name, state, errMsg string) {
	j.mu.Lock()
	if o := j.objState(kind, name); o != nil {
		o.State, o.Error = state, errMsg
	}
	j.mu.Unlock()
	j.touch()
}

// tableMigrated dice si la tabla de un trigger llegó al destino (hecha por esta migración o ya existente y omitida).
func (j *Job) tableUsable(name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	ts := j.tables[name]
	return ts != nil && (ts.state == model.TblDone)
}

// phaseObjects crea rutinas, vistas y triggers, en serie y por ese orden (fases D, E y F).
func (j *Job) phaseObjects(ctx context.Context, ss *sess) error {
	conn, err := j.acquire(ctx, ss.target)
	if err != nil {
		return err
	}
	defer j.unregister(conn)
	create := func(c model.MigrationCode, kind string) error {
		err := j.retryOp(ctx, func() error { return conn.CreateObject(ctx, c, c.CreateSQL) })
		if err == nil {
			j.setObj(kind, c.Name, "done", "")
			j.addCreated(kind, c.Name)
		}
		return err
	}
	pendingObj := func(kind, name string) bool {
		j.mu.Lock()
		defer j.mu.Unlock()
		o := j.objState(kind, name)
		return o != nil && o.State != "done"
	}

	j.setPhase(model.PhaseRoutines)
	for _, r := range j.p.Routines {
		if !pendingObj(r.Kind, r.Name) {
			continue
		}
		if err := create(r, r.Kind); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			j.setObj(r.Kind, r.Name, "failed", errMessage(err))
			j.fail(r.Name, "routine", err)
		}
	}

	j.setPhase(model.PhaseViews)
	var left []model.MigrationCode
	for _, v := range j.p.Views {
		if pendingObj("view", v.Name) {
			left = append(left, v)
		}
	}
	lastErr := map[string]error{}
	for len(left) > 0 { // una vista puede depender de otra: rondas hasta que no haya progreso
		var next []model.MigrationCode
		for _, v := range left {
			if err := create(v, "view"); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				lastErr[v.Name] = err
				next = append(next, v)
			}
		}
		if len(next) == len(left) {
			break
		}
		left = next
	}
	for _, v := range left {
		j.setObj("view", v.Name, "failed", errMessage(lastErr[v.Name]))
		j.fail(v.Name, "view", lastErr[v.Name])
	}

	j.setPhase(model.PhaseTriggers)
	for _, tr := range j.p.Triggers {
		if !pendingObj("trigger", tr.Name) {
			continue
		}
		if !j.tableUsable(tr.Table) {
			j.setObj("trigger", tr.Name, "skipped", "su tabla no se migró")
			continue
		}
		if err := create(tr, "trigger"); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			j.setObj("trigger", tr.Name, "failed", errMessage(err))
			j.fail(tr.Name, "trigger", err)
		}
	}
	return nil
}

// ---- Retry, Verify, Cleanup ----

// Retry repite solo lo que falló (spec 23): tablas (se vacían y se vuelven a copiar), claves foráneas y objetos con código.
func (j *Job) Retry(parent context.Context) error {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return apperr.New(apperr.QueryFailed, "La migración sigue en curso").WithDetail("busy")
	}
	for _, ts := range j.tables {
		if ts.state == model.TblFailed {
			ts.state, ts.err = model.TblPending, ""
			ts.rows, ts.done, ts.all, ts.planned = 0, 0, 0, false
		}
	}
	for _, o := range j.objects {
		if o.State == "failed" || o.State == "skipped" {
			o.State, o.Error = "pending", ""
		}
	}
	j.failed = nil
	j.fatal = ""
	j.userStop = false
	j.mu.Unlock()
	j.ended = time.Time{}
	return j.Start(parent)
}

// Verify comprueba claves foráneas huérfanas y recuentos de filas (spec 23 ⚑D11). Solo lectura; informa, no deshace nada.
func (j *Job) Verify(ctx context.Context, opts model.VerifyOptions) (*model.MigrationVerification, error) {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return nil, apperr.New(apperr.QueryFailed, "La migración sigue en curso").WithDetail("busy")
	}
	j.mu.Unlock()
	ss, err := j.open(ctx, false)
	if err != nil {
		return nil, err
	}
	defer ss.close()
	v, err := j.verify(ctx, ss, opts)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	j.verif = v
	j.mu.Unlock()
	return v, nil
}

func (j *Job) verify(ctx context.Context, ss *sess, opts model.VerifyOptions) (*model.MigrationVerification, error) {
	out := &model.MigrationVerification{Orphans: []model.OrphanRow{}, RowCounts: []model.RowCountDiff{}, Objects: []model.ObjectDiff{}, Checked: true}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(j.p.Workers)

	var srcSess driver.MigrationSession
	if opts.RowCounts {
		if ss.route == model.RouteServer { // el origen se cuenta con conexiones de solo lectura aparte
			s, err := j.p.Source.OpenMigration(ctx, driver.MigrationOpen{Role: driver.RoleSource, Database: j.p.SourceDB, Size: j.p.Workers})
			if err != nil {
				return nil, err
			}
			defer s.Close()
			srcSess = s
		} else {
			srcSess = ss.source
		}
	}
	for _, ts := range j.pendingTables(model.TblDone) {
		ts := ts
		if opts.RowCounts && !ts.empty {
			g.Go(func() error {
				sc, err := j.acquire(gctx, srcSess)
				if err != nil {
					return err
				}
				defer j.unregister(sc)
				tc, err := j.acquire(gctx, ss.target)
				if err != nil {
					return err
				}
				defer j.unregister(tc)
				a, err := sc.CountRows(gctx, ts.t.Name)
				if err != nil {
					return err
				}
				b, err := tc.CountRows(gctx, ts.t.Name)
				if err != nil {
					return err
				}
				if a != b {
					mu.Lock()
					out.RowCounts = append(out.RowCounts, model.RowCountDiff{Table: ts.t.Name, Source: a, Target: b})
					mu.Unlock()
				}
				return nil
			})
		}
		if opts.ForeignKeys {
			for _, fk := range ts.t.ForeignKeys {
				fk := fk
				if fk.RefSchema == j.p.SourceDB {
					fk.RefSchema = j.p.TargetDB
				}
				g.Go(func() error {
					tc, err := j.acquire(gctx, ss.target)
					if err != nil {
						return err
					}
					defer j.unregister(tc)
					const limit = 1000
					n, err := tc.Orphans(gctx, ts.t.Name, fk, limit)
					if err != nil {
						return err
					}
					if n > 0 {
						mu.Lock()
						out.Orphans = append(out.Orphans, model.OrphanRow{ForeignKey: fk.Name, Table: ts.t.Name, Count: n, AtLeast: n >= limit})
						mu.Unlock()
					}
					return nil
				})
			}
		}
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if opts.Objects {
		if err := j.verifyObjects(ctx, out); err != nil {
			return nil, err
		}
	}
	sort.Slice(out.Orphans, func(a, b int) bool {
		return out.Orphans[a].Table+out.Orphans[a].ForeignKey < out.Orphans[b].Table+out.Orphans[b].ForeignKey
	})
	sort.Slice(out.RowCounts, func(a, b int) bool { return out.RowCounts[a].Table < out.RowCounts[b].Table })
	return out, nil
}

// verifyObjects compara el código de cada vista, rutina y trigger que esta migración creó con el que hay ahora en el destino (⚑D22).
func (j *Job) verifyObjects(ctx context.Context, out *model.MigrationVerification) error {
	mine := map[string]string{}
	var refs []model.ObjectRef
	add := func(list []model.MigrationCode, kind string) {
		for _, c := range list {
			k := c.Kind
			if kind != "" {
				k = kind
			}
			j.mu.Lock()
			st := j.objState(k, c.Name)
			done := st != nil && st.State == "done"
			j.mu.Unlock()
			if !done {
				continue // los que fallaron ya salen en la lista de fallos
			}
			mine[k+":"+strings.ToLower(c.Name)] = c.CreateSQL
			refs = append(refs, model.ObjectRef{Kind: k, Name: c.Name})
		}
	}
	add(j.p.Routines, "")
	add(j.p.Views, "view")
	add(j.p.Triggers, "trigger")
	if len(refs) == 0 {
		return nil
	}
	theirs, err := j.p.Target.MigrationObjectCode(ctx, j.p.TargetDB, refs)
	if err != nil {
		return err
	}
	got := map[string]string{}
	for _, c := range theirs {
		got[c.Kind+":"+strings.ToLower(c.Name)] = c.CreateSQL
	}
	for _, r := range refs {
		key := r.Kind + ":" + strings.ToLower(r.Name)
		out.ObjectsChecked++
		code, found := got[key]
		if !found {
			out.Objects = append(out.Objects, model.ObjectDiff{Kind: r.Kind, Name: r.Name, Status: "missing"})
			continue
		}
		status := map[string]string{"same": "same", "different": "different", "unknown": "unreadable"}[CompareCode(mine[key], code)]
		out.Objects = append(out.Objects, model.ObjectDiff{Kind: r.Kind, Name: r.Name, Status: status})
	}
	sort.Slice(out.Objects, func(a, b int) bool {
		return out.Objects[a].Kind+out.Objects[a].Name < out.Objects[b].Kind+out.Objects[b].Name
	})
	return nil
}

// Cleanup borra, en orden inverso, solo lo que creó esta migración (⚑D12). El servicio ya comprobó el nombre escrito.
func (j *Job) Cleanup(ctx context.Context) error {
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		return apperr.New(apperr.QueryFailed, "La migración sigue en curso").WithDetail("busy")
	}
	created := append([]model.MigrationCreated(nil), j.created...)
	j.mu.Unlock()
	rev := make([]model.MigrationCreated, 0, len(created))
	for i := len(created) - 1; i >= 0; i-- {
		rev = append(rev, created[i])
	}
	if err := j.p.Target.MigrationDrop(ctx, j.p.TargetDB, rev); err != nil {
		return err
	}
	j.mu.Lock()
	j.created = nil
	for _, ts := range j.tables {
		ts.created = false
	}
	j.mu.Unlock()
	j.touch()
	return nil
}
