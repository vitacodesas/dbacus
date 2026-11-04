package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func init() { purgePause = 0 }

func seedProfile(t *testing.T, s *Store, name string) string {
	t.Helper()
	p, err := s.SaveProfile(context.Background(), model.ConnectionProfile{Name: name, Driver: "mysql"})
	require.NoError(t, err)
	return p.ID
}

func allMetrics(v float64) map[string]float64 {
	m := make(map[string]float64, len(MonitorMetrics))
	for _, name := range MonitorMetrics {
		m[name] = v
	}
	return m
}

func count(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(1) FROM `+table).Scan(&n))
	return n
}

func monitorTables() []string {
	return []string{"monitor_run", "monitor_sample", "monitor_digest", "monitor_digest_sample", "monitor_rollup_hour", "monitor_rollup_hour_digest",
		"monitor_rollup_day", "monitor_rollup_day_digest", "monitor_annotation"}
}

// totals suma ejecuciones, tiempo y muestras de todos los niveles: lo que no puede cambiar al agregar o recortar.
type totals struct{ execs, totalUs, samples int64 }

func sumAll(t *testing.T, s *Store) totals {
	t.Helper()
	var tt totals
	one := func(q string, dst ...any) { require.NoError(t, s.db.QueryRow(q).Scan(dst...)) }
	var e1, e2, e3, u1, u2, u3, n1, n2, n3 int64
	one(`SELECT COALESCE(SUM(execs),0), COALESCE(SUM(total_us),0) FROM monitor_digest_sample`, &e1, &u1)
	one(`SELECT COALESCE(SUM(execs),0), COALESCE(SUM(total_us),0) FROM monitor_rollup_hour_digest`, &e2, &u2)
	one(`SELECT COALESCE(SUM(execs),0), COALESCE(SUM(total_us),0) FROM monitor_rollup_day_digest`, &e3, &u3)
	one(`SELECT COUNT(1) FROM monitor_sample`, &n1)
	one(`SELECT COALESCE(SUM(samples),0) FROM monitor_rollup_hour`, &n2)
	one(`SELECT COALESCE(SUM(samples),0) FROM monitor_rollup_day`, &n3)
	tt.execs, tt.totalUs, tt.samples = e1+e2+e3, u1+u2+u3, n1+n2+n3
	return tt
}

func TestMonitorMetricColumnsMatchMigration(t *testing.T) {
	s := newStore(t)
	cols := func(table string) map[string]bool {
		rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		require.NoError(t, err)
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var n string
			require.NoError(t, rows.Scan(&n))
			out[n] = true
		}
		return out
	}
	raw, hour, day := cols("monitor_sample"), cols("monitor_rollup_hour"), cols("monitor_rollup_day")
	for _, m := range MonitorMetrics {
		require.True(t, raw[m], m)
		for _, suf := range []string{"_avg", "_min", "_max"} {
			require.True(t, hour[m+suf], m+suf)
			require.True(t, day[m+suf], m+suf)
		}
	}
	require.Len(t, raw, len(MonitorMetrics)+7) // id, run_id, profile_id, ts, span_ms, after_gap, proc_json
}

func TestInsertSampleDictionaryAndAtomicity(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0, IntervalSec: 30, ServerVersion: "8.0.36", ServerID: "uuid-1"})
	require.NoError(t, err)

	long := strings.Repeat("é", MaxDigestTextBytes) // 2 bytes por carácter: debe recortarse sin partir un rune
	in := SampleInput{RunID: run.ID, ProfileID: pid, At: t0.Add(30 * time.Second), SpanMs: 30000, Metrics: map[string]float64{"qps": 12.5}, ProcJSON: `{"u":{"app":3}}`,
		Digests: []DigestDelta{
			{Schema: "shop", Digest: "d1", Text: "SELECT ?", Execs: 10, TotalUs: 500},
			{Schema: "", Digest: OtherDigest, Execs: 1, TotalUs: 1},
			{Schema: "shop", Digest: "d2", Text: long, Execs: 2, TotalUs: 7},
		}}
	_, err = s.InsertSample(ctx, in)
	require.NoError(t, err)
	in.At = in.At.Add(30 * time.Second)
	in.Digests = []DigestDelta{{Schema: "shop", Digest: "d1", Execs: 5, TotalUs: 100}} // sin texto: no pisa el guardado
	_, err = s.InsertSample(ctx, in)
	require.NoError(t, err)

	require.Equal(t, 3, count(t, s, "monitor_digest"))
	require.Equal(t, 4, count(t, s, "monitor_digest_sample"))
	var text string
	require.NoError(t, s.db.QueryRow(`SELECT text FROM monitor_digest WHERE digest = 'd1'`).Scan(&text))
	require.Equal(t, "SELECT ?", text)
	require.NoError(t, s.db.QueryRow(`SELECT text FROM monitor_digest WHERE digest = 'd2'`).Scan(&text))
	require.LessOrEqual(t, len(text), MaxDigestTextBytes)
	require.True(t, strings.HasPrefix(text, "é"))
	var nullQps, nullSlow *float64
	require.NoError(t, s.db.QueryRow(`SELECT qps, slow_queries FROM monitor_sample LIMIT 1`).Scan(&nullQps, &nullSlow))
	require.NotNil(t, nullQps)
	require.Nil(t, nullSlow, "una métrica ausente se guarda NULL")

	missing, err := s.MissingDigestTexts(ctx, pid, []DigestKey{{"shop", "d1"}, {"shop", "dX"}, {"shop", "d2"}})
	require.NoError(t, err)
	require.Equal(t, []DigestKey{{"shop", "dX"}}, missing)

	// Una muestra con una ejecución inexistente no deja nada a medias.
	before := []int{count(t, s, "monitor_sample"), count(t, s, "monitor_digest"), count(t, s, "monitor_digest_sample")}
	_, err = s.InsertSample(ctx, SampleInput{RunID: 9999, ProfileID: pid, At: t0, Digests: []DigestDelta{{Schema: "x", Digest: "new", Text: "t"}}})
	require.Error(t, err)
	require.Equal(t, before, []int{count(t, s, "monitor_sample"), count(t, s, "monitor_digest"), count(t, s, "monitor_digest_sample")})

	runs, err := s.ListMonitorRuns(ctx, pid)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, 2, runs[0].Samples)
	require.Equal(t, RunRunning, runs[0].Reason)
	require.Nil(t, runs[0].EndedAt)
}

func TestStartupClosesDanglingRuns(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	withSamples, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0, IntervalSec: 30})
	require.NoError(t, err)
	empty, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0.Add(time.Hour), IntervalSec: 30})
	require.NoError(t, err)
	stopped, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0.Add(2 * time.Hour), IntervalSec: 30})
	require.NoError(t, err)
	end := t0.Add(3 * time.Hour)
	require.NoError(t, s.CloseMonitorRun(ctx, stopped.ID, RunStopped, end))
	require.NoError(t, s.CloseMonitorRun(ctx, stopped.ID, RunCrashed, end.Add(time.Hour)), "cerrar dos veces no cambia nada")
	var nf *apperr.AppError
	require.ErrorAs(t, s.CloseMonitorRun(ctx, 12345, RunStopped, end), &nf)
	require.Equal(t, apperr.NotFound, nf.Code)

	last := t0.Add(10 * time.Minute)
	for _, at := range []time.Time{t0.Add(time.Minute), last} {
		_, err = s.InsertSample(ctx, SampleInput{RunID: withSamples.ID, ProfileID: pid, At: at, AfterGap: at == last})
		require.NoError(t, err)
	}

	n, err := s.CloseDanglingMonitorRuns(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	n, err = s.CloseDanglingMonitorRuns(ctx)
	require.NoError(t, err)
	require.Zero(t, n)

	runs, err := s.ListMonitorRuns(ctx, pid)
	require.NoError(t, err)
	byID := map[int64]MonitorRun{}
	for _, r := range runs {
		byID[r.ID] = r
	}
	require.Equal(t, RunAppClosed, byID[withSamples.ID].Reason)
	require.Equal(t, last.UnixMilli(), byID[withSamples.ID].EndedAt.UnixMilli(), "termina en la última muestra")
	require.Equal(t, 1, byID[withSamples.ID].Gaps)
	require.Equal(t, RunAppClosed, byID[empty.ID].Reason)
	require.Equal(t, empty.StartedAt.UnixMilli(), byID[empty.ID].EndedAt.UnixMilli(), "sin muestras termina en su inicio")
	require.Equal(t, RunStopped, byID[stopped.ID].Reason)
	require.Equal(t, end.UnixMilli(), byID[stopped.ID].EndedAt.UnixMilli())
}

func TestMonitorAnnotations(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	a, err := s.AddMonitorAnnotation(ctx, MonitorAnnotation{ProfileID: pid, At: t0, Text: "deploy v2.14"})
	require.NoError(t, err)
	_, err = s.AddMonitorAnnotation(ctx, MonitorAnnotation{ProfileID: pid, At: t0.Add(48 * time.Hour), Text: "índice nuevo"})
	require.NoError(t, err)
	got, err := s.ListMonitorAnnotations(ctx, pid, t0.Add(-time.Hour), t0.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "deploy v2.14", got[0].Text)
	require.NoError(t, s.DeleteMonitorAnnotation(ctx, a.ID))
	var ae *apperr.AppError
	require.ErrorAs(t, s.DeleteMonitorAnnotation(ctx, a.ID), &ae)
	require.Equal(t, apperr.NotFound, ae.Code)
}

// fill inserta muestras cada step entre [from, to) en una transacción; devuelve las muestras escritas.
func fill(t *testing.T, s *Store, pid string, run int64, from, to time.Time, step time.Duration, digests int) []SampleInput {
	t.Helper()
	var ins []SampleInput
	i := 0
	for at := from; at.Before(to); at = at.Add(step) {
		ds := make([]DigestDelta, 0, digests+1)
		for d := range digests {
			ds = append(ds, DigestDelta{Schema: "shop", Digest: fmt.Sprintf("d%03d", (i+d)%200), Text: fmt.Sprintf("SELECT ? FROM t%d", (i+d)%200),
				Execs: int64(1 + d), TotalUs: int64(100 * (1 + d)), MaxObservedUs: int64(i%50 + d), RowsExamined: 10, RowsSent: 1, NoIndex: int64(d % 2), TmpDisk: 0, Errors: 0})
		}
		ds = append(ds, DigestDelta{Digest: OtherDigest, Execs: 3, TotalUs: 30})
		ins = append(ins, SampleInput{RunID: run, ProfileID: pid, At: at, SpanMs: step.Milliseconds(), Metrics: allMetrics(float64(i%100) + 1), ProcJSON: `{"u":{"app":3,"report":1}}`, Digests: ds})
		i++
	}
	_, err := s.insertSamples(context.Background(), ins)
	require.NoError(t, err)
	return ins
}

func TestRetentionRollsUpBeforeDelete(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0, IntervalSec: 900})
	require.NoError(t, err)
	now := t0.Add(10*24*time.Hour + 30*time.Minute)
	ins := fill(t, s, pid, run.ID, t0, now, 15*time.Minute, 3)
	before := sumAll(t, s)
	require.Equal(t, int64(len(ins)), before.samples)

	// Valor esperado de qps (media global) para comprobar la media ponderada tras dos niveles de agregación.
	var sumQps float64
	for _, in := range ins {
		sumQps += in.Metrics["qps"]
	}

	res, err := s.PurgeMonitor(ctx, now, MonitorRetention{RawDays: 2, HourlyDays: 5, DailyDays: 365})
	require.NoError(t, err)
	require.Positive(t, res.HoursRolled)
	require.Positive(t, res.DaysRolled)
	require.False(t, res.CapTrimmed)

	after := sumAll(t, s)
	require.Equal(t, before, after, "agregar no pierde ejecuciones, tiempo ni muestras")

	rawCut := truncMs(now.UnixMilli()-2*dayMs, hourMs)
	var minRaw int64
	require.NoError(t, s.db.QueryRow(`SELECT MIN(ts) FROM monitor_sample`).Scan(&minRaw))
	require.GreaterOrEqual(t, minRaw, rawCut)
	var minHour int64
	require.NoError(t, s.db.QueryRow(`SELECT MIN(hour_ts) FROM monitor_rollup_hour`).Scan(&minHour))
	require.GreaterOrEqual(t, minHour, truncMs(now.UnixMilli()-5*dayMs, dayMs))
	require.Positive(t, count(t, s, "monitor_rollup_day"))

	var weighted, n float64
	require.NoError(t, s.db.QueryRow(`SELECT COALESCE(SUM(qps), 0), COUNT(1) FROM monitor_sample`).Scan(&weighted, &n))
	var h, hn float64
	require.NoError(t, s.db.QueryRow(`SELECT COALESCE(SUM(qps_avg * samples), 0), COALESCE(SUM(samples), 0) FROM monitor_rollup_hour`).Scan(&h, &hn))
	var d, dn float64
	require.NoError(t, s.db.QueryRow(`SELECT COALESCE(SUM(qps_avg * samples), 0), COALESCE(SUM(samples), 0) FROM monitor_rollup_day`).Scan(&d, &dn))
	require.InDelta(t, sumQps, weighted+h+d, 1e-6, "la suma de qps se conserva con medias ponderadas")

	// Idempotente: una segunda pasada no cambia nada.
	res2, err := s.PurgeMonitor(ctx, now, MonitorRetention{RawDays: 2, HourlyDays: 5, DailyDays: 365})
	require.NoError(t, err)
	require.Zero(t, res2.HoursRolled+res2.DaysRolled)
	require.Equal(t, before, sumAll(t, s))

	// Los días caducados sí se borran (esto es lo único que pierde datos) y el diccionario sin uso se limpia.
	res3, err := s.PurgeMonitor(ctx, now, MonitorRetention{RawDays: 2, HourlyDays: 5, DailyDays: 3})
	require.NoError(t, err)
	require.Positive(t, res3.DayRowsDeleted)
	require.Less(t, sumAll(t, s).samples, before.samples)
}

func TestRetentionSizeCap(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0, IntervalSec: 30})
	require.NoError(t, err)
	now := t0.Add(24*time.Hour + 20*time.Minute)
	fill(t, s, pid, run.ID, t0, now, 30*time.Second, 10)
	before := sumAll(t, s)
	used, err := s.usedBytes(ctx)
	require.NoError(t, err)

	// La fecha no recorta nada (RawDays=7); solo lo hace el tope.
	res, err := s.PurgeMonitor(ctx, now, MonitorRetention{RawDays: 7, HourlyDays: 90, DailyDays: 365})
	require.NoError(t, err)
	require.False(t, res.CapTrimmed)
	require.Zero(t, res.HoursRolled)

	capBytes := used / 2
	res, err = s.PurgeMonitor(ctx, now, MonitorRetention{RawDays: 7, HourlyDays: 90, DailyDays: 365, MaxBytes: capBytes})
	require.NoError(t, err)
	require.True(t, res.CapTrimmed)
	require.Positive(t, res.HoursRolled)
	require.LessOrEqual(t, res.UsedBytes, capBytes)
	require.Zero(t, res.DayRowsDeleted, "primero se recorta lo crudo")
	require.Equal(t, before, sumAll(t, s), "recortar crudo conserva los totales (quedan como rollups)")

	// La hora en curso nunca se toca.
	var inCurrent int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(1) FROM monitor_sample WHERE ts >= ?`, truncMs(now.UnixMilli(), hourMs)).Scan(&inCurrent))
	require.Equal(t, 40, inCurrent) // 20 minutos a 30 s

	// Con un tope imposible se agota lo agregable, sin bucle infinito ni tocar la hora en curso.
	res, err = s.PurgeMonitor(ctx, now, MonitorRetention{RawDays: 7, HourlyDays: 90, DailyDays: 365, MaxBytes: 1})
	require.NoError(t, err)
	require.True(t, res.CapTrimmed)
	require.Equal(t, 40, count(t, s, "monitor_sample"))
}

