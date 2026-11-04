package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

// Persistencia del monitoreo DBA (spec 22, ADR-007). Los tipos de fila son propios del paquete: el servicio los
// traduce a los DTO de model. Tiempos en ms desde epoch; duraciones de digest en µs.

const (
	RunRunning      = "running"
	RunStopped      = "stopped"
	RunDisconnected = "disconnected"
	RunAppClosed    = "app_closed"
	RunCrashed      = "crashed"

	// OtherDigest es la fila que suma lo que quedó fuera del top-K de una muestra (spec 22 ⚑D5).
	OtherDigest = "__other__"

	// MaxDigestTextBytes limita el texto normalizado que se guarda por digest.
	MaxDigestTextBytes = 4 << 10

	hourMs = int64(time.Hour / time.Millisecond)
	dayMs  = 24 * hourMs
)

// MonitorMetrics son las columnas de métrica de monitor_sample y, con sufijo _avg/_min/_max, de los rollups.
// El orden es el de la migración 003; TestMonitorMetricColumnsMatchMigration lo comprueba.
var MonitorMetrics = []string{
	"qps", "com_select", "com_insert", "com_update", "com_delete", "slow_queries", "aborted_connects", "aborted_clients",
	"row_lock_waits", "row_lock_time_ms", "bp_reads", "bp_read_requests", "tmp_tables", "tmp_disk_tables", "select_full_join",
	"bytes_received", "bytes_sent", "threads_connected", "threads_running", "proc_total", "proc_running", "proc_sleeping",
	"proc_waiting_lock", "proc_longest_sec", "proc_long_count",
}

// MonitorRun es una sesión de monitoreo (spec 22 ⚑D15).
type MonitorRun struct {
	ID            int64
	ProfileID     string
	StartedAt     time.Time
	EndedAt       *time.Time
	Reason        string
	IntervalSec   int
	ServerVersion string
	ServerID      string
	Samples       int // solo lectura: calculado al listar
	Gaps          int // solo lectura: muestras con after_gap
}

// DigestDelta es el delta de un digest en una muestra. Digest == OtherDigest agrupa el resto del servidor.
type DigestDelta struct {
	Schema        string
	Digest        string
	Text          string // vacío si ya está en el diccionario
	Execs         int64
	TotalUs       int64
	MaxObservedUs int64
	RowsExamined  int64
	RowsSent      int64
	NoIndex       int64
	TmpDisk       int64
	Errors        int64
}

// SampleInput es una muestra completa; se escribe en una sola transacción.
type SampleInput struct {
	RunID     int64
	ProfileID string
	At        time.Time
	SpanMs    int64
	AfterGap  bool
	Metrics   map[string]float64 // claves de MonitorMetrics; ausente = NULL
	ProcJSON  string             // top por usuario/base; nunca INFO (spec 22 ⚑D7)
	Digests   []DigestDelta
}

// MonitorAnnotation es una nota del usuario sobre la línea de tiempo.
type MonitorAnnotation struct {
	ID        int64
	ProfileID string
	At        time.Time
	Text      string
}

// DigestKey identifica un digest dentro de un perfil.
type DigestKey struct{ Schema, Digest string }

func (s *Store) OpenMonitorRun(ctx context.Context, r MonitorRun) (MonitorRun, error) {
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	r.StartedAt = r.StartedAt.UTC().Truncate(time.Millisecond)
	r.Reason, r.EndedAt = RunRunning, nil
	res, err := s.db.ExecContext(ctx, `INSERT INTO monitor_run (profile_id, started_at, reason, interval_sec, server_version, server_id) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ProfileID, r.StartedAt.UnixMilli(), RunRunning, r.IntervalSec, r.ServerVersion, r.ServerID)
	if err != nil {
		return MonitorRun{}, err
	}
	r.ID, err = res.LastInsertId()
	return r, err
}

// CloseMonitorRun cierra una ejecución abierta. Cerrar una ya cerrada no cambia nada (idempotente); un id desconocido es NOT_FOUND.
func (s *Store) CloseMonitorRun(ctx context.Context, id int64, reason string, endedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE monitor_run SET ended_at = ?, reason = ? WHERE id = ? AND ended_at IS NULL`, endedAt.UnixMilli(), reason, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM monitor_run WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return apperr.New(apperr.NotFound, "La ejecución de monitoreo no existe")
	} else if err != nil {
		return err
	}
	return nil
}

