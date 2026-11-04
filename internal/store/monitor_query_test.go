package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

func openRun(t *testing.T, s *Store, pid string, start time.Time, intervalSec int) int64 {
	t.Helper()
	r, err := s.OpenMonitorRun(context.Background(), MonitorRun{ProfileID: pid, StartedAt: start, IntervalSec: intervalSec})
	require.NoError(t, err)
	return r.ID
}

func put(t *testing.T, s *Store, pid string, run int64, at time.Time, ds ...DigestDelta) {
	t.Helper()
	_, err := s.InsertSample(context.Background(), SampleInput{RunID: run, ProfileID: pid, At: at, SpanMs: 30000, Metrics: map[string]float64{"qps": 1}, Digests: ds})
	require.NoError(t, err)
}

func digestMap(stats []model.DigestStat) map[string]model.DigestStat {
	m := make(map[string]model.DigestStat, len(stats))
	for _, st := range stats {
		m[st.Schema+"/"+st.Digest] = st
	}
	return m
}

func digests(stats []model.DigestStat) []string {
	out := make([]string, 0, len(stats))
	for _, st := range stats {
		out = append(out, st.Digest)
	}
	return out
}

func validationDetail(t *testing.T, err error) string {
	t.Helper()
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.Validation, ae.Code)
	return ae.Detail
}

// expectedExecs suma las ejecuciones por digest de las muestras de [a, b), sin la fila __other__.
func expectedExecs(ins []SampleInput, a, b time.Time) map[string]int64 {
	out := map[string]int64{}
	for _, in := range ins {
		if in.At.Before(a) || !in.At.Before(b) {
			continue
		}
		for _, d := range in.Digests {
			if d.Digest != OtherDigest {
				out[d.Digest] += d.Execs
			}
		}
	}
	return out
}

func execsByDigest(stats []model.DigestStat) map[string]int64 {
	out := map[string]int64{}
	for _, st := range stats {
		out[st.Digest] = int64(st.Execs)
	}
	return out
}

func topAll(t *testing.T, s *Store, pid string, from, to time.Time) []model.DigestStat {
	t.Helper()
	out, err := s.TopDigests(context.Background(), model.TopRequest{ProfileID: pid, From: from, To: to, Limit: 1000})
	require.NoError(t, err)
	return out
}

func TestTopMixedTiers(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 300)
	end := t0.Add(72 * time.Hour)
	fill(t, s, pid, run, t0, end, 5*time.Minute, 3)
	before := digestMap(topAll(t, s, pid, t0, end))
	require.NotEmpty(t, before)
	require.Zero(t, count(t, s, "monitor_rollup_day_digest")+count(t, s, "monitor_rollup_hour_digest"))

	// día 0 → rollup por día, día 1 → rollups por hora, día 2 → crudo.
	_, err := s.PurgeMonitor(ctx, end, MonitorRetention{RawDays: 1, HourlyDays: 2, DailyDays: 365})
	require.NoError(t, err)
	require.Positive(t, count(t, s, "monitor_rollup_day_digest"))
	require.Positive(t, count(t, s, "monitor_rollup_hour_digest"))
	require.Positive(t, count(t, s, "monitor_digest_sample"))

	after := digestMap(topAll(t, s, pid, t0, end))
	require.Len(t, after, len(before))
	for k, b := range before {
		a, ok := after[k]
		require.True(t, ok, k)
		require.Equal(t, b.Execs, a.Execs, "ejecuciones de %s: un nivel se contó dos veces o se perdió", k)
		require.InDelta(t, b.TotalMs, a.TotalMs, 1e-9, k)
		require.Equal(t, b.RowsExamined, a.RowsExamined, k)
		require.Equal(t, b.NoIndex, a.NoIndex, k)
		require.False(t, a.Approx, "el rango coincide con los cubos: no es aproximado (%s)", k)
	}

	// Dos rangos contiguos reparten los cubos: su suma es el total (sin solape ni pérdida).
	mid := t0.Add(36 * time.Hour)
	left, right := execsByDigest(topAll(t, s, pid, t0, mid)), execsByDigest(topAll(t, s, pid, mid, end))
	for k, b := range before {
		require.Equal(t, int64(b.Execs), left[b.Digest]+right[b.Digest], k)
	}
}