func TestProfileDeleteCascades(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	keep := seedProfile(t, s, "keep")
	gone := seedProfile(t, s, "gone")
	now := t0.Add(6 * 24 * time.Hour)
	for _, pid := range []string{keep, gone} {
		run, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0, IntervalSec: 300})
		require.NoError(t, err)
		fill(t, s, pid, run.ID, t0, now, 15*time.Minute, 3)
		_, err = s.AddMonitorAnnotation(ctx, MonitorAnnotation{ProfileID: pid, At: t0, Text: "x"})
		require.NoError(t, err)
	}
	_, err := s.PurgeMonitor(ctx, now, MonitorRetention{RawDays: 1, HourlyDays: 3, DailyDays: 365}) // crea rollups de hora y de día
	require.NoError(t, err)
	for _, tbl := range monitorTables() {
		require.Positive(t, count(t, s, tbl), tbl)
	}
	keepBefore := map[string]int{}
	for _, tbl := range monitorTables() {
		require.NoError(t, s.db.QueryRow(`SELECT COUNT(1) FROM `+tbl+` WHERE EXISTS (SELECT 1)`).Scan(new(int)))
		keepBefore[tbl] = count(t, s, tbl)
	}

	require.NoError(t, s.DeleteProfile(ctx, gone))
	for _, tbl := range monitorTables() {
		require.Less(t, count(t, s, tbl), keepBefore[tbl], tbl)
		require.Positive(t, count(t, s, tbl), tbl+": el otro perfil sigue intacto")
	}
	var leftovers int
	require.NoError(t, s.db.QueryRow(`SELECT (SELECT COUNT(1) FROM monitor_run WHERE profile_id = ?) + (SELECT COUNT(1) FROM monitor_sample WHERE profile_id = ?)
		+ (SELECT COUNT(1) FROM monitor_digest WHERE profile_id = ?) + (SELECT COUNT(1) FROM monitor_rollup_day WHERE profile_id = ?)
		+ (SELECT COUNT(1) FROM monitor_annotation WHERE profile_id = ?)`, gone, gone, gone, gone, gone).Scan(&leftovers))
	require.Zero(t, leftovers)

	// «Borrar datos» por perfil, sin borrar el perfil.
	require.NoError(t, s.PurgeMonitorData(ctx, keep, nil))
	for _, tbl := range monitorTables() {
		require.Zero(t, count(t, s, tbl), tbl)
	}
	_, err = s.GetProfile(ctx, keep)
	require.NoError(t, err)
}

