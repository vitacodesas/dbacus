package services

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/migrate"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/session"
)

// Eventos de la migración de schema (spec 02 / spec 23). Los id de job viajan como texto. Nunca llevan datos de filas.

type MigrateTableProgress struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	RowsDone   int64  `json:"rowsDone"`
	RowsEst    int64  `json:"rowsEst"`
	ChunksDone int    `json:"chunksDone"`
	ChunksAll  int    `json:"chunksAll"`
}

type MigrateProgressEvent struct {
	JobID      string                 `json:"jobId"`
	Phase      string                 `json:"phase"`
	Rows       int64                  `json:"rows"`
	EstRows    int64                  `json:"estRows"`
	Bytes      int64                  `json:"bytes"`
	ElapsedMs  int64                  `json:"elapsedMs"`
	TablesDone int                    `json:"tablesDone"`
	TablesAll  int                    `json:"tablesTotal"`
	PerTable   []MigrateTableProgress `json:"perTable"`
}

type MigrateStateEvent struct {
	JobID string `json:"jobId"`
	State string `json:"state"`
	Phase string `json:"phase"`
}

type MigrateDoneEvent struct {
	JobID     string `json:"jobId"`
	State     string `json:"state"`
	Failed    int    `json:"failed"`
	ElapsedMs int64  `json:"elapsedMs"`
}

func init() {
	application.RegisterEvent[MigrateProgressEvent]("migrate:progress")
	application.RegisterEvent[MigrateStateEvent]("migrate:state")
	application.RegisterEvent[MigrateDoneEvent]("migrate:done")
}

type MigrationStore interface {
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
}

type MigrationDeps struct {
	Sessions *session.Manager
	Schema   *SchemaService
	Store    MigrationStore
	History  func(model.HistoryEntry) // opcional: una entrada resumen por migración (spec 23 ⚑D17)
	Emit     func(name string, data any)
}

// MigrationService copia un schema a otro (spec 23). Vue elige origen, destino y objetos; Go lo planifica, lo ejecuta con goroutines
// acotadas y avisa por eventos. El origen solo se lee (⚑D19).
type MigrationService struct {
	d MigrationDeps

	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	seq  int
	jobs map[string]*migRun
	busy map[string]string // sessionID → job activo
	done []string          // jobs terminados, del más viejo al más nuevo
}

type migRun struct {
	job      *migrate.Job
	prep     *migrate.Prepared
	srcSess  string
	tgtSess  string
	profile  string
	stopFns  []func() bool
	released bool
}

const keepFinishedJobs = 20

func NewMigrationService(d MigrationDeps) *MigrationService {
	ctx, cancel := context.WithCancel(context.Background())
	return &MigrationService{d: d, ctx: ctx, cancel: cancel, jobs: map[string]*migRun{}, busy: map[string]string{}}
}

// ServiceShutdown cancela las migraciones en curso y espera a que terminen.
func (s *MigrationService) ServiceShutdown() error {
	s.mu.Lock()
	runs := make([]*migRun, 0, len(s.jobs))
	for _, r := range s.jobs {
		runs = append(runs, r)
	}
	s.mu.Unlock()
	for _, r := range runs {
		r.job.Cancel()
	}
	s.cancel()
	for _, r := range runs {
		r.job.Wait()
	}
	return nil
}

func (s *MigrationService) emit(name string, data any) {
	if s.d.Emit != nil {
		s.d.Emit(name, data)
	}
}

func progressEvent(j model.MigrationJob) MigrateProgressEvent {
	ev := MigrateProgressEvent{JobID: j.ID, Phase: j.Phase, Rows: j.Rows, EstRows: j.EstRows, Bytes: j.Bytes, ElapsedMs: j.ElapsedMs, TablesDone: j.TablesDone, TablesAll: j.TablesAll, PerTable: []MigrateTableProgress{}}
	for _, t := range j.Tables {
		ev.PerTable = append(ev.PerTable, MigrateTableProgress{Name: t.Name, State: t.State, RowsDone: t.RowsDone, RowsEst: t.RowsEst, ChunksDone: t.ChunksDone, ChunksAll: t.ChunksAll})
	}
	return ev
}

// endpoints resuelve las dos sesiones y comprueba lo que no depende del plan: sesiones vivas, driver capaz y destino escribible.
type migEndpoints struct {
	src, tgt *session.Session
	same     bool
	deps     migrate.PlanDeps
}

