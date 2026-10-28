//go:build integration

package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/model"
	"tabulariswails/internal/session"
	"tabulariswails/internal/testutil/mysqlbox"
)

// El servidor es la garantía real del modo solo lectura (spec 10): aunque algo se colara por el guardia de Go, una
// conexión de un perfil ReadOnly no puede escribir. Se salta QueryService a propósito y se habla con la conexión.
func TestReadOnlyProfileEnforcedByServerIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			in := mysqlbox.Start(t, image)
			sessions := session.NewManager(session.Options{})
			t.Cleanup(sessions.CloseAll)
			open := func(readOnly bool) *session.Session {
				s, err := sessions.Open(context.Background(), session.OpenRequest{
					Profile:  model.ConnectionProfile{ID: "p", Driver: "mysql", Host: in.Host, Port: in.Port, User: mysqlbox.User, Database: mysqlbox.Database, ReadOnly: readOnly, TLS: model.TLSConfig{Mode: "preferred"}},
					Password: mysqlbox.Password,
				})
				require.NoError(t, err)
				return s
			}
			run := func(s *session.Session, tab, stmt string) error {
				c, err := s.AcquireConn(context.Background(), tab)
				require.NoError(t, err)
				r, err := c.Run(context.Background(), stmt)
				if err == nil {
					_ = r.Close()
				}
				return err
			}

			rw := open(false)
			require.NoError(t, run(rw, "setup", "CREATE TABLE ro_probe (id INT PRIMARY KEY)"))
			require.NoError(t, run(rw, "setup", "INSERT INTO ro_probe VALUES (1)"))

			ro := open(true)
			require.NoError(t, run(ro, "a", "SELECT * FROM ro_probe"), "leer sí se puede")
			for _, stmt := range []string{"INSERT INTO ro_probe VALUES (2)", "DELETE FROM ro_probe", "DROP TABLE ro_probe", "CREATE TABLE other (id INT)"} {
				err := run(ro, "a", stmt)
				require.Error(t, err, stmt)
				require.Contains(t, err.Error(), "READ ONLY", stmt)
			}
			require.NoError(t, run(rw, "setup", "SELECT 1 FROM ro_probe"), "la tabla sigue ahí")
		})
	}
}
