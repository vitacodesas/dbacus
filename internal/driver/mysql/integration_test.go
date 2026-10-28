//go:build integration

package mysql

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver"
	"tabulariswails/internal/model"
	"tabulariswails/internal/testutil/mysqlbox"
)

func openPool(t *testing.T, in mysqlbox.Instance, mut func(*driver.ConnConfig)) driver.Pool {
	t.Helper()
	cfg := driver.ConnConfig{
		Profile:  model.ConnectionProfile{Host: in.Host, Port: in.Port, User: mysqlbox.User, Database: mysqlbox.Database, TLS: model.TLSConfig{Mode: "preferred"}},
		Password: mysqlbox.Password,
	}
	if mut != nil {
		mut(&cfg)
	}
	p, err := (&Driver{}).Open(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func collect(t *testing.T, r driver.RunResult) [][]any {
	t.Helper()
	defer r.Close()
	var rows [][]any
	for r.Next() {
		rows = append(rows, r.Row())
	}
	require.NoError(t, r.Err())
	return rows
}

func TestIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			t.Run("ping y versión", func(t *testing.T) { testPing(t, in) })
			t.Run("credenciales inválidas", func(t *testing.T) { testBadAuth(t, in) })
			t.Run("base inexistente", func(t *testing.T) { testUnknownDB(t, in) })
			t.Run("tls", func(t *testing.T) { testTLSModes(t, in) })
			t.Run("consultas y tipos", func(t *testing.T) { testTypes(t, in) })
			t.Run("exec", func(t *testing.T) { testExec(t, in) })
			t.Run("sesión por conexión", func(t *testing.T) { testSessionIsolation(t, in) })
			t.Run("kill conserva la conexión", func(t *testing.T) { testKill(t, in) })
			t.Run("exec parametrizado y transacciones", func(t *testing.T) { testExecParams(t, in) })
			t.Run("sintaxis", func(t *testing.T) { testSyntax(t, in) })
			t.Run("esquema", func(t *testing.T) { testSchema(t, in) })
		})
	}
}

func testPing(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, nil)
	ctx := context.Background()
	require.NoError(t, p.Ping(ctx))
	v, err := p.ServerVersion(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, v)
}

func testBadAuth(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, func(c *driver.ConnConfig) { c.Password = "mala" })
	require.Equal(t, apperr.AuthFailed, code(t, p.Ping(context.Background())))
}

func testUnknownDB(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, func(c *driver.ConnConfig) { c.Profile.Database = "no_existe" })
	require.Equal(t, apperr.Validation, code(t, p.Ping(context.Background())))
}

func testTLSModes(t *testing.T, in mysqlbox.Instance) {
	// La imagen oficial de MariaDB no activa TLS: "required" debe fallar con TLS_ERROR en vez de caer a texto plano.
	noTLS := strings.HasPrefix(in.Image, "mariadb")
	for _, mode := range []string{"disable", "preferred", "required"} {
		p := openPool(t, in, func(c *driver.ConnConfig) { c.Profile.TLS.Mode = mode })
		err := p.Ping(context.Background())
		if noTLS && mode == "required" {
			require.Equal(t, apperr.TLSError, code(t, err), mode)
			continue
		}
		require.NoError(t, err, mode)
	}
	// El certificado autofirmado del contenedor no pasa la verificación completa.
	p := openPool(t, in, func(c *driver.ConnConfig) { c.Profile.TLS.Mode = "verify-full" })
	require.Equal(t, apperr.TLSError, code(t, p.Ping(context.Background())))
}

func testTypes(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, nil)
	ctx := context.Background()
	c, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer c.Release()

	r, err := c.Run(ctx, `SELECT 18446744073709551615 AS big, CAST('12.340' AS DECIMAL(10,3)) AS dec_, NULL AS nul,
		'😀 ñ' AS txt, CAST(1 AS UNSIGNED) AS u, 1.5e0 AS f, CAST('2024-01-02 03:04:05.123456' AS DATETIME(6)) AS dt, X'00FF' AS bin`)
	require.NoError(t, err)
	cols := r.Columns()
	require.Len(t, cols, 8)
	require.Equal(t, "big", cols[0].Name)
	require.NotNil(t, cols[1].Precision)
	rows := collect(t, r)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Nil(t, row[2])
	require.Equal(t, "😀 ñ", string(row[3].([]byte)))
	require.Equal(t, []byte{0x00, 0xFF}, row[7])
}

