package migrate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/driver/fake"
	"github.com/vitacodesas/dbacus/internal/model"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// env son los dos «servidores» de una prueba. En la ruta del servidor, origen y destino son el mismo.
type env struct {
	src, dst *fake.MigEngine
	srcPool  driver.SchemaMigrator
	dstPool  driver.SchemaMigrator
	same     bool
}

func pool(t *testing.T, e *fake.MigEngine) driver.SchemaMigrator {
	t.Helper()
	d := fake.New("f")
	d.Mig = e
	p, err := d.Open(context.Background(), driver.ConnConfig{}, nil)
	require.NoError(t, err)
	return p.(driver.SchemaMigrator)
}

func newEnv(t *testing.T, same bool) *env {
	t.Helper()
	src := fake.NewMigEngine()
	e := &env{src: src, dst: src, same: same}
	e.srcPool = pool(t, src)
	e.dstPool = e.srcPool
	if !same {
		e.dst = fake.NewMigEngine()
		e.dstPool = pool(t, e.dst)
	}
	return e
}

func tbl(name string, fks ...model.ForeignKey) model.MigrationTable {
	return model.MigrationTable{Name: name, Columns: []string{"id", "v"}, CreateSQL: "CREATE TABLE `" + name + "`", HasPrimaryKey: true, ForeignKeys: fks,
		Key: model.ChunkKeySpec{Kind: model.ChunkPK, Columns: []string{"id"}}, AvgRowLen: 16}
}

// seed deja un schema `src` con: customers (25 filas), orders (95 filas, FK a customers y auto-referencia), log (sin índices), una
// vista, una rutina y un trigger.
func (e *env) seed() {
	e.src.AddTable("src", tbl("customers"), 25)
	e.src.AddTable("src", tbl("orders",
		model.ForeignKey{Name: "fk_cust", Columns: []string{"cid"}, RefSchema: "src", RefTable: "customers", RefColumns: []string{"id"}},
		model.ForeignKey{Name: "fk_self", Columns: []string{"parent"}, RefSchema: "src", RefTable: "orders", RefColumns: []string{"id"}},
		model.ForeignKey{Name: "fk_ext", Columns: []string{"x"}, RefSchema: "otro", RefTable: "t", RefColumns: []string{"id"}},
	), 95)
	logT := tbl("log")
	logT.HasPrimaryKey = false
	logT.Key = model.ChunkKeySpec{Kind: model.ChunkNone}
	e.src.AddTable("src", logT, 7)
	d := e.src.DBs["src"]
	d.Views = []model.MigrationCode{{Name: "v_orders", Kind: "view", CreateSQL: "CREATE VIEW `v_orders` AS select * from `src`.`orders`"}}
	d.Routines = []model.MigrationCode{{Name: "p_count", Kind: "procedure", CreateSQL: "CREATE DEFINER=`root`@`localhost` PROCEDURE `p_count`() SELECT COUNT(*) FROM `src`.`orders`"}}
	d.Triggers = []model.MigrationCode{{Name: "trg_orders", Kind: "trigger", Table: "orders", CreateSQL: "CREATE TRIGGER `trg_orders` BEFORE INSERT ON `orders` FOR EACH ROW SET NEW.v = 1"}}
}

func (e *env) plan(t *testing.T, opts model.MigrationOptions) *Prepared {
	t.Helper()
	p, err := Plan(context.Background(), PlanDeps{Source: e.srcPool, Target: e.dstPool, SameSession: e.same},
		model.MigrationRequest{Source: model.MigrationEndpoint{Database: "src"}, Target: model.MigrationEndpoint{Database: "dst"}, Options: opts})
	require.NoError(t, err)
	// Trozos diminutos para que haya varios con filas de ejemplo.
	for n := range p.ChunkRows {
		p.ChunkRows[n] = 10
	}
	return p
}

func noSleep(context.Context, time.Duration) error { return nil }

func runJob(t *testing.T, p *Prepared, cb Callbacks) *Job {
	t.Helper()
	j := NewJob("j1", p, cb)
	j.SetClock(nil, noSleep)
	require.NoError(t, j.Start(context.Background()))
	j.Wait()
	return j
}

