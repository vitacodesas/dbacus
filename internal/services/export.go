package services

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/wailsapp/wails/v3/pkg/application"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/export"
	"tabulariswails/internal/results"
	"tabulariswails/internal/session"
)

const (
	exportSourceBuffer = "buffer" // lo que ya está en memoria
	exportSourceRerun  = "rerun"  // vuelve a ejecutar la sentencia y escribe directo al archivo
	exportWindow       = 1000
)

type ExportProgress struct {
	JobID string `json:"jobId"`
	Rows  int64  `json:"rows"`
	Bytes int64  `json:"bytes"`
}

type ExportDone struct {
	JobID    string           `json:"jobId"`
	Path     string           `json:"path"`
	Rows     int64            `json:"rows"`
	Bytes    int64            `json:"bytes"`
	Canceled bool             `json:"canceled"`
	Error    *apperr.AppError `json:"error,omitempty"`
}

func init() {
	application.RegisterEvent[ExportProgress]("export:progress")
	application.RegisterEvent[ExportDone]("export:done")
}

type ExportRequest struct {
	QueryID   string         `json:"queryId"`
	StmtIndex int            `json:"stmtIndex"`
	Source    string         `json:"source"` // buffer | rerun
	Options   export.Options `json:"options"`
}

// ExportStart es la respuesta inmediata: el trabajo sigue en segundo plano y termina con export:done.
type ExportStart struct {
	JobID    string `json:"jobId"`
	Path     string `json:"path"`
	Canceled bool   `json:"canceled"` // el usuario cerró el diálogo de guardado: no hay trabajo
}

type ExportDeps struct {
	Sessions *session.Manager
	Results  *results.Manager
	// PickSave abre el diálogo nativo de guardado; devuelve "" si el usuario cancela.
	PickSave func(suggestedName string) (string, error)
	Emit     func(name string, data any)
}

type exportJob struct {
	id       string
	cancel   context.CancelFunc
	canceled atomic.Bool
	killFn   atomic.Pointer[func()]
}

// ExportService escribe resultados a CSV/JSON sin pasar por Vue (spec 07).
type ExportService struct {
	d ExportDeps

	baseCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu   sync.Mutex
	jobs map[string]*exportJob
}

func NewExportService(d ExportDeps) *ExportService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ExportService{d: d, baseCtx: ctx, cancel: cancel, jobs: map[string]*exportJob{}}
}

// ServiceShutdown (hook de Wails, no se enlaza a Vue) cancela las exportaciones y espera a que borren sus parciales.
func (s *ExportService) ServiceShutdown() error {
	s.cancel()
	s.wg.Wait()
	return nil
}

func (s *ExportService) emit(name string, data any) {
	if s.d.Emit != nil {
		s.d.Emit(name, data)
	}
}

// Export pide el destino con el diálogo nativo y lanza la escritura. Bloquea solo mientras el diálogo está abierto.
func (s *ExportService) Export(ctx context.Context, req ExportRequest) (ExportStart, error) {
	if err := req.Options.Validate(); err != nil {
		return ExportStart{}, err
	}
	if req.Source != exportSourceBuffer && req.Source != exportSourceRerun {
		return ExportStart{}, apperr.New(apperr.Validation, "Origen de exportación no válido").WithDetail("source")
	}
	run, err := s.d.Results.Get(req.QueryID)
	if err != nil {
		return ExportStart{}, toAppErr(err)
	}
	buf, err := run.Buffer(req.StmtIndex)
	if err != nil {
		return ExportStart{}, toAppErr(err)
	}
	if len(buf.Columns()) == 0 {
		return ExportStart{}, apperr.New(apperr.Validation, "Esta sentencia no devuelve filas que exportar")
	}
	sql, database := buf.Source()
	if req.Source == exportSourceRerun && strings.TrimSpace(sql) == "" {
		return ExportStart{}, apperr.New(apperr.Validation, "No hay sentencia que volver a ejecutar")
	}
	if s.d.PickSave == nil {
		return ExportStart{}, apperr.New(apperr.Unsupported, "No hay diálogo de guardado disponible")
	}
	name := fmt.Sprintf("resultado-%s.%s", time.Now().Format("20060102-150405"), export.Extension(req.Options))
	path, err := s.d.PickSave(name)
	if err != nil {
		return ExportStart{}, apperr.Wrap(apperr.Internal, "No se pudo abrir el diálogo de guardado", err)
	}
	if path == "" {
		return ExportStart{Canceled: true}, nil
	}

	jctx, cancel := context.WithCancel(s.baseCtx)
	job := &exportJob{id: uuid.NewString(), cancel: cancel}
	s.mu.Lock()
	s.jobs[job.id] = job
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.jobs, job.id)
			s.mu.Unlock()
		}()
		done := s.runJob(jctx, job, path, req, run.SessionID, sql, database, buf)
		s.emit("export:done", done)
	}()
	return ExportStart{JobID: job.id, Path: path}, nil
}

