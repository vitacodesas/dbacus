package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/driver/fake"
	"tabulariswails/internal/export"
	"tabulariswails/internal/model"
)

type exportEnv struct {
	*queryEnv
	x    *ExportService
	dest string
	pick func(string) (string, error)
}

func newExportEnv(t *testing.T) *exportEnv {
	t.Helper()
	qe := newQueryEnv(t)
	ee := &exportEnv{queryEnv: qe, dest: filepath.Join(t.TempDir(), "out.csv")}
	ee.pick = func(string) (string, error) { return ee.dest, nil }
	ee.x = NewExportService(ExportDeps{
		Sessions: qe.sessions, Results: qe.q.d.Results, Emit: qe.q.d.Emit,
		PickSave: func(name string) (string, error) { return ee.pick(name) },
	})
	t.Cleanup(func() { _ = ee.x.ServiceShutdown() })
	return ee
}

func (e *exportEnv) exportDone(t *testing.T) ExportDone {
	t.Helper()
	return e.next(t, "export:done").(ExportDone)
}

var csvOpts = export.Options{Format: export.FormatCSV, Header: true}

func TestExportFromBufferWritesCSV(t *testing.T) {
	e := newExportEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"id", "name"}, intRows(2500)...), nil
	}
	qid := e.execute(t, "t1", "SELECT * FROM t")
	done, failed := e.terminal(t, qid)
	require.Nil(t, failed)
	require.EqualValues(t, 2500, done.Results[0].RowCount)

	start, err := e.x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "buffer", Options: csvOpts})
	require.NoError(t, err)
	require.NotEmpty(t, start.JobID)
	res := e.exportDone(t)
	require.Nil(t, res.Error)
	require.EqualValues(t, 2500, res.Rows)

	data, err := os.ReadFile(e.dest)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	require.Len(t, lines, 2501) // cabecera + filas
	require.Equal(t, "id,name", lines[0])
	require.EqualValues(t, len(data), res.Bytes)
	require.NoFileExists(t, e.dest+".partial")
}

func TestExportRerunWritesMoreThanTheBufferKept(t *testing.T) {
	e := newExportEnv(t)
	var sqls []string
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		sqls = append(sqls, stmt)
		return fake.Rows([]string{"id", "name"}, intRows(5000)...), nil
	}
	qid := e.execute(t, "t1", "SELECT * FROM t", func(r *ExecuteRequest) { r.MaxRows = 100; r.AutoLimit = 100 })
	done, _ := e.terminal(t, qid)
	require.True(t, done.Results[0].Truncated)

	_, err := e.x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "rerun", Options: csvOpts})
	require.NoError(t, err)
	res := e.exportDone(t)
	require.Nil(t, res.Error)
	require.EqualValues(t, 5000, res.Rows)
	require.Equal(t, "SELECT * FROM t", sqls[len(sqls)-1], "se re-ejecuta la sentencia original, sin el LIMIT automático")

	sess, _ := e.sessions.Get(e.sid)
	require.Equal(t, 1, sess.TabCount(), "la conexión de la exportación se libera (solo queda la de la pestaña)")
}

func TestExportUserClosingTheDialogStartsNothing(t *testing.T) {
	e := newExportEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"a"}, []any{"x"}), nil
	}
	qid := e.execute(t, "t1", "SELECT 1")
	e.terminal(t, qid)
	e.pick = func(string) (string, error) { return "", nil }
	start, err := e.x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "buffer", Options: csvOpts})
	require.NoError(t, err)
	require.True(t, start.Canceled)
	require.Empty(t, start.JobID)
}

func TestExportSuggestsNameWithExtension(t *testing.T) {
	e := newExportEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"a"}, []any{"x"}), nil
	}
	qid := e.execute(t, "t1", "SELECT 1")
	e.terminal(t, qid)
	var got string
	e.pick = func(name string) (string, error) { got = name; return "", nil }
	_, err := e.x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "buffer", Options: export.Options{Format: export.FormatJSON, NDJSON: true}})
	require.NoError(t, err)
	require.Regexp(t, `^resultado-\d{8}-\d{6}\.ndjson$`, got)
}

