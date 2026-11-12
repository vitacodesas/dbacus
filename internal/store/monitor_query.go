package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

// Lectura del monitoreo (spec 22, T16.5): ranking, serie de un digest, series de métricas y comparación de periodos.
//
// Los tres niveles (crudo, hora, día) son disjuntos por construcción: PurgeMonitor agrega una hora/día completo y borra
// lo agregado en la misma transacción. Por eso una lectura mixta es la suma de los tres niveles, sin doble conteo.
// Un rollup solo cuenta si el punto medio de su cubo cae en [from, to): así dos rangos contiguos reparten los cubos sin
// solaparse. Si el cubo no cabe entero en el rango, el resultado es aproximado (Approx).

const (
	topDefaultLimit = 50
	topMaxLimit     = 1000
	defaultInterval = 30 * time.Second
)

// systemSchemas son las bases internas de MySQL/MariaDB; sus digests se ocultan salvo IncludeSystem.
var systemSchemas = map[string]bool{"mysql": true, "sys": true, "performance_schema": true, "information_schema": true}

func validationErr(detail, msg string) error {
	return apperr.New(apperr.Validation, msg).WithDetail(detail)
}

func checkRange(from, to time.Time) (int64, int64, error) {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return 0, 0, validationErr("range", "El rango de tiempo no es válido")
	}
	return from.UnixMilli(), to.UnixMilli(), nil
}

// bucketWidth es el ancho (ms) de cubo que garantiza ≤ maxPts cubos alineados en [from, to).
func bucketWidth(fromMs, toMs int64, maxPts int) int64 {
	if maxPts < 3 {
		maxPts = 3
	}
	if maxPts > model.MonitorMaxPoints {
		maxPts = model.MonitorMaxPoints
	}
	span := toMs - fromMs
	n := int64(maxPts - 2)
	w := (span + n - 1) / n
	if w < 1 {
		w = 1
	}
	return w
}

func isAggregateDigest(digest string) bool { return digest == "" || digest == OtherDigest }

// ---- Ranking ----

type digestAgg struct {
	id                                                  int64
	digest, schema, text                                string
	execs, totalUs, maxUs, rowsEx, rowsSent, noIdx, tmp int64
	errs                                                int64
	approx                                              bool
}

func (a *digestAgg) key() DigestKey { return DigestKey{Schema: a.schema, Digest: a.digest} }

func (a *digestAgg) stat() model.DigestStat {
	st := model.DigestStat{
		Digest: a.digest, Schema: a.schema, Text: a.text, Execs: uint64(a.execs), TotalMs: float64(a.totalUs) / 1000,
		MaxObservedMs: float64(a.maxUs) / 1000, RowsExamined: uint64(a.rowsEx), RowsSent: uint64(a.rowsSent),
		NoIndex: uint64(a.noIdx), TmpDisk: uint64(a.tmp), Errors: uint64(a.errs), Approx: a.approx,
	}
	if a.execs > 0 {
		st.AvgMs = st.TotalMs / float64(a.execs)
	}
	return st
}

