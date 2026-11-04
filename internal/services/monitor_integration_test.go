//go:build integration

package services

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/store"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

// TestMonitorEndToEndIntegration recorre Start → muestras → Top/Series/Digest → Stop contra un servidor real, con carga
// generada y con un literal «secreto» que nunca debe llegar al histórico (spec 22 ⚑D7). Dura ~15 s por imagen: el
// intervalo mínimo es 5 s y hacen falta una línea base y dos deltas.
func TestMonitorEndToEndIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = st.Close() })
			sessions := session.NewManager(session.Options{})
			t.Cleanup(sessions.CloseAll)

			var samples, procEvents atomic.Int32
			var mu sync.Mutex
			var states []MonitorStateEvent
			svc := NewMonitorService(MonitorDeps{Sessions: sessions, Store: st, Emit: func(name string, data any) {
				switch name {
				case "monitor:sample":
					samples.Add(1)
				case "monitor:processes":
					procEvents.Add(1)
				case "monitor:state":
					mu.Lock()
					states = append(states, data.(MonitorStateEvent))
					mu.Unlock()
				}
			}})
			t.Cleanup(func() { _ = svc.ServiceShutdown() })

			open := func(name string, readOnly bool) (*session.Session, model.ConnectionProfile) {
				p, err := st.SaveProfile(ctx, model.ConnectionProfile{Name: name, Driver: "mysql", Host: in.Host, Port: in.Port,
					User: mysqlbox.User, Database: mysqlbox.Database, ReadOnly: readOnly, TLS: model.TLSConfig{Mode: "preferred"}})
				require.NoError(t, err)
				s, err := sessions.Open(ctx, session.OpenRequest{Profile: p, Password: mysqlbox.Password})
				require.NoError(t, err)
				return s, p
			}
			sess, prof := open("rw", false)

			caps, err := svc.Capabilities(ctx, sess.ID)
			require.NoError(t, err)
			t.Logf("%s: processlist=%s/%s digest=%s/%s status=%s/%s", image, caps.Processlist.State, caps.Processlist.Reason,
				caps.Digest.State, caps.Digest.Reason, caps.Status.State, caps.Status.Reason)
			require.Equal(t, model.SourceOK, caps.Status.State)

			// Carga con dos formas de consulta; la segunda cuesta más y debe quedar arriba por tiempo.
			loadCtx, stopLoad := context.WithCancel(ctx)
			var loadWG sync.WaitGroup
			loadWG.Add(1)
			go func() {
				defer loadWG.Done()
				c, err := sess.AcquireConn(loadCtx, "load")
				if err != nil {
					return
				}
				for i := 0; loadCtx.Err() == nil; i++ {
					stmt := "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'zzsecret'"
					if i%10 == 0 {
						stmt = "SELECT SLEEP(0.05), 'zzsecret'"
					}
					r, err := c.Run(loadCtx, stmt)
					if err != nil {
						return
					}
					for r.Next() {
					}
					_ = r.Close()
					time.Sleep(5 * time.Millisecond)
				}
			}()
			t.Cleanup(func() { stopLoad(); loadWG.Wait() })

			started := time.Now()
			run, err := svc.Start(ctx, sess.ID, model.MonitorConfig{IntervalSec: 5})
			require.NoError(t, err)
			require.NoError(t, svc.WatchProcesses(ctx, sess.ID, 1000))
			require.Eventually(t, func() bool { return samples.Load() >= 3 && procEvents.Load() >= 1 }, 40*time.Second, 200*time.Millisecond,
				"tres muestras (línea base y dos deltas) y una vista en vivo")
			require.NoError(t, svc.Stop(ctx, run.ID))
			require.NoError(t, svc.UnwatchProcesses(sess.ID))
			stopLoad()
			loadWG.Wait()

			runs, err := svc.Runs(ctx, prof.ID)
			require.NoError(t, err)
			require.Len(t, runs, 1)
			require.Equal(t, "stopped", runs[0].Reason)
			require.GreaterOrEqual(t, runs[0].Samples, 3)

			from, to := started.Add(-time.Minute), time.Now().Add(time.Minute)
			series, err := svc.Series(ctx, model.SeriesRequest{ProfileID: prof.ID, From: from, To: to, Metrics: []string{"qps", "threads_connected"}})
			require.NoError(t, err)
			require.Len(t, series, 2)
			for _, s := range series {
				require.NotEmpty(t, s.Points, s.Metric)
			}

			if caps.Digest.State == model.SourceUnavailable {
				t.Logf("%s: digest no disponible (%s): se omiten las comprobaciones del ranking", image, caps.Digest.Reason)
			} else {
				top, err := svc.Top(ctx, model.TopRequest{ProfileID: prof.ID, From: from, To: to, OrderBy: model.OrderExecs, Limit: 50, IncludeSystem: true})
				require.NoError(t, err)
				var found *model.DigestStat
				for i := range top {
					require.NotContains(t, top[i].Text, "zzsecret", "el texto normalizado no lleva literales")
					// ⚑D16: ni las lecturas del monitor ni sus SET de preparación aparecen en su propio ranking.
					lower := strings.ToLower(top[i].Text)
					for _, own := range []string{"processlist", "events_statements_summary_by_digest", "set session", "show global status where", "max_connections"} {
						require.NotContains(t, lower, own, "el monitor no debe verse a sí mismo: %q", top[i].Text)
					}
					if lt := strings.ToLower(top[i].Text); strings.Contains(lt, "table_name") && strings.Contains(lt, "information_schema") {
						found = &top[i]
					}
				}
				require.NotNil(t, found, "la consulta de carga aparece en el ranking: %d digests", len(top))
				require.Greater(t, found.Execs, uint64(10))
				require.Greater(t, found.TotalMs, 0.0)

				d, err := svc.Digest(ctx, prof.ID, found.Digest, from, to)
				require.NoError(t, err)
				require.NotEmpty(t, d.Points)
				t.Logf("%s: %d digests; la de carga: %d ejecuciones, %.1f ms en total", image, len(top), found.Execs, found.TotalMs)
			}

			// Perfil de solo lectura: el monitor funciona igual y la conexión del monitor no puede escribir (spec 22 ⚑D8).
			roSess, roProf := open("ro", true)
			roRun, err := svc.Start(ctx, roSess.ID, model.MonitorConfig{IntervalSec: 5})
			require.NoError(t, err)
			require.Eventually(t, func() bool { return samples.Load() >= 4 }, 20*time.Second, 100*time.Millisecond)
			require.NoError(t, svc.Stop(ctx, roRun.ID))
			roRuns, err := svc.Runs(ctx, roProf.ID)
			require.NoError(t, err)
			require.Len(t, roRuns, 1)

			mu.Lock()
			defer mu.Unlock()
			var running, stopped int
			for _, s := range states {
				switch s.State {
				case "running":
					if s.Reason == "" { // «running/recovered» es una fuente que se recuperó, no una ejecución nueva
						running++
					}
				case "stopped":
					stopped++
				}
			}
			require.Equal(t, 2, running)
			require.Equal(t, 2, stopped)
		})
	}
}
