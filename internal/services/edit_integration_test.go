//go:build integration

package services

import (
	"context"
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