func (s *MigrationService) endpoints(ctx context.Context, req model.MigrationRequest) (*migEndpoints, error) {
	if strings.TrimSpace(req.Source.SessionID) == "" {
		return nil, apperr.New(apperr.Validation, "Falta la conexión de origen").WithDetail("source")
	}
	if strings.TrimSpace(req.Target.SessionID) == "" {
		return nil, apperr.New(apperr.Validation, "Falta la conexión de destino").WithDetail("target")
	}
	src, err := s.d.Sessions.Get(req.Source.SessionID)
	if err != nil {
		return nil, toAppErr(err)
	}
	tgt, err := s.d.Sessions.Get(req.Target.SessionID)
	if err != nil {
		return nil, toAppErr(err)
	}
	sm, ok1 := src.Pool.(driver.SchemaMigrator)
	tm, ok2 := tgt.Pool.(driver.SchemaMigrator)
	if !ok1 || !ok2 {
		return nil, apperr.New(apperr.Unsupported, "Este motor no soporta migrar schemas")
	}
	// El destino no puede ser de solo lectura; el origen sí (solo se lee).
	if sessionReadOnly(ctx, s.d.Store, tgt) {
		return nil, errReadOnly("La conexión de destino es de solo lectura: no se puede migrar a ella")
	}
	same := src.ID == tgt.ID
	return &migEndpoints{src: src, tgt: tgt, same: same, deps: migrate.PlanDeps{Source: sm, Target: tm, SameSession: same}}, nil
}

// Plan inspecciona origen y destino y devuelve el plan, sin crear nada.
func (s *MigrationService) Plan(ctx context.Context, req model.MigrationRequest) (model.MigrationPlan, error) {
	ep, err := s.endpoints(ctx, req)
	if err != nil {
		return model.MigrationPlan{}, err
	}
	p, err := migrate.Plan(ctx, ep.deps, req)
	if err != nil {
		return model.MigrationPlan{}, toAppErr(err)
	}
	return p.Plan, nil
}

// Start repite el plan y exige que su hash coincida con el que vio el usuario (stale_plan si el origen cambió), y lanza el job.
func (s *MigrationService) Start(ctx context.Context, req model.MigrationRequest, planHash string) (model.MigrationJob, error) {
	ep, err := s.endpoints(ctx, req)
	if err != nil {
		return model.MigrationJob{}, err
	}
	p, err := migrate.Plan(ctx, ep.deps, req)
	if err != nil {
		return model.MigrationJob{}, toAppErr(err)
	}
	if planHash == "" || p.Plan.PlanHash != planHash {
		return model.MigrationJob{}, apperr.New(apperr.Validation, "El origen o el destino cambiaron desde que se calculó el plan: vuelve a revisarlo").WithDetail("stale_plan")
	}

	s.mu.Lock()
	for _, id := range []string{ep.src.ID, ep.tgt.ID} {
		if j, busy := s.busy[id]; busy {
			s.mu.Unlock()
			return model.MigrationJob{}, apperr.Newf(apperr.QueryFailed, "Ya hay una migración en curso con esta conexión (%s)", j).WithDetail("busy")
		}
	}
	s.seq++
	id := fmt.Sprintf("mig-%d-%d", time.Now().Unix(), s.seq)
	run := &migRun{prep: p, srcSess: ep.src.ID, tgtSess: ep.tgt.ID, profile: ep.tgt.ProfileID}
	run.job = migrate.NewJob(id, p, migrate.Callbacks{
		OnProgress: func(j model.MigrationJob) { s.emit("migrate:progress", progressEvent(j)) },
		OnState: func(j model.MigrationJob) {
			s.emit("migrate:state", MigrateStateEvent{JobID: j.ID, State: j.State, Phase: j.Phase})
		},
		OnDone: func(r model.MigrationResult) { s.finished(run, r) },
	})
	s.jobs[id] = run
	s.busy[ep.src.ID], s.busy[ep.tgt.ID] = id, id
	s.mu.Unlock()

	if err := s.launch(run, ep.src, ep.tgt, run.job.Start); err != nil {
		s.release(run)
		return model.MigrationJob{}, err
	}
	slog.Info("migración iniciada", "job", id, "route", p.Plan.Route, "workers", p.Workers, "tables", len(p.Tables))
	return run.job.Snapshot(), nil
}

// launch arranca el job atado a las dos sesiones: si cualquiera se cierra, el job se cancela.
func (s *MigrationService) launch(run *migRun, src, tgt *session.Session, start func(context.Context) error) error {
	ctx, cancel := context.WithCancel(s.ctx)
	run.stopFns = []func() bool{
		context.AfterFunc(src.Context(), cancel),
		context.AfterFunc(tgt.Context(), cancel),
	}
	if err := start(ctx); err != nil {
		cancel()
		return toAppErr(err)
	}
	go func() { // el contexto se libera cuando el job termina
		run.job.Wait()
		cancel()
	}()
	return nil
}

func (s *MigrationService) release(run *migRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.released {
		return
	}
	run.released = true
	for _, id := range []string{run.srcSess, run.tgtSess} {
		if s.busy[id] == run.job.ID {
			delete(s.busy, id)
		}
	}
	for _, stop := range run.stopFns {
		stop()
	}
	s.done = append(s.done, run.job.ID)
	for len(s.done) > keepFinishedJobs {
		delete(s.jobs, s.done[0])
		s.done = s.done[1:]
	}
}