// CloseDanglingMonitorRuns cierra como app_closed las ejecuciones que quedaron abiertas (cierre o caída de la app).
// ended_at es la última muestra de la ejecución, o su inicio si no llegó a tomar ninguna.
func (s *Store) CloseDanglingMonitorRuns(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE monitor_run SET reason = ?, ended_at = COALESCE((SELECT MAX(ts) FROM monitor_sample WHERE run_id = monitor_run.id), started_at)
		WHERE ended_at IS NULL`, RunAppClosed)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ListMonitorRuns devuelve las ejecuciones de un perfil, la más reciente primero.
func (s *Store) ListMonitorRuns(ctx context.Context, profileID string) ([]MonitorRun, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.profile_id, r.started_at, r.ended_at, r.reason, r.interval_sec, r.server_version, r.server_id,
		       (SELECT COUNT(1) FROM monitor_sample WHERE run_id = r.id),
		       (SELECT COALESCE(SUM(after_gap), 0) FROM monitor_sample WHERE run_id = r.id)
		FROM monitor_run r WHERE r.profile_id = ? ORDER BY r.started_at DESC, r.id DESC`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonitorRun{}
	for rows.Next() {
		var (
			r        MonitorRun
			started  int64
			ended    sql.NullInt64
			samples  int
			gapCount int
		)
		if err := rows.Scan(&r.ID, &r.ProfileID, &started, &ended, &r.Reason, &r.IntervalSec, &r.ServerVersion, &r.ServerID, &samples, &gapCount); err != nil {
			return nil, err
		}
		r.StartedAt = time.UnixMilli(started).UTC()
		if ended.Valid {
			t := time.UnixMilli(ended.Int64).UTC()
			r.EndedAt = &t
		}
		r.Samples, r.Gaps = samples, gapCount
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertSample escribe una muestra y sus deltas de digest en una única transacción y devuelve el id de la muestra.
func (s *Store) InsertSample(ctx context.Context, in SampleInput) (int64, error) {
	ids, err := s.insertSamples(ctx, []SampleInput{in})
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// insertSamples escribe varias muestras en una transacción (las usan las pruebas de volumen; en producción es una por llamada).
func (s *Store) insertSamples(ctx context.Context, ins []SampleInput) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	w, err := newSampleWriter(ctx, tx)
	if err != nil {
		return nil, err
	}
	defer w.close()
	ids := make([]int64, 0, len(ins))
	for _, in := range ins {
		id, err := w.write(in)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

type sampleWriter struct {
	ctx                            context.Context
	sample, dict, dictRef, digests *sql.Stmt
}

func newSampleWriter(ctx context.Context, tx *sql.Tx) (*sampleWriter, error) {
	cols := strings.Join(MonitorMetrics, ", ")
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(MonitorMetrics)), ", ")
	w := &sampleWriter{ctx: ctx}
	var err error
	if w.sample, err = tx.PrepareContext(ctx, `INSERT INTO monitor_sample (run_id, profile_id, ts, span_ms, after_gap, `+cols+`, proc_json) VALUES (?, ?, ?, ?, ?, `+marks+`, ?)`); err != nil {
		return nil, err
	}
	if w.dict, err = tx.PrepareContext(ctx, `
		INSERT INTO monitor_digest (profile_id, digest, schema_name, text, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (profile_id, digest, schema_name) DO UPDATE SET
			last_seen = MAX(last_seen, excluded.last_seen),
			text = CASE WHEN excluded.text <> '' THEN excluded.text ELSE text END
		RETURNING id`); err != nil {
		w.close()
		return nil, err
	}
	if w.digests, err = tx.PrepareContext(ctx, `
		INSERT INTO monitor_digest_sample (sample_id, digest_id, ts, execs, total_us, max_observed_us, rows_examined, rows_sent, no_index, tmp_disk, errors)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (sample_id, digest_id) DO UPDATE SET execs = execs + excluded.execs, total_us = total_us + excluded.total_us,
			max_observed_us = MAX(max_observed_us, excluded.max_observed_us), rows_examined = rows_examined + excluded.rows_examined,
			rows_sent = rows_sent + excluded.rows_sent, no_index = no_index + excluded.no_index, tmp_disk = tmp_disk + excluded.tmp_disk,
			errors = errors + excluded.errors`); err != nil {
		w.close()
		return nil, err
	}
	return w, nil
}

func (w *sampleWriter) close() {
	for _, st := range []*sql.Stmt{w.sample, w.dict, w.digests} {
		if st != nil {
			_ = st.Close()
		}
	}
}

func (w *sampleWriter) write(in SampleInput) (int64, error) {
	ts := in.At.UnixMilli()
	args := make([]any, 0, len(MonitorMetrics)+6)
	args = append(args, in.RunID, in.ProfileID, ts, in.SpanMs, boolInt(in.AfterGap))
	for _, m := range MonitorMetrics {
		if v, ok := in.Metrics[m]; ok {
			args = append(args, v)
		} else {
			args = append(args, nil)
		}
	}
	args = append(args, in.ProcJSON)
	res, err := w.sample.ExecContext(w.ctx, args...)
	if err != nil {
		return 0, err
	}
	sampleID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, d := range in.Digests {
		var digestID int64
		if err := w.dict.QueryRowContext(w.ctx, in.ProfileID, d.Digest, d.Schema, truncateUTF8(d.Text, MaxDigestTextBytes), ts, ts).Scan(&digestID); err != nil {
			return 0, err
		}
		if _, err := w.digests.ExecContext(w.ctx, sampleID, digestID, ts, d.Execs, d.TotalUs, d.MaxObservedUs, d.RowsExamined, d.RowsSent, d.NoIndex, d.TmpDisk, d.Errors); err != nil {
			return 0, err
		}
	}
	return sampleID, nil
}

// MissingDigestTexts devuelve las claves de keys que aún no tienen texto en el diccionario del perfil: son las únicas
// cuyo DIGEST_TEXT hay que pedir al servidor.
func (s *Store) MissingDigestTexts(ctx context.Context, profileID string, keys []DigestKey) ([]DigestKey, error) {
	have := make(map[DigestKey]bool, len(keys))
	const chunk = 400
	for i := 0; i < len(keys); i += chunk {
		part := keys[i:min(i+chunk, len(keys))]
		args := make([]any, 0, len(part)+1)
		args = append(args, profileID)
		for _, k := range part {
			args = append(args, k.Digest)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT digest, schema_name FROM monitor_digest WHERE profile_id = ? AND text <> '' AND digest IN (`+placeholders(len(part))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k DigestKey
			if err := rows.Scan(&k.Digest, &k.Schema); err != nil {
				_ = rows.Close()
				return nil, err
			}
			have[k] = true
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	var missing []DigestKey
	for _, k := range keys {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	return missing, nil
}

func (s *Store) AddMonitorAnnotation(ctx context.Context, a MonitorAnnotation) (MonitorAnnotation, error) {
	a.At = a.At.UTC().Truncate(time.Millisecond)
	res, err := s.db.ExecContext(ctx, `INSERT INTO monitor_annotation (profile_id, at, text) VALUES (?, ?, ?)`, a.ProfileID, a.At.UnixMilli(), a.Text)
	if err != nil {
		return MonitorAnnotation{}, err
	}
	a.ID, err = res.LastInsertId()
	return a, err
}

func (s *Store) DeleteMonitorAnnotation(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM monitor_annotation WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.New(apperr.NotFound, "La anotación no existe")
	}
	return nil
}

// ListMonitorAnnotations devuelve las anotaciones de [from, to] por orden de fecha.
func (s *Store) ListMonitorAnnotations(ctx context.Context, profileID string, from, to time.Time) ([]MonitorAnnotation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, profile_id, at, text FROM monitor_annotation WHERE profile_id = ? AND at >= ? AND at <= ? ORDER BY at, id`,
		profileID, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MonitorAnnotation{}
	for rows.Next() {
		var (
			a  MonitorAnnotation
			at int64
		)
		if err := rows.Scan(&a.ID, &a.ProfileID, &at, &a.Text); err != nil {
			return nil, err
		}
		a.At = time.UnixMilli(at).UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// PurgeMonitorData borra datos de monitoreo de un perfil. before == nil borra todo (ejecuciones, muestras, rollups,
// diccionario y anotaciones); con fecha, solo lo anterior. Borrar el perfil ya lo hace en cascada (foreign_keys=1).
func (s *Store) PurgeMonitorData(ctx context.Context, profileID string, before *time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if before == nil {
		// Las ejecuciones arrastran muestras y deltas; el diccionario y los rollups cuelgan del perfil.
		for _, q := range []string{
			`DELETE FROM monitor_run WHERE profile_id = ?`,
			`DELETE FROM monitor_rollup_hour WHERE profile_id = ?`,
			`DELETE FROM monitor_rollup_day WHERE profile_id = ?`,
			`DELETE FROM monitor_digest WHERE profile_id = ?`,
			`DELETE FROM monitor_annotation WHERE profile_id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, profileID); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	b := before.UnixMilli()
	for _, q := range []string{
		`DELETE FROM monitor_sample WHERE profile_id = ? AND ts < ?`,
		`DELETE FROM monitor_rollup_hour WHERE profile_id = ? AND hour_ts + ` + fmt.Sprint(hourMs) + ` <= ?`,
		`DELETE FROM monitor_rollup_day WHERE profile_id = ? AND day_ts + ` + fmt.Sprint(dayMs) + ` <= ?`,
		`DELETE FROM monitor_annotation WHERE profile_id = ? AND at < ?`,
		`DELETE FROM monitor_run WHERE profile_id = ? AND ended_at IS NOT NULL AND ended_at < ? AND NOT EXISTS (SELECT 1 FROM monitor_sample WHERE run_id = monitor_run.id)`,
	} {
		if _, err := tx.ExecContext(ctx, q, profileID, b); err != nil {
			return err
		}
	}
	if err := deleteOrphanDigests(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Retención (ADR-007) ----

// MonitorRetention son los límites de la purga; MaxBytes <= 0 desactiva el tope de tamaño.
type MonitorRetention struct {
	RawDays, HourlyDays, DailyDays int
	MaxBytes                       int64
}

func MonitorRetentionFromSettings(st model.Settings) MonitorRetention {
	return MonitorRetention{RawDays: st.MonitorRawDays, HourlyDays: st.MonitorHourlyDays, DailyDays: st.MonitorDailyDays, MaxBytes: int64(st.MonitorMaxMiB) << 20}
}

type MonitorPurgeResult struct {
	HoursRolled     int   // horas crudas agregadas y borradas
	DaysRolled      int   // días de rollups por hora agregados y borrados
	DayRowsDeleted  int   // días caducados borrados
	CapTrimmed      bool  // el tope de tamaño obligó a recortar antes de la fecha
	UsedBytes       int64 // tamaño usado del archivo al terminar
	DigestsOrphaned int
}

// purgePause es la espera entre lotes para no ahogar al historial en la conexión de escritura única; las pruebas la ponen en 0.
var purgePause = 5 * time.Millisecond

// PurgeMonitor aplica la retención escalonada: crudo → hora (al pasar RawDays), hora → día (al pasar HourlyDays), día borrado
// (al pasar DailyDays); después, si el archivo supera MaxBytes, recorta primero lo crudo más antiguo (agregándolo antes), luego horas y
// por último días. Cada lote es una transacción de una hora o un día, y siempre agrega antes de borrar. Nunca toca la hora en curso.
func (s *Store) PurgeMonitor(ctx context.Context, now time.Time, r MonitorRetention) (MonitorPurgeResult, error) {
	var res MonitorPurgeResult
	nowMs := now.UnixMilli()
	rawCut := truncMs(nowMs-int64(r.RawDays)*dayMs, hourMs)
	for {
		more, err := s.rollOldestRawHour(ctx, rawCut)
		if err != nil {
			return res, err
		}
		if !more {
			break
		}
		res.HoursRolled++
		if err := pause(ctx); err != nil {
			return res, err
		}
	}
	hourCut := truncMs(nowMs-int64(r.HourlyDays)*dayMs, dayMs)
	for {
		more, err := s.rollOldestHourRollup(ctx, hourCut)
		if err != nil {
			return res, err
		}
		if !more {
			break
		}
		res.DaysRolled++
		if err := pause(ctx); err != nil {
			return res, err
		}
	}
	n, err := s.deleteOldDays(ctx, nowMs-int64(r.DailyDays)*dayMs)
	if err != nil {
		return res, err
	}
	res.DayRowsDeleted += n

	if r.MaxBytes > 0 {
		hourNow, dayNow := truncMs(nowMs, hourMs), truncMs(nowMs, dayMs)
		for {
			used, err := s.usedBytes(ctx)
			if err != nil {
				return res, err
			}
			if used <= r.MaxBytes {
				break
			}
			if more, err := s.rollOldestRawHour(ctx, hourNow); err != nil {
				return res, err
			} else if more {
				res.CapTrimmed = true
				res.HoursRolled++
			} else if more, err := s.rollOldestHourRollup(ctx, dayNow+dayMs); err != nil {
				return res, err
			} else if more {
				res.CapTrimmed = true
				res.DaysRolled++
			} else if n, err := s.deleteOldestDay(ctx); err != nil {
				return res, err
			} else if n > 0 {
				res.CapTrimmed = true
				res.DayRowsDeleted += n
			} else {
				break // no queda nada que recortar
			}
			if err := pause(ctx); err != nil {
				return res, err
			}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	if res.DigestsOrphaned, err = deleteOrphanDigestsCount(ctx, tx); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.UsedBytes, err = s.usedBytes(ctx)
	return res, err
}

func pause(ctx context.Context) error {
	if purgePause <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(purgePause):
		return nil
	}
}

// usedBytes es el tamaño usado del archivo: las páginas liberadas por un borrado no cuentan aunque el archivo no se encoja.
func (s *Store) usedBytes(ctx context.Context) (int64, error) {
	var pages, free, size int64
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT page_count FROM pragma_page_count), (SELECT freelist_count FROM pragma_freelist_count), (SELECT page_size FROM pragma_page_size)`).Scan(&pages, &free, &size); err != nil {
		return 0, err
	}
	return (pages - free) * size, nil
}

// rollOldestRawHour agrega la hora cruda más antigua y la borra, si termina antes de limit. Devuelve false si no hay nada que hacer.
func (s *Store) rollOldestRawHour(ctx context.Context, limit int64) (bool, error) {
	var minTs sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(ts) FROM monitor_sample`).Scan(&minTs); err != nil {
		return false, err
	}
	if !minTs.Valid {
		return false, nil
	}
	h := truncMs(minTs.Int64, hourMs)
	if h+hourMs > limit {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{rawToHourSQL(), rawToHourDigestSQL()} {
		if _, err := tx.ExecContext(ctx, q, h, h+hourMs); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM monitor_sample WHERE ts >= ? AND ts < ?`, h, h+hourMs); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// rollOldestHourRollup agrega el día más antiguo de rollups por hora y borra esas horas, si el día termina antes de limit.
func (s *Store) rollOldestHourRollup(ctx context.Context, limit int64) (bool, error) {
	var minTs sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(hour_ts) FROM monitor_rollup_hour`).Scan(&minTs); err != nil {
		return false, err
	}
	if !minTs.Valid {
		return false, nil
	}
	d := truncMs(minTs.Int64, dayMs)
	if d+dayMs > limit {
		return false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{hourToDaySQL(), hourToDayDigestSQL()} {
		if _, err := tx.ExecContext(ctx, q, d, d+dayMs); err != nil {
			return false, err
		}
	}
	for _, q := range []string{`DELETE FROM monitor_rollup_hour WHERE hour_ts >= ? AND hour_ts < ?`, `DELETE FROM monitor_rollup_hour_digest WHERE hour_ts >= ? AND hour_ts < ?`} {
		if _, err := tx.ExecContext(ctx, q, d, d+dayMs); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func (s *Store) deleteOldDays(ctx context.Context, cutoff int64) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM monitor_rollup_day WHERE day_ts + ? <= ?`, dayMs, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `DELETE FROM monitor_rollup_day_digest WHERE day_ts + ? <= ?`, dayMs, cutoff); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

func (s *Store) deleteOldestDay(ctx context.Context) (int, error) {
	var minTs sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(day_ts) FROM monitor_rollup_day`).Scan(&minTs); err != nil || !minTs.Valid {
		return 0, err
	}
	return s.deleteOldDays(ctx, minTs.Int64+dayMs)
}

func deleteOrphanDigests(ctx context.Context, tx *sql.Tx) error {
	_, err := deleteOrphanDigestsCount(ctx, tx)
	return err
}

// deleteOrphanDigestsCount borra del diccionario los digests sin ningún dato (crudo ni rollups) que los use.
func deleteOrphanDigestsCount(ctx context.Context, tx *sql.Tx) (int, error) {
	res, err := tx.ExecContext(ctx, `
		DELETE FROM monitor_digest WHERE NOT EXISTS (SELECT 1 FROM monitor_digest_sample WHERE digest_id = monitor_digest.id)
		AND NOT EXISTS (SELECT 1 FROM monitor_rollup_hour_digest WHERE digest_id = monitor_digest.id)
		AND NOT EXISTS (SELECT 1 FROM monitor_rollup_day_digest WHERE digest_id = monitor_digest.id)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// ---- SQL de rollups, generado a partir de MonitorMetrics ----

func metricCols() string {
	var b strings.Builder
	for _, m := range MonitorMetrics {
		fmt.Fprintf(&b, ", %s_avg, %s_min, %s_max", m, m, m)
	}
	return b.String()
}

// mergeSet fusiona una fila nueva con una existente de la misma clave (llegada tardía tras un rollup): media ponderada por muestras.
func mergeSet(table string) string {
	var b strings.Builder
	b.WriteString("samples = samples + excluded.samples, first_ts = MIN(first_ts, excluded.first_ts), last_ts = MAX(last_ts, excluded.last_ts), span_ms = span_ms + excluded.span_ms")
	for _, m := range MonitorMetrics {
		fmt.Fprintf(&b, `, %[2]s_avg = CASE WHEN %[1]s.%[2]s_avg IS NULL THEN excluded.%[2]s_avg WHEN excluded.%[2]s_avg IS NULL THEN %[1]s.%[2]s_avg
			ELSE (%[1]s.%[2]s_avg * %[1]s.samples + excluded.%[2]s_avg * excluded.samples) / (%[1]s.samples + excluded.samples) END,
			%[2]s_min = MIN(COALESCE(%[1]s.%[2]s_min, excluded.%[2]s_min), COALESCE(excluded.%[2]s_min, %[1]s.%[2]s_min)),
			%[2]s_max = MAX(COALESCE(%[1]s.%[2]s_max, excluded.%[2]s_max), COALESCE(excluded.%[2]s_max, %[1]s.%[2]s_max))`, table, m)
	}
	return b.String()
}

const digestMergeSet = `execs = execs + excluded.execs, total_us = total_us + excluded.total_us, max_observed_us = MAX(max_observed_us, excluded.max_observed_us),
	rows_examined = rows_examined + excluded.rows_examined, rows_sent = rows_sent + excluded.rows_sent, no_index = no_index + excluded.no_index,
	tmp_disk = tmp_disk + excluded.tmp_disk, errors = errors + excluded.errors`

const digestSums = `SUM(execs), SUM(total_us), MAX(max_observed_us), SUM(rows_examined), SUM(rows_sent), SUM(no_index), SUM(tmp_disk), SUM(errors)`

func rawToHourSQL() string {
	var sel strings.Builder
	for _, m := range MonitorMetrics {
		fmt.Fprintf(&sel, ", AVG(%[1]s), MIN(%[1]s), MAX(%[1]s)", m)
	}
	return `INSERT INTO monitor_rollup_hour (profile_id, hour_ts, samples, first_ts, last_ts, span_ms` + metricCols() + `)
		SELECT profile_id, (ts / ` + fmt.Sprint(hourMs) + `) * ` + fmt.Sprint(hourMs) + `, COUNT(1), MIN(ts), MAX(ts), SUM(span_ms)` + sel.String() + `
		FROM monitor_sample WHERE ts >= ? AND ts < ? GROUP BY profile_id
		ON CONFLICT (profile_id, hour_ts) DO UPDATE SET ` + mergeSet("monitor_rollup_hour")
}

func rawToHourDigestSQL() string {
	return `INSERT INTO monitor_rollup_hour_digest (profile_id, hour_ts, digest_id, execs, total_us, max_observed_us, rows_examined, rows_sent, no_index, tmp_disk, errors)
		SELECT d.profile_id, (x.ts / ` + fmt.Sprint(hourMs) + `) * ` + fmt.Sprint(hourMs) + `, x.digest_id, SUM(x.execs), SUM(x.total_us), MAX(x.max_observed_us),
			SUM(x.rows_examined), SUM(x.rows_sent), SUM(x.no_index), SUM(x.tmp_disk), SUM(x.errors)
		FROM monitor_digest_sample x JOIN monitor_digest d ON d.id = x.digest_id WHERE x.ts >= ? AND x.ts < ? GROUP BY d.profile_id, x.digest_id
		ON CONFLICT (profile_id, hour_ts, digest_id) DO UPDATE SET ` + digestMergeSet
}

func hourToDaySQL() string {
	var sel strings.Builder
	for _, m := range MonitorMetrics {
		fmt.Fprintf(&sel, `, SUM(%[1]s_avg * samples) / SUM(CASE WHEN %[1]s_avg IS NOT NULL THEN samples END), MIN(%[1]s_min), MAX(%[1]s_max)`, m)
	}
	return `INSERT INTO monitor_rollup_day (profile_id, day_ts, samples, first_ts, last_ts, span_ms` + metricCols() + `)
		SELECT profile_id, (hour_ts / ` + fmt.Sprint(dayMs) + `) * ` + fmt.Sprint(dayMs) + `, SUM(samples), MIN(first_ts), MAX(last_ts), SUM(span_ms)` + sel.String() + `
		FROM monitor_rollup_hour WHERE hour_ts >= ? AND hour_ts < ? GROUP BY profile_id
		ON CONFLICT (profile_id, day_ts) DO UPDATE SET ` + mergeSet("monitor_rollup_day")
}

func hourToDayDigestSQL() string {
	return `INSERT INTO monitor_rollup_day_digest (profile_id, day_ts, digest_id, execs, total_us, max_observed_us, rows_examined, rows_sent, no_index, tmp_disk, errors)
		SELECT profile_id, (hour_ts / ` + fmt.Sprint(dayMs) + `) * ` + fmt.Sprint(dayMs) + `, digest_id, ` + digestSums + `
		FROM monitor_rollup_hour_digest WHERE hour_ts >= ? AND hour_ts < ? GROUP BY profile_id, digest_id
		ON CONFLICT (profile_id, day_ts, digest_id) DO UPDATE SET ` + digestMergeSet
}

// ---- utilidades ----

func truncMs(v, unit int64) int64 {
	// Floor también para valores negativos (no ocurre con fechas reales, pero evita sorpresas).
	q := v / unit
	if v%unit < 0 {
		q--
	}
	return q * unit
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// truncateUTF8 recorta a max bytes sin partir un carácter.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