func TestPurgeMonitorDataBefore(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0, IntervalSec: 300})
	require.NoError(t, err)
	fill(t, s, pid, run.ID, t0, t0.Add(4*time.Hour), 5*time.Minute, 2)
	cut := t0.Add(2 * time.Hour)
	require.NoError(t, s.PurgeMonitorData(ctx, pid, &cut))
	var minTs int64
	require.NoError(t, s.db.QueryRow(`SELECT MIN(ts) FROM monitor_sample`).Scan(&minTs))
	require.Equal(t, cut.UnixMilli(), minTs)
	require.Equal(t, 24, count(t, s, "monitor_sample"))
}

func TestMonitorSettingsValidation(t *testing.T) {
	require.Nil(t, ValidateSettings(model.DefaultSettings()))
	bad := map[string]func(*model.Settings){
		"monitorRawDays":         func(s *model.Settings) { s.MonitorRawDays = 0 },
		"monitorHourlyDays":      func(s *model.Settings) { s.MonitorHourlyDays = s.MonitorRawDays - 1 },
		"monitorDailyDays":       func(s *model.Settings) { s.MonitorDailyDays = s.MonitorHourlyDays - 1 },
		"monitorMaxMiB":          func(s *model.Settings) { s.MonitorMaxMiB = 1 },
		"monitorIntervalSec":     func(s *model.Settings) { s.MonitorIntervalSec = 1 },
		"monitorLiveIntervalSec": func(s *model.Settings) { s.MonitorLiveIntervalSec = 0 },
	}
	for field, mut := range bad {
		st := model.DefaultSettings()
		mut(&st)
		ae := ValidateSettings(st)
		require.NotNil(t, ae, field)
		require.Equal(t, field, ae.Detail)
	}
	hi := model.DefaultSettings()
	hi.MonitorLiveIntervalSec = 61
	require.Equal(t, "monitorLiveIntervalSec", ValidateSettings(hi).Detail)
	// Ajustes guardados antes de existir la vista en vivo: conservan los valores por defecto (automática, 2 s).
	d := model.DefaultSettings()
	require.True(t, d.MonitorLiveAuto)
	require.Equal(t, 2, d.MonitorLiveIntervalSec)
	require.Equal(t, MonitorRetention{RawDays: 7, HourlyDays: 90, DailyDays: 365, MaxBytes: 500 << 20}, MonitorRetentionFromSettings(d))
}

