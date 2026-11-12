//go:build integration

package services

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/monitor"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/store"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
	"path/filepath"
)

// TestMonitorOwnStatementsMatchServerDigestsIntegration ejecuta cada lectura del monitor contra un servidor real y
// comprueba que el texto normalizado que el SERVIDOR guarda de cada una la reconoce monitor.IsOwnStatement (⚑D16). Es la
// prueba de que la lista cerrada no se desfasa de lo que MySQL/MariaDB escriben en DIGEST_TEXT.
func TestMonitorOwnStatementsMatchServerDigestsIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "app.db"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = st.Close() })
			sessions := session.NewManager(session.Options{})
			t.Cleanup(sessions.CloseAll)
			p, err := st.SaveProfile(ctx, model.ConnectionProfile{Name: "own", Driver: "mysql", Host: in.Host, Port: in.Port,
				User: mysqlbox.User, Database: mysqlbox.Database, TLS: model.TLSConfig{Mode: "preferred"}})
			require.NoError(t, err)
			sess, err := sessions.Open(ctx, session.OpenRequest{Profile: p, Password: mysqlbox.Password})
			require.NoError(t, err)
			r, ok := sess.Pool.(driver.ActivityReader)
			require.True(t, ok)

			caps, err := r.Probe(ctx)
			require.NoError(t, err)
			if caps.Digest.State == model.SourceUnavailable {
				t.Skipf("%s: digest no disponible (%s)", image, caps.Digest.Reason)
			}
			// Las lecturas del bucle de muestreo, dos veces para que el digest exista y tenga delta.
			for i := 0; i < 2; i++ {
				_, err = r.GlobalStatus(ctx, monitor.StatusNames())
				require.NoError(t, err)
				_, err = r.Processes(ctx)
				require.NoError(t, err)
				_, err = r.MaxConnections(ctx)
				require.NoError(t, err)
				list, err := r.Digests(ctx)
				require.NoError(t, err)
				var ds []string
				for _, c := range list {
					if c.Digest != "" {
						ds = append(ds, c.Digest)
					}
				}
				texts, err := r.DigestTexts(ctx, ds)
				require.NoError(t, err)
				if i == 0 {
					continue
				}
				var own, other []string
				for _, tx := range texts {
					u := strings.ToUpper(strings.TrimSpace(strings.ReplaceAll(tx, "`", "")))
					fromMonitor := strings.HasPrefix(u, "SET SESSION") || strings.HasPrefix(u, "SHOW GLOBAL STATUS WHERE") ||
						strings.HasPrefix(u, "SELECT ID ,") || strings.HasPrefix(u, "SELECT SCHEMA_NAME ,") ||
						strings.HasPrefix(u, "SELECT DIGEST ,") || strings.HasPrefix(u, "SELECT CONNECTION_ID") ||
						strings.HasPrefix(u, "SELECT @@GLOBAL") || strings.HasPrefix(u, "SELECT ? FROM PERFORMANCE_SCHEMA") ||
						strings.HasPrefix(u, "SELECT STATEMENT_DIGEST") // sonda de ProcessDigest (⚑D17)
					if fromMonitor {
						own = append(own, tx)
						require.True(t, monitor.IsOwnStatement(tx), "el servidor guardó %q y IsOwnStatement no lo reconoce", tx)
					} else {
						other = append(other, tx)
						require.False(t, monitor.IsOwnStatement(tx), "falso positivo: %q", tx)
					}
				}
				sort.Strings(own)
				for _, o := range own {
					t.Logf("%s propio: %s", image, o)
				}
				require.GreaterOrEqual(t, len(own), 6, "el monitor deja al menos 6 formas distintas en el digest")
				sort.Strings(other)
				for _, o := range other {
					t.Logf("%s resto: %.150s", image, o)
				}
				t.Logf("%s: %d propios, %d del resto", image, len(own), len(other))
			}
		})
	}
}
