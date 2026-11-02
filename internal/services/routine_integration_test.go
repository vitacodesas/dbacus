//go:build integration

package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

type routineBox struct {
	t    *testing.T
	sess *session.Session
	svc  *RoutineService
	hist []model.HistoryEntry
}

func newRoutineBox(t *testing.T, in mysqlbox.Instance, readOnly bool) *routineBox {
	t.Helper()
	sessions := session.NewManager(session.Options{})
	t.Cleanup(sessions.CloseAll)
	prof := model.ConnectionProfile{ID: "p", Driver: "mysql", Host: in.Host, Port: in.Port, User: mysqlbox.User, Database: mysqlbox.Database, ReadOnly: readOnly, TLS: model.TLSConfig{Mode: "preferred"}}
	s, err := sessions.Open(context.Background(), session.OpenRequest{Profile: prof, Password: mysqlbox.Password})
	require.NoError(t, err)
	b := &routineBox{t: t, sess: s}
	b.svc = NewRoutineService(RoutineDeps{Sessions: sessions, Schema: NewSchemaService(sessions, nil), Store: altStore{prof},
		History: func(h model.HistoryEntry) { b.hist = append(b.hist, h) }})
	return b
}

func (b *routineBox) sql(stmt string) {
	b.t.Helper()
	c, err := b.sess.AcquireConn(context.Background(), "setup")
	require.NoError(b.t, err)
	r, err := c.Run(context.Background(), stmt)
	require.NoError(b.t, err, stmt)
	require.NoError(b.t, r.Close())
}

func (b *routineBox) scalar(stmt string) string {
	b.t.Helper()
	c, err := b.sess.AcquireConn(context.Background(), "setup")
	require.NoError(b.t, err)
	r, err := c.Run(context.Background(), stmt)
	require.NoError(b.t, err, stmt)
	defer r.Close()
	if !r.Next() {
		return ""
	}
	switch v := r.Row()[0].(type) {
	case []byte:
		return string(v)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (b *routineBox) get(kind, name string) model.RoutineDetail {
	b.t.Helper()
	d, err := b.svc.Get(context.Background(), b.sess.ID, mysqlbox.Database, kind, name)
	require.NoError(b.t, err)
	return d
}

func (b *routineBox) apply(ch model.RoutineChange) model.RoutineResult {
	b.t.Helper()
	ctx := context.Background()
	p, err := b.svc.Preview(ctx, b.sess.ID, ch)
	require.NoError(b.t, err)
	res, err := b.svc.Apply(ctx, b.sess.ID, ch, p.SQLHash)
	require.NoError(b.t, err)
	return res
}

func forEachRoutineImage(t *testing.T, fn func(t *testing.T, in mysqlbox.Instance, b *routineBox)) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			fn(t, in, newRoutineBox(t, in, false))
		})
	}
}

func TestRoutinesFiveHundredIntegration(t *testing.T) {
	forEachRoutineImage(t, func(t *testing.T, _ mysqlbox.Instance, b *routineBox) {
		for i := 0; i < 500; i++ {
			create := fmt.Sprintf("CREATE PROCEDURE r%03d() BEGIN SELECT %d; END", i, i)
			if i%2 == 1 {
				create = fmt.Sprintf("CREATE FUNCTION r%03d() RETURNS INT DETERMINISTIC RETURN %d", i, i)
			}
			b.sql(create)
		}
		start := time.Now()
		l, err := b.svc.List(context.Background(), b.sess.ID, mysqlbox.Database)
		took := time.Since(start)
		require.NoError(t, err)
		require.Len(t, l, 500)
		require.Less(t, took, time.Second, "listar 500 rutinas")
		t.Logf("listar 500 rutinas: %s", took)
		require.Equal(t, "r000", l[0].Name)
		require.Equal(t, model.KindProcedure, l[0].Kind)
		require.Equal(t, model.KindFunction, l[1].Kind)
	})
}

