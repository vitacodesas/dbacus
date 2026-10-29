//go:build integration

package services

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	_ "github.com/vitacodesas/dbacus/internal/driver/mysql"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/results"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

type liveEnv struct {
	q      *QueryService
	sid    string
	image  string
	events chan namedEvent
	log    []string
	mu     sync.Mutex
}

func newLiveEnv(t *testing.T, in mysqlbox.Instance) *liveEnv {
	t.Helper()
	sessions := session.NewManager(session.Options{})
	t.Cleanup(sessions.CloseAll)
	sess, err := sessions.Open(context.Background(), session.OpenRequest{
		Profile:  model.ConnectionProfile{ID: "p", Driver: "mysql", Host: in.Host, Port: in.Port, User: mysqlbox.User, Database: mysqlbox.Database, TLS: model.TLSConfig{Mode: "preferred"}},
		Password: mysqlbox.Password,
	})
	require.NoError(t, err)

	e := &liveEnv{sid: sess.ID, image: in.Image, events: make(chan namedEvent, 4096)}
	emit := func(name string, data any) {
		e.mu.Lock()
		e.log = append(e.log, name)
		e.mu.Unlock()
		e.events <- namedEvent{name, data}
	}
	e.q = NewQueryService(QueryDeps{
		Sessions: sessions, Results: results.NewManager(2 << 30), Schema: NewSchemaService(sessions, emit),
		Store: &memStore{settings: model.DefaultSettings()}, Emit: emit,
	})
	t.Cleanup(func() { _ = e.q.ServiceShutdown() })
	return e
}

func (e *liveEnv) run(t *testing.T, tab, sql string, mut ...func(*ExecuteRequest)) (string, *QueryDone, *QueryError) {
	t.Helper()
	req := ExecuteRequest{SessionID: e.sid, TabID: tab, SQL: sql}
	for _, m := range mut {
		m(&req)
	}
	res, err := e.q.Execute(context.Background(), req)
	require.NoError(t, err)
	deadline := time.After(60 * time.Second)
	for {
		select {
		case ev := <-e.events:
			switch d := ev.data.(type) {
			case QueryDone:
				if d.QueryID == res.QueryID {
					return res.QueryID, &d, nil
				}
			case QueryError:
				if d.QueryID == res.QueryID {
					return res.QueryID, nil, &d
				}
			}
		case <-deadline:
			t.Fatalf("la ejecución no terminó: %s", sql)
		}
	}
}

func (e *liveEnv) mustRun(t *testing.T, tab, sql string, mut ...func(*ExecuteRequest)) (string, *QueryDone) {
	t.Helper()
	id, done, failed := e.run(t, tab, sql, mut...)
	require.Nil(t, failed, "%+v", failed)
	return id, done
}

func TestQueryServiceIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			t.Run("tipos extremos", e.types)
			t.Run("varias sentencias y error", e.multi)
			t.Run("estado por pestaña", e.tabState)
			t.Run("USE y base vigente", e.useStatement)
			t.Run("cancelar SLEEP", e.cancelSleep)
			t.Run("truncado con KILL y conexión viva", e.truncate)
			t.Run("streaming de muchas filas", e.bigResult)
			t.Run("DDL invalida esquema", e.ddl)
		})
	}
}

