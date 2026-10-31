package services

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/driver/fake"
	"github.com/vitacodesas/dbacus/internal/model"
)

func newQueryFiles(t *testing.T, e *env, mut ...func(*QueryFilesDeps)) (*QueryFilesService, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Consultas")
	deps := QueryFilesDeps{Store: e.st, DefaultRoot: root, Home: t.TempDir()}
	for _, m := range mut {
		m(&deps)
	}
	return NewQueryFilesService(deps), root
}

func TestSaveCreatesFileAndHeader(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	prof, err := e.svc.Save(ctx, e.profile("Prod"), ConnectionSecrets{})
	require.NoError(t, err)
	svc, root := newQueryFiles(t, e)

	tree, err := svc.List(ctx)
	require.NoError(t, err)
	require.True(t, tree.Root.IsDefault)
	require.True(t, tree.Root.Exists)
	require.True(t, tree.Root.Writable)
	require.NotNil(t, tree.Nodes, "Vue siempre recibe un array")

	ref, err := svc.Save(ctx, model.SaveQueryRequest{Path: "Informes/ventas", SQL: "SELECT 1", ProfileID: prof.ID, Database: "shop", RememberConnection: true})
	require.NoError(t, err)
	require.Equal(t, "Informes/ventas.sql", ref.Path)
	require.FileExists(t, filepath.Join(root, "Informes", "ventas.sql"))

	f, err := svc.Open(ctx, ref.Path)
	require.NoError(t, err)
	require.Equal(t, "SELECT 1", f.SQL)
	require.Equal(t, prof.ID, f.ProfileID)
	require.Equal(t, "Prod", f.ProfileName, "el nombre lo pone Go desde el perfil")
	require.Equal(t, "shop", f.Database)
	require.False(t, f.ProfileMissing)

	tree, err = svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, tree.Nodes, 2)
	require.Equal(t, "Prod", tree.Nodes[1].ProfileName)

	// Un perfil que no existe se rechaza al guardar y se marca al abrir.
	_, err = svc.Save(ctx, model.SaveQueryRequest{Path: "x", SQL: "1", ProfileID: "nope", RememberConnection: true})
	require.Equal(t, "profileId", detail(t, err))
	require.NoError(t, e.svc.Delete(ctx, prof.ID, false))
	f, err = svc.Open(ctx, ref.Path)
	require.NoError(t, err)
	require.True(t, f.ProfileMissing)
}

// Abrir (y guardar) una consulta nunca envía nada a la base (spec 20 ⚑D7).
func TestOpenNeverExecutes(t *testing.T) {
	ctx := context.Background()
	e := newQueryEnv(t)
	var sent atomic.Int32
	e.drv.OnRun = func(context.Context, string) (driver.RunResult, error) { sent.Add(1); return &fake.Result{}, nil }
	e.drv.OnExec = func(context.Context, string, []any) (int64, error) { sent.Add(1); return 0, nil }
	svc, _ := newQueryFiles(t, e.env)

	ref, err := svc.Save(ctx, model.SaveQueryRequest{Path: "peligro", SQL: "DROP TABLE users;"})
	require.NoError(t, err)
	_, err = svc.Open(ctx, ref.Path)
	require.NoError(t, err)
	_, err = svc.List(ctx)
	require.NoError(t, err)
	_, err = svc.Search(ctx, model.QuerySearchRequest{Text: "drop", Content: true})
	require.NoError(t, err)
	require.Zero(t, sent.Load(), "el driver no recibió sentencias")
}

// Guardar un DROP en un perfil de solo lectura es posible, pero ejecutarlo sigue rechazándose antes de enviarse.
func TestSavedQueryStillHitsReadOnlyGuard(t *testing.T) {
	ctx := context.Background()
	e := newQueryEnv(t, func(p *model.ConnectionProfile) { p.ReadOnly = true })
	var sent []string
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		sent = append(sent, stmt)
		return &fake.Result{}, nil
	}
	svc, _ := newQueryFiles(t, e.env)
	profiles, err := e.svc.List(ctx)
	require.NoError(t, err)

	ref, err := svc.Save(ctx, model.SaveQueryRequest{Path: "limpiar", SQL: "DROP TABLE t", ProfileID: profiles[0].ID, RememberConnection: true})
	require.NoError(t, err, "guardar no es SQL")
	f, err := svc.Open(ctx, ref.Path)
	require.NoError(t, err)

	_, err = e.q.Execute(ctx, ExecuteRequest{SessionID: e.sid, TabID: "ro", SQL: f.SQL})
	ae := apperr.From(err)
	require.Equal(t, apperr.QueryFailed, ae.Code)
	require.Equal(t, "readonly", ae.Detail)
	require.Empty(t, sent)
}

