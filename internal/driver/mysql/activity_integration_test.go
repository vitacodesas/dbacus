//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

func TestMonitorIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			maria := strings.HasPrefix(image, "mariadb")
			t.Run("capacidades de root", func(t *testing.T) { testMonitorCapsRoot(t, in, maria) })
			t.Run("capacidades con privilegios parciales", func(t *testing.T) { testMonitorCapsPartial(t, in, maria) })
			t.Run("consumidor del digest apagado", func(t *testing.T) { testMonitorConsumerOff(t, in, maria) })
			t.Run("procesos", func(t *testing.T) { testProcesslist(t, in) })
			t.Run("estado global", func(t *testing.T) { testGlobalStatus(t, in) })
			t.Run("digest", func(t *testing.T) { testDigests(t, in, maria) })
			t.Run("la conexión del monitor es de solo lectura", func(t *testing.T) { testMonReadOnly(t, in) })
			t.Run("uint64 por encima de int64", func(t *testing.T) { testUint64Scan(t, in) })
			if os.Getenv("DBACUS_BENCH_MONITOR") == "1" && !maria {
				t.Run("coste", func(t *testing.T) { testMonitorCost(t, in) })
			}
		})
	}
}

func execAll(t *testing.T, p driver.Pool, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	c, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer c.Release()
	for _, s := range stmts {
		r, err := c.Run(ctx, s)
		require.NoError(t, err, s)
		require.NoError(t, r.Close())
	}
}

func activity(t *testing.T, p driver.Pool) driver.ActivityReader {
	t.Helper()
	a, ok := p.(driver.ActivityReader)
	require.True(t, ok)
	return a
}

func asUser(user string) func(*driver.ConnConfig) {
	return func(c *driver.ConnConfig) { c.Profile.User, c.Profile.Database, c.Password = user, "", "pw" }
}

func testMonitorCapsRoot(t *testing.T, in mysqlbox.Instance, maria bool) {
	caps, err := activity(t, openPool(t, in, nil)).Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, maria, caps.IsMariaDB)
	require.NotEmpty(t, caps.ServerVersion)
	require.NotEmpty(t, caps.ServerID)
	require.Equal(t, model.SourceOK, caps.Status.State)
	require.Equal(t, model.SourceOK, caps.Processlist.State)
	if maria {
		// [Probable] MariaDB trae performance_schema apagado por defecto.
		require.Equal(t, model.ReasonPerfSchemaOff, caps.Digest.Reason)
		require.Contains(t, caps.Digest.Hint, "reiniciarlo")
		return
	}
	require.Equal(t, model.SourceOK, caps.Digest.State, caps.Digest.Reason)
}

func testMonitorCapsPartial(t *testing.T, in mysqlbox.Instance, maria bool) {
	root := openPool(t, in, nil)
	execAll(t, root,
		"CREATE USER 'mon_noproc'@'%' IDENTIFIED BY 'pw'",
		"GRANT SELECT ON performance_schema.* TO 'mon_noproc'@'%'",
		"CREATE USER 'mon_nops'@'%' IDENTIFIED BY 'pw'",
		"GRANT PROCESS ON *.* TO 'mon_nops'@'%'",
		"CREATE USER 'mon_none'@'%' IDENTIFIED BY 'pw'",
	)
	// Una segunda sesión de otro usuario, para que «solo se ven los propios» sea distinguible.
	other, err := root.Acquire(context.Background())
	require.NoError(t, err)
	defer other.Release()

	t.Run("sin PROCESS: own_only con el GRANT exacto", func(t *testing.T) {
		caps, err := activity(t, openPool(t, in, asUser("mon_noproc"))).Probe(context.Background())
		require.NoError(t, err)
		require.Equal(t, model.SourceDegraded, caps.Processlist.State)
		require.Equal(t, model.ReasonOwnOnly, caps.Processlist.Reason)
		require.Equal(t, "GRANT PROCESS ON *.* TO 'mon_noproc'@'%';", caps.Processlist.Hint)
		if !maria {
			require.Equal(t, model.SourceOK, caps.Digest.State, caps.Digest.Reason)
		}
		// Processes sigue funcionando: solo trae los hilos propios.
		ps, err := activity(t, openPool(t, in, asUser("mon_noproc"))).Processes(context.Background())
		require.NoError(t, err)
		for _, p := range ps {
			require.Equal(t, "mon_noproc", p.User)
		}
	})
	t.Run("con PROCESS y sin performance_schema", func(t *testing.T) {
		caps, err := activity(t, openPool(t, in, asUser("mon_nops"))).Probe(context.Background())
		require.NoError(t, err)
		require.Equal(t, model.SourceOK, caps.Processlist.State)
		require.Equal(t, model.SourceOK, caps.Status.State)
		require.Equal(t, model.SourceUnavailable, caps.Digest.State)
		if maria {
			require.Equal(t, model.ReasonPerfSchemaOff, caps.Digest.Reason)
		} else {
			require.Equal(t, model.ReasonNoPrivilege, caps.Digest.Reason)
			require.Equal(t, "GRANT SELECT ON performance_schema.* TO 'mon_nops'@'%';", caps.Digest.Hint)
		}
	})
	t.Run("sin ningún privilegio no es un error de Probe", func(t *testing.T) {
		caps, err := activity(t, openPool(t, in, asUser("mon_none"))).Probe(context.Background())
		require.NoError(t, err)
		require.Equal(t, model.ReasonOwnOnly, caps.Processlist.Reason)
		require.Equal(t, model.SourceUnavailable, caps.Digest.State)
	})
}