// aggregateDigests suma los tres niveles por digest en [from, to).
func (s *Store) aggregateDigests(ctx context.Context, profileID string, from, to int64) ([]*digestAgg, error) {
	tier := func(table, tsCol string, unit int64) string {
		return fmt.Sprintf(`SELECT digest_id, execs, total_us, max_observed_us, rows_examined, rows_sent, no_index, tmp_disk, errors,
				CASE WHEN %[2]s >= ? AND %[2]s + %[3]d <= ? THEN 0 ELSE 1 END
			FROM %[1]s WHERE profile_id = ? AND %[2]s + %[4]d >= ? AND %[2]s + %[4]d < ?`, table, tsCol, unit, unit/2)
	}
	q := `WITH u AS (
			SELECT x.digest_id, x.execs, x.total_us, x.max_observed_us, x.rows_examined, x.rows_sent, x.no_index, x.tmp_disk, x.errors, 0 AS ap
			FROM monitor_sample s JOIN monitor_digest_sample x ON x.sample_id = s.id
			WHERE s.profile_id = ? AND s.ts >= ? AND s.ts < ?
			UNION ALL ` + tier("monitor_rollup_hour_digest", "hour_ts", hourMs) + `
			UNION ALL ` + tier("monitor_rollup_day_digest", "day_ts", dayMs) + `
		)
		SELECT u.digest_id, d.digest, d.schema_name, d.text, SUM(u.execs), SUM(u.total_us), MAX(u.max_observed_us),
			SUM(u.rows_examined), SUM(u.rows_sent), SUM(u.no_index), SUM(u.tmp_disk), SUM(u.errors), MAX(u.ap)
		FROM u JOIN monitor_digest d ON d.id = u.digest_id GROUP BY u.digest_id`
	rows, err := s.db.QueryContext(ctx, q, profileID, from, to, from, to, profileID, from, to, from, to, profileID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*digestAgg
	for rows.Next() {
		var (
			a  digestAgg
			ap int
		)
		if err := rows.Scan(&a.id, &a.digest, &a.schema, &a.text, &a.execs, &a.totalUs, &a.maxUs, &a.rowsEx, &a.rowsSent, &a.noIdx, &a.tmp, &a.errs, &ap); err != nil {
			return nil, err
		}
		a.approx = ap != 0
		out = append(out, &a)
	}
	return out, rows.Err()
}

func visibleDigest(a *digestAgg, schema string, includeSystem bool) bool {
	if schema != "" && a.schema != schema {
		return false
	}
	if !includeSystem && (isAggregateDigest(a.digest) || systemSchemas[strings.ToLower(a.schema)]) {
		return false
	}
	return true
}

// orderValue es el valor por el que se ordena (mayor primero) y si la fila entra en el ranking de ese criterio:
// «sin índice», «tablas en disco» y «errores» muestran solo lo que tiene al menos uno.
func orderValue(orderBy string, a *digestAgg) (float64, bool) {
	switch orderBy {
	case model.OrderExecs:
		return float64(a.execs), a.execs > 0
	case model.OrderAvgTime:
		if a.execs == 0 {
			return 0, false
		}
		return float64(a.totalUs) / float64(a.execs), true
	case model.OrderRowsRatio:
		return float64(a.rowsEx) / float64(max(a.rowsSent, 1)), a.rowsEx > 0
	case model.OrderNoIndex:
		return float64(a.noIdx), a.noIdx > 0
	case model.OrderTmpDisk:
		return float64(a.tmp), a.tmp > 0
	case model.OrderErrors:
		return float64(a.errs), a.errs > 0
	default: // total_time
		return float64(a.totalUs), a.execs > 0
	}
}

func validOrder(o string) bool {
	switch o {
	case model.OrderTotalTime, model.OrderExecs, model.OrderAvgTime, model.OrderRowsRatio, model.OrderNoIndex, model.OrderTmpDisk, model.OrderErrors:
		return true
	}
	return false
}

func markChange(st *model.DigestStat, prev *digestAgg) {
	if prev == nil || prev.execs == 0 {
		st.IsNew = true
		return
	}
	p := prev.stat()
	st.Prev = &p
	if p.AvgMs > 0 {
		st.ChangePct = (st.AvgMs - p.AvgMs) / p.AvgMs * 100
	}
	st.Regression = st.ChangePct >= model.RegressionMinPct && st.Execs >= model.RegressionMinExecs && p.Execs >= model.RegressionMinExecs
}

func byKey(aggs []*digestAgg) map[DigestKey]*digestAgg {
	m := make(map[DigestKey]*digestAgg, len(aggs))
	for _, a := range aggs {
		m[a.key()] = a
	}
	return m
}

// TopDigests devuelve el ranking de digests de [From, To) por el criterio OrderBy (por defecto, tiempo total).
// IncludeSystem=false oculta las bases del sistema y las filas agregadas (__other__ y la de tabla de digests llena).
// Con Compare añade el periodo previo de igual duración (Prev, ChangePct, IsNew, Regression).
func (s *Store) TopDigests(ctx context.Context, req model.TopRequest) ([]model.DigestStat, error) {
	from, to, err := checkRange(req.From, req.To)
	if err != nil {
		return nil, err
	}
	if req.ProfileID == "" {
		return nil, validationErr("profileId", "Falta la conexión")
	}
	order := req.OrderBy
	if order == "" {
		order = model.OrderTotalTime
	}
	if !validOrder(order) {
		return nil, validationErr("orderBy", "Criterio de orden desconocido")
	}
	limit := req.Limit
	if limit == 0 {
		limit = topDefaultLimit
	}
	if limit < 0 || limit > topMaxLimit {
		return nil, validationErr("topK", fmt.Sprintf("El límite debe estar entre 1 y %d", topMaxLimit))
	}
	cur, err := s.aggregateDigests(ctx, req.ProfileID, from, to)
	if err != nil {
		return nil, err
	}
	var prev map[DigestKey]*digestAgg
	if req.Compare {
		p, err := s.aggregateDigests(ctx, req.ProfileID, from-(to-from), from)
		if err != nil {
			return nil, err
		}
		prev = byKey(p)
	}
	type ranked struct {
		a *digestAgg
		v float64
	}
	var list []ranked
	for _, a := range cur {
		if !visibleDigest(a, req.Schema, req.IncludeSystem) {
			continue
		}
		if v, ok := orderValue(order, a); ok {
			list = append(list, ranked{a, v})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		if list[i].a.totalUs != list[j].a.totalUs {
			return list[i].a.totalUs > list[j].a.totalUs
		}
		if list[i].a.digest != list[j].a.digest {
			return list[i].a.digest < list[j].a.digest
		}
		return list[i].a.schema < list[j].a.schema
	})
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]model.DigestStat, 0, len(list))
	for _, r := range list {
		st := r.a.stat()
		if req.Compare {
			markChange(&st, prev[r.a.key()])
		}
		out = append(out, st)
	}
	digests := make([]string, 0, len(out))
	for _, st := range out {
		if !isAggregateDigest(st.Digest) {
			digests = append(digests, st.Digest)
		}
	}
	actors, err := s.digestActors(ctx, req.ProfileID, digests, from, to)
	if err != nil {
		return nil, err
	}
	for i := range out {
		all := actors[out[i].Digest]
		out[i].ActorCount = len(all)
		out[i].Actors = append([]model.DigestActor{}, all[:min(len(all), topActors)]...)
	}
	return out, nil
}