// finished se llama al terminar el job (también tras un Retry): avisa, refresca el árbol y deja una entrada resumen en el historial.
func (s *MigrationService) finished(run *migRun, r model.MigrationResult) {
	s.release(run)
	s.emit("migrate:done", MigrateDoneEvent{JobID: r.Job.ID, State: r.Job.State, Failed: len(r.Failed), ElapsedMs: r.Job.ElapsedMs})
	if s.d.Schema != nil {
		db := run.prep.TargetDB
		if run.prep.TargetNew {
			db = "" // aparece una base nueva: se refresca la lista entera
		}
		_ = s.d.Schema.invalidate(run.tgtSess, db)
	}
	if s.d.History != nil {
		stmt := fmt.Sprintf("-- Migración de schema `%s` → `%s` (%d tablas, %d filas, estado %s)", run.prep.SourceDB, run.prep.TargetDB, r.Job.TablesDone, r.Job.Rows, r.Job.State)
		e := model.HistoryEntry{ProfileID: run.profile, SQL: stmt, Database: run.prep.TargetDB, ExecutedAt: r.Job.StartedAt.UTC(), ElapsedMs: r.Job.ElapsedMs,
			OK: r.Job.State == model.MigDone, RowCount: r.Job.Rows}
		if len(r.Failed) > 0 {
			e.ErrorMessage = fmt.Sprintf("%d objetos con error", len(r.Failed))
		}
		s.d.History(e)
	}
	slog.Info("migración terminada", "job", r.Job.ID, "state", r.Job.State, "failed", len(r.Failed), "elapsedMs", r.Job.ElapsedMs, "rows", r.Job.Rows)
}

func (s *MigrationService) get(id string) (*migRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.jobs[id]
	if !ok {
		return nil, apperr.New(apperr.NotFound, "La migración ya no está en memoria")
	}
	return r, nil
}

// Cancel detiene la migración; el destino se queda como está (spec 23 ⚑D10).
func (s *MigrationService) Cancel(jobID string) error {
	r, err := s.get(jobID)
	if err != nil {
		return err
	}
	r.job.Cancel()
	return nil
}

// Job devuelve el estado y el resultado del job.
func (s *MigrationService) Job(jobID string) (model.MigrationResult, error) {
	r, err := s.get(jobID)
	if err != nil {
		return model.MigrationResult{}, err
	}
	return r.job.Result(), nil
}

// Retry repite solo lo que falló.
func (s *MigrationService) Retry(ctx context.Context, jobID string) (model.MigrationJob, error) {
	r, err := s.get(jobID)
	if err != nil {
		return model.MigrationJob{}, err
	}
	src, err := s.d.Sessions.Get(r.srcSess)
	if err != nil {
		return model.MigrationJob{}, toAppErr(err)
	}
	tgt, err := s.d.Sessions.Get(r.tgtSess)
	if err != nil {
		return model.MigrationJob{}, toAppErr(err)
	}
	if sessionReadOnly(ctx, s.d.Store, tgt) {
		return model.MigrationJob{}, errReadOnly("La conexión de destino es de solo lectura")
	}
	s.mu.Lock()
	for _, id := range []string{r.srcSess, r.tgtSess} {
		if j, busy := s.busy[id]; busy {
			s.mu.Unlock()
			return model.MigrationJob{}, apperr.Newf(apperr.QueryFailed, "Ya hay una migración en curso con esta conexión (%s)", j).WithDetail("busy")
		}
	}
	r.released = false
	s.busy[r.srcSess], s.busy[r.tgtSess] = jobID, jobID
	s.done = removeString(s.done, jobID)
	s.mu.Unlock()
	if err := s.launch(r, src, tgt, r.job.Retry); err != nil {
		s.release(r)
		return model.MigrationJob{}, err
	}
	return r.job.Snapshot(), nil
}

func removeString(list []string, v string) []string {
	out := list[:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// Verify comprueba claves foráneas huérfanas y recuentos de filas (solo lectura; informa, no deshace nada).
func (s *MigrationService) Verify(ctx context.Context, jobID string, opts model.VerifyOptions) (model.MigrationVerification, error) {
	r, err := s.get(jobID)
	if err != nil {
		return model.MigrationVerification{}, err
	}
	if !opts.ForeignKeys && !opts.RowCounts && !opts.Objects {
		return model.MigrationVerification{}, apperr.New(apperr.Validation, "Elige qué verificar").WithDetail("objects")
	}
	v, err := r.job.Verify(ctx, opts)
	if err != nil {
		return model.MigrationVerification{}, toAppErr(err)
	}
	return *v, nil
}

// Cleanup borra solo lo que creó la migración (spec 23 ⚑D12); exige escribir el nombre del schema destino.
func (s *MigrationService) Cleanup(ctx context.Context, jobID, confirmName string) error {
	r, err := s.get(jobID)
	if err != nil {
		return err
	}
	if confirmName != r.prep.TargetDB {
		return apperr.New(apperr.Validation, "Escribe el nombre del schema destino para confirmar").WithDetail("confirm")
	}
	tgt, err := s.d.Sessions.Get(r.tgtSess)
	if err != nil {
		return toAppErr(err)
	}
	if sessionReadOnly(ctx, s.d.Store, tgt) {
		return errReadOnly("La conexión de destino es de solo lectura")
	}
	if err := r.job.Cleanup(ctx); err != nil {
		return toAppErr(err)
	}
	if s.d.Schema != nil {
		_ = s.d.Schema.invalidate(r.tgtSess, "")
	}
	return nil
}
