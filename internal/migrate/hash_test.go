package migrate

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/model"
)

// El hash del plan solo debe cambiar cuando cambia algo que el usuario vio: no con cada insert del origen ni con las conexiones libres.
func TestPlanHashIgnoresValuesThatChangeOnTheirOwn(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	opts := model.MigrationOptions{Workers: 8}
	base := e.plan(t, opts)

	// Alguien inserta en el origen: el contador AUTO_INCREMENT del SHOW CREATE sube.
	customers := e.src.DBs["src"].Tables["customers"]
	for _, n := range []string{"5", "99"} {
		customers.CreateSQL = "CREATE TABLE `customers` AUTO_INCREMENT=" + n
		require.Equal(t, base.Plan.PlanHash, e.plan(t, opts).Plan.PlanHash, "AUTO_INCREMENT=%s no debe invalidar el plan", n)
	}
	customers.CreateSQL = "CREATE TABLE `customers`"

	// Cambian las conexiones libres: los workers efectivos se recortan distinto, pero lo pedido es lo mismo.
	e.src.UsedConns = 90
	busy := e.plan(t, opts)
	require.Less(t, busy.Workers, base.Workers, "el plan sí recorta los workers")
	require.Equal(t, base.Plan.PlanHash, busy.Plan.PlanHash)
	e.src.UsedConns = 5

	// Lo que sí importa invalida el plan: la estructura, la elección de tablas y los workers pedidos.
	customers.Columns = append(append([]string{}, customers.Columns...), "extra")
	require.NotEqual(t, base.Plan.PlanHash, e.plan(t, opts).Plan.PlanHash, "una columna nueva cambia el plan")
	customers.Columns = customers.Columns[:len(customers.Columns)-1]
	require.Equal(t, base.Plan.PlanHash, e.plan(t, opts).Plan.PlanHash)
	require.NotEqual(t, base.Plan.PlanHash, e.plan(t, model.MigrationOptions{Workers: 2}).Plan.PlanHash, "otros workers pedidos")
	d := e.src.DBs["src"]
	d.Views = d.Views[:0]
	require.NotEqual(t, base.Plan.PlanHash, e.plan(t, opts).Plan.PlanHash, "una vista que desaparece")
}