// CompareDigests compara el periodo base A con el actual B (spec 22 ⚑D14). Regresiones: latencia media ↑ ≥ 25 % con
// ≥ 100 ejecuciones en ambos, por impacto (aumento de tiempo total) descendente. New: están en B y no en A. Gone: en A y no en B.
// Oculta las bases del sistema y las filas agregadas.
func (s *Store) CompareDigests(ctx context.Context, profileID string, a, b model.TimeRange) (model.Comparison, error) {
	af, at, err := checkRange(a.From, a.To)
	if err != nil {
		return model.Comparison{}, err
	}
	bf, bt, err := checkRange(b.From, b.To)
	if err != nil {
		return model.Comparison{}, err
	}
	if profileID == "" {
		return model.Comparison{}, validationErr("profileId", "Falta la conexión")
	}
	aa, err := s.aggregateDigests(ctx, profileID, af, at)
	if err != nil {
		return model.Comparison{}, err
	}
	ba, err := s.aggregateDigests(ctx, profileID, bf, bt)
	if err != nil {
		return model.Comparison{}, err
	}
	am, bm := byKey(aa), byKey(ba)
	res := model.Comparison{Regressions: []model.DigestStat{}, New: []model.DigestStat{}, Gone: []model.DigestStat{}}
	impact := map[DigestKey]float64{}
	for _, cur := range ba {
		if !visibleDigest(cur, "", false) || cur.execs == 0 {
			continue
		}
		st := cur.stat()
		markChange(&st, am[cur.key()])
		switch {
		case st.IsNew:
			res.New = append(res.New, st)
		case st.Regression:
			res.Regressions = append(res.Regressions, st)
			impact[cur.key()] = (st.AvgMs - st.Prev.AvgMs) * float64(st.Execs)
		}
	}
	for _, old := range aa {
		if !visibleDigest(old, "", false) || old.execs == 0 {
			continue
		}
		if cur := bm[old.key()]; cur == nil || cur.execs == 0 {
			res.Gone = append(res.Gone, old.stat())
		}
	}
	sort.SliceStable(res.Regressions, func(i, j int) bool {
		ki, kj := DigestKey{res.Regressions[i].Schema, res.Regressions[i].Digest}, DigestKey{res.Regressions[j].Schema, res.Regressions[j].Digest}
		return impact[ki] > impact[kj]
	})
	byTotal := func(l []model.DigestStat) {
		sort.SliceStable(l, func(i, j int) bool { return l[i].TotalMs > l[j].TotalMs })
	}
	byTotal(res.New)
	byTotal(res.Gone)
	return res, nil
}