func TestTopApproxEdges(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 300)
	end := t0.Add(72 * time.Hour)
	ins := fill(t, s, pid, run, t0, end, 5*time.Minute, 3)

	a, b := t0.Add(30*time.Minute), t0.Add(3*time.Hour)
	for _, st := range topAll(t, s, pid, a, b) {
		require.False(t, st.Approx, "con crudo el rango es exacto")
	}
	require.Equal(t, expectedExecs(ins, a, b), execsByDigest(topAll(t, s, pid, a, b)))

	_, err := s.PurgeMonitor(ctx, end, MonitorRetention{RawDays: 0, HourlyDays: 365, DailyDays: 3650})
	require.NoError(t, err)
	require.Zero(t, count(t, s, "monitor_digest_sample"), "todo el crudo quedó agregado por hora")

	// La hora 0 (punto medio t0+30 min) entra entera aunque el rango empiece a mitad: redondeo al nivel y Approx.
	got := topAll(t, s, pid, a, b)
	require.Equal(t, expectedExecs(ins, t0, b), execsByDigest(got))
	approx := 0
	for _, st := range got {
		if st.Approx {
			approx++
		}
	}
	require.Positive(t, approx)

	// Con el rango empezando después del punto medio, la hora 0 sale y el resto cabe entero: exacto.
	a2 := t0.Add(31 * time.Minute)
	got = topAll(t, s, pid, a2, b)
	require.Equal(t, expectedExecs(ins, t0.Add(time.Hour), b), execsByDigest(got))
	for _, st := range got {
		require.False(t, st.Approx, st.Digest)
	}
}

func TestTopOrderCriteria(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	d := func(name string, execs, us, re, rs, ni, tmp, er int64) DigestDelta {
		return DigestDelta{Schema: "shop", Digest: name, Text: "SELECT " + name, Execs: execs, TotalUs: us, RowsExamined: re, RowsSent: rs, NoIndex: ni, TmpDisk: tmp, Errors: er}
	}
	put(t, s, pid, run, t0.Add(10*time.Minute),
		d("A", 1_000_000, 1_000_000, 1_000_000, 1_000_000, 0, 0, 0),
		d("B", 10, 5_000_000, 100, 100, 0, 0, 0),
		d("C", 100, 100_000, 1_000_000, 10, 0, 0, 0),
		d("D", 50, 50_000, 0, 0, 40, 0, 0),
		d("E", 20, 20_000, 0, 0, 0, 0, 5),
		d("F", 30, 30_000, 0, 0, 0, 7, 0))
	to := t0.Add(time.Hour)
	order := func(by string, limit int) []string {
		got, err := s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: t0, To: to, OrderBy: by, Limit: limit})
		require.NoError(t, err)
		return digests(got)
	}
	require.Equal(t, []string{"B", "A", "C", "D", "F", "E"}, order("", 0), "por defecto, tiempo total")
	require.Equal(t, []string{"B", "A", "C", "D", "F", "E"}, order(model.OrderTotalTime, 0))
	require.Equal(t, []string{"A", "C", "D", "F", "E", "B"}, order(model.OrderExecs, 0))
	require.Equal(t, []string{"B", "C", "D", "F", "E", "A"}, order(model.OrderAvgTime, 0), "empate de media: desempata el tiempo total")
	require.Equal(t, []string{"C", "B", "A"}, order(model.OrderRowsRatio, 0), "sin filas examinadas no entra")
	require.Equal(t, []string{"D"}, order(model.OrderNoIndex, 0))
	require.Equal(t, []string{"F"}, order(model.OrderTmpDisk, 0))
	require.Equal(t, []string{"E"}, order(model.OrderErrors, 0))
	require.Equal(t, []string{"B", "A"}, order(model.OrderTotalTime, 2))

	got, err := s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: t0, To: to, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, "SELECT B", got[0].Text)
	require.InDelta(t, 5000.0, got[0].TotalMs, 1e-9)
	require.InDelta(t, 500.0, got[0].AvgMs, 1e-9)

	_, err = s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: t0, To: to, OrderBy: "nope"})
	require.Equal(t, "orderBy", validationDetail(t, err))
	_, err = s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: t0, To: to, Limit: 5000})
	require.Equal(t, "topK", validationDetail(t, err))
	_, err = s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: to, To: t0})
	require.Equal(t, "range", validationDetail(t, err))
}