func testMonitorConsumerOff(t *testing.T, in mysqlbox.Instance, maria bool) {
	if maria {
		t.Skip("MariaDB trae performance_schema apagado: la rama consumer_off no aplica")
	}
	root := openPool(t, in, nil)
	execAll(t, root, "UPDATE performance_schema.setup_consumers SET ENABLED = 'NO' WHERE NAME = 'statements_digest'")
	defer execAll(t, root, "UPDATE performance_schema.setup_consumers SET ENABLED = 'YES' WHERE NAME = 'statements_digest'")
	caps, err := activity(t, root).Probe(context.Background())
	require.NoError(t, err)
	require.Equal(t, model.ReasonConsumerOff, caps.Digest.Reason)
	require.Contains(t, caps.Digest.Hint, "setup_consumers")
}

func testProcesslist(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, nil)
	act := activity(t, p)
	ctx := context.Background()

	busy, err := p.Acquire(ctx)
	require.NoError(t, err)
	long := "SELECT SLEEP(4) /* dbacus_marker " + strings.Repeat("x", 3000) + " */"
	done := make(chan struct{})
	go func() {
		defer close(done)
		if r, err := busy.Run(ctx, long); err == nil {
			_ = r.Close()
		}
	}()
	defer func() { <-done; _ = busy.Release() }()

	var found *model.Process
	var ps []model.Process
	require.Eventually(t, func() bool {
		var err error
		ps, err = act.Processes(ctx)
		require.NoError(t, err)
		for i := range ps {
			if strings.Contains(ps[i].Info, "dbacus_marker") {
				found = &ps[i]
				return true
			}
		}
		return false
	}, 3*time.Second, 100*time.Millisecond)

	require.Equal(t, "root", found.User)
	require.Equal(t, "Query", found.Command)
	require.True(t, found.InfoTruncated)
	require.LessOrEqual(t, len(found.Info), infoMaxBytes)
	for _, x := range ps {
		require.NotContains(t, x.Info, "INFORMATION_SCHEMA.PROCESSLIST", "la conexión del monitor no debe aparecer")
	}
	for i := 1; i < len(ps); i++ {
		require.GreaterOrEqual(t, ps[i-1].TimeSec, ps[i].TimeSec, "orden por tiempo descendente")
	}

	t.Run("respaldo SHOW FULL PROCESSLIST", func(t *testing.T) {
		var out []model.Process
		require.NoError(t, p.(*pool).withMon(ctx, func(ctx context.Context, c *sql.Conn) error {
			var self uint64
			require.NoError(t, c.QueryRowContext(ctx, sqlConnectionID).Scan(&self))
			rows, err := c.QueryContext(ctx, sqlProcesslistFallback)
			require.NoError(t, err)
			out, err = scanProcesses(rows, self)
			return err
		}))
		var seen bool
		for _, x := range out {
			require.NotContains(t, x.Info, "SHOW FULL PROCESSLIST")
			seen = seen || strings.Contains(x.Info, "dbacus_marker")
		}
		require.True(t, seen)
	})
}

