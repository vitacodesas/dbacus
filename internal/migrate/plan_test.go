package migrate

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

func planErr(t *testing.T, e *env, req model.MigrationRequest) *apperr.AppError {
	t.Helper()
	_, err := Plan(context.Background(), PlanDeps{Source: e.srcPool, Target: e.dstPool, SameSession: e.same}, req)
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	return ae
}

func request(src, dst string) model.MigrationRequest {
	return model.MigrationRequest{Source: model.MigrationEndpoint{Database: src}, Target: model.MigrationEndpoint{Database: dst}}
}

func TestPlanValidation(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	for name, tc := range map[string]struct {
		req    model.MigrationRequest
		detail string
	}{
		"mismo schema":       {request("src", "src"), "same_schema"},
		"destino de sistema": {request("src", "mysql"), "system_schema"},
		"origen de sistema":  {request("information_schema", "dst"), "system_schema"},
		"nombre con punto":   {request("src", "a.b"), "database"},
		"nombre vacío":       {request("src", ""), "database"},
		"nombre largo":       {request("src", strings.Repeat("x", 65)), "database"},
	} {
		t.Run(name, func(t *testing.T) { require.Equal(t, tc.detail, planErr(t, e, tc.req).Detail) })
	}
	r := request("src", "dst")
	r.Options.Workers = 17
	require.Equal(t, "workers", planErr(t, e, r).Detail)
	r.Options = model.MigrationOptions{ChunkRows: 10}
	require.Equal(t, "chunkRows", planErr(t, e, r).Detail)
	r = request("src", "dst")
	r.Objects.Tables = []string{"no_existe"}
	require.Equal(t, "objects", planErr(t, e, r).Detail)
}

func TestPlanSameSchemaByServerCanonicalName(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	// Con lower_case_table_names el servidor trata «SRC» y «src» como la misma base: la comparación es suya, no nuestra.
	ae := planErr(t, e, request("src", "SRC"))
	require.Equal(t, "same_schema", ae.Detail)
}

func TestPlanRejectsTablesWithoutPKWhenRequired(t *testing.T) {
	e := newEnv(t, false)
	e.seed()
	e.dst.RequirePK = true
	ae := planErr(t, e, request("src", "dst"))
	require.Equal(t, "incompatible", ae.Detail)
	require.Contains(t, ae.Message, "log")
	require.NotContains(t, e.dst.Log(), "createdb:dst", "no se crea nada antes de fallar")

	r := request("src", "dst")
	r.Objects.Tables = []string{"customers", "orders"} // sin «log»
	p, err := Plan(context.Background(), PlanDeps{Source: e.srcPool, Target: e.dstPool}, r)
	require.NoError(t, err)
	require.Len(t, p.Tables, 2)
}

func TestPlanRejectsUnknownCollation(t *testing.T) {
	e := newEnv(t, false)
	e.seed()
	e.dst.Collations = map[string]bool{"utf8mb4_general_ci": true}
	ae := planErr(t, e, request("src", "dst"))
	require.Equal(t, "incompatible", ae.Detail)
	require.Contains(t, ae.Message, "utf8mb4_0900_ai_ci")
}

func TestPlanRejectsSpatialAcrossServersButNotInServer(t *testing.T) {
	for _, same := range []bool{false, true} {
		e := newEnv(t, same)
		e.seed()
		g := tbl("geo")
		g.Spatial = []string{"loc"}
		e.src.AddTable("src", g, 1)
		if same {
			_, err := Plan(context.Background(), PlanDeps{Source: e.srcPool, Target: e.dstPool, SameSession: true}, request("src", "dst"))
			require.NoError(t, err, "dentro del servidor se copian")
			continue
		}
		require.Equal(t, "spatial", planErr(t, e, request("src", "dst")).Detail)
	}
}