// ---- Detalle de un digest ----

type detailBucket struct {
	tsSum, n       float64
	execs, totalUs int64
	approx         bool
}

// DigestDetail devuelve los totales de un digest en [from, to) y su serie (≤ 300 puntos), sumando las bases en que aparece.
// digestActors devuelve, por digest, todos los usuarios y hosts vistos en [from, to), por muestras descendente. Una sola
// consulta por tanda de digests (sin N+1). Rango > retención por hora: solo lo que siga guardado.
func (s *Store) digestActors(ctx context.Context, profileID string, digests []string, fromMs, toMs int64) (map[string][]model.DigestActor, error) {
	out := make(map[string][]model.DigestActor, len(digests))
	const chunk = 400
	for i := 0; i < len(digests); i += chunk {
		part := digests[i:min(i+chunk, len(digests))]
		args := make([]any, 0, len(part)+3)
		args = append(args, profileID, fromMs-fromMs%hourMs, toMs)
		for _, d := range part {
			args = append(args, d)
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT digest, user, host, SUM(seen), MAX(last_ts) FROM monitor_digest_actor
			WHERE profile_id = ? AND hour_ts >= ? AND hour_ts < ? AND digest IN (`+placeholders(len(part))+`)
			GROUP BY digest, user, host`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				d string
				a model.DigestActor
			)
			if err := rows.Scan(&d, &a.User, &a.Host, &a.Seen, &a.LastSeen); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[d] = append(out[d], a)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	for _, list := range out {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Seen != list[j].Seen {
				return list[i].Seen > list[j].Seen
			}
			if list[i].User != list[j].User {
				return list[i].User < list[j].User
			}
			return list[i].Host < list[j].Host
		})
	}
	return out, nil
}

// topActors es el máximo de actores que lleva cada fila del ranking; el resto se cuenta en ActorCount y se ve en el detalle.
const topActors = 3

func (s *Store) DigestDetail(ctx context.Context, profileID, digest string, from, to time.Time) (model.DigestDetail, error) {
	fromMs, toMs, err := checkRange(from, to)
	if err != nil {
		return model.DigestDetail{}, err
	}
	if profileID == "" {
		return model.DigestDetail{}, validationErr("profileId", "Falta la conexión")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, schema_name, text FROM monitor_digest WHERE profile_id = ? AND digest = ? ORDER BY schema_name`, profileID, digest)
	if err != nil {
		return model.DigestDetail{}, err
	}
	var (
		ids     []any
		detail  = model.DigestDetail{Digest: digest, Schemas: []string{}}
		schemas []string
	)
	for rows.Next() {
		var (
			id         int64
			schema, tx string
		)
		if err := rows.Scan(&id, &schema, &tx); err != nil {
			_ = rows.Close()
			return model.DigestDetail{}, err
		}
		ids = append(ids, id)
		schemas = append(schemas, schema)
		if detail.Text == "" {
			detail.Text = tx
		}
	}
	if err := rows.Close(); err != nil {
		return model.DigestDetail{}, err
	}
	if len(ids) == 0 {
		return model.DigestDetail{}, apperr.New(apperr.NotFound, "La consulta no está en el diccionario de este perfil")
	}
	detail.Schemas = schemas
	in := placeholders(len(ids))
	w := bucketWidth(fromMs, toMs, model.MonitorMaxPoints)
	buckets := map[int64]*detailBucket{}
	agg := digestAgg{digest: digest, text: detail.Text}
	if len(schemas) == 1 {
		agg.schema = schemas[0]
	}

	scan := func(q string, args ...any) error {
		r, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var (
				b, execs, total, mx, re, rs, ni, td, er int64
				tsSum, n                                float64
				ap                                      int
			)
			if err := r.Scan(&b, &tsSum, &n, &execs, &total, &mx, &re, &rs, &ni, &td, &er, &ap); err != nil {
				return err
			}
			bk := buckets[b]
			if bk == nil {
				bk = &detailBucket{}
				buckets[b] = bk
			}
			bk.tsSum += tsSum
			bk.n += n
			bk.execs += execs
			bk.totalUs += total
			bk.approx = bk.approx || ap != 0
			agg.execs += execs
			agg.totalUs += total
			agg.maxUs = max(agg.maxUs, mx)
			agg.rowsEx += re
			agg.rowsSent += rs
			agg.noIdx += ni
			agg.tmp += td
			agg.errs += er
			agg.approx = agg.approx || ap != 0
		}
		return r.Err()
	}
	sums := `SUM(execs), SUM(total_us), MAX(max_observed_us), SUM(rows_examined), SUM(rows_sent), SUM(no_index), SUM(tmp_disk), SUM(errors)`
	idArgs := func(extra ...any) []any { return append(append([]any{}, ids...), extra...) }
	if err := scan(fmt.Sprintf(`SELECT (ts / %[1]d) * %[1]d AS b, SUM(ts), COUNT(1), %[2]s, 0 FROM monitor_digest_sample
		WHERE digest_id IN (%[3]s) AND ts >= ? AND ts < ? GROUP BY b`, w, sums, in), idArgs(fromMs, toMs)...); err != nil {
		return model.DigestDetail{}, err
	}
	for _, t := range []struct {
		table, col string
		unit       int64
	}{{"monitor_rollup_hour_digest", "hour_ts", hourMs}, {"monitor_rollup_day_digest", "day_ts", dayMs}} {
		mid := fmt.Sprintf("(%s + %d)", t.col, t.unit/2)
		if err := scan(fmt.Sprintf(`SELECT (%[1]s / %[2]d) * %[2]d AS b, SUM(%[1]s), COUNT(1), %[3]s,
				MAX(CASE WHEN %[4]s >= ? AND %[4]s + %[5]d <= ? THEN 0 ELSE 1 END)
			FROM %[6]s WHERE digest_id IN (%[7]s) AND %[1]s >= ? AND %[1]s < ? GROUP BY b`, mid, w, sums, t.col, t.unit, t.table, in),
			append(append([]any{fromMs, toMs}, ids...), fromMs, toMs)...); err != nil {
			return model.DigestDetail{}, err
		}
	}
	detail.Stat = agg.stat()
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	detail.Points = make([]model.DigestPoint, 0, len(keys))
	for _, k := range keys {
		bk := buckets[k]
		p := model.DigestPoint{T: time.UnixMilli(int64(bk.tsSum / bk.n)).UTC(), Execs: uint64(bk.execs), TotalMs: float64(bk.totalUs) / 1000}
		if bk.execs > 0 {
			p.AvgMs = p.TotalMs / float64(bk.execs)
		}
		detail.Points = append(detail.Points, p)
	}
	if detail.Gaps, err = s.monitorGaps(ctx, profileID, fromMs, toMs); err != nil {
		return model.DigestDetail{}, err
	}
	actors, err := s.digestActors(ctx, profileID, []string{digest}, fromMs, toMs)
	if err != nil {
		return model.DigestDetail{}, err
	}
	detail.Actors = append([]model.DigestActor{}, actors[digest]...)
	return detail, nil
}

