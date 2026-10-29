//go:build integration

package services

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/paging"
	"github.com/vitacodesas/dbacus/internal/results"
	"github.com/vitacodesas/dbacus/internal/testutil/mysqlbox"
)

// ten es una tabla derivada de 10 filas: seis en producto cruzado dan un millón sin variables de sesión.
const ten = "(SELECT 0 n UNION ALL SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3 UNION ALL SELECT 4 UNION ALL SELECT 5 UNION ALL SELECT 6 UNION ALL SELECT 7 UNION ALL SELECT 8 UNION ALL SELECT 9)"

// cellText devuelve la celda como texto exacto (enteros, BIGINT enormes y DECIMAL).
func cellText(c results.Cell) string {
	switch v := c.V.(type) {
	case int64:
		return strconv.FormatInt(v, 10)
	case string:
		return v
	}
	return fmt.Sprint(c.V)
}

func (e *liveEnv) col0(t *testing.T, qid string, stmt int) []string {
	t.Helper()
	var out []string
	for off := 0; ; off += 1000 {
		w, err := e.q.GetRows(qid, stmt, off, 1000)
		require.NoError(t, err)
		for _, r := range w.Rows {
			out = append(out, cellText(r[0]))
		}
		if len(w.Rows) < 1000 {
			return out
		}
	}
}

func (e *liveEnv) page(t *testing.T, qid, dir string) StatementResult {
	t.Helper()
	r, err := e.q.Page(context.Background(), PageRequest{QueryID: qid, StmtIndex: 0, Direction: dir})
	require.NoError(t, err)
	return r
}

