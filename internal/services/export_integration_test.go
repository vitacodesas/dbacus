//go:build integration

package services

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/export"
	"tabulariswails/internal/testutil/mysqlbox"
)

func TestExportServiceIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			t.Run("1M de filas con memoria acotada", e.exportMillion)
			t.Run("tipos extremos a JSON", e.exportTypes)
			t.Run("cancelar una exportación grande", e.exportCancel)
		})
	}
}

func (e *liveEnv) exporter(dest string) *ExportService {
	return NewExportService(ExportDeps{
		Sessions: e.q.d.Sessions, Results: e.q.d.Results, Emit: e.q.d.Emit,
		PickSave: func(string) (string, error) { return dest, nil },
	})
}

func (e *liveEnv) exportDone(t *testing.T, timeout time.Duration) ExportDone {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-e.events:
			if d, ok := ev.data.(ExportDone); ok {
				return d
			}
		case <-deadline:
			t.Fatal("la exportación no terminó")
		}
	}
}

// million produce 1 000 000 filas con un producto cruzado de 6 tablas de 10 filas (sin variables de sesión: la
// exportación usa su propia conexión, que no las heredaría).
const million = `SELECT d0.n + d1.n*10 + d2.n*100 + d3.n*1000 + d4.n*10000 + d5.n*100000 AS id, CONCAT('fila-', d0.n, d1.n, d2.n, d3.n, d4.n, d5.n) AS txt
FROM (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d0
CROSS JOIN (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d1
CROSS JOIN (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d2
CROSS JOIN (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d3
CROSS JOIN (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d4
CROSS JOIN (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d5`

func (e *liveEnv) exportMillion(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "million.csv")
	x := e.exporter(dest)
	t.Cleanup(func() { _ = x.ServiceShutdown() })

	qid, done := e.mustRun(t, "x1", million, func(r *ExecuteRequest) { r.MaxRows = 1000; r.AutoLimit = 0 })
	require.True(t, done.Results[0].Truncated)

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(25 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()

	started := time.Now()
	_, err := x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "rerun", Options: export.Options{Format: export.FormatCSV, Header: true}})
	require.NoError(t, err)
	res := e.exportDone(t, 3*time.Minute)
	close(stop)
	require.Nil(t, res.Error)
	require.EqualValues(t, 1_000_000, res.Rows)

	extra := int64(peak.Load()) - int64(base.HeapAlloc)
	t.Logf("1M filas, %d MB en disco, %s, memoria extra máxima %d MB", res.Bytes>>20, time.Since(started).Round(time.Millisecond), extra>>20)
	require.Less(t, extra, int64(100<<20), "la exportación no debe acumular filas en memoria")

	f, err := os.Open(dest)
	require.NoError(t, err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		lines++
	}
	require.Equal(t, 1_000_001, lines)
}

func (e *liveEnv) exportTypes(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "types.json")
	x := e.exporter(dest)
	t.Cleanup(func() { _ = x.ServiceShutdown() })
	qid, _ := e.mustRun(t, "x2", `SELECT CAST(18446744073709551615 AS UNSIGNED) AS a, 12.50 AS b, X'00FF10' AS c, JSON_OBJECT('k', 1) AS d,
		TIMESTAMP('2024-01-02 03:04:05.123456') AS e, NULL AS f, '😀 "x"' AS g`)

	_, err := x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "rerun", Options: export.Options{Format: export.FormatJSON}})
	require.NoError(t, err)
	res := e.exportDone(t, 30*time.Second)
	require.Nil(t, res.Error)

	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(data, &rows), string(data))
	require.Len(t, rows, 1)
	r := rows[0]
	require.Equal(t, "18446744073709551615", r["a"])
	require.Equal(t, "12.50", r["b"])
	require.Equal(t, "AP8Q", r["c"])           // base64 de 00 FF 10
	if strings.HasPrefix(e.image, "mariadb") { // en MariaDB JSON es un alias de LONGTEXT: llega como texto
		require.JSONEq(t, `{"k": 1}`, r["d"].(string))
	} else {
		require.Equal(t, map[string]any{"k": 1.0}, r["d"])
	}
	require.Equal(t, "2024-01-02T03:04:05.123456", r["e"])
	require.Nil(t, r["f"])
	require.Equal(t, "😀 \"x\"", r["g"])
}

func (e *liveEnv) exportCancel(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "cancel.csv")
	x := e.exporter(dest)
	t.Cleanup(func() { _ = x.ServiceShutdown() })
	// 10 M de filas (multiplicando por 10 el producto cruzado): tarda segundos, así que la cancelación llega en pleno vuelo.
	big := million + " CROSS JOIN (SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9) d6"
	qid, _ := e.mustRun(t, "x3", big, func(r *ExecuteRequest) { r.MaxRows = 10; r.AutoLimit = 0 })

	start, err := x.Export(context.Background(), ExportRequest{QueryID: qid, Source: "rerun", Options: export.Options{Format: export.FormatCSV}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		st, err := os.Stat(dest + ".partial")
		return err == nil && st.Size() > 0
	}, 20*time.Second, 20*time.Millisecond)

	began := time.Now()
	require.NoError(t, x.CancelExport(start.JobID))
	res := e.exportDone(t, 10*time.Second)
	require.True(t, res.Canceled, "%+v", res)
	require.Less(t, time.Since(began), 2*time.Second)
	require.NoFileExists(t, dest+".partial")
	require.NoFileExists(t, dest)

	// La conexión de la exportación se liberó y la sesión sigue sirviendo consultas.
	sess, _ := e.q.d.Sessions.Get(e.sid)
	require.Eventually(t, func() bool { return sess.TabCount() <= 3 }, 5*time.Second, 50*time.Millisecond)
	_, done := e.mustRun(t, "x3", "SELECT 1")
	require.EqualValues(t, 1, done.Results[0].RowCount)
}