// ---- Series de métricas ----

type seriesBucket struct {
	tsSum, tsW, vSum, vCnt float64
	min, max               float64
}

// SeriesData devuelve una serie por métrica con ≤ MaxPoints (tope 300) puntos, huecos y anotaciones del rango.
// Lee crudo, hora y día a la vez (son disjuntos) y los reparte en cubos de ancho fijo; cada punto trae Min/Max del cubo.
func (s *Store) SeriesData(ctx context.Context, req model.SeriesRequest) ([]model.Series, error) {
	fromMs, toMs, err := checkRange(req.From, req.To)
	if err != nil {
		return nil, err
	}
	if req.ProfileID == "" {
		return nil, validationErr("profileId", "Falta la conexión")
	}
	if len(req.Metrics) == 0 {
		return nil, validationErr("metric", "Elige al menos una métrica")
	}
	known := make(map[string]bool, len(MonitorMetrics))
	for _, m := range MonitorMetrics {
		known[m] = true
	}
	known[MetricBufferPoolHitRatio] = true // derivada, no es una columna
	for _, m := range req.Metrics {
		if !known[m] {
			return nil, validationErr("metric", "Métrica desconocida")
		}
	}
	maxPts := req.MaxPoints
	if maxPts <= 0 || maxPts > model.MonitorMaxPoints {
		maxPts = model.MonitorMaxPoints
	}
	w := bucketWidth(fromMs, toMs, maxPts)
	gaps, err := s.monitorGaps(ctx, req.ProfileID, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	annos, err := s.ListMonitorAnnotations(ctx, req.ProfileID, req.From, req.To.Add(-time.Millisecond))
	if err != nil {
		return nil, err
	}
	notes := make([]model.Annotation, 0, len(annos))
	for _, a := range annos {
		notes = append(notes, model.Annotation{ID: strconv.FormatInt(a.ID, 10), ProfileID: a.ProfileID, At: a.At, Text: a.Text})
	}
	out := make([]model.Series, 0, len(req.Metrics))
	for _, m := range req.Metrics {
		var (
			pts   []model.SeriesPoint
			level string
		)
		if m == MetricBufferPoolHitRatio {
			pts, level, err = s.hitRatioPoints(ctx, req.ProfileID, fromMs, toMs, w)
		} else {
			pts, level, err = s.metricPoints(ctx, req.ProfileID, m, fromMs, toMs, w)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, model.Series{Metric: m, Level: level, Points: pts, Gaps: gaps, Annotations: notes})
	}
	return out, nil
}

// metricPoints lee una métrica de los tres niveles, ya repartida en cubos de w ms en SQL. m viene de MonitorMetrics (validada).
func (s *Store) metricPoints(ctx context.Context, profileID, m string, from, to, w int64) ([]model.SeriesPoint, string, error) {
	buckets := map[int64]*seriesBucket{}
	rank := 0 // 0 crudo, 1 hora, 2 día: el nivel de la serie es el más grueso que aportó
	merge := func(q string, level int, args ...any) error {
		rows, err := s.db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				b                              int64
				tsSum, tsW, vSum, vCnt, mn, mx sql.NullFloat64
			)
			if err := rows.Scan(&b, &tsSum, &tsW, &vSum, &vCnt, &mn, &mx); err != nil {
				return err
			}
			if !vCnt.Valid || vCnt.Float64 <= 0 {
				continue // métrica NULL en todo el cubo (fuente no disponible): sin punto
			}
			bk := buckets[b]
			if bk == nil {
				bk = &seriesBucket{min: mn.Float64, max: mx.Float64}
				buckets[b] = bk
			}
			bk.tsSum += tsSum.Float64
			bk.tsW += tsW.Float64
			bk.vSum += vSum.Float64
			bk.vCnt += vCnt.Float64
			bk.min, bk.max = min(bk.min, mn.Float64), max(bk.max, mx.Float64)
			rank = max(rank, level)
		}
		return rows.Err()
	}
	if err := merge(fmt.Sprintf(`SELECT (ts / %[1]d) * %[1]d AS b, SUM(ts), COUNT(1), SUM(%[2]s), COUNT(%[2]s), MIN(%[2]s), MAX(%[2]s)
		FROM monitor_sample WHERE profile_id = ? AND ts >= ? AND ts < ? GROUP BY b`, w, m), 0, profileID, from, to); err != nil {
		return nil, "", err
	}
	for i, t := range []struct {
		table, col string
		unit       int64
	}{{"monitor_rollup_hour", "hour_ts", hourMs}, {"monitor_rollup_day", "day_ts", dayMs}} {
		mid := fmt.Sprintf("(%s + %d)", t.col, t.unit/2)
		if err := merge(fmt.Sprintf(`SELECT (%[1]s / %[2]d) * %[2]d AS b, SUM(%[1]s * samples), SUM(samples),
				SUM(CASE WHEN %[3]s_avg IS NOT NULL THEN %[3]s_avg * samples END), SUM(CASE WHEN %[3]s_avg IS NOT NULL THEN samples END),
				MIN(%[3]s_min), MAX(%[3]s_max)
			FROM %[4]s WHERE profile_id = ? AND %[1]s >= ? AND %[1]s < ? GROUP BY b`, mid, w, m, t.table), i+1, profileID, from, to); err != nil {
			return nil, "", err
		}
	}
	keys := make([]int64, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	pts := make([]model.SeriesPoint, 0, len(keys))
	for _, k := range keys {
		bk := buckets[k]
		p := model.SeriesPoint{T: time.UnixMilli(int64(bk.tsSum / bk.tsW)).UTC(), V: bk.vSum / bk.vCnt}
		if bk.vCnt > 1 {
			mn, mx := bk.min, bk.max
			p.Min, p.Max = &mn, &mx
		}
		pts = append(pts, p)
	}
	return pts, [...]string{"raw", "hour", "day"}[rank], nil
}

