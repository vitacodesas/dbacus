// Package monitor muestrea una conexión (spec 22): calcula deltas de contadores acumulados, agrega procesos y
// escribe muestras en el store. No habla con la red ni con SQLite por su cuenta: usa driver.ActivityReader y la
// interfaz Store, para poder probarlo con falsos.
package monitor

import (
	"math"
	"sort"
	"time"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/store"
)

// OverflowDigest representa la fila de digest NULL (tabla de digests llena). Su texto lo pone el monitor, no el servidor.
const (
	OverflowDigest = "__overflow__"
	OverflowText   = "(otros: tabla de digests llena)"
)

// Variables de SHOW GLOBAL STATUS que son contadores acumulados → tasa por segundo (clave de store.MonitorMetrics).
var counterMetrics = map[string]string{
	"Questions":                        "qps",
	"Com_select":                       "com_select",
	"Com_insert":                       "com_insert",
	"Com_update":                       "com_update",
	"Com_delete":                       "com_delete",
	"Slow_queries":                     "slow_queries",
	"Aborted_connects":                 "aborted_connects",
	"Aborted_clients":                  "aborted_clients",
	"Innodb_row_lock_waits":            "row_lock_waits",
	"Innodb_row_lock_time":             "row_lock_time_ms",
	"Innodb_buffer_pool_reads":         "bp_reads",
	"Innodb_buffer_pool_read_requests": "bp_read_requests",
	"Created_tmp_tables":               "tmp_tables",
	"Created_tmp_disk_tables":          "tmp_disk_tables",
	"Select_full_join":                 "select_full_join",
	"Bytes_received":                   "bytes_received",
	"Bytes_sent":                       "bytes_sent",
}

// Variables instantáneas: se guardan tal cual.
var instantMetrics = map[string]string{
	"Threads_connected": "threads_connected",
	"Threads_running":   "threads_running",
}