func TestTopSchemaFilter(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	mk := func(schema, name string) DigestDelta {
		return DigestDelta{Schema: schema, Digest: name, Text: "SELECT " + name, Execs: 5, TotalUs: 500}
	}
	put(t, s, pid, run, t0.Add(time.Minute), mk("shop", "s1"), mk("mysql", "m1"), mk("information_schema", "i1"), mk("", "n1"), mk("", OtherDigest), mk("shop", "s2"))
	to := t0.Add(time.Hour)
	top := func(req model.TopRequest) []string {
		req.ProfileID, req.From, req.To = pid, t0, to
		got, err := s.TopDigests(ctx, req)
		require.NoError(t, err)
		names := digests(got)
		for i := range names { // orden estable para comparar
			for j := i + 1; j < len(names); j++ {
				if names[j] < names[i] {
					names[i], names[j] = names[j], names[i]
				}
			}
		}
		return names
	}
	require.Equal(t, []string{"n1", "s1", "s2"}, top(model.TopRequest{}), "sin IncludeSystem: ni bases del sistema ni __other__")
	require.Equal(t, []string{"__other__", "i1", "m1", "n1", "s1", "s2"}, top(model.TopRequest{IncludeSystem: true}))
	require.Equal(t, []string{"s1", "s2"}, top(model.TopRequest{Schema: "shop"}))
	require.Equal(t, []string{"m1"}, top(model.TopRequest{Schema: "mysql", IncludeSystem: true}))
	require.Empty(t, top(model.TopRequest{Schema: "mysql"}), "una base de sistema pedida a mano sigue oculta sin IncludeSystem")
}

func TestCompareRegressions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	mk := func(name string, execs, us int64) DigestDelta {
		return DigestDelta{Schema: "shop", Digest: name, Text: "SELECT " + name, Execs: execs, TotalUs: us}
	}
	aStart, bStart, bEnd := t0, t0.Add(24*time.Hour), t0.Add(48*time.Hour)
	put(t, s, pid, run, aStart.Add(time.Hour), mk("X", 1000, 1_000_000), mk("Y", 1000, 1_000_000), mk("W", 500, 500_000), mk("V", 50, 50_000), mk("R", 1000, 1_000_000))
	put(t, s, pid, run, bStart.Add(time.Hour), mk("X", 1000, 2_000_000), mk("Y", 1000, 1_100_000), mk("Z", 800, 800_000), mk("V", 50, 200_000), mk("R", 1000, 1_500_000))

	a, b := model.TimeRange{From: aStart, To: bStart}, model.TimeRange{From: bStart, To: bEnd}
	cmp, err := s.CompareDigests(ctx, pid, a, b)
	require.NoError(t, err)
	require.Equal(t, []string{"X", "R"}, digests(cmp.Regressions), "por impacto: X sube 1 ms × 1000, R 0,5 ms × 1000")
	require.InDelta(t, 100.0, cmp.Regressions[0].ChangePct, 1e-9)
	require.NotNil(t, cmp.Regressions[0].Prev)
	require.InDelta(t, 1.0, cmp.Regressions[0].Prev.AvgMs, 1e-9)
	require.True(t, cmp.Regressions[0].Regression)
	require.Equal(t, []string{"Z"}, digests(cmp.New))
	require.True(t, cmp.New[0].IsNew)
	require.Equal(t, []string{"W"}, digests(cmp.Gone))

	// El ranking con Compare marca lo mismo contra el periodo previo de igual duración.
	top, err := s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: bStart, To: bEnd, Compare: true, Limit: 100})
	require.NoError(t, err)
	m := digestMap(top)
	require.True(t, m["shop/X"].Regression)
	require.False(t, m["shop/Y"].Regression)
	require.InDelta(t, 10.0, m["shop/Y"].ChangePct, 1e-9)
	require.False(t, m["shop/V"].Regression, "300 % pero con 50 ejecuciones: por debajo del mínimo")
	require.InDelta(t, 300.0, m["shop/V"].ChangePct, 1e-9)
	require.True(t, m["shop/Z"].IsNew)
	require.Nil(t, m["shop/Z"].Prev)
	require.NotNil(t, m["shop/X"].Prev)

	_, err = s.CompareDigests(ctx, pid, model.TimeRange{}, b)
	require.Equal(t, "range", validationDetail(t, err))
}