// ---- Huecos ----

type covSegment struct {
	first, last int64
	interval    time.Duration
}

// monitorGaps devuelve los tramos de [from, to) sin datos: más de 2× el intervalo de la ejecución entre dos puntos
// (también entre ejecuciones y al principio o al final del rango), más los marcados con after_gap. Mira una hora más allá de
// cada borde para no inventar huecos en los extremos. Sin ningún dato cerca, no hay huecos que señalar.
func (s *Store) monitorGaps(ctx context.Context, profileID string, from, to int64) ([]model.TimeGap, error) {
	type runInfo struct {
		id, start, end int64
		interval       time.Duration
	}
	rr, err := s.db.QueryContext(ctx, `SELECT r.id, r.started_at, COALESCE(r.ended_at, (SELECT COALESCE(MAX(ts), r.started_at) FROM monitor_sample WHERE run_id = r.id)), r.interval_sec
		FROM monitor_run r WHERE r.profile_id = ? ORDER BY r.started_at`, profileID)
	if err != nil {
		return nil, err
	}
	var runs []runInfo
	for rr.Next() {
		var (
			ri  runInfo
			sec int64
		)
		if err := rr.Scan(&ri.id, &ri.start, &ri.end, &sec); err != nil {
			_ = rr.Close()
			return nil, err
		}
		ri.interval = time.Duration(sec) * time.Second
		runs = append(runs, ri)
	}
	if err := rr.Close(); err != nil {
		return nil, err
	}
	byID := make(map[int64]time.Duration, len(runs))
	for _, r := range runs {
		byID[r.id] = r.interval
	}
	intervalAt := func(t int64) time.Duration {
		for _, r := range runs {
			if t >= r.start && t <= r.end {
				return r.interval
			}
		}
		return defaultInterval
	}

	lo, hi := from-hourMs, to+hourMs
	var segs []covSegment
	var flagged []model.TimeGap
	rows, err := s.db.QueryContext(ctx, `SELECT ts, span_ms, after_gap, run_id FROM monitor_sample WHERE profile_id = ? AND ts >= ? AND ts < ? ORDER BY ts`, profileID, lo, hi)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ts, span, after, run int64
		if err := rows.Scan(&ts, &span, &after, &run); err != nil {
			_ = rows.Close()
			return nil, err
		}
		iv, ok := byID[run]
		if !ok {
			iv = defaultInterval
		}
		segs = append(segs, covSegment{ts, ts, iv})
		if after != 0 && span > 0 {
			flagged = append(flagged, model.TimeGap{From: time.UnixMilli(ts - span).UTC(), To: time.UnixMilli(ts).UTC()})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, table := range []string{"monitor_rollup_hour", "monitor_rollup_day"} {
		rows, err := s.db.QueryContext(ctx, `SELECT first_ts, last_ts FROM `+table+` WHERE profile_id = ? AND last_ts >= ? AND first_ts < ?`, profileID, lo, hi)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var f, l int64
			if err := rows.Scan(&f, &l); err != nil {
				_ = rows.Close()
				return nil, err
			}
			segs = append(segs, covSegment{f, l, intervalAt(f)})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	if len(segs) == 0 {
		return []model.TimeGap{}, nil
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].first < segs[j].first })

	var gaps []model.TimeGap
	add := func(a, b int64) {
		a, b = max(a, from), min(b, to)
		if b > a {
			gaps = append(gaps, model.TimeGap{From: time.UnixMilli(a).UTC(), To: time.UnixMilli(b).UTC()})
		}
	}
	prevEnd := segs[0].first
	if segs[0].first >= from {
		prevEnd = from // sin datos antes del rango: el hueco de cabecera se mide desde el inicio
	}
	for _, sg := range segs {
		if sg.first-prevEnd > 2*sg.interval.Milliseconds() {
			add(prevEnd, sg.first)
		}
		prevEnd = max(prevEnd, sg.last)
	}
	if to-prevEnd > 2*segs[len(segs)-1].interval.Milliseconds() {
		add(prevEnd, to)
	}
	for _, g := range flagged {
		add(g.From.UnixMilli(), g.To.UnixMilli())
	}
	return mergeGaps(gaps), nil
}

