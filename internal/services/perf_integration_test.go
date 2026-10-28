//go:build integration

package services

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/testutil/mysqlbox"
)

// TestQueryPerfIntegration mide los criterios de rendimiento del spec 06 con 1 M de filas reales:
// primera ventana visible < 500 ms y memoria del buffer acotada. Las cifras se registran con -v para docs/benchmarks.md.
func TestQueryPerfIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			e.firstWindow(t)
		})
	}
}

func (e *liveEnv) firstWindow(t *testing.T) {
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	start := time.Now()
	res, err := e.q.Execute(context.Background(), ExecuteRequest{SessionID: e.sid, TabID: "perf", SQL: million, MaxRows: 1_000_000})
	require.NoError(t, err)

	// El grid pide la ventana 0 en cuanto llega query:statement y la repite con cada query:progress.
	var first time.Duration
	for first == 0 {
		w, err := e.q.GetRows(res.QueryID, 0, 0, 200)
		if err == nil && len(w.Rows) == 200 {
			first = time.Since(start)
		} else {
			require.Less(t, time.Since(start), 10*time.Second, "la primera ventana no llegó")
			time.Sleep(5 * time.Millisecond)
		}
	}
	require.Less(t, first, 500*time.Millisecond, "criterio del spec 06: primera ventana < 500 ms")

	var done *QueryDone
	deadline := time.After(2 * time.Minute)
	for done == nil {
		select {
		case ev := <-e.events:
			if d, ok := ev.data.(QueryDone); ok && d.QueryID == res.QueryID {
				done = &d
			}
		case <-deadline:
			t.Fatal("la consulta de 1 M de filas no terminó")
		}
	}
	total := time.Since(start)
	require.EqualValues(t, 1_000_000, done.Results[0].RowCount)

	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	buf := e.q.d.Results.Total()

	// Acceso aleatorio una vez cargado: debe ser instantáneo.
	probe := time.Now()
	for _, off := range []int{0, 250_000, 500_000, 999_800} {
		w, err := e.q.GetRows(res.QueryID, 0, off, 200)
		require.NoError(t, err)
		require.Len(t, w.Rows, 200)
	}
	random := time.Since(probe) / 4

	t.Logf("1M filas: primera ventana %s · completa %s (%.0f filas/s) · buffer %d MiB · heap +%d MiB · GetRows(200) %s",
		first.Round(time.Millisecond), total.Round(time.Millisecond), 1e6/total.Seconds(), buf>>20,
		(int64(after.HeapAlloc)-int64(base.HeapAlloc))>>20, random.Round(time.Microsecond))
}