func (e *liveEnv) types(t *testing.T) {
	qid, done := e.mustRun(t, "types", `SELECT 18446744073709551615 AS big, 9007199254740993 AS big_signed, CAST('12.340' AS DECIMAL(10,3)) AS dec_,
		NULL AS nul, '😀 ñ' AS txt, JSON_OBJECT('a', JSON_ARRAY(1, 2)) AS js, CAST('2024-01-02 03:04:05.123456' AS DATETIME(6)) AS dt, X'00FF' AS bin,
		1.5e0 AS dbl, CAST(1 AS SIGNED) AS small`)
	require.Len(t, done.Results[0].Columns, 10)
	w, err := e.q.GetRows(qid, 0, 0, 10)
	require.NoError(t, err)
	row := w.Rows[0]
	require.Equal(t, results.Cell{V: "18446744073709551615", T: results.TBigint}, row[0], "BIGINT UNSIGNED máximo sin pérdida")
	require.Equal(t, results.Cell{V: "9007199254740993", T: results.TBigint}, row[1], "más allá de 2^53")
	require.Equal(t, results.Cell{V: "12.340", T: results.TDecimal}, row[2])
	require.Equal(t, results.Cell{}, row[3])
	require.Equal(t, results.Cell{V: "😀 ñ"}, row[4])
	if strings.HasPrefix(e.image, "mariadb") {
		require.Empty(t, row[5].T, "en MariaDB JSON es un alias de texto: no hay tipo JSON propio")
	} else {
		require.Equal(t, results.TJSON, row[5].T)
	}
	require.Contains(t, row[5].V, `"a"`)
	require.Equal(t, results.Cell{V: "2024-01-02 03:04:05.123456", T: results.TDatetime}, row[6])
	require.Equal(t, results.Cell{V: "0x00ff", T: results.TBytes}, row[7])
	require.Equal(t, results.Cell{V: 1.5}, row[8])
	require.Equal(t, results.Cell{V: int64(1)}, row[9])
}

func (e *liveEnv) multi(t *testing.T) {
	qid, done, failed := e.run(t, "multi", "SELECT 1 AS a; SELECT NOPE_NO_EXISTE(); SELECT 3")
	require.Nil(t, done)
	require.Equal(t, 1, failed.StmtIndex)
	require.Contains(t, []string{apperr.QueryFailed, apperr.QuerySyntax}, failed.Error.Code)
	require.NotEmpty(t, failed.Error.Message)
	w, err := e.q.GetRows(qid, 0, 0, 10)
	require.NoError(t, err, "el resultado de la primera sentencia se conserva")
	require.Len(t, w.Rows, 1)

	_, _, failed = e.run(t, "multi", "SELEC 1")
	require.Equal(t, apperr.QuerySyntax, failed.Error.Code)
}

func (e *liveEnv) tabState(t *testing.T) {
	e.mustRun(t, "tabA", "SET @x = 42")
	idA, _ := e.mustRun(t, "tabA", "SELECT @x")
	idB, _ := e.mustRun(t, "tabB", "SELECT @x")
	a, _ := e.q.GetRows(idA, 0, 0, 1)
	b, _ := e.q.GetRows(idB, 0, 0, 1)
	require.Equal(t, int64(42), a.Rows[0][0].V, "la pestaña conserva sus variables de sesión")
	require.Equal(t, results.Cell{}, b.Rows[0][0], "otra pestaña no las ve")
}

func (e *liveEnv) useStatement(t *testing.T) {
	e.mustRun(t, "use", "CREATE DATABASE IF NOT EXISTS otra_base")
	_, done := e.mustRun(t, "use", "USE otra_base")
	require.Equal(t, "otra_base", done.Database)
	id, _ := e.mustRun(t, "use", "SELECT DATABASE()")
	w, _ := e.q.GetRows(id, 0, 0, 1)
	require.Equal(t, "otra_base", w.Rows[0][0].V)

	_, done = e.mustRun(t, "use", "SELECT 1", func(r *ExecuteRequest) { r.Database = mysqlbox.Database })
	require.Equal(t, mysqlbox.Database, done.Database, "el selector de base cambia la base de la pestaña")
}