func testExec(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, nil)
	ctx := context.Background()
	c, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer c.Release()

	for _, s := range []string{"DROP TABLE IF EXISTS t_exec", "CREATE TABLE t_exec (id INT AUTO_INCREMENT PRIMARY KEY, v INT)"} {
		r, err := c.Run(ctx, s)
		require.NoError(t, err)
		require.Nil(t, r.Columns())
		require.NoError(t, r.Close())
	}
	r, err := c.Run(ctx, "INSERT INTO t_exec (v) VALUES (1),(2),(3)")
	require.NoError(t, err)
	rows, last, _ := r.Affected()
	require.EqualValues(t, 3, rows)
	require.EqualValues(t, 1, last)
	r, err = c.Run(ctx, "UPDATE t_exec SET v = v + 1 WHERE v > 1")
	require.NoError(t, err)
	rows, _, _ = r.Affected()
	require.EqualValues(t, 2, rows)
}

func testSessionIsolation(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, nil)
	ctx := context.Background()
	a, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer a.Release()
	b, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer b.Release()
	require.NotEqual(t, a.ID(), b.ID())

	r, err := a.Run(ctx, "SET @x = 42")
	require.NoError(t, err)
	_ = r.Close()
	r, err = a.Run(ctx, "SELECT @x")
	require.NoError(t, err)
	require.EqualValues(t, 42, collect(t, r)[0][0], "mismo hilo conserva @x")
	r, err = b.Run(ctx, "SELECT @x")
	require.NoError(t, err)
	require.Nil(t, collect(t, r)[0][0], "otra conexión no lo ve")
}

func testKill(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, nil)
	ctx := context.Background()
	c, err := p.Acquire(ctx)
	require.NoError(t, err)
	defer c.Release()
	r, err := c.Run(ctx, "SET @keep = 7")
	require.NoError(t, err)
	_ = r.Close()

	done := make(chan error, 1)
	go func() {
		r, err := c.Run(ctx, "SELECT SLEEP(60)")
		if err == nil {
			for r.Next() {
			}
			err = r.Err()
			_ = r.Close()
		}
		done <- err
	}()
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	require.NoError(t, c.Kill(ctx))

	select {
	case err := <-done:
		// MySQL devuelve SLEEP()=1 sin error al interrumpirse; MariaDB/otros pueden devolver 1317. Ambos son válidos.
		if err != nil {
			require.Equal(t, apperr.QueryCanceled, code(t, err))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("la consulta no terminó tras Kill")
	}
	require.Less(t, time.Since(start), 3*time.Second)

	r, err = c.Run(ctx, "SELECT @keep")
	require.NoError(t, err, "la conexión sigue viva")
	require.EqualValues(t, 7, collect(t, r)[0][0])
}

func testSyntax(t *testing.T, in mysqlbox.Instance) {
	p := openPool(t, in, nil)
	c, err := p.Acquire(context.Background())
	require.NoError(t, err)
	defer c.Release()
	_, err = c.Run(context.Background(), "SELEC 1")
	require.Equal(t, apperr.QuerySyntax, code(t, err))
}

func cellStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "<NULL>"
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