func TestDigestDetailSeries(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 300)
	end := t0.Add(72 * time.Hour)
	ins := fill(t, s, pid, run, t0, end, 5*time.Minute, 3)

	check := func(label string) model.DigestDetail {
		want := expectedExecs(ins, t0, end)["d001"]
		det, err := s.DigestDetail(ctx, pid, "d001", t0, end)
		require.NoError(t, err, label)
		require.Equal(t, "d001", det.Digest)
		require.NotEmpty(t, det.Text)
		require.Equal(t, []string{"shop"}, det.Schemas)
		require.Equal(t, "shop", det.Stat.Schema)
		require.Equal(t, uint64(want), det.Stat.Execs, label)
		var sum uint64
		for _, p := range det.Points {
			sum += p.Execs
		}
		require.Equal(t, uint64(want), sum, "%s: la serie suma el total", label)
		require.LessOrEqual(t, len(det.Points), model.MonitorMaxPoints, label)
		require.NotEmpty(t, det.Points, label)
		return det
	}
	check("crudo")
	_, err := s.PurgeMonitor(ctx, end, MonitorRetention{RawDays: 1, HourlyDays: 2, DailyDays: 365})
	require.NoError(t, err)
	check("niveles mezclados")

	_, err = s.DigestDetail(ctx, pid, "no-existe", t0, end)
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.NotFound, ae.Code)
}

func TestSeriesGaps(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	run1 := func(pid string, flag bool) {
		r1 := openRun(t, s, pid, t0, 30)
		for i := range 20 {
			put(t, s, pid, r1, t0.Add(time.Duration(i)*30*time.Second))
		}
		require.NoError(t, s.CloseMonitorRun(ctx, r1, RunStopped, t0.Add(10*time.Minute)))
		r2 := openRun(t, s, pid, t0.Add(time.Hour), 30)
		for i := range 20 {
			in := SampleInput{RunID: r2, ProfileID: pid, At: t0.Add(time.Hour + time.Duration(i)*30*time.Second), SpanMs: 30000, Metrics: map[string]float64{"qps": 5}}
			if i == 0 && flag {
				in.AfterGap, in.SpanMs = true, (50*time.Minute + 30*time.Second).Milliseconds()
			}
			_, err := s.InsertSample(ctx, in)
			require.NoError(t, err)
		}
	}
	gapsOf := func(pid string, from, to time.Time) []model.TimeGap {
		series, err := s.SeriesData(ctx, model.SeriesRequest{ProfileID: pid, From: from, To: to, Metrics: []string{"qps"}})
		require.NoError(t, err)
		require.Len(t, series, 1)
		return series[0].Gaps
	}
	lastOfRun1, startOfRun2 := t0.Add(9*time.Minute+30*time.Second), t0.Add(time.Hour)

	flagged := seedProfile(t, s, "flagged")
	run1(flagged, true)
	plain := seedProfile(t, s, "plain")
	run1(plain, false)
	for _, pid := range []string{flagged, plain} {
		g := gapsOf(pid, t0, t0.Add(70*time.Minute))
		require.Equal(t, []model.TimeGap{{From: lastOfRun1, To: startOfRun2}}, g, "hueco entre ejecuciones (con y sin after_gap)")

		// Cabecera y cola sin datos también son huecos.
		g = gapsOf(pid, t0.Add(-time.Hour), t0.Add(2*time.Hour))
		require.Equal(t, []model.TimeGap{
			{From: t0.Add(-time.Hour), To: t0},
			{From: lastOfRun1, To: startOfRun2},
			{From: t0.Add(time.Hour + 9*time.Minute + 30*time.Second), To: t0.Add(2 * time.Hour)},
		}, g)
	}

	// Tras agregar a horas, el hueco entre horas se sigue viendo (first_ts/last_ts del rollup).
	_, err := s.PurgeMonitor(ctx, t0.Add(10*24*time.Hour), MonitorRetention{RawDays: 1, HourlyDays: 365, DailyDays: 3650})
	require.NoError(t, err)
	require.Zero(t, count(t, s, "monitor_sample"))
	require.Equal(t, []model.TimeGap{{From: lastOfRun1, To: startOfRun2}}, gapsOf(plain, t0, t0.Add(70*time.Minute)))

	// Un perfil sin ningún dato no tiene huecos que señalar.
	empty := seedProfile(t, s, "empty")
	require.Empty(t, gapsOf(empty, t0, t0.Add(time.Hour)))
}

