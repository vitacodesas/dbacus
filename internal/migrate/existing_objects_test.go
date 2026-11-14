package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

// El destino ya tiene una rutina idéntica, una vista distinta y un trigger cuyo código no se pudo leer.
func (e *env) seedExistingObjects() {
	e.src.AddTable("dst", tbl("otra_tabla"), 1)
	d := e.src.DBs["dst"]
	d.Routines = []model.MigrationCode{{Name: "p_count", Kind: "procedure", CreateSQL: "CREATE DEFINER=`admin`@`%` PROCEDURE `p_count`()\n  SELECT COUNT(*)   FROM `dst`.`orders`"}}
	d.Views = []model.MigrationCode{{Name: "v_orders", Kind: "view", CreateSQL: "CREATE VIEW `v_orders` AS select 1 AS `a`"}}
	d.Triggers = []model.MigrationCode{{Name: "trg_orders", Kind: "trigger", Table: "orders", CreateSQL: ""}}
}

func TestPlanRejectsExistingObjectsAndNamesThem(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	e.seedExistingObjects()
	_, err := e.planObjects(t, model.MigrationObjects{}, model.MigrationOptions{})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "exists", ae.Detail)
	for _, n := range []string{"p_count", "v_orders", "trg_orders"} {
		require.Contains(t, ae.Message, n, "el mensaje nombra lo que choca, no solo las tablas")
	}
}

func TestPlanSkipsExistingObjectsAndComparesTheirCode(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	e.seedExistingObjects()
	p, err := e.planObjects(t, model.MigrationObjects{}, model.MigrationOptions{SkipExisting: true, Workers: 2})
	require.NoError(t, err)

	skipped := map[string]model.PlanObject{}
	for _, list := range [][]model.PlanObject{p.Plan.Routines, p.Plan.Views, p.Plan.Triggers} {
		for _, o := range list {
			if o.Skipped {
				skipped[o.Name] = o
			}
		}
	}
	require.Equal(t, "same", skipped["p_count"].Compare, "se ignoran DEFINER y espacios")
	require.Equal(t, "different", skipped["v_orders"].Compare)
	require.Equal(t, "unknown", skipped["trg_orders"].Compare, "sin código legible no se afirma nada")
	require.Len(t, skipped, 3)

	level := map[string]string{}
	for _, w := range p.Plan.Warnings {
		if strings.HasPrefix(w.Code, "object_exists_") {
			level[w.Object] = w.Code + "/" + w.Level
		}
	}
	require.Equal(t, map[string]string{"p_count": "object_exists_same/info", "v_orders": "object_exists_different/warning", "trg_orders": "object_exists_unknown/warning"}, level)

	for _, s := range p.Plan.Statements {
		require.NotContains(t, s, "p_count", "no se crea lo que se omite")
		require.NotContains(t, s, "v_orders")
	}
	j := runJob(t, p, Callbacks{})
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
	require.Empty(t, r.Failed, "nada falla por duplicado")
	for _, l := range e.src.Log() {
		require.False(t, strings.HasPrefix(l, "object:dst:"), "no se crea ni reemplaza ningún objeto existente: %s", l)
	}
	d := e.src.DBs["dst"]
	require.Contains(t, d.Views[0].CreateSQL, "select 1", "la vista del destino queda como estaba")

	// Cambiar lo que hay en el destino invalida el plan: el usuario vio otra comparación.
	d.Views[0].CreateSQL = "CREATE VIEW `v_orders` AS select * from `dst`.`orders`"
	q, err := e.planObjects(t, model.MigrationObjects{}, model.MigrationOptions{SkipExisting: true, Workers: 2})
	require.NoError(t, err)
	require.NotEqual(t, p.Plan.PlanHash, q.Plan.PlanHash)
}

// La verificación compara el código de lo que la migración creó con lo que hay ahora en el destino (⚑D22).
func TestVerifyComparesCreatedObjects(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	p := e.plan(t, model.MigrationOptions{Workers: 2})
	j := runJob(t, p, Callbacks{})
	require.Equal(t, model.MigDone, j.Result().Job.State)

	v, err := j.Verify(context.Background(), model.VerifyOptions{Objects: true})
	require.NoError(t, err)
	require.Equal(t, 3, v.ObjectsChecked, "rutina, vista y trigger")
	require.Equal(t, []model.ObjectDiff{
		{Kind: "procedure", Name: "p_count", Status: "same"},
		{Kind: "trigger", Name: "trg_orders", Status: "same"},
		{Kind: "view", Name: "v_orders", Status: "same"},
	}, v.Objects, "recién creados coinciden y salen todos, también los iguales")

	d := e.src.DBs["dst"]
	d.Views[0].CreateSQL = "CREATE VIEW `v_orders` AS select 1"
	d.Triggers = nil
	d.Routines[0].CreateSQL = ""
	v, err = j.Verify(context.Background(), model.VerifyOptions{Objects: true})
	require.NoError(t, err)
	require.Equal(t, 3, v.ObjectsChecked)
	require.Equal(t, []model.ObjectDiff{
		{Kind: "procedure", Name: "p_count", Status: "unreadable"},
		{Kind: "trigger", Name: "trg_orders", Status: "missing"},
		{Kind: "view", Name: "v_orders", Status: "different"},
	}, v.Objects)

	// Sin pedirlo no se comparan objetos.
	v, err = j.Verify(context.Background(), model.VerifyOptions{RowCounts: true})
	require.NoError(t, err)
	require.Zero(t, v.ObjectsChecked)
}
