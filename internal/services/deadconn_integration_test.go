//go:build integration

package services

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

// Reproduce el fallo «sql: connection is already closed»: la conexión que la sesión tiene guardada para la pestaña está
// cerrada (lo que queda tras cancelar a la fuerza una consulta). La siguiente ejecución debe funcionar sola.
func TestDeadTabConnectionIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			qid, done := e.mustRun(t, "tab", "SELECT 1", func(r *ExecuteRequest) { r.Database = mysqlbox.Database })
			require.EqualValues(t, 1, done.Results[0].RowCount)
			_ = qid

			sess, err := e.q.d.Sessions.Get(e.sid)
			require.NoError(t, err)
			conn, ok := sess.TabConn("tab")
			require.True(t, ok)
			require.NoError(t, conn.Release()) // la conexión cacheada de la pestaña queda cerrada, sin que la sesión lo sepa

			qid, done = e.mustRun(t, "tab", "SELECT DATABASE(), 2", func(r *ExecuteRequest) { r.Database = mysqlbox.Database })
			require.EqualValues(t, 1, done.Results[0].RowCount)
			w, err := e.q.GetRows(qid, 0, 0, 1)
			require.NoError(t, err)
			require.Equal(t, mysqlbox.Database, w.Rows[0][0].V, "la base elegida se volvió a seleccionar en la conexión nueva")
		})
	}
}