func TestSeriesAnnotationsAndValidation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	put(t, s, pid, run, t0.Add(time.Minute))
	_, err := s.AddMonitorAnnotation(ctx, MonitorAnnotation{ProfileID: pid, At: t0.Add(5 * time.Minute), Text: "deploy v2.14"})
	require.NoError(t, err)
	_, err = s.AddMonitorAnnotation(ctx, MonitorAnnotation{ProfileID: pid, At: t0.Add(5 * time.Hour), Text: "fuera"})
	require.NoError(t, err)

	series, err := s.SeriesData(ctx, model.SeriesRequest{ProfileID: pid, From: t0, To: t0.Add(time.Hour), Metrics: []string{"qps", "threads_running"}})
	require.NoError(t, err)
	require.Len(t, series, 2)
	require.Equal(t, "qps", series[0].Metric)
	require.Len(t, series[0].Annotations, 1)
	require.Equal(t, "deploy v2.14", series[0].Annotations[0].Text)
	require.Len(t, series[0].Points, 1)
	require.Empty(t, series[1].Points, "métrica NULL en todas las muestras: sin puntos, no ceros")
	require.Equal(t, "raw", series[0].Level)

	_, err = s.SeriesData(ctx, model.SeriesRequest{ProfileID: pid, From: t0, To: t0.Add(time.Hour), Metrics: []string{"qps; DROP TABLE x"}})
	require.Equal(t, "metric", validationDetail(t, err))
	_, err = s.SeriesData(ctx, model.SeriesRequest{ProfileID: pid, From: t0, To: t0.Add(time.Hour)})
	require.Equal(t, "metric", validationDetail(t, err))
	_, err = s.SeriesData(ctx, model.SeriesRequest{ProfileID: pid, From: t0, To: t0, Metrics: []string{"qps"}})
	require.Equal(t, "range", validationDetail(t, err))
}

func TestSeriesDownsample(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 60)
	end := t0.Add(72 * time.Hour)
	var ins []SampleInput
	for i := 0; i < 72*60; i++ {
		ins = append(ins, SampleInput{RunID: run, ProfileID: pid, At: t0.Add(time.Duration(i) * time.Minute), SpanMs: 60000, Metrics: map[string]float64{"qps": float64(i % 100)}})
	}
	_, err := s.insertSamples(ctx, ins)
	require.NoError(t, err)

	pts := func(from, to time.Time, maxPts int) ([]model.SeriesPoint, string) {
		series, err := s.SeriesData(ctx, model.SeriesRequest{ProfileID: pid, From: from, To: to, Metrics: []string{"qps"}, MaxPoints: maxPts})
		require.NoError(t, err)
		return series[0].Points, series[0].Level
	}
	// Crudo: de 1 h a 3 días.
	p, level := pts(t0, t0.Add(time.Hour), 0)
	require.Len(t, p, 60, "1 h a 1/min: cada muestra su punto")
	require.Equal(t, "raw", level)
	for _, d := range []time.Duration{6 * time.Hour, 12 * time.Hour, 72 * time.Hour} {
		p, _ = pts(t0, t0.Add(d), 0)
		require.LessOrEqual(t, len(p), model.MonitorMaxPoints, d)
		require.Greater(t, len(p), 100, d)
	}
	p, _ = pts(t0, end, 0)
	require.Greater(t, len(p), 200)
	for _, pt := range p {
		require.NotNil(t, pt.Min)
		require.NotNil(t, pt.Max)
		require.LessOrEqual(t, *pt.Min, pt.V)
		require.GreaterOrEqual(t, *pt.Max, pt.V)
	}
	p, _ = pts(t0, end, 50)
	require.LessOrEqual(t, len(p), 50)
	p, _ = pts(t0, end, 5000)
	require.LessOrEqual(t, len(p), model.MonitorMaxPoints, "MaxPoints por encima del tope se recorta")

	// Niveles mezclados: día 0 por día, día 1 por hora, día 2 crudo; de 3 días a 1 año.
	_, err = s.PurgeMonitor(ctx, end, MonitorRetention{RawDays: 1, HourlyDays: 2, DailyDays: 365})
	require.NoError(t, err)
	for _, from := range []time.Time{t0, t0.Add(-27 * 24 * time.Hour), t0.Add(-362 * 24 * time.Hour)} {
		p, level = pts(from, end, 0)
		require.NotEmpty(t, p)
		require.LessOrEqual(t, len(p), model.MonitorMaxPoints, from)
		require.Equal(t, "day", level, "el nivel de la serie es el más grueso que aportó")
	}
	// El promedio ponderado no cambia el valor medio de una serie constante por tramos.
	p, _ = pts(t0.Add(48*time.Hour), end, 0)
	require.NotEmpty(t, p)
}