func TestMonitorAutoStartPersistsInProfile(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	p, err := s.SaveProfile(ctx, model.ConnectionProfile{Name: "p", Driver: "mysql", MonitorAutoStart: true})
	require.NoError(t, err)
	got, err := s.GetProfile(ctx, p.ID)
	require.NoError(t, err)
	require.True(t, got.MonitorAutoStart)
}

// TestMonitorSevenDaysAt30sVolume mide el volumen que predice el spec 22 (7 días crudos, 30 s, top-K=30 + __other__).
// No afirma tamaños: los registra con t.Log para anotarlos en el spec.
func TestMonitorSevenDaysAt30sVolume(t *testing.T) {
	if os.Getenv("DBACUS_MONITOR_VOLUME") == "" {
		t.Skip("medición de volumen (minutos con -race): DBACUS_MONITOR_VOLUME=1 go test -run SevenDays ./internal/store")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	s, err := Open(ctx, path)
	require.NoError(t, err)
	defer s.Close()
	pid := seedProfile(t, s, "p")
	run, err := s.OpenMonitorRun(ctx, MonitorRun{ProfileID: pid, StartedAt: t0, IntervalSec: 30})
	require.NoError(t, err)

	start := time.Now()
	now := t0.Add(7 * 24 * time.Hour)
	for day := range 7 { // una transacción por día para no inflar el WAL
		fill(t, s, pid, run.ID, t0.Add(time.Duration(day)*24*time.Hour), t0.Add(time.Duration(day+1)*24*time.Hour), 30*time.Second, 30)
	}
	insertTook := time.Since(start)
	_, err = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	require.NoError(t, err)
	st, err := os.Stat(path)
	require.NoError(t, err)

	samples, digestRows := count(t, s, "monitor_sample"), count(t, s, "monitor_digest_sample")
	require.Equal(t, 7*2880, samples)
	require.Equal(t, 7*2880*31, digestRows)
	t.Logf("7 d a 30 s, K=30+otros: %d muestras, %d filas de digest (%.0f/día), archivo %.1f MiB (%.1f MiB/día), diccionario %d digests, insertar %s",
		samples, digestRows, float64(digestRows)/7, float64(st.Size())/(1<<20), float64(st.Size())/(1<<20)/7, count(t, s, "monitor_digest"), insertTook.Round(time.Millisecond))

	pstart := time.Now()
	res, err := s.PurgeMonitor(ctx, now.Add(90*24*time.Hour), MonitorRetention{RawDays: 7, HourlyDays: 365, DailyDays: 3650})
	require.NoError(t, err)
	_, err = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	require.NoError(t, err)
	t.Logf("agregar los 7 días a hora: %d horas en %s; rollups hora %d filas, digest-hora %d filas; usado %.1f MiB",
		res.HoursRolled, time.Since(pstart).Round(time.Millisecond), count(t, s, "monitor_rollup_hour"), count(t, s, "monitor_rollup_hour_digest"), float64(res.UsedBytes)/(1<<20))
}