// mergeGaps ordena y une huecos que se tocan o se solapan.
func mergeGaps(in []model.TimeGap) []model.TimeGap {
	out := []model.TimeGap{}
	if len(in) == 0 {
		return out
	}
	sort.Slice(in, func(i, j int) bool { return in[i].From.Before(in[j].From) })
	cur := in[0]
	for _, g := range in[1:] {
		if !g.From.After(cur.To) {
			if g.To.After(cur.To) {
				cur.To = g.To
			}
			continue
		}
		out = append(out, cur)
		cur = g
	}
	return append(out, cur)
}

// MetricBufferPoolHitRatio es la métrica derivada «aciertos del buffer pool» (%), calculada aquí con bp_reads y
// bp_read_requests (spec 22: Vue no calcula). No es una columna de monitor_sample.
const MetricBufferPoolHitRatio = "bp_hit_ratio"

// hitRatioPoints devuelve 100·(1 − bp_reads / bp_read_requests) por cubo. Un cubo sin lecturas lógicas no tiene punto.
func (s *Store) hitRatioPoints(ctx context.Context, profileID string, from, to, w int64) ([]model.SeriesPoint, string, error) {
	reads, lv1, err := s.metricPoints(ctx, profileID, "bp_reads", from, to, w)
	if err != nil {
		return nil, "", err
	}
	reqs, lv2, err := s.metricPoints(ctx, profileID, "bp_read_requests", from, to, w)
	if err != nil {
		return nil, "", err
	}
	byT := make(map[int64]float64, len(reqs))
	for _, p := range reqs {
		byT[p.T.UnixMilli()] = p.V
	}
	out := make([]model.SeriesPoint, 0, len(reads))
	for _, p := range reads {
		req, ok := byT[p.T.UnixMilli()]
		if !ok || req <= 0 {
			continue
		}
		out = append(out, model.SeriesPoint{T: p.T, V: min(max(100*(1-p.V/req), 0), 100)})
	}
	return out, levelMax(lv1, lv2), nil
}

func levelMax(a, b string) string {
	rank := map[string]int{"": 0, "raw": 0, "hour": 1, "day": 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
