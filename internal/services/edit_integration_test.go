//go:build integration

package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/edit"
	"tabulariswails/internal/testutil/mysqlbox"
)

// La elegibilidad depende de metadatos reales: columnas generadas, índices con prefijo, nulabilidad y vistas.
func TestEditEligibilityIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			svc := NewEditService(EditDeps{Sessions: e.q.d.Sessions, Results: e.q.d.Results, Schema: e.q.d.Schema, Store: &memStore{}})
			for _, ddl := range []string{
				"CREATE TABLE ed_pk (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT NULL, base INT NULL, dbl INT AS (base * 2) VIRTUAL, ts TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP, raw BLOB NULL)",
				"CREATE TABLE ed_comp (a INT NOT NULL, b INT NOT NULL, v VARCHAR(10), PRIMARY KEY (a, b))",
				"CREATE TABLE ed_uq (code VARCHAR(20) NOT NULL, v INT, UNIQUE KEY uq_code (code))",
				"CREATE TABLE ed_uq_prefix (code VARCHAR(20) NOT NULL, v INT, UNIQUE KEY uq_code (code(5)))",
				"CREATE TABLE ed_uq_null (code VARCHAR(20) NULL, v INT, UNIQUE KEY uq_code (code))",
				"CREATE TABLE ed_nokey (v INT)",
				"CREATE TABLE ed_tmp (id INT PRIMARY KEY)",
				"CREATE VIEW ed_view AS SELECT id, name FROM ed_pk",
			} {
				e.mustRun(t, "ddl", ddl)
			}

			info := func(sql string) edit.Info {
				t.Helper()
				qid, _ := e.mustRun(t, "ed", sql)
				i, err := svc.GetEditInfo(context.Background(), qid, 0)
				require.NoError(t, err)
				return i
			}

			i := info("SELECT * FROM ed_pk")
			require.True(t, i.Editable, i.Reason)
			require.Equal(t, mysqlbox.Database, i.Database)
			require.Equal(t, []int{0}, i.KeyColumns)
			want := map[string]string{"id": edit.ColKey, "name": "", "note": "", "base": "", "dbl": edit.ColGenerated, "ts": "", "raw": edit.ColBinary}
			for idx, name := range []string{"id", "name", "note", "base", "dbl", "ts", "raw"} {
				require.Equal(t, want[name], i.Columns[idx].Reason, name)
			}

			require.True(t, info("SELECT name, id FROM ed_pk WHERE id > 0 ORDER BY id LIMIT 5").Editable)
			require.Equal(t, edit.ReasonKeyNotSelected, info("SELECT name FROM ed_pk").Reason)
			require.Equal(t, edit.ReasonNoSingleTable, info("SELECT * FROM ed_pk a JOIN ed_comp b ON a.id = b.a").Reason)

			c := info("SELECT * FROM ed_comp")
			require.True(t, c.Editable, c.Reason)
			require.Equal(t, []int{0, 1}, c.KeyColumns)
			require.Equal(t, edit.ReasonKeyNotSelected, info("SELECT a, v FROM ed_comp").Reason)

			u := info("SELECT * FROM ed_uq")
			require.True(t, u.Editable, u.Reason)
			require.Equal(t, []int{0}, u.KeyColumns)
			require.Equal(t, edit.ReasonNoKey, info("SELECT * FROM ed_uq_prefix").Reason, "un índice único por prefijo no identifica la fila")
			require.Equal(t, edit.ReasonNoKey, info("SELECT * FROM ed_uq_null").Reason, "un único sobre columna NULL no identifica la fila")
			require.Equal(t, edit.ReasonNoKey, info("SELECT * FROM ed_nokey").Reason)
			require.Equal(t, edit.ReasonView, info("SELECT * FROM ed_view").Reason)

			// La tabla desaparece después de ejecutar la consulta (otra sesión hizo DROP): no se puede editar un resultado huérfano.
			qid, _ := e.mustRun(t, "ed", "SELECT * FROM ed_tmp")
			e.mustRun(t, "ddl", "DROP TABLE ed_tmp")
			gone, err := svc.GetEditInfo(context.Background(), qid, 0)
			require.NoError(t, err)
			require.Equal(t, edit.ReasonTableNotFound, gone.Reason)
		})
	}
}