func (e *liveEnv) cancelSleep(t *testing.T) {
	e.mustRun(t, "slp", "SET @keep = 7")
	res, err := e.q.Execute(context.Background(), ExecuteRequest{SessionID: e.sid, TabID: "slp", SQL: "SELECT SLEEP(60)"})
	require.NoError(t, err)
	time.Sleep(500 * time.Millisecond)

	start := time.Now()
	require.NoError(t, e.q.Cancel(res.QueryID))
	deadline := time.After(5 * time.Second)
	for finished := false; !finished; {
		select {
		case ev := <-e.events:
			if d, ok := ev.data.(QueryError); ok && d.QueryID == res.QueryID {
				require.Equal(t, apperr.QueryCanceled, d.Error.Code)
				finished = true
			}
			if d, ok := ev.data.(QueryDone); ok && d.QueryID == res.QueryID {
				t.Fatal("una consulta cancelada no puede terminar como correcta")
			}
		case <-deadline:
			t.Fatal("Cancel no devolvió el control")
		}
	}
	require.Less(t, time.Since(start), time.Second, "cancelar SELECT SLEEP(60) vuelve en menos de 1 s")

	id, _ := e.mustRun(t, "slp", "SELECT @keep")
	w, _ := e.q.GetRows(id, 0, 0, 1)
	require.Equal(t, int64(7), w.Rows[0][0].V, "la conexión y sus variables sobreviven a la cancelación")

	// Ningún hilo sigue ejecutando el SLEEP en el servidor.
	pid, _ := e.mustRun(t, "chk", "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE INFO LIKE 'SELECT SLEEP(60)%' AND ID <> CONNECTION_ID()")
	pw, _ := e.q.GetRows(pid, 0, 0, 1)
	require.Equal(t, int64(0), pw.Rows[0][0].V)
}

// generator devuelve un SELECT que produce n filas (CTE recursiva) y la sentencia que sube el límite de recursión.
func (e *liveEnv) generator(n int) (setup, query string) {
	if strings.HasPrefix(e.image, "mariadb") {
		return "SET SESSION max_recursive_iterations = 4294967295",
			"WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM s WHERE n < " + itoa(n) + ") SELECT n, CONCAT('fila-', n) AS txt FROM s"
	}
	return "SET SESSION cte_max_recursion_depth = 4000000",
		"WITH RECURSIVE s(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM s WHERE n < " + itoa(n) + ") SELECT n, CONCAT('fila-', n) AS txt FROM s"
}

func itoa(n int) string { return strconv.Itoa(n) }

func (e *liveEnv) truncate(t *testing.T) {
	setup, q := e.generator(500_000)
	e.mustRun(t, "trunc", setup)
	start := time.Now()
	qid, done := e.mustRun(t, "trunc", q) // MaxRows por defecto: 100 000
	r := done.Results[0]
	require.EqualValues(t, 100_000, r.RowCount)
	require.True(t, r.Truncated)
	t.Logf("100k filas de un resultado de 500k en %s", time.Since(start))

	w, err := e.q.GetRows(qid, 0, 99_990, 100)
	require.NoError(t, err)
	require.Len(t, w.Rows, 10)
	require.Equal(t, int64(100_000), w.Rows[9][0].V)

	// La consulta se abortó con KILL y la conexión sigue siendo utilizable.
	id, _ := e.mustRun(t, "trunc", "SELECT 'sigo viva'")
	w, _ = e.q.GetRows(id, 0, 0, 1)
	require.Equal(t, "sigo viva", w.Rows[0][0].V)
}

func (e *liveEnv) bigResult(t *testing.T) {
	setup, q := e.generator(300_000)
	e.mustRun(t, "big", setup)
	start := time.Now()
	qid, done := e.mustRun(t, "big", q, func(r *ExecuteRequest) { r.MaxRows = 1_000_000 })
	require.EqualValues(t, 300_000, done.Results[0].RowCount)
	require.False(t, done.Results[0].Truncated)
	t.Logf("300k filas en %s", time.Since(start))

	for _, off := range []int{0, 150_000, 299_999} {
		w, err := e.q.GetRows(qid, 0, off, 5)
		require.NoError(t, err)
		require.Equal(t, int64(off+1), w.Rows[0][0].V)
		require.Equal(t, "fila-"+itoa(off+1), w.Rows[0][1].V)
	}
}

func (e *liveEnv) ddl(t *testing.T) {
	e.mustRun(t, "ddl", "DROP TABLE IF EXISTS t_ddl")
	schema := NewSchemaService(e.q.d.Sessions, nil)
	before, err := schema.ListTables(context.Background(), e.sid, mysqlbox.Database)
	require.NoError(t, err)

	e.mustRun(t, "ddl", "CREATE TABLE t_ddl (id INT PRIMARY KEY)")
	after, err := schema.ListTables(context.Background(), e.sid, mysqlbox.Database)
	require.NoError(t, err)
	require.Len(t, after, len(before)+1, "el CREATE ejecutado desde el editor invalida la caché de esquema")
}