func TestPlanCapsWorkersByFreeConnections(t *testing.T) {
	e := newEnv(t, false)
	e.seed()
	e.dst.MaxConns, e.dst.UsedConns = 20, 8 // 12 libres → la mitad son 6, menos los 2 de planificación = 4
	r := request("src", "dst")
	r.Options.Workers = 16
	p, err := Plan(context.Background(), PlanDeps{Source: e.srcPool, Target: e.dstPool}, r)
	require.NoError(t, err)
	require.Equal(t, 4, p.Workers)
	var capped bool
	for _, w := range p.Plan.Warnings {
		capped = capped || w.Code == "workers_capped"
	}
	require.True(t, capped)

	e.dst.UsedConns = 18
	require.Equal(t, "connections", planErr(t, e, request("src", "dst")).Detail)
}

func TestPlanStatementsAndWarnings(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	e.src.DBs["src"].Views = append(e.src.DBs["src"].Views, model.MigrationCode{Name: "v_str", Kind: "view", CreateSQL: "CREATE VIEW `v_str` AS select 'datos de src' AS `x`"})
	e.src.DBs["src"].Routines = append(e.src.DBs["src"].Routines, model.MigrationCode{Name: "p_dyn", Kind: "procedure", CreateSQL: "CREATE PROCEDURE `p_dyn`() BEGIN PREPARE s FROM 'SELECT 1'; EXECUTE s; END"})
	p := e.plan(t, model.MigrationOptions{})
	require.Equal(t, model.RouteServer, p.Plan.Route)
	require.True(t, p.Plan.TargetNew)
	require.Len(t, p.Plan.Tables, 3)
	require.NotEmpty(t, p.Plan.PlanHash)
	joined := strings.Join(p.Plan.Statements, "\n")
	require.Contains(t, joined, "CREATE DATABASE `dst`")
	require.Contains(t, joined, "`dst`.`orders`")
	require.NotContains(t, joined, "`src`.`orders`", "el SQL que se va a ejecutar ya viene reescrito")
	codes := map[string]bool{}
	for _, w := range p.Plan.Warnings {
		codes[w.Code+":"+w.Object] = true
	}
	require.True(t, codes["no_index:log"])
	require.True(t, codes["dynamic_sql:p_dyn"])
	require.True(t, codes["string_mentions_source:v_str"])
	require.True(t, codes["fk_external_schema:orders.fk_ext"])
	require.True(t, codes["definer_foreign:p_count"], "el DEFINER original era otro usuario")
	require.True(t, codes["route_server:"])

	// El hash cambia si el origen cambia entre el plan y el inicio.
	h1 := p.Plan.PlanHash
	e.src.DBs["src"].Tables["customers"].CreateSQL = "CREATE TABLE `customers` (id int)"
	e.src.DBs["src"].Tables["customers"].MigrationTable.CreateSQL = "CREATE TABLE `customers` (id int)"
	p2 := e.plan(t, model.MigrationOptions{})
	require.NotEqual(t, h1, p2.Plan.PlanHash)
	p3 := e.plan(t, model.MigrationOptions{})
	require.Equal(t, p2.Plan.PlanHash, p3.Plan.PlanHash, "el hash es determinista")
}

func TestPlanDoesNotCreateAnything(t *testing.T) {
	e := newEnv(t, false)
	e.seed()
	e.plan(t, model.MigrationOptions{})
	require.Empty(t, e.dst.Log())
	require.Empty(t, e.dst.DBs)
}

func TestPlanWarnsAboveEightWorkers(t *testing.T) {
	e := newEnv(t, false)
	e.seed()
	codes := func(w int) map[string]bool {
		r := request("src", "dst")
		r.Options.Workers = w
		p, err := Plan(context.Background(), PlanDeps{Source: e.srcPool, Target: e.dstPool}, r)
		require.NoError(t, err)
		m := map[string]bool{}
		for _, x := range p.Plan.Warnings {
			m[x.Code] = true
		}
		return m
	}
	require.False(t, codes(8)["many_workers"])
	require.True(t, codes(12)["many_workers"])
}