func TestMigrateServerRoute(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	p := e.plan(t, model.MigrationOptions{Workers: 3})
	require.Equal(t, model.RouteServer, p.Plan.Route)

	var progress, done atomic.Int32
	j := runJob(t, p, Callbacks{OnProgress: func(model.MigrationJob) { progress.Add(1) }, OnDone: func(model.MigrationResult) { done.Add(1) }})
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
	require.Empty(t, r.Failed)
	require.EqualValues(t, 1, done.Load())
	require.Positive(t, progress.Load(), "el estado final siempre se emite")

	for _, n := range []string{"customers", "orders", "log"} {
		require.Equal(t, e.src.RowCount("src", n), e.src.RowCount("dst", n), n)
	}
	require.Equal(t, 127, int(r.Job.Rows))
	require.Equal(t, 3, r.Job.TablesDone)

	// Orden de las fases: schema → tablas → datos → claves → rutinas → vistas → triggers.
	log := e.src.Log()
	idx := func(prefix string) int {
		for i, l := range log {
			if strings.HasPrefix(l, prefix) {
				return i
			}
		}
		return -1
	}
	last := func(prefix string) int {
		n := -1
		for i, l := range log {
			if strings.HasPrefix(l, prefix) {
				n = i
			}
		}
		return n
	}
	require.Equal(t, 0, idx("createdb:dst"))
	require.Less(t, last("exec:dst:CREATE TABLE"), idx("insert:dst."), "las tablas existen antes de copiar")
	require.Less(t, last("insert:dst."), idx("fk:dst."), "las foráneas van después de los datos")
	require.Less(t, last("fk:dst."), idx("object:dst:procedure"))
	require.Less(t, idx("object:dst:procedure"), idx("object:dst:view"))
	require.Less(t, idx("object:dst:view"), idx("object:dst:trigger"), "los triggers van al final")
	require.Greater(t, idx("object:dst:trigger"), last("insert:dst."), "ningún trigger existe durante la carga")

	// Las foráneas se re-apuntan al destino; la que apunta a otro schema se conserva.
	require.Contains(t, log, "fk:dst.orders.fk_cust->dst.customers")
	require.Contains(t, log, "fk:dst.orders.fk_self->dst.orders")
	require.Contains(t, log, "fk:dst.orders.fk_ext->otro.t")
	// El SQL de las vistas y rutinas queda con el schema nuevo y sin DEFINER.
	d := e.src.DBs["dst"]
	require.Contains(t, d.Views[0].CreateSQL, "`dst`.`orders`")
	require.NotContains(t, d.Routines[0].CreateSQL, "DEFINER")
	require.Contains(t, d.Routines[0].CreateSQL, "`dst`.`orders`")
	// Y el origen no se escribió: sigue idéntico.
	require.Equal(t, 95, e.src.RowCount("src", "orders"))
}

func TestMigrateGoRouteBoundsMemory(t *testing.T) {
	e := newEnv(t, false)
	e.seed()
	// Una tabla ancha (512 B por fila) y un escritor lento: decenas de lotes y presión real sobre el canal.
	wide := tbl("wide")
	for i := 0; i < 62; i++ {
		wide.Columns = append(wide.Columns, fmt.Sprintf("c%d", i))
	}
	wide.Columns = append([]string{"id", "v"}, wide.Columns...)
	e.src.AddTable("src", wide, 20_000)
	e.dst.Latency = 3 * time.Millisecond
	fake.ResetInflight()
	p := e.plan(t, model.MigrationOptions{Workers: 2})
	require.Equal(t, model.RouteGo, p.Plan.Route)
	p.ChunkRows["wide"] = 20_000 // un solo trozo grande: varios lotes por trozo
	j := runJob(t, p, Callbacks{})
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
	e.dst.Latency = 0
	require.Equal(t, 20_000, e.dst.RowCount("dst", "wide"))
	// Lotes leídos y aún no escritos: nunca más de lo que cabe en el canal más los que están en las manos de lector y escritor.
	require.LessOrEqual(t, e.src.MaxInflight(), int64(p.Workers*(pipeDepth+2)), "contrapresión")
}