// TestKeysetPagingIntegration recorre tablas reales de punta a punta, ida y vuelta, y compara cada recorrido con la
// consulta completa sin paginar. Cubre lo que SQLite no puede: DECIMAL exacto, BIGINT UNSIGNED por encima de 2^63 y
// 2^53, y el comportamiento real de cada servidor con empates masivos.
func TestKeysetPagingIntegration(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			e.mustRun(t, "setup", "CREATE TABLE pg (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT, price DECIMAL(12,2) NOT NULL, qty INT NOT NULL, note VARCHAR(20), PRIMARY KEY (id), KEY k_price (price))")
			e.mustRun(t, "setup", "INSERT INTO pg (price, qty, note) SELECT ((d0.n + d1.n*3 + d2.n) % 7) * 12.5, (d1.n + d3.n) % 4, CONCAT('n', d0.n, d1.n, d2.n, d3.n) FROM "+ten+" d0, "+ten+" d1, "+ten+" d2, "+ten+" d3")
			// Ids en los bordes de los tipos: pasan de 2^53 y de 2^63 y llegan al máximo de BIGINT UNSIGNED.
			e.mustRun(t, "setup", "INSERT INTO pg (id, price, qty, note) VALUES (9007199254740993, 12.50, 1, 'a'), (9223372036854775808, 12.50, 1, 'b'), (18446744073709551614, 0.01, 2, 'c'), (18446744073709551615, 99999999.99, 3, 'd')")

			// Los valores delicados tienen que ser el BORDE entre una página y la siguiente: con páginas de una fila, cada
			// uno se reenvía al servidor como literal. Cualquier paso por float los redondea y repite o salta filas.
			e.mustRun(t, "setup", "CREATE TABLE dec_t (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, amt DECIMAL(30,10) NOT NULL)")
			e.mustRun(t, "setup", "INSERT INTO dec_t (amt) VALUES (1234567890123456789.0000000001), (1234567890123456789.0000000002), (1234567890123456789.0000000003), (1234567890123456789.0000000004), (-0.0000000001), (0.0000000000)")
			edge := []struct{ name, sql, oracle string }{
				{"ids por encima de 2^53, 2^63 y hasta 2^64-1", "SELECT * FROM pg WHERE id >= 9007199254740993", "SELECT id FROM pg WHERE id >= 9007199254740993 ORDER BY id"},
				{"los mismos en orden descendente", "SELECT * FROM pg WHERE id >= 9007199254740993 ORDER BY id DESC", "SELECT id FROM pg WHERE id >= 9007199254740993 ORDER BY id DESC"},
			}
			for _, c := range edge {
				t.Run(c.name, func(t *testing.T) {
					oqid, _ := e.mustRun(t, "oracle", c.oracle)
					want := e.col0(t, oqid, 0)
					require.Len(t, want, 4)
					qid, done := e.mustRun(t, "pager", c.sql, func(r *ExecuteRequest) { r.AutoLimit = 1 })
					r := done.Results[0]
					require.Equal(t, "keyset", r.Paging.Mode, r.Paging.Reason)
					got := e.col0(t, qid, 0)
					for guard := 0; r.Paging.HasNext; guard++ {
						require.Less(t, guard, 5000, "el recorrido no termina: el cursor repite filas")
						r = e.page(t, qid, "next")
						got = append(got, e.col0(t, qid, 0)...)
					}
					require.Equal(t, want, got, "cada valor delicado, una vez y sin redondear")
				})
			}
			t.Run("DECIMAL(30,10) que solo difiere en el último dígito", func(t *testing.T) {
				oqid, _ := e.mustRun(t, "oracle", "SELECT amt FROM dec_t ORDER BY amt, id")
				want := e.col0(t, oqid, 0)
				require.Len(t, want, 6)
				for _, order := range []string{"", " ORDER BY amt DESC"} {
					qid, done := e.mustRun(t, "pager", "SELECT amt, id FROM dec_t"+order, func(r *ExecuteRequest) { r.AutoLimit = 1 })
					r := done.Results[0]
					require.Equal(t, "keyset", r.Paging.Mode, r.Paging.Reason)
					seen := map[string]int{}
					for _, v := range e.col0(t, qid, 0) {
						seen[v]++
					}
					for guard := 0; r.Paging.HasNext; guard++ {
						require.Less(t, guard, 5000, "el recorrido no termina: el cursor repite filas")
						r = e.page(t, qid, "next")
						for _, v := range e.col0(t, qid, 0) {
							seen[v]++
						}
					}
					require.Len(t, seen, 6, "las seis filas distintas, ni una repetida ni saltada (orden %q)", order)
					for _, v := range want {
						require.Equal(t, 1, seen[v], "valor %s (orden %q)", v, order)
					}
				}
			})

			cases := []struct{ name, sql, oracle string }{
				{"sin ORDER BY", "SELECT * FROM pg", "SELECT id FROM pg ORDER BY id"},
				{"ORDER BY de un decimal con empates", "SELECT * FROM pg ORDER BY price DESC", "SELECT id FROM pg ORDER BY price DESC, id"},
				{"WHERE y varias columnas con direcciones mixtas", "SELECT * FROM pg WHERE qty >= 1 ORDER BY qty, price DESC", "SELECT id FROM pg WHERE qty >= 1 ORDER BY qty, price DESC, id"},
				{"lista parcial de columnas", "SELECT id, price, qty FROM pg ORDER BY price", "SELECT id FROM pg ORDER BY price, id"},
				{"clave descendente", "SELECT * FROM pg ORDER BY id DESC", "SELECT id FROM pg ORDER BY id DESC"},
			}
			for _, c := range cases {
				oqid, _, of := e.run(t, "oracle", c.oracle, func(r *ExecuteRequest) { r.MaxRows = 100_000 })
				require.Nil(t, of)
				want := e.col0(t, oqid, 0)
				require.Greater(t, len(want), 5000, "la tabla de prueba tiene ~10 000 filas (el WHERE deja unas 7 500)")

				for _, size := range []int{37, 1000} {
					t.Run(fmt.Sprintf("%s/n=%d", c.name, size), func(t *testing.T) {
						qid, done, failed := e.run(t, "pager", c.sql, func(r *ExecuteRequest) { r.AutoLimit = size })
						require.Nil(t, failed)
						r := done.Results[0]
						require.Equal(t, "keyset", r.Paging.Mode, r.Paging.Reason)

						got := e.col0(t, qid, 0)
						pages := 1
						for guard := 0; r.Paging.HasNext; guard++ {
							require.Less(t, guard, 5000, "el recorrido no termina: el cursor repite filas")
							r = e.page(t, qid, "next")
							pages++
							got = append(got, e.col0(t, qid, 0)...)
						}
						require.Equal(t, want, got, "hacia delante: cada fila una vez y en orden")
						require.Equal(t, (len(want)+size-1)/size, pages)

						var back []string
						back = append(back, e.col0(t, qid, 0)...)
						for guard := 0; r.Paging.HasPrev; guard++ {
							require.Less(t, guard, 5000, "el recorrido hacia atrás no termina")
							r = e.page(t, qid, "prev")
							back = append(e.col0(t, qid, 0), back...)
						}
						require.Equal(t, want, back, "hacia atrás desde el final")

						last := e.page(t, qid, "last")
						require.False(t, last.Paging.HasNext)
						require.Equal(t, want[len(want)-min(size, len(want)):], e.col0(t, qid, 0), "«Última» son las últimas filas")
						first := e.page(t, qid, "first")
						require.Equal(t, 1, first.Paging.Page)
						require.Equal(t, want[:size], e.col0(t, qid, 0))
					})
				}
			}
		})
	}
}

