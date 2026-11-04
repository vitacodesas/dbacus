package monitor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/store"
)

// Textos reales capturados de events_statements_summary_by_digest en MySQL 8.0 y 8.4 con
// TestMonitorOwnStatementsMatchServerDigestsIntegration (el servidor reescribe, p. ej., USER como SYSTEM_USER).
var realOwnTexts = []string{
	"SELECT ? FROM `performance_schema` . `events_statements_summary_by_digest` LIMIT ?",
	"SELECT @@GLOBAL . `max_connections`",
	"SELECT SCHEMA_NAME , `DIGEST` , `COUNT_STAR` , `SUM_TIMER_WAIT` , `MAX_TIMER_WAIT` , `SUM_ROWS_EXAMINED` , `SUM_ROWS_SENT` , `SUM_NO_INDEX_USED` , `SUM_NO_GOOD_INDEX_USED` , `SUM_CREATED_TMP_DISK_TABLES` , `SUM_ERRORS` FROM `performance_schema` . `events_statements_summary_by_digest`",
	"SELECT `ID` , SYSTEM_USER , HOST , `DB` , `COMMAND` , TIME , `STATE` , `INFO` FROM `INFORMATION_SCHEMA` . `PROCESSLIST` WHERE `ID` != `CONNECTION_ID` ( )",
	"SET SESSION TRANSACTION READ ONLY",
	"SET SESSION `max_execution_time` = ?",
	"SHOW GLOBAL STATUS WHERE `Variable_name` IN (...)",
	// Formas que el monitor no deja hoy en el digest pero que existen en su código.
	"SET SESSION `max_statement_time` = ?",
	"SELECT CONNECTION_ID ( )",
	"SELECT `DIGEST` , `DIGEST_TEXT` FROM `performance_schema` . `events_statements_summary_by_digest` WHERE `DIGEST` IN (...)",
}

// Consultas que NO son del monitor aunque se le parezcan: un usuario real las ejecuta.
var notOwnTexts = []string{
	"SELECT * FROM `orders` WHERE `id` = ?",
	"SELECT * FROM `INFORMATION_SCHEMA` . `PROCESSLIST`",
	"SELECT COUNT ( DISTINCTROW SYSTEM_USER ) FROM `INFORMATION_SCHEMA` . `PROCESSLIST`",
	"SHOW FULL PROCESSLIST",
	"SHOW GLOBAL STATUS LIKE ?",
	"SHOW GLOBAL STATUS",
	"SELECT `VERSION` ( )",
	"SELECT CURRENT_USER ( )",
	"SELECT @@`server_uuid`",
	"SET SESSION `sql_mode` = ?",
	"SELECT `DIGEST_TEXT` , `COUNT_STAR` FROM `performance_schema` . `events_statements_summary_by_digest` ORDER BY `SUM_TIMER_WAIT` DESC LIMIT ?",
	"",
}

func TestIsOwnStatement(t *testing.T) {
	for _, s := range realOwnTexts {
		if !IsOwnStatement(s) {
			t.Errorf("debería ser propia: %q", s)
		}
	}
	for _, s := range notOwnTexts {
		if IsOwnStatement(s) {
			t.Errorf("no debería ser propia: %q", s)
		}
	}
}

func TestIsOwnStatementIgnoresCaseAndSpacing(t *testing.T) {
	if !IsOwnStatement("  set   session transaction   read only ") || !IsOwnStatement("show global status where variable_name in (...)") {
		t.Fatal("la comparación debe ignorar mayúsculas, espacios y comillas invertidas")
	}
}