// testExecParams cubre Conn.Exec (spec 13): parámetros sin interpolar, valores extremos, NULL, filas afectadas y
// transacciones (commit, rollback, Release con la transacción abierta).
func testExecParams(t *testing.T, in mysqlbox.Instance) {
	ctx := context.Background()
	p := openPool(t, in, nil)
	c, err := p.Acquire(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Release() })

	run := func(stmt string) [][]any {
		t.Helper()
		r, err := c.Run(ctx, stmt)
		require.NoError(t, err, stmt)
		return collect(t, r)
	}
	one := func(stmt string) []string {
		t.Helper()
		rows := run(stmt)
		require.Len(t, rows, 1, stmt)
		out := make([]string, len(rows[0]))
		for i, v := range rows[0] {
			out[i] = cellStr(v)
		}
		return out
	}
	run("DROP TABLE IF EXISTS ex_p")
	run("CREATE TABLE ex_p (id BIGINT UNSIGNED PRIMARY KEY, s VARCHAR(100) NULL, d DECIMAL(20,4) NULL, dt DATETIME(6) NULL, b BLOB NULL) DEFAULT CHARSET=utf8mb4")

	t.Run("valores extremos sin pérdida", func(t *testing.T) {
		n, err := c.Exec(ctx, "INSERT INTO ex_p VALUES (?, ?, ?, ?, ?)", uint64(math.MaxUint64), "emoji 😀 'comilla' \\ \"doble\"", "12345678901234.5678", "2024-01-02 03:04:05.123456", []byte{0, 0xff, 0x10})
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		got := one("SELECT CAST(id AS CHAR), s, CAST(d AS CHAR), CAST(dt AS CHAR), HEX(b) FROM ex_p")
		require.Equal(t, []string{"18446744073709551615", "emoji 😀 'comilla' \\ \"doble\"", "12345678901234.5678", "2024-01-02 03:04:05.123456", "00FF10"}, got)
	})

	t.Run("el valor nunca se interpreta como SQL", func(t *testing.T) {
		evil := "x'; DROP TABLE ex_p; --"
		n, err := c.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ?", evil, uint64(math.MaxUint64))
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		require.Equal(t, []string{evil}, one("SELECT s FROM ex_p"))
	})

	t.Run("NULL y comparación null-safe", func(t *testing.T) {
		n, err := c.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ?", nil, uint64(math.MaxUint64))
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		require.Equal(t, []string{"<NULL>"}, one("SELECT s FROM ex_p"))
		// `<=>` con NULL encuentra la fila; `=` no.
		n, err = c.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ? AND s <=> ?", "ya no es null", uint64(math.MaxUint64), nil)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
	})

	t.Run("filas encontradas aunque el valor no cambie, y cero si no hay fila", func(t *testing.T) {
		// Conexión normal: afectadas = cambiadas (lo que ve el usuario al ejecutar su propio UPDATE).
		n, err := c.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ?", "ya no es null", uint64(math.MaxUint64))
		require.NoError(t, err)
		require.EqualValues(t, 0, n, "sin CLIENT_FOUND_ROWS una fila encontrada pero igual cuenta 0")

		// Conexión de edición: afectadas = encontradas, que es lo que permite el control optimista sin falsos conflictos.
		ec, err := p.AcquireEdit(ctx)
		require.NoError(t, err)
		defer ec.Release()
		n, err = ec.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ?", "ya no es null", uint64(math.MaxUint64))
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		n, err = ec.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ?", "x", uint64(7))
		require.NoError(t, err)
		require.EqualValues(t, 0, n, "sin fila, cero")
	})

	t.Run("error del servidor", func(t *testing.T) {
		_, err := c.Exec(ctx, "UPDATE no_existe SET a = ?", 1)
		require.Error(t, err)
		var ae *apperr.AppError
		require.ErrorAs(t, err, &ae)
		require.Equal(t, apperr.QueryFailed, ae.Code)
	})

	t.Run("transacciones", func(t *testing.T) {
		other, err := p.Acquire(ctx)
		require.NoError(t, err)
		defer other.Release()
		val := func(cn driver.Conn) string {
			r, err := cn.Run(ctx, "SELECT s FROM ex_p")
			require.NoError(t, err)
			rows := collect(t, r)
			return cellStr(rows[0][0])
		}
		id := uint64(math.MaxUint64)

		require.NoError(t, c.Begin(ctx))
		require.Error(t, c.Begin(ctx), "no se anidan")
		_, err = c.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ?", "dentro", id)
		require.NoError(t, err)
		require.Equal(t, "ya no es null", val(other), "otra conexión no ve lo no confirmado")
		require.NoError(t, c.Rollback(ctx))
		require.Equal(t, "ya no es null", val(c))

		require.NoError(t, c.Begin(ctx))
		_, err = c.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ?", "confirmado", id)
		require.NoError(t, err)
		require.NoError(t, c.Commit(ctx))
		require.Equal(t, "confirmado", val(other))
		require.Error(t, c.Commit(ctx), "commit sin transacción")
		require.NoError(t, c.Rollback(ctx), "rollback sin transacción es inocuo")

		// Una transacción olvidada se revierte al liberar la conexión.
		third, err := p.Acquire(ctx)
		require.NoError(t, err)
		require.NoError(t, third.Begin(ctx))
		_, err = third.Exec(ctx, "UPDATE ex_p SET s = ? WHERE id = ?", "olvidado", id)
		require.NoError(t, err)
		require.NoError(t, third.Release())
		require.Equal(t, "confirmado", val(other))
	})
}
