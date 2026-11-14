package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

func (e *env) planObjects(t *testing.T, obj model.MigrationObjects, opts model.MigrationOptions) (*Prepared, error) {
	t.Helper()
	return Plan(context.Background(), PlanDeps{Source: e.srcPool, Target: e.dstPool, SameSession: e.same},
		model.MigrationRequest{Source: model.MigrationEndpoint{Database: "src"}, Target: model.MigrationEndpoint{Database: "dst"}, Objects: obj, Options: opts})
}

func TestPlanStructureOnlyMustBeAmongSelectedTables(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	_, err := e.planObjects(t, model.MigrationObjects{Tables: []string{"orders"}, StructureOnly: []string{"customers"}}, model.MigrationOptions{})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "objects", ae.Detail)
	require.Contains(t, ae.Message, "customers")
}

func TestPlanStructureOnlyDescribesTheTable(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	p, err := e.planObjects(t, model.MigrationObjects{StructureOnly: []string{"customers"}}, model.MigrationOptions{})
	require.NoError(t, err)
	var got model.PlanTable
	for _, pt := range p.Plan.Tables {
		if pt.Name == "customers" {
			got = pt
		}
	}
	require.True(t, got.StructureOnly)
	require.Zero(t, got.EstRows)
	require.Zero(t, got.Chunks)
	var warn bool
	for _, w := range p.Plan.Warnings {
		warn = warn || (w.Code == "fk_structure_only" && w.Object == "orders.fk_cust")
	}
	require.True(t, warn, "orders (con datos) apunta a una tabla que se crea vacía: %+v", p.Plan.Warnings)

	// Con el mismo plan cambia el hash si cambia qué tablas llevan datos.
	q, err := e.planObjects(t, model.MigrationObjects{}, model.MigrationOptions{})
	require.NoError(t, err)
	require.NotEqual(t, p.Plan.PlanHash, q.Plan.PlanHash)
}

func TestMigrateStructureOnlyCreatesEmptyTable(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	with := tbl("counters")
	with.CreateSQL = "CREATE TABLE `counters` AUTO_INCREMENT=42" // el motor simulado solo entiende «CREATE TABLE `nombre`»: si no se quita la opción, la tabla no se registra
	with.AutoIncColumn, with.AutoIncrement = "id", 42
	e.src.AddTable("src", with, 9)

	p, err := e.planObjects(t, model.MigrationObjects{StructureOnly: []string{"customers", "counters"}}, model.MigrationOptions{Workers: 3, VerifyRowCounts: true})
	require.NoError(t, err)
	for n := range p.ChunkRows {
		p.ChunkRows[n] = 10
	}
	j := runJob(t, p, Callbacks{})
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)

	require.Zero(t, e.src.RowCount("dst", "customers"))
	require.Zero(t, e.src.RowCount("dst", "counters"))
	require.Equal(t, 95, e.src.RowCount("dst", "orders"))
	require.Equal(t, 7, e.src.RowCount("dst", "log"))
	require.EqualValues(t, 102, r.Job.Rows)
	require.EqualValues(t, 102, r.Job.EstRows, "las tablas vacías no suman al total esperado")
	require.Equal(t, 4, r.Job.TablesDone)
	for _, tb := range r.Job.Tables {
		require.Equal(t, tb.Name == "customers" || tb.Name == "counters", tb.StructureOnly, tb.Name)
	}
	for _, l := range e.src.Log() {
		require.False(t, strings.HasPrefix(l, "insert:dst.customers") || strings.HasPrefix(l, "insert:dst.counters"), l)
		require.NotContains(t, l, "AUTO_INCREMENT=42", "una tabla vacía empieza en 1")
		require.NotContains(t, l, "autoinc:dst.counters", "no se fija el contador del origen")
	}

	v, err := j.Verify(context.Background(), model.VerifyOptions{RowCounts: true})
	require.NoError(t, err)
	require.Empty(t, v.RowCounts, "una tabla vacía a propósito no es una diferencia")
}