// TestKeysetPagingWarnsWhenTheOrderHasNoIndex comprueba contra el servidor real el aviso de EXPLAIN (spec 18 §9).
func TestKeysetPagingWarnsWhenTheOrderHasNoIndex(t *testing.T) {
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			e.mustRun(t, "setup", "CREATE TABLE w (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, qty INT NOT NULL, KEY k_id_qty (qty))")
			e.mustRun(t, "setup", "CREATE TABLE u (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, qty INT NOT NULL)")
			e.mustRun(t, "setup", "INSERT INTO u (qty) SELECT d0.n + d1.n*10 FROM "+ten+" d0, "+ten+" d1")

			_, done, _ := e.run(t, "t1", "SELECT * FROM u ORDER BY qty", func(r *ExecuteRequest) { r.AutoLimit = 10 })
			require.Equal(t, "no_index", done.Results[0].Paging.Warning, "ordenar por una columna sin índice obliga a ordenar en cada página")
			_, done, _ = e.run(t, "t2", "SELECT * FROM u", func(r *ExecuteRequest) { r.AutoLimit = 10 })
			require.Empty(t, done.Results[0].Paging.Warning, "por la clave primaria no hace falta ordenar")
		})
	}
}

// pagingPerfRows son las filas de la tabla de la prueba de rendimiento: 5 M en local (spec 18 §9) y 1 M en CI para no
// gastar minutos ni memoria de los runners. DBACUS_PERF_ROWS lo fija a mano.
func pagingPerfRows(t *testing.T) int {
	if v := os.Getenv("DBACUS_PERF_ROWS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err)
		return n
	}
	if os.Getenv("CI") != "" {
		return 1_000_000
	}
	return 5_000_000
}