// TestEditApplyIntegration prueba el ciclo completo contra servidores reales: tipos extremos, claves BIGINT UNSIGNED y
// compuestas, triggers, conflictos atómicos y filas borradas.
func TestEditApplyIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			svc := NewEditService(EditDeps{Sessions: e.q.d.Sessions, Results: e.q.d.Results, Schema: e.q.d.Schema, Store: &memStore{}})
			ctx := context.Background()
			for _, ddl := range []string{
				`CREATE TABLE ed_a (
					id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
					name VARCHAR(20) NOT NULL,
					note TEXT NULL,
					price DECIMAL(12,4) NULL,
					big BIGINT UNSIGNED NULL,
					at DATETIME(6) NULL,
					flag TINYINT(1) NULL,
					ts TIMESTAMP NULL DEFAULT NULL,
					touched VARCHAR(20) NULL
				) DEFAULT CHARSET=utf8mb4`,
				`CREATE TRIGGER ed_a_bu BEFORE UPDATE ON ed_a FOR EACH ROW SET NEW.touched = 'por-trigger'`,
				`INSERT INTO ed_a (id, name, note, price, big, at, flag, ts) VALUES
					(1, 'uno', NULL, 1.5, 1, '2024-01-01 00:00:00.000001', 0, '2024-01-01 00:00:00'),
					(2, 'dos', 'nota', 2.25, NULL, NULL, NULL, NULL),
					(18446744073709551615, 'maximo', 'x', NULL, 18446744073709551615, NULL, 1, NULL)`,
				`CREATE TABLE ed_c (a INT NOT NULL, b VARCHAR(10) NOT NULL, v VARCHAR(30) NULL, PRIMARY KEY (a, b))`,
				`INSERT INTO ed_c VALUES (1, 'x', 'a'), (1, 'y', 'b'), (2, 'x', 'c')`,
			} {
				e.mustRun(t, "ddl", ddl)
			}
			open := func(sql string) string {
				qid, _ := e.mustRun(t, "ed", sql)
				return qid
			}
			stage := func(qid string, row, col int, v edit.Value) {
				t.Helper()
				_, err := svc.Stage(ctx, qid, 0, row, col, v)
				require.NoError(t, err)
			}
			db := func(sql string) []any {
				t.Helper()
				qid, _ := e.mustRun(t, "chk", sql)
				w, err := e.q.GetRows(qid, 0, 0, 100)
				require.NoError(t, err)
				var out []any
				for _, r := range w.Rows {
					for _, c := range r {
						if c.V == nil {
							out = append(out, nil)
						} else {
							out = append(out, fmt.Sprint(c.V)) // el protocolo de texto puede entregar 0/1 como número o como texto
						}
					}
				}
				return out
			}

			t.Run("tipos extremos sin pérdida y relectura del trigger", func(t *testing.T) {
				qid := open("SELECT * FROM ed_a ORDER BY id")
				// columnas: 0 id, 1 name, 2 note, 3 price, 4 big, 5 at, 6 flag, 7 ts, 8 touched
				stage(qid, 0, 1, edit.Value{Text: "😀 'c' \\ \"d\""})
				stage(qid, 0, 2, edit.Value{Text: ""}) // cadena vacía, no NULL
				stage(qid, 0, 3, edit.Value{Text: "12345678.1234"})
				stage(qid, 0, 4, edit.Value{Text: "18446744073709551615"})
				stage(qid, 0, 5, edit.Value{Text: "2024-02-29 23:59:59.999999"})
				stage(qid, 0, 6, edit.Value{Text: "true"})
				stage(qid, 0, 7, edit.Value{Text: "2024-06-01 10:00:00"})
				stage(qid, 1, 2, edit.Value{Null: true}) // NULL explícito
				stage(qid, 2, 4, edit.Value{Text: "7"})  // fila con clave BIGINT UNSIGNED máxima

				res, err := svc.Apply(ctx, qid, 0)
				require.NoError(t, err)
				require.Nil(t, res.Conflict)
				require.Equal(t, 3, res.Applied)

				got := db("SELECT name, note IS NULL, CAST(price AS CHAR), CAST(big AS CHAR), CAST(at AS CHAR), flag, touched FROM ed_a WHERE id = 1")
				require.Equal(t, []any{"😀 'c' \\ \"d\"", "0", "12345678.1234", "18446744073709551615", "2024-02-29 23:59:59.999999", "1", "por-trigger"}, got)
				require.Equal(t, []any{"1"}, db("SELECT note IS NULL FROM ed_a WHERE id = 2"))
				require.Equal(t, []any{""}, db("SELECT note FROM ed_a WHERE id = 1"), "la cadena vacía no se convirtió en NULL")
				require.Equal(t, []any{"7"}, db("SELECT CAST(big AS CHAR) FROM ed_a WHERE id = 18446744073709551615"))

				// El grid ve el valor que dejó el trigger, no solo lo escrito.
				w, err := e.q.GetRows(qid, 0, 0, 10)
				require.NoError(t, err)
				require.Equal(t, "por-trigger", w.Rows[0][8].V)
				require.False(t, w.Rows[0][1].Edited)
			})

			t.Run("un conflicto en una fila no aplica ninguna", func(t *testing.T) {
				qid := open("SELECT * FROM ed_c ORDER BY a, b")
				stage(qid, 0, 2, edit.Value{Text: "nuevo-1"}) // (1,'x')
				stage(qid, 2, 2, edit.Value{Text: "nuevo-3"}) // (2,'x')
				e.mustRun(t, "other", "UPDATE ed_c SET v = 'otra-sesion' WHERE a = 2 AND b = 'x'")

				res, err := svc.Apply(ctx, qid, 0)
				require.NoError(t, err)
				require.NotNil(t, res.Conflict)
				require.Equal(t, 2, res.Conflict.Row)
				require.Equal(t, edit.ConflictChanged, res.Conflict.Reason)
				require.Equal(t, []any{"a", "b", "otra-sesion"}, db("SELECT v FROM ed_c ORDER BY a, b"), "la fila sin conflicto tampoco se aplicó")
				info, _ := svc.GetEditInfo(ctx, qid, 0)
				require.Equal(t, 2, info.Pending)

				// Descartar y editar de nuevo sobre datos frescos sí funciona.
				_, err = svc.Discard(ctx, qid, 0)
				require.NoError(t, err)
				qid = open("SELECT * FROM ed_c ORDER BY a, b")
				stage(qid, 2, 2, edit.Value{Text: "nuevo-3"})
				res, err = svc.Apply(ctx, qid, 0)
				require.NoError(t, err)
				require.Equal(t, 1, res.Applied)
				require.Equal(t, []any{"nuevo-3"}, db("SELECT v FROM ed_c WHERE a = 2 AND b = 'x'"))
			})

			t.Run("una fila borrada se informa como inexistente", func(t *testing.T) {
				qid := open("SELECT * FROM ed_c ORDER BY a, b")
				stage(qid, 1, 2, edit.Value{Text: "zzz"}) // (1,'y')
				e.mustRun(t, "other", "DELETE FROM ed_c WHERE a = 1 AND b = 'y'")
				res, err := svc.Apply(ctx, qid, 0)
				require.NoError(t, err)
				require.NotNil(t, res.Conflict)
				require.Equal(t, edit.ConflictMissing, res.Conflict.Reason)
			})

			t.Run("la clave compuesta localiza una sola fila", func(t *testing.T) {
				qid := open("SELECT b, a, v FROM ed_c WHERE a = 1 ORDER BY b") // orden de columnas distinto y filtro
				i, err := svc.GetEditInfo(ctx, qid, 0)
				require.NoError(t, err)
				require.True(t, i.Editable, i.Reason)
				stage(qid, 0, 2, edit.Value{Text: "compuesta"})
				res, err := svc.Apply(ctx, qid, 0)
				require.NoError(t, err)
				require.Equal(t, 1, res.Applied)
				require.Equal(t, []any{"compuesta"}, db("SELECT v FROM ed_c WHERE a = 1 AND b = 'x'"))
				require.Equal(t, []any{"nuevo-3"}, db("SELECT v FROM ed_c WHERE a = 2 AND b = 'x'"), "las demás filas no se tocan")
			})

			t.Run("el valor no se interpreta como SQL", func(t *testing.T) {
				qid := open("SELECT * FROM ed_c WHERE a = 2")
				stage(qid, 0, 2, edit.Value{Text: "x'; DROP TABLE ed_c; --"})
				_, err := svc.Apply(ctx, qid, 0)
				require.NoError(t, err)
				require.Equal(t, []any{"x'; DROP TABLE ed_c; --"}, db("SELECT v FROM ed_c WHERE a = 2"))
			})
		})
	}
}