func TestPickRootDoesNotMoveFiles(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	other := filepath.Join(t.TempDir(), "Otra")
	var picked string
	svc, root := newQueryFiles(t, e, func(d *QueryFilesDeps) {
		d.PickDir = func(string) (string, error) { return picked, nil }
	})

	_, err := svc.Save(ctx, model.SaveQueryRequest{Path: "uno", SQL: "SELECT 1"})
	require.NoError(t, err)

	picked = ""
	got, err := svc.PickRoot(ctx)
	require.NoError(t, err)
	require.Nil(t, got, "cancelar el diálogo no cambia nada")

	picked = other
	got, err = svc.PickRoot(ctx)
	require.NoError(t, err)
	require.False(t, got.IsDefault)
	tree, err := svc.List(ctx)
	require.NoError(t, err)
	require.Empty(t, tree.Nodes, "la carpeta nueva está vacía")
	require.FileExists(t, filepath.Join(root, "uno.sql"), "la anterior sigue intacta")
	require.NoFileExists(t, filepath.Join(other, "uno.sql"))

	// Guardar ahora va a la carpeta nueva.
	_, err = svc.Save(ctx, model.SaveQueryRequest{Path: "dos", SQL: "SELECT 2"})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(other, "dos.sql"))

	r, err := svc.ResetRoot(ctx)
	require.NoError(t, err)
	require.True(t, r.IsDefault)
	tree, err = svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, tree.Nodes, 1)
	require.Equal(t, "uno.sql", tree.Nodes[0].Path)

	// Raíces peligrosas.
	for _, bad := range []string{"relativa", string(filepath.Separator)} {
		picked = bad
		_, err = svc.PickRoot(ctx)
		require.Equal(t, "root", detail(t, err), bad)
	}
}

func TestPickRootRejectsHomeAndUnsupported(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	home := t.TempDir()
	svc, _ := newQueryFiles(t, e, func(d *QueryFilesDeps) {
		d.Home = home
		d.PickDir = func(string) (string, error) { return home, nil }
	})
	_, err := svc.PickRoot(ctx)
	require.Equal(t, "root", detail(t, err))

	svc, _ = newQueryFiles(t, e)
	_, err = svc.PickRoot(ctx)
	require.Equal(t, apperr.Unsupported, apperr.From(err).Code)
	require.Equal(t, apperr.Unsupported, apperr.From(svc.Reveal(ctx, "")).Code)
}

func TestRevealResolvesInsideRoot(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var gotPath string
	var gotDir bool
	svc, root := newQueryFiles(t, e, func(d *QueryFilesDeps) {
		d.Reveal = func(p string, isDir bool) error { gotPath, gotDir = p, isDir; return nil }
	})
	_, err := svc.Save(ctx, model.SaveQueryRequest{Path: "a/q", SQL: "x"})
	require.NoError(t, err)

	require.NoError(t, svc.Reveal(ctx, ""))
	real, _ := filepath.EvalSymlinks(root)
	require.Equal(t, real, gotPath)
	require.True(t, gotDir)
	require.NoError(t, svc.Reveal(ctx, "a/q"))
	require.Equal(t, filepath.Join(real, "a", "q.sql"), gotPath)
	require.False(t, gotDir)
	require.Error(t, svc.Reveal(ctx, "../fuera"))
}

func TestMoveAndDeleteThroughService(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	svc, root := newQueryFiles(t, e)
	_, err := svc.Save(ctx, model.SaveQueryRequest{Path: "a/q", SQL: "x"})
	require.NoError(t, err)
	ch, err := svc.Move(ctx, "a", "b")
	require.NoError(t, err)
	require.Equal(t, []model.PathChange{{From: "a/q.sql", To: "b/q.sql"}}, ch)
	require.NoError(t, svc.Delete(ctx, "b/q.sql"))
	require.NoFileExists(t, filepath.Join(root, "b", "q.sql"))
	require.DirExists(t, filepath.Join(root, ".papelera"))
	svc.PurgeTrash(ctx) // no falla ni borra lo reciente
	require.DirExists(t, filepath.Join(root, ".papelera"))
}

func TestPurgeTrashDoesNotCreateTheRoot(t *testing.T) {
	e := newEnv(t)
	svc, root := newQueryFiles(t, e)
	svc.PurgeTrash(context.Background())
	require.NoDirExists(t, root, "arrancar la app no crea la carpeta (evita pedir permisos de Documentos)")
}

// El contenido de una consulta (puede llevar literales sensibles) nunca va a los logs (regla de oro 5).
func TestQueryFilesLogsNoSQL(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })

	ctx := context.Background()
	e := newEnv(t)
	svc, root := newQueryFiles(t, e)
	const secret = "clave-super-secreta-42"
	ref, err := svc.Save(ctx, model.SaveQueryRequest{Path: "q", SQL: "UPDATE u SET pass = '" + secret + "'"})
	require.NoError(t, err)
	_, err = svc.Open(ctx, ref.Path)
	require.NoError(t, err)
	_, err = svc.Search(ctx, model.QuerySearchRequest{Text: secret, Content: true})
	require.NoError(t, err)
	// Un fallo de E/S tampoco filtra el contenido.
	require.NoError(t, os.Chmod(filepath.Join(root, "q.sql"), 0o000))
	_, _ = svc.Open(ctx, ref.Path)
	_ = os.Chmod(filepath.Join(root, "q.sql"), 0o600)

	require.NotEmpty(t, buf.String(), "el log de depuración sí registra la operación")
	require.False(t, strings.Contains(buf.String(), secret), buf.String())
}
