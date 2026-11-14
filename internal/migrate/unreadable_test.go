package migrate

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
)

// SHOW CREATE PROCEDURE devuelve NULL (cadena vacía en Go) a quien solo tiene EXECUTE: la rutina se omite en el plan con su motivo.
func TestPlanSkipsObjectsWhoseCodeCannotBeRead(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	d := e.src.DBs["src"]
	d.Routines = append(d.Routines,
		model.MigrationCode{Name: "lsp_get_schools", Kind: "procedure", CreateSQL: ""},
		model.MigrationCode{Name: "lsp_get_detail", Kind: "function", CreateSQL: "   "})
	p := e.plan(t, model.MigrationOptions{Workers: 2})

	var names []string
	for _, r := range p.Plan.Routines {
		if !r.Skipped {
			names = append(names, r.Name)
		}
	}
	require.Equal(t, []string{"p_count"}, names, "solo la rutina legible entra en el plan")
	got := map[string]bool{}
	for _, w := range p.Plan.Warnings {
		if w.Code == "unreadable_code" {
			got[w.Object] = true
		}
	}
	require.Equal(t, map[string]bool{"lsp_get_schools": true, "lsp_get_detail": true}, got)
	// Siguen en la lista del plan, marcadas, para que se vean en el paso de contenido y no desaparezcan sin explicación.
	marked := map[string]string{}
	for _, o := range p.Plan.Routines {
		if o.Skipped {
			marked[o.Name] = o.Kind + "/" + o.Compare
		}
	}
	require.Equal(t, map[string]string{"lsp_get_schools": "procedure/unreadable", "lsp_get_detail": "function/unreadable"}, marked)

	j := runJob(t, p, Callbacks{})
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
	require.Empty(t, r.Failed, "nada falla a mitad de la migración")
}