func median(ds []time.Duration) time.Duration {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

// timeSQL ejecuta una consulta cruda (sin paginar) varias veces y devuelve la mediana de lo que tarda en estar lista.
func (e *liveEnv) timeSQL(t *testing.T, sqlText string, runs int) time.Duration {
	t.Helper()
	var ds []time.Duration
	for i := 0; i < runs; i++ {
		start := time.Now()
		_, done, failed := e.run(t, "timing", sqlText, func(r *ExecuteRequest) { r.AutoLimit = 0; r.MaxRows = 10_000 })
		require.Nil(t, failed, sqlText)
		require.NotNil(t, done)
		ds = append(ds, time.Since(start))
	}
	return median(ds)
}

func (e *liveEnv) scalar(t *testing.T, sqlText string) []string {
	t.Helper()
	qid, _, failed := e.run(t, "scalar", sqlText)
	require.Nil(t, failed, sqlText)
	w, err := e.q.GetRows(qid, 0, 0, 10)
	require.NoError(t, err)
	require.Len(t, w.Rows, 1)
	out := make([]string, len(w.Rows[0]))
	for i, c := range w.Rows[0] {
		out[i] = cellText(c)
	}
	return out
}

// TestKeysetPagingPerfIntegration mide el criterio de rendimiento del spec 18 §9: una página profunda con cursor cuesta
// casi lo mismo que la primera, y un OFFSET equivalente no. Las cifras se registran con -v para docs/benchmarks.md.
func TestKeysetPagingPerfIntegration(t *testing.T) {
	rows := pagingPerfRows(t)
	const pageSize = 1000
	for _, image := range mysqlbox.Images() {
		t.Run(image, func(t *testing.T) {
			e := newLiveEnv(t, mysqlbox.Start(t, image))
			e.mustRun(t, "setup", "CREATE TABLE big (id BIGINT NOT NULL AUTO_INCREMENT, v BIGINT NOT NULL, pad CHAR(8) NOT NULL, PRIMARY KEY (id), KEY k_v (v))")
			fillStart := time.Now()
			for left := rows; left > 0; left -= 1_000_000 {
				n := min(left, 1_000_000)
				e.mustRun(t, "setup", fmt.Sprintf("INSERT INTO big (v, pad) SELECT FLOOR(RAND()*1000000), 'abcdefgh' FROM %s d0, %s d1, %s d2, %s d3, %s d4, %s d5 LIMIT %d", ten, ten, ten, ten, ten, ten, n))
			}
			e.mustRun(t, "setup", "ANALYZE TABLE big")
			t.Logf("tabla de %d filas creada en %s", rows, time.Since(fillStart).Round(time.Second))

			q := func(n string) string { return "`" + n + "`" }
			byID := paging.Plan{List: "*", From: "`" + mysqlbox.Database + "`.`big`", PageSize: pageSize, Order: []paging.Col{{Name: "id"}}}
			byV := paging.Plan{List: "*", From: "`" + mysqlbox.Database + "`.`big`", PageSize: pageSize, Order: []paging.Col{{Name: "v", Desc: true}, {Name: "id"}}}
			build := func(p paging.Plan, d paging.Dir, edge []string) string {
				s, err := p.Build(d, edge, q)
				require.NoError(t, err)
				return s
			}
			const runs = 5

			firstID := e.timeSQL(t, build(byID, paging.First, nil), runs)
			firstV := e.timeSQL(t, build(byV, paging.First, nil), runs)

			type depth struct {
				name string
				at   int
			}
			var worstKeyID, worstKeyV time.Duration
			for _, d := range []depth{{"50 %", rows / 2}, {"99 %", rows * 99 / 100}} {
				edgeID := e.scalar(t, fmt.Sprintf("SELECT id FROM big ORDER BY id LIMIT 1 OFFSET %d", d.at-1))
				edgeV := e.scalar(t, fmt.Sprintf("SELECT v, id FROM big ORDER BY v DESC, id LIMIT 1 OFFSET %d", d.at-1))

				deepID := e.timeSQL(t, build(byID, paging.Next, edgeID), runs)
				deepV := e.timeSQL(t, build(byV, paging.Next, edgeV), runs)
				offID := e.timeSQL(t, fmt.Sprintf("SELECT * FROM big ORDER BY id LIMIT %d OFFSET %d", pageSize, d.at), runs)
				offV := e.timeSQL(t, fmt.Sprintf("SELECT * FROM big ORDER BY v DESC, id LIMIT %d OFFSET %d", pageSize, d.at), runs)
				t.Logf("profundidad %s · por id: cursor %s, OFFSET %s (×%.0f) · por v desc: cursor %s, OFFSET %s (×%.0f)",
					d.name, deepID.Round(time.Microsecond), offID.Round(time.Microsecond), float64(offID)/float64(deepID),
					deepV.Round(time.Microsecond), offV.Round(time.Microsecond), float64(offV)/float64(deepV))
				worstKeyID, worstKeyV = max(worstKeyID, deepID), max(worstKeyV, deepV)
			}

			// «Última» con el servicio real: una consulta, sin recorrer la tabla.
			qid, done, failed := e.run(t, "pager", "SELECT * FROM big", func(r *ExecuteRequest) { r.AutoLimit = pageSize })
			require.Nil(t, failed)
			require.Equal(t, "keyset", done.Results[0].Paging.Mode)
			lastStart := time.Now()
			last := e.page(t, qid, "last")
			lastTook := time.Since(lastStart)
			require.False(t, last.Paging.HasNext)
			require.EqualValues(t, pageSize, last.RowCount)
			t.Logf("filas %d · primera página: por id %s, por v %s · página profunda más lenta: por id %s, por v %s · «Última» %s",
				rows, firstID.Round(time.Microsecond), firstV.Round(time.Microsecond), worstKeyID.Round(time.Microsecond), worstKeyV.Round(time.Microsecond), lastTook.Round(time.Millisecond))

			// Criterio del spec: ≤ 3× la primera. Por debajo de 20 ms el ruido de la medición domina y no se exige proporción.
			require.LessOrEqual(t, worstKeyID, max(3*firstID, 20*time.Millisecond), "página profunda por id")
			require.LessOrEqual(t, worstKeyV, max(3*firstV, 20*time.Millisecond), "página profunda por v desc")
			require.Less(t, lastTook, max(3*firstID, 50*time.Millisecond), "«Última» cuesta como una página, no como recorrer la tabla")
		})
	}
}