func TestMigrateRetriesAndNeverDuplicates(t *testing.T) {
	for name, mut := range map[string]func(*fake.MigEngine){
		"el COMMIT falla": func(e *fake.MigEngine) {
			e.OnCommit = func(table string, seq, attempt int) error {
				if table == "orders" && seq == 3 && attempt == 1 {
					return apperr.New(apperr.Timeout, "lock wait").AsRetryable()
				}
				return nil
			}
		},
		"el COMMIT se aplica pero se pierde el acuse": func(e *fake.MigEngine) {
			e.LoseAck = func(table string, seq, attempt int) bool { return table == "orders" && seq == 3 && attempt == 1 }
		},
	} {
		for _, same := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/mismo servidor=%v", name, same), func(t *testing.T) {
				e := newEnv(t, same)
				e.seed()
				mut(e.dst)
				j := runJob(t, e.plan(t, model.MigrationOptions{Workers: 2}), Callbacks{})
				r := j.Result()
				require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
				require.Equal(t, 95, e.dst.RowCount("dst", "orders"), "ni filas perdidas ni repetidas")
				require.Equal(t, 2, e.dst.Attempts("orders", 3))
			})
		}
	}
}

func TestMigratePermanentFailureThenRetryOnlyFailed(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	var failing atomic.Bool
	failing.Store(true)
	e.src.OnCommit = func(table string, seq, attempt int) error {
		if table == "orders" && failing.Load() {
			return apperr.New(apperr.QueryFailed, "tabla corrupta")
		}
		return nil
	}
	p := e.plan(t, model.MigrationOptions{Workers: 2})
	j := runJob(t, p, Callbacks{})
	r := j.Result()
	require.Equal(t, model.MigPartial, r.Job.State)
	require.Len(t, r.Failed, 1)
	require.Equal(t, "orders", r.Failed[0].Object)
	require.Equal(t, 25, e.src.RowCount("dst", "customers"), "lo demás sigue adelante")
	for _, tb := range r.Job.Tables {
		if tb.Name == "orders" {
			require.Equal(t, model.TblFailed, tb.State)
		}
	}
	require.NotContains(t, e.src.Log(), "fk:dst.orders.fk_cust->dst.customers", "una tabla fallida no recibe sus foráneas")

	failing.Store(false)
	before := e.src.Attempts("customers", 0)
	require.NoError(t, j.Retry(context.Background()))
	j.Wait()
	r = j.Result()
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
	require.Equal(t, 95, e.src.RowCount("dst", "orders"))
	require.Equal(t, 25, e.src.RowCount("dst", "customers"), "no se repitió lo que ya estaba")
	require.Equal(t, before, e.src.Attempts("customers", 0), "Retry no vuelve a copiar las tablas hechas")
	require.Contains(t, e.src.Log(), "fk:dst.orders.fk_cust->dst.customers")
}

func TestMigrateCancelKillsStatementsAndLeavesNoGoroutines(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("mismo servidor=%v", same), func(t *testing.T) {
			e := newEnv(t, same)
			e.seed()
			e.src.AddTable("src", tbl("big"), 5000)
			e.src.Latency = 30 * time.Millisecond
			e.dst.Latency = 30 * time.Millisecond
			p := e.plan(t, model.MigrationOptions{Workers: 2})
			p.ChunkRows["big"] = 50
			var started sync.WaitGroup
			started.Add(1)
			var once sync.Once
			j := NewJob("j", p, Callbacks{OnState: func(s model.MigrationJob) {
				if s.Phase == model.PhaseData {
					once.Do(started.Done)
				}
			}})
			j.SetClock(nil, noSleep)
			require.NoError(t, j.Start(context.Background()))
			started.Wait()
			time.Sleep(80 * time.Millisecond)
			j.Cancel()
			j.Wait()
			r := j.Result()
			require.Equal(t, model.MigCanceled, r.Job.State)
			require.Less(t, e.dst.RowCount("dst", "big"), 5000)
			e.dst.Latency = 0
			if same {
				require.NotEmpty(t, e.src.Killed, "KILL QUERY de los hilos propios")
			}
		})
	}
}

func TestMigrateParentCancelIsNotReportedAsDone(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	e.src.Latency = 40 * time.Millisecond
	p := e.plan(t, model.MigrationOptions{Workers: 1})
	ctx, cancel := context.WithCancel(context.Background())
	j := NewJob("j", p, Callbacks{})
	j.SetClock(nil, noSleep)
	require.NoError(t, j.Start(ctx))
	time.Sleep(60 * time.Millisecond)
	cancel() // p. ej. se cerró la sesión de origen
	j.Wait()
	e.src.Latency = 0
	r := j.Result()
	require.NotEqual(t, model.MigDone, r.Job.State)
	require.NotEmpty(t, r.Job.Error)
}