func TestRoutineReplacePreservesPropertiesIntegration(t *testing.T) {
	forEachRoutineImage(t, func(t *testing.T, _ mysqlbox.Instance, b *routineBox) {
		b.sql("SET SESSION sql_mode = 'ANSI_QUOTES,STRICT_ALL_TABLES'")
		b.sql("CREATE PROCEDURE p(IN a INT, OUT b VARCHAR(20)) COMMENT 'mi comentario' SQL SECURITY INVOKER BEGIN SET b = 'x'; SELECT 1 AS \"col\"; END")
		b.sql("SET SESSION sql_mode = DEFAULT")

		before := b.get(model.KindProcedure, "p")
		require.Contains(t, before.SQLMode, "ANSI_QUOTES")
		require.Len(t, before.Params, 2)
		require.Equal(t, "OUT", before.Params[1].Mode)

		res := b.apply(model.RoutineChange{Database: mysqlbox.Database, Kind: model.KindProcedure, Name: "p", Op: model.RoutineReplace,
			Fingerprint: before.Fingerprint,
			SQL:         "CREATE PROCEDURE p(IN a INT, OUT b VARCHAR(20)) COMMENT 'mi comentario' SQL SECURITY INVOKER BEGIN SET b = 'y'; SELECT 1 AS \"col\"; END"})
		require.Nil(t, res.Failed, "%v", res.Failed)
		require.False(t, res.Restored)
		require.Len(t, res.Applied, 2)
		after := res.Detail
		require.NotNil(t, after)
		require.Equal(t, before.SQLMode, after.SQLMode, "el sql_mode de la rutina sobrevive")
		require.Equal(t, "mi comentario", after.Comment)
		require.Equal(t, "INVOKER", after.SecurityType)
		require.Contains(t, after.CreateSQL, `'y'`)
		require.Equal(t, before.CollationConnection, after.CollationConnection)
		require.Equal(t, before.Definer, after.Definer)

		l, err := b.svc.List(context.Background(), b.sess.ID, mysqlbox.Database)
		require.NoError(t, err)
		for _, r := range l {
			require.False(t, strings.HasPrefix(r.Name, tmpPrefix), "no queda ninguna rutina temporal")
		}
		require.Len(t, b.hist, 2)
	})
}

func TestRoutineBadSyntaxLeavesOriginalIntegration(t *testing.T) {
	forEachRoutineImage(t, func(t *testing.T, _ mysqlbox.Instance, b *routineBox) {
		b.sql("CREATE FUNCTION f() RETURNS INT DETERMINISTIC RETURN 7")
		before := b.get(model.KindFunction, "f")
		res := b.apply(model.RoutineChange{Database: mysqlbox.Database, Kind: model.KindFunction, Name: "f", Op: model.RoutineReplace,
			SQL: "CREATE FUNCTION f() RETURNS INT DETERMINISTIC RETURN SELEC 1"})
		require.NotNil(t, res.Failed)
		require.Equal(t, 1, res.Failed.Index, "falla en la pre-validación, antes de borrar nada")
		require.Empty(t, res.Applied)
		require.Equal(t, before.CreateSQL, b.get(model.KindFunction, "f").CreateSQL)
		require.Equal(t, "7", b.scalar("SELECT f()"))
		l, _ := b.svc.List(context.Background(), b.sess.ID, mysqlbox.Database)
		require.Len(t, l, 1)
	})
}

func TestRoutineRestoreIntegration(t *testing.T) {
	forEachRoutineImage(t, func(t *testing.T, _ mysqlbox.Instance, b *routineBox) {
		b.sql("CREATE FUNCTION f() RETURNS INT DETERMINISTIC RETURN 7")
		before := b.get(model.KindFunction, "f")
		ctx := context.Background()
		ch := model.RoutineChange{Database: mysqlbox.Database, Kind: model.KindFunction, Name: "f", Op: model.RoutineReplace,
			SQL: "CREATE FUNCTION f() RETURNS INT DETERMINISTIC RETURN 8"}
		// Un fallo real del CREATE final no se puede provocar tras una pre-validación correcta: se reproduce el estado
		// posterior al DROP y se comprueba que la copia recrea la original idéntica (los fallos se cubren con el driver falso).
		p2, _ := b.svc.plan(ctx, b.sess.ID, ch)
		conn, err := p2.sess.Pool.AcquireEdit(ctx)
		require.NoError(t, err)
		defer conn.Release()
		require.NoError(t, conn.Use(ctx, mysqlbox.Database))
		_, _, err = conn.Exec(ctx, p2.steps[3].sql) // DROP FUNCTION f
		require.NoError(t, err)
		var res model.RoutineResult
		b.svc.restore(p2, conn, &res)
		require.True(t, res.Restored)
		require.Equal(t, before.CreateSQL, b.get(model.KindFunction, "f").CreateSQL, "la copia recrea la original idéntica")
	})
}