func testGlobalStatus(t *testing.T, in mysqlbox.Instance) {
	act := activity(t, openPool(t, in, nil))
	got, err := act.GlobalStatus(context.Background(), []string{"Uptime", "Threads_connected", "Questions", "Com_select", "Ssl_version", "Variable_que_no_existe"})
	require.NoError(t, err)
	require.Positive(t, got["Uptime"])
	require.Positive(t, got["Threads_connected"])
	require.Contains(t, got, "Questions")
	require.NotContains(t, got, "Variable_que_no_existe", "una variable ausente no es un error")

	_, err = act.GlobalStatus(context.Background(), []string{"Uptime'; DROP TABLE x; --"})
	require.Error(t, err)

	// max_connections: el máximo con el que se compara Threads_connected (tarjeta «conexiones/máximo»).
	max, err := act.MaxConnections(context.Background())
	require.NoError(t, err)
	require.Positive(t, max)
	require.GreaterOrEqual(t, max, got["Threads_connected"])
}

func testDigests(t *testing.T, in mysqlbox.Instance, maria bool) {
	root := openPool(t, in, nil)
	act := activity(t, root)
	ctx := context.Background()
	if maria {
		// Con performance_schema apagado las tablas existen pero no acumulan: lo que importa es que no sea un error
		// de la llamada y que Probe lo explique (ver «capacidades de root»).
		_, err := act.Digests(ctx)
		require.NoError(t, err)
		return
	}
	for i := 0; i < 3; i++ {
		execAll(t, root, "SELECT 424242 AS dbacus_marker_x")
	}
	ds, err := act.Digests(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, ds)
	digests := make([]string, 0, len(ds))
	for _, d := range ds {
		if d.Digest != "" {
			digests = append(digests, d.Digest)
		}
	}
	// Más de un lote (digestTextBatch = 200) con digests inexistentes mezclados.
	query := append([]string{}, digests...)
	for i := 0; len(query) < 450; i++ {
		query = append(query, fmt.Sprintf("%064x", i))
	}
	texts, err := act.DigestTexts(ctx, query)
	require.NoError(t, err)
	var marker *model.DigestCounters
	for i, d := range ds {
		if strings.Contains(texts[d.Digest], "dbacus_marker_x") {
			marker = &ds[i]
		}
	}
	require.NotNil(t, marker, "el digest de la consulta de prueba")
	require.GreaterOrEqual(t, marker.Execs, uint64(3))
	require.Positive(t, marker.TimerPs)
	require.NotContains(t, texts[marker.Digest], "424242", "el digest está normalizado: sin literales")
	require.Contains(t, texts[marker.Digest], "?")
	for _, txt := range texts {
		require.LessOrEqual(t, len(txt), digestTextMaxBytes)
	}
	empty, err := act.DigestTexts(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func testMonReadOnly(t *testing.T, in mysqlbox.Instance) {
	root := openPool(t, in, nil) // perfil de lectura-escritura
	execAll(t, root, "CREATE TABLE testdb.mon_ro (a INT)")
	err := root.(*pool).withMon(context.Background(), func(ctx context.Context, c *sql.Conn) error {
		_, err := c.ExecContext(ctx, "INSERT INTO testdb.mon_ro VALUES (1)")
		return err
	})
	require.Error(t, err, "la conexión del monitor no puede escribir aunque el perfil sí")
	c, err := root.Acquire(context.Background())
	require.NoError(t, err)
	defer c.Release()
	r, err := c.Run(context.Background(), "SELECT COUNT(*) FROM testdb.mon_ro")
	require.NoError(t, err)
	require.EqualValues(t, "0", fmt.Sprint(collect(t, r)[0][0]))
}

func testUint64Scan(t *testing.T, in mysqlbox.Instance) {
	var got uint64
	require.NoError(t, openPool(t, in, nil).(*pool).withMon(context.Background(), func(ctx context.Context, c *sql.Conn) error {
		return c.QueryRowContext(ctx, "SELECT CAST(18446744073709551615 AS UNSIGNED)").Scan(&got)
	}))
	require.Equal(t, uint64(math.MaxUint64), got)
}

// testMonitorCost mide lo que cuesta cada lectura del monitor (spec 22 «Estimaciones no verificadas»). Se ejecuta con
// DBACUS_BENCH_MONITOR=1 y escribe las cifras en el log del test.
func testMonitorCost(t *testing.T, in mysqlbox.Instance) {
	root := openPool(t, in, nil)
	act := activity(t, root)
	ctx := context.Background()

	var size int
	c, err := root.Acquire(ctx)
	require.NoError(t, err)
	r, err := c.Run(ctx, "SELECT @@performance_schema_digests_size")
	require.NoError(t, err)
	fmt.Sscan(fmt.Sprint(collect(t, r)[0][0]), &size)
	// Digests distintos baratos: el alias forma parte del digest.
	start := time.Now()
	for i := 0; i < 12000; i++ {
		r, err := c.Run(ctx, fmt.Sprintf("SELECT %d AS dbacus_cost_%d", i, i))
		require.NoError(t, err)
		_ = r.Close()
	}
	t.Logf("%s: digests_size=%d; 12000 sentencias distintas en %s", in.Image, size, time.Since(start).Round(time.Millisecond))
	require.NoError(t, c.Release())

	measure := func(name string, fn func() (int, error)) {
		var tot, worst time.Duration
		var n int
		const runs = 7
		for i := 0; i < runs; i++ {
			s := time.Now()
			var err error
			n, err = fn()
			require.NoError(t, err)
			d := time.Since(s)
			tot += d
			worst = max(worst, d)
		}
		t.Logf("%s: %s -> %d filas, media %s, peor %s (%d lecturas)", in.Image, name, n, (tot / runs).Round(100*time.Microsecond), worst.Round(100*time.Microsecond), runs)
	}
	var all []model.DigestCounters
	measure("Digests (sin texto)", func() (int, error) {
		var err error
		all, err = act.Digests(ctx)
		return len(all), err
	})
	var overflow int
	for _, d := range all {
		if d.Digest == "" {
			overflow++
		}
	}
	t.Logf("%s: fila de desbordamiento (DIGEST NULL): %d", in.Image, overflow)
	var hashes []string
	for _, d := range all {
		if d.Digest != "" {
			hashes = append(hashes, d.Digest)
		}
	}
	measure("DigestTexts de todos", func() (int, error) { m, err := act.DigestTexts(ctx, hashes); return len(m), err })
	measure("DigestTexts de 20 nuevos", func() (int, error) { m, err := act.DigestTexts(ctx, hashes[:20]); return len(m), err })
	measure("GlobalStatus (19 variables)", func() (int, error) {
		m, err := act.GlobalStatus(ctx, []string{"Threads_connected", "Threads_running", "Questions", "Com_select", "Com_insert", "Com_update", "Com_delete",
			"Slow_queries", "Aborted_connects", "Aborted_clients", "Innodb_row_lock_waits", "Innodb_row_lock_time", "Innodb_buffer_pool_reads",
			"Innodb_buffer_pool_read_requests", "Created_tmp_tables", "Created_tmp_disk_tables", "Select_full_join", "Bytes_received", "Uptime"})
		return len(m), err
	})

	// Processlist con ~140 conexiones abiertas (max_connections por defecto: 151).
	var held []driver.Conn
	var mu sync.Mutex
	for i := 0; i < 6; i++ {
		p := openPool(t, in, nil)
		for j := 0; j < 23; j++ {
			cc, err := p.Acquire(ctx)
			require.NoError(t, err)
			mu.Lock()
			held = append(held, cc)
			mu.Unlock()
		}
	}
	defer func() {
		for _, h := range held {
			_ = h.Release()
		}
	}()
	measure(fmt.Sprintf("Processes con %d conexiones", len(held)), func() (int, error) { ps, err := act.Processes(ctx); return len(ps), err })
}
