package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/driver/fake"
	"tabulariswails/internal/model"
)

type schemaEnv struct {
	*env
	schema *fake.Schema
	svc    *SchemaService
	events chan SchemaInvalidated
	sid    string
}

func newSchemaEnv(t *testing.T) *schemaEnv {
	e := newEnv(t)
	sc := &fake.Schema{
		DBs:      []model.Database{{Name: "app"}, {Name: "mysql", IsSystem: true}},
		TablesBy: map[string][]model.Table{"app": {{Name: "users", Type: "table"}}},
		Details:  map[string]model.TableDetail{"app.users": {Table: model.Table{Name: "users"}, Columns: []model.Column{{Name: "id"}}}},
		Cat:      map[string]model.CompletionCatalog{"app": {Tables: []model.CatalogTable{{Name: "users"}}}},
	}
	e.drv.SchemaReader = sc
	p, err := e.svc.Save(context.Background(), e.profile("S"), ConnectionSecrets{})
	require.NoError(t, err)
	info, err := e.svc.Connect(context.Background(), p.ID, nil)
	require.NoError(t, err)

	events := make(chan SchemaInvalidated, 8)
	svc := NewSchemaService(e.sessions, func(name string, data any) {
		require.Equal(t, "schema:invalidated", name)
		events <- data.(SchemaInvalidated)
	})
	return &schemaEnv{env: e, schema: sc, svc: svc, events: events, sid: info.SessionID}
}

func TestSchemaReadsAndCaches(t *testing.T) {
	e := newSchemaEnv(t)
	ctx := context.Background()

	for range 3 {
		dbs, err := e.svc.ListDatabases(ctx, e.sid)
		require.NoError(t, err)
		require.Len(t, dbs, 2)
	}
	require.EqualValues(t, 1, e.schema.Calls.Load(), "las lecturas repetidas salen de la caché")

	tables, err := e.svc.ListTables(ctx, e.sid, "app")
	require.NoError(t, err)
	require.Equal(t, "users", tables[0].Name)
	d, err := e.svc.DescribeTable(ctx, e.sid, "app", "users")
	require.NoError(t, err)
	require.Equal(t, "id", d.Columns[0].Name)
	cat, err := e.svc.GetCompletionCatalog(ctx, e.sid, "app")
	require.NoError(t, err)
	require.Equal(t, "users", cat.Tables[0].Name)
}

func TestSchemaValidationAndUnknownSession(t *testing.T) {
	e := newSchemaEnv(t)
	ctx := context.Background()
	_, err := e.svc.ListTables(ctx, e.sid, " ")
	require.Equal(t, apperr.Validation, code(t, err))
	_, err = e.svc.DescribeTable(ctx, e.sid, "app", "")
	require.Equal(t, "table", detail(t, err))
	_, err = e.svc.ListDatabases(ctx, "nada")
	require.Equal(t, apperr.SessionClosed, code(t, err))
	require.Equal(t, apperr.SessionClosed, code(t, e.svc.Refresh("nada", "")))
}

func TestSchemaCollapsesConcurrentReads(t *testing.T) {
	e := newSchemaEnv(t)
	release := make(chan struct{})
	e.schema.Delay = func(ctx context.Context) error { <-release; return nil }

	var wg sync.WaitGroup
	for range 15 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.ListTables(context.Background(), e.sid, "app")
			require.NoError(t, err)
		}()
	}
	time.Sleep(60 * time.Millisecond)
	close(release)
	wg.Wait()
	require.EqualValues(t, 1, e.schema.Calls.Load())
}

func TestSchemaRefreshInvalidatesAndNotifies(t *testing.T) {
	e := newSchemaEnv(t)
	ctx := context.Background()
	_, _ = e.svc.ListDatabases(ctx, e.sid)
	_, _ = e.svc.ListTables(ctx, e.sid, "app")
	require.EqualValues(t, 2, e.schema.Calls.Load())

	require.NoError(t, e.svc.Refresh(e.sid, "app"))
	require.Equal(t, SchemaInvalidated{SessionID: e.sid, Database: "app"}, <-e.events)
	_, _ = e.svc.ListDatabases(ctx, e.sid) // no depende de "app": sigue cacheada
	_, _ = e.svc.ListTables(ctx, e.sid, "app")
	require.EqualValues(t, 3, e.schema.Calls.Load(), "solo se recargó la base refrescada")

	require.NoError(t, e.svc.Refresh(e.sid, ""))
	require.Equal(t, SchemaInvalidated{SessionID: e.sid}, <-e.events)
	_, _ = e.svc.ListDatabases(ctx, e.sid)
	require.EqualValues(t, 4, e.schema.Calls.Load())
}

func TestSchemaErrorsAreMappedAndNotCached(t *testing.T) {
	e := newSchemaEnv(t)
	e.schema.Err = apperr.New(apperr.QueryFailed, "sin permisos")
	_, err := e.svc.ListDatabases(context.Background(), e.sid)
	require.Equal(t, apperr.QueryFailed, code(t, err))
	e.schema.Err = nil
	dbs, err := e.svc.ListDatabases(context.Background(), e.sid)
	require.NoError(t, err)
	require.Len(t, dbs, 2)
}

func TestDisconnectCancelsInFlightSchemaLoad(t *testing.T) {
	e := newSchemaEnv(t)
	started := make(chan struct{})
	e.schema.Delay = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	errc := make(chan error, 1)
	go func() {
		_, err := e.svc.ListDatabases(context.Background(), e.sid)
		errc <- err
	}()
	<-started
	start := time.Now()
	require.NoError(t, e.sessions.Close(e.sid))
	select {
	case err := <-errc:
		require.Error(t, err)
		require.Less(t, time.Since(start), 2*time.Second)
	case <-time.After(3 * time.Second):
		t.Fatal("la carga no se canceló al desconectar")
	}
}

func TestSelectStatementEscapesIdentifiers(t *testing.T) {
	e := newSchemaEnv(t)
	got, err := e.svc.SelectStatement(e.sid, "my`db", "ta ble`; DROP", 0)
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `my``db`.`ta ble``; DROP` LIMIT 100", got)
	got, _ = e.svc.SelectStatement(e.sid, "a", "b", 99999)
	require.Equal(t, "SELECT * FROM `a`.`b` LIMIT 10000", got)
	_, err = e.svc.SelectStatement(e.sid, "a", " ", 10)
	require.Equal(t, "table", detail(t, err))
	_, err = e.svc.SelectStatement("nada", "a", "b", 10)
	require.Equal(t, apperr.SessionClosed, code(t, err))
}