// CancelExport aborta una exportación; el archivo parcial se borra. Es idempotente.
func (s *ExportService) CancelExport(jobID string) error {
	s.mu.Lock()
	job := s.jobs[jobID]
	s.mu.Unlock()
	if job == nil {
		return nil
	}
	job.canceled.Store(true)
	if k := job.killFn.Load(); k != nil {
		(*k)()
	}
	// Si KILL QUERY no basta (o no hay consulta en curso), el ctx corta lo que quede.
	time.AfterFunc(cancelGrace, job.cancel)
	return nil
}

// countingWriter cuenta los bytes escritos para el progreso.
type countingWriter struct {
	f *os.File
	n atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.f.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// runJob escribe a "<destino>.partial" y lo renombra al terminar: cancelar o fallar nunca deja un archivo a medias
// ni pisa un archivo existente.
func (s *ExportService) runJob(ctx context.Context, job *exportJob, path string, req ExportRequest, sessionID, sql, database string, buf *results.Buffer) ExportDone {
	done := ExportDone{JobID: job.id, Path: path}
	fail := func(err error) ExportDone {
		if job.canceled.Load() {
			done.Canceled = true
			return done
		}
		done.Error = apperr.From(err)
		return done
	}

	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fail(apperr.Wrap(apperr.Internal, "No se pudo crear el archivo de destino", err))
	}
	cw := &countingWriter{f: f}
	bw := bufio.NewWriterSize(cw, 256*1024)
	cleanup := func() { _ = f.Close(); _ = os.Remove(tmp) }

	w, err := export.New(bw, req.Options)
	if err != nil {
		cleanup()
		return fail(err)
	}

	var rows int64
	lastProgress := time.Now()
	tick := func() {
		if now := time.Now(); now.Sub(lastProgress) >= progressEvery {
			lastProgress = now
			s.emit("export:progress", ExportProgress{JobID: job.id, Rows: rows, Bytes: cw.n.Load()})
		}
	}
	write := func(cells []results.Cell) error {
		if err := ctx.Err(); err != nil || job.canceled.Load() {
			return context.Canceled
		}
		if err := w.Row(cells); err != nil {
			return err
		}
		rows++
		tick()
		return nil
	}

	if req.Source == exportSourceBuffer {
		err = s.fromBuffer(buf, w, write)
	} else {
		err = s.fromRerun(ctx, job, sessionID, sql, database, w, write)
	}
	if err == nil {
		err = w.End()
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		cleanup()
		if errors.Is(err, context.Canceled) && !job.canceled.Load() && ctx.Err() == nil {
			slog.Warn("exportación abortada", "err", err)
		}
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fail(apperr.Wrap(apperr.Internal, "No se pudo cerrar el archivo", err))
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fail(apperr.Wrap(apperr.Internal, "No se pudo guardar el archivo", err))
	}
	done.Rows, done.Bytes = rows, cw.n.Load()
	return done
}

func (s *ExportService) fromBuffer(buf *results.Buffer, w export.Writer, write func([]results.Cell) error) error {
	if err := w.Begin(buf.Columns()); err != nil {
		return err
	}
	for offset := 0; ; {
		rows, total, _, err := buf.Window(offset, exportWindow)
		if err != nil {
			return toAppErr(err)
		}
		for _, r := range rows {
			if err := write(r); err != nil {
				return err
			}
		}
		offset += len(rows)
		if offset >= total || len(rows) == 0 {
			return nil
		}
	}
}

// fromRerun vuelve a ejecutar la sentencia en una conexión propia (no la de ninguna pestaña) y escribe cada fila
// al archivo sin acumularla: la memoria no depende del tamaño del resultado.
func (s *ExportService) fromRerun(ctx context.Context, job *exportJob, sessionID, sql, database string, w export.Writer, write func([]results.Cell) error) error {
	sess, err := s.d.Sessions.Get(sessionID)
	if err != nil {
		return toAppErr(err)
	}
	key := "export:" + job.id
	conn, err := sess.AcquireConn(ctx, key)
	if err != nil {
		return toAppErr(err)
	}
	defer func() { _ = sess.CloseTab(key) }()

	kill := func() {
		kctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx), killTimeout)
		go func() {
			defer cancel()
			if err := conn.Kill(kctx); err != nil {
				slog.Warn("KILL QUERY de exportación falló; se cancela el contexto", "err", err)
				job.cancel()
			}
		}()
	}
	job.killFn.Store(&kill)
	if job.canceled.Load() { // Cancel llegó antes de publicar kill
		return context.Canceled
	}

	if database != "" {
		if err := conn.Use(ctx, database); err != nil {
			return toAppErr(err)
		}
	}
	res, err := conn.Run(ctx, sql)
	if err != nil {
		return toAppErr(err)
	}
	defer res.Close()
	cols := res.Columns()
	if len(cols) == 0 {
		return apperr.New(apperr.QueryFailed, "La sentencia ya no devuelve filas")
	}
	if err := w.Begin(cols); err != nil {
		return err
	}
	cells := make([]results.Cell, len(cols))
	for res.Next() {
		for j, v := range res.Row() {
			cells[j] = results.Convert(v, cols[j].DataType)
		}
		if err := write(cells); err != nil {
			// Cerrar un cursor con filas pendientes obliga al driver a leerlas todas: se aborta la consulta antes.
			kill()
			return err
		}
	}
	if err := res.Err(); err != nil {
		return toAppErr(err)
	}
	return nil
}
