package monitor

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/vitacodesas/dbacus/internal/model"
)

// LongProcessSec es el umbral de «proceso largo» (spec 22).
const LongProcessSec = 10

type nameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// procJSON es lo único de los procesos que se persiste: contadores por usuario, base y comando. Nunca INFO (⚑D7).
type procJSON struct {
	Users     []nameCount `json:"users"`
	DBs       []nameCount `json:"dbs"`
	Commands  []nameCount `json:"commands"`
	LongCount int         `json:"long"`
}

// idle: hilos que no son trabajo del usuario aunque su TIME sea enorme (conexiones dormidas, réplica, daemons).
func idle(cmd string) bool {
	c := strings.ToLower(cmd)
	return c == "sleep" || c == "daemon" || strings.HasPrefix(c, "binlog dump")
}

// Aggregate resume los procesos. Running cuenta todo lo que no es Sleep; LongestSec y el número de largos ignoran
// Sleep, Daemon y Binlog Dump (su TIME no mide una consulta). Info nunca sale de aquí.
func Aggregate(rows []model.Process) (sum model.ProcessSummary, longCount int) {
	sum = model.ProcessSummary{ByCommand: map[string]int{}, ByUser: map[string]int{}, Total: len(rows)}
	for _, p := range rows {
		if strings.EqualFold(p.Command, "Sleep") {
			sum.Sleeping++
		} else {
			sum.Running++
		}
		st := strings.ToLower(p.State)
		if strings.HasPrefix(st, "waiting for") && strings.Contains(st, "lock") {
			sum.WaitingLock++
		}
		if !idle(p.Command) {
			sum.LongestSec = max(sum.LongestSec, p.TimeSec)
			if p.TimeSec >= LongProcessSec {
				longCount++
			}
		}
		sum.ByCommand[p.Command]++
		sum.ByUser[p.User]++
	}
	return sum, longCount
}

// ProcessMetrics son las métricas proc_* de store.MonitorMetrics.
func ProcessMetrics(sum model.ProcessSummary, longCount int) map[string]float64 {
	return map[string]float64{
		"proc_total": float64(sum.Total), "proc_running": float64(sum.Running), "proc_sleeping": float64(sum.Sleeping),
		"proc_waiting_lock": float64(sum.WaitingLock), "proc_longest_sec": float64(sum.LongestSec), "proc_long_count": float64(longCount),
	}
}

// ProcJSON serializa el top 10 por usuario, base y comando para monitor_sample.proc_json.
func ProcJSON(rows []model.Process, longCount int) string {
	users, dbs, cmds := map[string]int{}, map[string]int{}, map[string]int{}
	for _, p := range rows {
		users[p.User]++
		if p.DB != "" {
			dbs[p.DB]++
		}
		cmds[p.Command]++
	}
	b, err := json.Marshal(procJSON{Users: top10(users), DBs: top10(dbs), Commands: top10(cmds), LongCount: longCount})
	if err != nil {
		return ""
	}
	return string(b)
}

func top10(m map[string]int) []nameCount {
	out := make([]nameCount, 0, len(m))
	for k, v := range m {
		out = append(out, nameCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// Snapshot arma lo que recibe la vista En vivo. Solo en memoria: lleva Info.
func Snapshot(at time.Time, rows []model.Process) model.ProcessSnapshot {
	sum, _ := Aggregate(rows)
	return model.ProcessSnapshot{At: at, Rows: rows, Summary: sum}
}