// StatusNames son las variables que hay que pedir al servidor (incluye Uptime, que detecta reinicios).
func StatusNames() []string {
	out := []string{"Uptime"}
	for n := range counterMetrics {
		out = append(out, n)
	}
	for n := range instantMetrics {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

type digestKey struct{ schema, digest string }

// Engine calcula deltas contra la lectura anterior de cada fuente. Cada fuente lleva su propio instante, así que un fallo
// de una no descuadra las tasas de la otra. No es seguro para uso concurrente: lo usa un solo Runner.
type Engine struct {
	haveStatus bool
	statusPrev map[string]uint64
	statusAt   time.Time
	uptime     uint64
	haveUptime bool

	haveDigest bool
	digestPrev map[digestKey]model.DigestCounters
}

func NewEngine() *Engine { return &Engine{} }

// Status devuelve las métricas de una lectura de SHOW GLOBAL STATUS. Las instantáneas salen siempre; las tasas solo
// si hay lectura previa. restarted indica que Uptime bajó: se descarta toda línea base (también la del digest) y no hay
// tasas ni deltas en esta lectura.
func (e *Engine) Status(at time.Time, vals map[string]uint64) (metrics map[string]float64, restarted bool) {
	metrics = map[string]float64{}
	for name, key := range instantMetrics {
		if v, ok := vals[name]; ok {
			metrics[key] = float64(v)
		}
	}
	if up, ok := vals["Uptime"]; ok {
		if e.haveUptime && up < e.uptime {
			restarted = true
			e.haveStatus = false
			e.ResetDigests()
		}
		e.uptime, e.haveUptime = up, true
	}
	if e.haveStatus {
		if span := at.Sub(e.statusAt).Seconds(); span > 0 {
			for name, key := range counterMetrics {
				cur, ok := vals[name]
				prev, had := e.statusPrev[name]
				if !ok || !had {
					continue
				}
				// Un contador que baja (FLUSH STATUS) cuenta desde cero: lo acumulado desde el reinicio es el delta.
				d := cur
				if cur >= prev {
					d = cur - prev
				}
				metrics[key] = float64(d) / span
			}
		}
	}
	e.statusPrev = make(map[string]uint64, len(vals))
	for k, v := range vals {
		e.statusPrev[k] = v
	}
	e.statusAt, e.haveStatus = at, true
	return metrics, restarted
}

// ResetDigests olvida la línea base del digest: la próxima lectura solo fija una nueva.
func (e *Engine) ResetDigests() { e.haveDigest, e.digestPrev = false, nil }

type rawDelta struct {
	key                                                               digestKey
	execs, timerPs, maxPs, rowsExam, rowsSent, noIndex, tmpDisk, errs uint64
}

// Digest devuelve los deltas de una lectura. La primera lectura (o la siguiente a un reinicio) solo fija la línea base
// y devuelve baseline=true sin filas. Se conservan el top-K por tiempo ∪ el top-K por ejecuciones con delta>0; el resto se
// suma en una fila OtherDigest para que Σ filas = delta total del servidor.
func (e *Engine) Digest(list []model.DigestCounters, topK int) (deltas []store.DigestDelta, baseline bool) {
	return e.DigestExcluding(list, topK, nil)
}

// ChangedDigests lista los digests (hash no vacío) cuyo contador subió, bajó o es nuevo respecto a la lectura anterior,
// sin tocar el estado. Son los candidatos a clasificar como propios del monitor antes de DigestExcluding. Sin línea base
// no hay candidatos.
func (e *Engine) ChangedDigests(list []model.DigestCounters) []string {
	if !e.haveDigest {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range list {
		if c.Digest == "" || seen[c.Digest] {
			continue
		}
		if p, ok := e.digestPrev[keyOf(c)]; !ok || c.Execs != p.Execs {
			seen[c.Digest] = true
			out = append(out, c.Digest)
		}
	}
	return out
}

// DigestExcluding es Digest sin los digests que skip(hash) marque (los del propio monitor, ⚑D16). Se excluyen ANTES del
// top-K y no entran en la fila __other__: Σ(top-K + __other__) = total del servidor − lo excluido.
func (e *Engine) DigestExcluding(list []model.DigestCounters, topK int, skip func(digest string) bool) (deltas []store.DigestDelta, baseline bool) {
	cur := make(map[digestKey]model.DigestCounters, len(list))
	for _, c := range list {
		cur[keyOf(c)] = c
	}
	prev, had := e.digestPrev, e.haveDigest
	e.digestPrev, e.haveDigest = cur, true
	if !had {
		return nil, true
	}
	if topK <= 0 {
		topK = 30
	}
	var all []rawDelta
	for k, c := range cur {
		if skip != nil && k.digest != OverflowDigest && skip(k.digest) {
			continue
		}
		p, seen := prev[k]
		var d rawDelta
		switch {
		case !seen || c.Execs < p.Execs:
			// Digest nuevo, desalojado y vuelto a entrar, o TRUNCATE: lo acumulado es el delta (ADR-007).
			d = rawDelta{key: k, execs: c.Execs, timerPs: c.TimerPs, rowsExam: c.RowsExamined, rowsSent: c.RowsSent, noIndex: c.NoIndex, tmpDisk: c.TmpDiskTables, errs: c.Errors}
		default:
			d = rawDelta{key: k, execs: c.Execs - p.Execs, timerPs: sub(c.TimerPs, p.TimerPs), rowsExam: sub(c.RowsExamined, p.RowsExamined),
				rowsSent: sub(c.RowsSent, p.RowsSent), noIndex: sub(c.NoIndex, p.NoIndex), tmpDisk: sub(c.TmpDiskTables, p.TmpDiskTables), errs: sub(c.Errors, p.Errors)}
		}
		d.maxPs = c.MaxTimerPs
		if d.execs > 0 {
			all = append(all, d)
		}
	}
	keep := selectTop(all, topK)
	var other rawDelta
	var haveOther bool
	for i, d := range all {
		if keep[i] {
			deltas = append(deltas, toDelta(d))
			continue
		}
		haveOther = true
		other.execs += d.execs
		other.timerPs += d.timerPs
		other.rowsExam += d.rowsExam
		other.rowsSent += d.rowsSent
		other.noIndex += d.noIndex
		other.tmpDisk += d.tmpDisk
		other.errs += d.errs
		other.maxPs = max(other.maxPs, d.maxPs)
	}
	sort.Slice(deltas, func(i, j int) bool { // orden estable para que las pruebas no dependan del mapa
		if deltas[i].Schema != deltas[j].Schema {
			return deltas[i].Schema < deltas[j].Schema
		}
		return deltas[i].Digest < deltas[j].Digest
	})
	if haveOther {
		o := toDelta(other)
		o.Schema, o.Digest, o.Text = "", store.OtherDigest, ""
		deltas = append(deltas, o)
	}
	return deltas, false
}

func keyOf(c model.DigestCounters) digestKey {
	if c.Digest == "" {
		return digestKey{"", OverflowDigest}
	}
	return digestKey{c.Schema, c.Digest}
}

// sub resta sin desbordar: un campo individual que baja cuenta desde cero.
func sub(cur, prev uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return cur
}

func selectTop(all []rawDelta, k int) []bool {
	keep := make([]bool, len(all))
	if len(all) <= k {
		for i := range keep {
			keep[i] = true
		}
		return keep
	}
	idx := make([]int, len(all))
	pick := func(less func(a, b rawDelta) bool) {
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(i, j int) bool {
			a, b := all[idx[i]], all[idx[j]]
			if less(a, b) {
				return true
			}
			if less(b, a) {
				return false
			}
			return a.key.schema+"\x00"+a.key.digest < b.key.schema+"\x00"+b.key.digest
		})
		for _, i := range idx[:k] {
			keep[i] = true
		}
	}
	pick(func(a, b rawDelta) bool { return a.timerPs > b.timerPs })
	pick(func(a, b rawDelta) bool { return a.execs > b.execs })
	return keep
}

func toDelta(d rawDelta) store.DigestDelta {
	out := store.DigestDelta{
		Schema: d.key.schema, Digest: d.key.digest,
		Execs: clamp(d.execs), TotalUs: clamp(d.timerPs / 1_000_000), MaxObservedUs: clamp(d.maxPs / 1_000_000),
		RowsExamined: clamp(d.rowsExam), RowsSent: clamp(d.rowsSent), NoIndex: clamp(d.noIndex), TmpDisk: clamp(d.tmpDisk), Errors: clamp(d.errs),
	}
	if d.key.digest == OverflowDigest {
		out.Text = OverflowText
	}
	return out
}

func clamp(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
