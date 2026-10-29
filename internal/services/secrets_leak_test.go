package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/driver/fake"
	"github.com/vitacodesas/dbacus/internal/logging"
)

// Criterio del spec 10: la contraseña de prueba no aparece en app.db, ni en los logs, ni en lo que viaja a Vue
// (retornos de servicios y eventos), tampoco por los caminos de error.
func TestSecretsNeverReachDBLogsOrEvents(t *testing.T) {
	const (
		password = "S3cr3t-Pa$$-7f3a91"
		literal  = "LITERAL-9c1d-no-en-logs" // un valor de negocio dentro del SQL: no debe llegar a logs a nivel info
	)
	ctx := context.Background()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(logging.NewHandler(&logs, slog.LevelInfo)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := newQueryEnv(t)
	profiles, err := e.svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	saved, err := e.svc.Save(ctx, profiles[0], ConnectionSecrets{Password: sp(password)})
	require.NoError(t, err)

	// Lo que Vue recibe de los servicios.
	var wire []any
	wire = append(wire, saved)
	got, err := e.svc.Get(ctx, saved.ID)
	require.NoError(t, err)
	list, err := e.svc.List(ctx)
	require.NoError(t, err)
	wire = append(wire, got, list)

	// Conexión correcta, consulta con un literal y un error del servidor que menciona la contraseña usada.
	info, err := e.svc.Connect(ctx, saved.ID, nil)
	require.NoError(t, err)
	wire = append(wire, info)
	e.drv.OnRun = func(_ context.Context, stmt string) (driver.RunResult, error) {
		if strings.Contains(stmt, "FALLA") {
			return nil, errors.New("Access denied for user 'root' (using password: YES)")
		}
		return &fake.Result{}, nil
	}
	e.execute(t, "leak", "SELECT '"+literal+"'")
	e.next(t, "query:done")
	e.execute(t, "leak", "SELECT 'FALLA'")
	e.next(t, "query:error")

	// Un fallo de conexión con la contraseña guardada.
	e.drv.PingErr = errors.New("dial tcp 127.0.0.1:3306: connect: connection refused")
	_, err = e.svc.Connect(ctx, saved.ID, nil)
	require.Error(t, err)
	wire = append(wire, err.Error())

	// Eventos emitidos hacia Vue durante todo lo anterior.
	for len(e.events) > 0 {
		ev := <-e.events
		wire = append(wire, ev.name, ev.data)
	}
	body, err := json.Marshal(wire)
	require.NoError(t, err)
	require.NotContains(t, string(body), password, "lo que viaja a Vue")

	require.NotContains(t, logs.String(), password, "logs")
	require.NotContains(t, logs.String(), literal, "el SQL con literales no se registra a nivel info")

	for _, f := range []string{e.dbPath, e.dbPath + "-wal"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		require.NotContains(t, string(raw), password, f)
	}
}