func TestRoutineGrantsAndDefinerIntegration(t *testing.T) {
	forEachRoutineImage(t, func(t *testing.T, _ mysqlbox.Instance, b *routineBox) {
		b.sql("CREATE USER 'lector'@'%' IDENTIFIED BY 'x'")
		b.sql("CREATE DEFINER = 'lector'@'%' PROCEDURE p() SELECT 1")
		b.sql("GRANT EXECUTE ON PROCEDURE " + mysqlbox.Database + ".p TO 'lector'@'%'")
		require.Contains(t, b.scalar("SHOW GRANTS FOR 'lector'@'%'"), "USAGE")

		d := b.get(model.KindProcedure, "p")
		require.Contains(t, d.Definer, "lector")
		p, err := b.svc.Preview(context.Background(), b.sess.ID, model.RoutineChange{Database: mysqlbox.Database, Kind: model.KindProcedure, Name: "p",
			Op: model.RoutineReplace, SQL: d.CreateSQL})
		require.NoError(t, err)
		var codes []string
		for _, w := range p.Warnings {
			codes = append(codes, w.Code)
		}
		require.Contains(t, codes, "definer_foreign")

		grants := func() string {
			c, _ := b.sess.AcquireConn(context.Background(), "setup")
			r, err := c.Run(context.Background(), "SHOW GRANTS FOR 'lector'@'%'")
			require.NoError(t, err)
			defer r.Close()
			var out []string
			for r.Next() {
				out = append(out, fmt.Sprint(string(r.Row()[0].([]byte))))
			}
			return strings.Join(out, "\n")
		}
		require.Contains(t, grants(), "PROCEDURE", "el GRANT existe antes de editar")
		res := b.apply(model.RoutineChange{Database: mysqlbox.Database, Kind: model.KindProcedure, Name: "p", Op: model.RoutineReplace, SQL: d.CreateSQL})
		require.Nil(t, res.Failed, "%v", res.Failed)
		t.Logf("grants tras editar: %s", grants())
		require.NotContains(t, grants(), "PROCEDURE", "DROP elimina los GRANT de la rutina (spec 21)")
	})
}

func TestRoutineReadOnlyServerLayerIntegration(t *testing.T) {
	forEachRoutineImage(t, func(t *testing.T, in mysqlbox.Instance, w *routineBox) {
		w.sql("CREATE PROCEDURE p() SELECT 1")
		b := newRoutineBox(t, in, true)
		l, err := b.svc.List(context.Background(), b.sess.ID, mysqlbox.Database)
		require.NoError(t, err)
		require.Len(t, l, 1)
		_, err = b.svc.Get(context.Background(), b.sess.ID, mysqlbox.Database, model.KindProcedure, "p")
		require.NoError(t, err)
		_, err = b.svc.Preview(context.Background(), b.sess.ID, model.RoutineChange{Database: mysqlbox.Database, Kind: model.KindProcedure, Name: "p", Op: model.RoutineDrop})
		require.Equal(t, "readonly", detail(t, err))
		// Aunque se salte el guardia de Go, la conexión de solo lectura no ejecuta el DROP.
		conn, err := b.sess.Pool.AcquireEdit(context.Background())
		require.NoError(t, err)
		defer conn.Release()
		_, _, err = conn.Exec(context.Background(), "DROP PROCEDURE p")
		require.Error(t, err)
	})
}