func TestMigrateSkipsTablesThatAlreadyExist(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	e.src.AddTable("dst", tbl("customers"), 3) // ya existe en el destino
	_, err := Plan(context.Background(), PlanDeps{Source: e.srcPool, Target: e.dstPool, SameSession: true},
		model.MigrationRequest{Source: model.MigrationEndpoint{Database: "src"}, Target: model.MigrationEndpoint{Database: "dst"}})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "exists", ae.Detail)
	require.Contains(t, ae.Message, "customers")

	p := e.plan(t, model.MigrationOptions{SkipExisting: true})
	require.Equal(t, []string{"customers"}, p.SkippedTables)
	j := runJob(t, p, Callbacks{})
	r := j.Result()
	require.Equal(t, model.MigDone, r.Job.State, "%+v", r.Failed)
	require.Equal(t, 3, e.src.RowCount("dst", "customers"), "la tabla existente no se toca")
	require.Equal(t, 95, e.src.RowCount("dst", "orders"))
	require.Contains(t, e.src.Log(), "fk:dst.orders.fk_cust->dst.customers", "las foráneas hacia una tabla omitida sí se crean")
	for _, c := range r.Created {
		require.NotEqual(t, "customers", c.Name)
	}
}

func TestVerifyFindsOrphansAndRowCountDiffs(t *testing.T) {
	e := newEnv(t, false)
	e.seed()
	e.dst.OrphanCount = func(table, fk string) int64 {
		if table == "orders" && fk == "fk_cust" {
			return 4
		}
		return 0
	}
	p := e.plan(t, model.MigrationOptions{Workers: 2})
	j := runJob(t, p, Callbacks{})
	require.Equal(t, model.MigDone, j.Result().Job.State)
	require.Nil(t, j.Result().Verification, "la verificación es opcional y está apagada por defecto")

	// Alguien borra filas del destino: el recuento lo detecta.
	e.dst.DBs["dst"].Tables["customers"].Rows = e.dst.DBs["dst"].Tables["customers"].Rows[:20]
	v, err := j.Verify(context.Background(), model.VerifyOptions{ForeignKeys: true, RowCounts: true})
	require.NoError(t, err)
	require.Equal(t, []model.OrphanRow{{ForeignKey: "fk_cust", Table: "orders", Count: 4}}, v.Orphans)
	require.Equal(t, []model.RowCountDiff{{Table: "customers", Source: 25, Target: 20}}, v.RowCounts)
}

func TestCleanupOnlyDropsCreatedObjects(t *testing.T) {
	e := newEnv(t, true)
	e.seed()
	e.src.AddTable("dst", tbl("preexistente"), 1)
	p := e.plan(t, model.MigrationOptions{SkipExisting: true})
	j := runJob(t, p, Callbacks{})
	require.Equal(t, model.MigDone, j.Result().Job.State)
	require.NoError(t, j.Cleanup(context.Background()))
	log := e.src.Log()
	var drops []string
	for _, l := range log {
		if strings.HasPrefix(l, "drop:") {
			drops = append(drops, l)
		}
	}
	require.NotContains(t, strings.Join(drops, " "), "preexistente")
	require.Contains(t, drops, "drop:table:orders")
	require.Contains(t, drops, "drop:table:customers")
	require.NotContains(t, drops, "drop:database:dst", "el schema existía: no lo creó la migración")
	require.Equal(t, 1, e.src.RowCount("dst", "preexistente"))
}

func TestCleanupDropsDatabaseItCreatedLast(t *testing.T) {
	e := newEnv(t, false)
	e.seed()
	j := runJob(t, e.plan(t, model.MigrationOptions{}), Callbacks{})
	require.NoError(t, j.Cleanup(context.Background()))
	var drops []string
	for _, l := range e.dst.Log() {
		if strings.HasPrefix(l, "drop:") {
			drops = append(drops, l)
		}
	}
	require.Equal(t, "drop:database:dst", drops[len(drops)-1], "el schema, que creó la migración, se borra el último")
}
