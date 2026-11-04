package monitor

import (
	"strings"
	"testing"

	"github.com/vitacodesas/dbacus/internal/model"
)

func TestAggregate(t *testing.T) {
	rows := []model.Process{
		{ID: 1, User: "app", DB: "d1", Command: "Query", TimeSec: 48, State: "executing", Info: "SELECT 1"},
		{ID: 2, User: "app", DB: "d1", Command: "Sleep", TimeSec: 300},
		{ID: 3, User: "etl", DB: "d2", Command: "Query", TimeSec: 0, State: "Waiting for table metadata lock"},
		{ID: 4, User: "repl", Command: "Binlog Dump GTID", TimeSec: 99999},
		{ID: 5, User: "app", DB: "d1", Command: "Query", TimeSec: 12, State: "Sending data"},
	}
	sum, long := Aggregate(rows)
	if sum.Total != 5 || sum.Sleeping != 1 || sum.Running != 4 || sum.WaitingLock != 1 {
		t.Fatalf("conteos: %+v", sum)
	}
	if sum.LongestSec != 48 || long != 2 {
		t.Fatalf("el Sleep y el Binlog Dump no cuentan como largos: longest=%d long=%d", sum.LongestSec, long)
	}
	if sum.ByUser["app"] != 3 || sum.ByCommand["Query"] != 3 {
		t.Fatalf("desglose: %+v", sum)
	}
	m := ProcessMetrics(sum, long)
	if m["proc_total"] != 5 || m["proc_long_count"] != 2 {
		t.Fatalf("métricas: %v", m)
	}
	if j := ProcJSON(rows, long); strings.Contains(j, "SELECT 1") || !strings.Contains(j, `"app"`) {
		t.Fatalf("proc_json: %s", j)
	}
}

func TestProcJSONTop10(t *testing.T) {
	var rows []model.Process
	for i := 0; i < 25; i++ {
		rows = append(rows, model.Process{User: string(rune('a' + i)), Command: "Query"})
	}
	if n := strings.Count(ProcJSON(rows, 0), `"count"`); n != 10+1 { // 10 usuarios + 1 comando
		t.Fatalf("top 10 por usuario: %d entradas", n)
	}
}