// endless produce filas sin parar, despacio, hasta que se cancela el contexto.
type endless struct {
	ctx context.Context
	n   int64
}

func (r *endless) Columns() []model.ColumnMeta {
	return []model.ColumnMeta{{Name: "n", DataType: "BIGINT"}}
}
func (r *endless) Next() bool {
	select {
	case <-r.ctx.Done():
		return false
	case <-time.After(200 * time.Microsecond):
		r.n++
		return true
	}
}
func (r *endless) Row() []any                    { return []any{r.n} }
func (r *endless) Err() error                    { return r.ctx.Err() }
func (r *endless) Affected() (int64, int64, int) { return 0, 0, 0 }
func (r *endless) Close() error                  { return nil }

func TestCancelExportRemovesPartialFileAndKeepsExistingOne(t *testing.T) {
	e := newExportEnv(t)
	calls := 0
	e.drv.OnRun = func(ctx context.Context, stmt string) (driver.RunResult, error) {
		calls++
		if calls == 1 {
			return fake.Rows([]string{"n"}, []any{int64(1)}), nil
		}
		return &endless{ctx: ctx}, nil
	}
	qid := e.execute(t, "t1", "SELECT n FROM t")
	e.terminal(t, qid)

	require.NoError(t, os.WriteFile(e.dest, []byte("anterior"), 0o600))
	start, err := e.x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "rerun", Options: csvOpts})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	require.FileExists(t, e.dest+".partial")

	began := time.Now()
	require.NoError(t, e.x.CancelExport(start.JobID))
	res := e.exportDone(t)
	require.True(t, res.Canceled)
	require.Nil(t, res.Error)
	require.Less(t, time.Since(began), time.Second)
	require.NoFileExists(t, e.dest+".partial")
	data, _ := os.ReadFile(e.dest)
	require.Equal(t, "anterior", string(data), "cancelar no toca el archivo que ya existía")
	require.NoError(t, e.x.CancelExport(start.JobID), "idempotente")
}

func TestExportRerunFailureReportsErrorAndLeavesNoFile(t *testing.T) {
	e := newExportEnv(t)
	calls := 0
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		calls++
		if calls == 1 {
			return fake.Rows([]string{"n"}, []any{int64(1)}), nil
		}
		return nil, apperr.New(apperr.QueryFailed, "tabla borrada")
	}
	qid := e.execute(t, "t1", "SELECT n FROM t")
	e.terminal(t, qid)
	_, err := e.x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "rerun", Options: csvOpts})
	require.NoError(t, err)
	res := e.exportDone(t)
	require.NotNil(t, res.Error)
	require.Equal(t, apperr.QueryFailed, res.Error.Code)
	require.NoFileExists(t, e.dest)
	require.NoFileExists(t, e.dest+".partial")
}

func TestExportValidation(t *testing.T) {
	e := newExportEnv(t)
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) {
		return fake.Rows([]string{"a"}, []any{"x"}), nil
	}
	qid := e.execute(t, "t1", "SELECT 1")
	e.terminal(t, qid)

	_, err := e.x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "buffer", Options: export.Options{Format: "xml"}})
	require.Equal(t, apperr.Validation, apperr.From(err).Code)
	_, err = e.x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "nube", Options: csvOpts})
	require.Equal(t, apperr.Validation, apperr.From(err).Code)
	_, err = e.x.Export(context.Background(), ExportRequest{QueryID: "no-existe", Source: "buffer", Options: csvOpts})
	require.Equal(t, apperr.ResultExpired, apperr.From(err).Code)

	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { return &fake.Result{Rows_: 1}, nil }
	q2 := e.execute(t, "t2", "UPDATE t SET a=1")
	e.terminal(t, q2)
	_, err = e.x.Export(context.Background(), ExportRequest{QueryID: q2, Source: "buffer", Options: csvOpts})
	require.Equal(t, apperr.Validation, apperr.From(err).Code, "una sentencia sin filas no se exporta")
}