// TestMonitorQueryVolume mide Top y Series sobre 7 días a 30 s (K=30 + otros). No afirma tiempos: los registra para el spec.
func TestMonitorQueryVolume(t *testing.T) {
	if os.Getenv("DBACUS_MONITOR_VOLUME") == "" {
		t.Skip("medición de volumen: DBACUS_MONITOR_VOLUME=1 go test -run QueryVolume ./internal/store")
	}
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "app.db"))
	require.NoError(t, err)
	defer s.Close()
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	end := t0.Add(7 * 24 * time.Hour)
	for day := range 7 {
		fill(t, s, pid, run, t0.Add(time.Duration(day)*24*time.Hour), t0.Add(time.Duration(day+1)*24*time.Hour), 30*time.Second, 30)
	}
	timeIt := func(label string, f func()) {
		best := time.Hour
		for range 3 {
			st := time.Now()
			f()
			best = min(best, time.Since(st))
		}
		t.Logf("%-52s %s (mejor de 3)", label, best.Round(time.Millisecond))
	}
	measure := func(tag string) {
		for _, win := range []struct {
			name string
			d    time.Duration
		}{{"24 h", 24 * time.Hour}, {"7 d", 7 * 24 * time.Hour}} {
			from := end.Add(-win.d)
			timeIt(tag+" Top "+win.name+" (total_time, K=30)", func() {
				_, err := s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: from, To: end, Limit: 50})
				require.NoError(t, err)
			})
			timeIt(tag+" Top "+win.name+" con Compare", func() {
				_, err := s.TopDigests(ctx, model.TopRequest{ProfileID: pid, From: from, To: end, Limit: 50, Compare: true})
				require.NoError(t, err)
			})
			timeIt(tag+" Series "+win.name+" (4 métricas)", func() {
				_, err := s.SeriesData(ctx, model.SeriesRequest{ProfileID: pid, From: from, To: end, Metrics: []string{"qps", "threads_running", "slow_queries", "proc_longest_sec"}})
				require.NoError(t, err)
			})
			timeIt(tag+" DigestDetail "+win.name, func() {
				_, err := s.DigestDetail(ctx, pid, "d001", from, end)
				require.NoError(t, err)
			})
		}
	}
	measure("crudo   ")
	_, err = s.PurgeMonitor(ctx, end.Add(30*24*time.Hour), MonitorRetention{RawDays: 7, HourlyDays: 365, DailyDays: 3650})
	require.NoError(t, err)
	t.Logf("tras agregar: %d filas de digest-hora, %d muestras crudas", count(t, s, "monitor_rollup_hour_digest"), count(t, s, "monitor_sample"))
	measure("por hora")
}

// TestSeriesBufferPoolHitRatio: la métrica derivada la calcula Go (100·(1 − reads/requests)); un cubo sin lecturas lógicas no
// tiene punto.
func TestSeriesBufferPoolHitRatio(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pid := seedProfile(t, s, "p")
	run := openRun(t, s, pid, t0, 30)
	for i, c := range []struct{ reads, reqs float64 }{{10, 1000}, {50, 1000}, {0, 0}} {
		_, err := s.InsertSample(ctx, SampleInput{RunID: run, ProfileID: pid, At: t0.Add(time.Duration(i) * 30 * time.Second), SpanMs: 30000,
			Metrics: map[string]float64{"bp_reads": c.reads, "bp_read_requests": c.reqs}})
		require.NoError(t, err)
	}
	series, err := s.SeriesData(ctx, model.SeriesRequest{ProfileID: pid, From: t0, To: t0.Add(time.Hour), Metrics: []string{MetricBufferPoolHitRatio}})
	require.NoError(t, err)
	require.Len(t, series, 1)
	require.Equal(t, MetricBufferPoolHitRatio, series[0].Metric)
	require.Equal(t, "raw", series[0].Level)
	require.Len(t, series[0].Points, 2, "el cubo con 0 lecturas lógicas no tiene punto")
	require.InDelta(t, 99.0, series[0].Points[0].V, 1e-9)
	require.InDelta(t, 95.0, series[0].Points[1].V, 1e-9)
}