// TestRunnerSkipsOwnDigests: los digests propios no se guardan y el texto leído al clasificar se reutiliza.
func TestRunnerSkipsOwnDigests(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		digestFn: func(n int) ([]model.DigestCounters, error) {
			return []model.DigestCounters{
				dc("db", "user1", uint64(10*n), uint64(n)*1e9),
				dc("", "own1", uint64(7*n), uint64(n)*1e9), // SET SESSION TRANSACTION READ ONLY
				dc("", "own2", uint64(3*n), uint64(n)*1e9), // consulta del processlist
			}, nil
		},
		textsFn: func(want []string) (map[string]string, error) {
			m := map[string]string{}
			for _, d := range want {
				switch d {
				case "own1":
					m[d] = realOwnTexts[4]
				case "own2":
					m[d] = realOwnTexts[3]
				default:
					m[d] = "SELECT * FROM `t` WHERE `a` = ?"
				}
			}
			return m, nil
		},
		procsFn: func(int) ([]model.Process, error) { return nil, nil },
	}
	cancelAfter(clk, cancel, 4)
	r := startRunner(t, parent, rd, st, clk, ev, allSources)
	waitDone(t, r)

	for i, s := range st.snapshot() {
		for _, d := range s.Digests {
			if d.Digest == "own1" || d.Digest == "own2" {
				t.Fatalf("la muestra %d guarda un digest propio: %+v", i, d)
			}
			if d.Digest == store.OtherDigest && i > 0 {
				t.Fatalf("los propios no deben acabar en __other__: %+v", d)
			}
		}
		if i > 0 && (len(s.Digests) != 1 || s.Digests[0].Digest != "user1") {
			t.Fatalf("la muestra %d debe traer solo user1: %+v", i, s.Digests)
		}
		// El texto solo viaja la primera vez; después ya está en el diccionario del store.
		if i == 1 && s.Digests[0].Text == "" {
			t.Fatalf("falta el texto de user1 en su primer delta: %+v", s.Digests)
		}
	}
	// Se clasifica una sola vez cada digest: en las muestras siguientes no se vuelve a pedir su texto.
	if n := rd.nText.Load(); n != 1 {
		t.Fatalf("DigestTexts se llamó %d veces, quiero 1 (los textos leídos al clasificar se reutilizan)", n)
	}
}

// TestRunnerClassifyFailureSkipsDigestWithoutLoss: si no se puede leer el texto, esa muestra no escribe digest y la línea
// base no avanza, así que el delta siguiente cubre los dos intervalos.
func TestRunnerClassifyFailureSkipsDigestWithoutLoss(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clk, st, ev := newClock(), newStore(), &events{}
	var textCalls atomic.Int32
	rd := &fakeReader{
		statusFn: func(n int) (map[string]uint64, error) { return statusVals(n), nil },
		digestFn: func(n int) ([]model.DigestCounters, error) {
			return []model.DigestCounters{dc("db", "a", uint64(10*n), uint64(n)*1e9)}, nil
		},
		textsFn: func(want []string) (map[string]string, error) {
			if textCalls.Add(1) == 1 {
				return nil, errors.New("texto no disponible")
			}
			m := map[string]string{}
			for _, d := range want {
				m[d] = "SELECT * FROM `t` WHERE `a` = ?"
			}
			return m, nil
		},
		procsFn: func(int) ([]model.Process, error) { return nil, nil },
	}
	cancelAfter(clk, cancel, 4)
	r := startRunner(t, parent, rd, st, clk, ev, allSources)
	waitDone(t, r)

	s := st.snapshot()
	if len(s) != 4 {
		t.Fatalf("muestras: %d", len(s))
	}
	if len(s[1].Digests) != 0 {
		t.Fatalf("la muestra cuyo texto no se pudo leer no escribe digest: %+v", s[1].Digests)
	}
	// Lecturas: n=1 base (10), n=2 falla, n=3 delta = 30-10 = 20 (cubre dos intervalos), n=4 delta 10.
	if len(s[2].Digests) != 1 || s[2].Digests[0].Execs != 20 {
		t.Fatalf("el delta posterior debe cubrir ambos intervalos (20): %+v", s[2].Digests)
	}
	if len(s[3].Digests) != 1 || s[3].Digests[0].Execs != 10 {
		t.Fatalf("la muestra siguiente vuelve a 10: %+v", s[3].Digests)
	}
}
