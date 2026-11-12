package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/vitacodesas/dbacus/internal/model"
)

// Monitoreo DBA (spec 22): implementa driver.ActivityReader sobre la conexión `mon` del pool. Todo es de solo lectura.
const (
	// Tope por llamada: una muestra no puede colgar la única conexión del monitor.
	monitorTimeout = 5 * time.Second
	// Truncados de lo que llega a memoria/SQLite (spec 22 ⚑D7 y «Almacenamiento»).
	infoMaxBytes       = 2048
	digestTextMaxBytes = 4096
	digestTextBatch    = 200

	kindMySQL   = 1
	kindMariaDB = 2
)

// Sentencias fijas del monitor. TestMonitorSQLIsReadOnlySafe comprueba que todas pasan sqlsplit.IsReadOnlySafe.
const (
	sqlProcesslist = `SELECT ID, USER, HOST, DB, COMMAND, TIME, STATE, INFO FROM INFORMATION_SCHEMA.PROCESSLIST WHERE ID <> CONNECTION_ID()`
	// Con el hash del digest de cada consulta en curso (MySQL 8.0+). Se calcula en el servidor sobre el INFO completo y solo sale el hash.
	sqlProcesslistDigest   = `SELECT ID, USER, HOST, DB, COMMAND, TIME, STATE, INFO, STATEMENT_DIGEST(INFO) FROM INFORMATION_SCHEMA.PROCESSLIST WHERE ID <> CONNECTION_ID()`
	sqlProcessDigestProbe  = `SELECT STATEMENT_DIGEST('SELECT 1')`
	sqlProcesslistFallback = `SHOW FULL PROCESSLIST`
	sqlConnectionID        = `SELECT CONNECTION_ID()`
	sqlMaxConnections      = `SELECT @@global.max_connections`
	// Sin DIGEST_TEXT: el texto se pide aparte solo para los digests nuevos. NO_INDEX y NO_GOOD_INDEX se suman en Go (uint64).
	sqlDigests = `SELECT SCHEMA_NAME, DIGEST, COUNT_STAR, SUM_TIMER_WAIT, MAX_TIMER_WAIT, SUM_ROWS_EXAMINED, SUM_ROWS_SENT, ` +
		`SUM_NO_INDEX_USED, SUM_NO_GOOD_INDEX_USED, SUM_CREATED_TMP_DISK_TABLES, SUM_ERRORS ` +
		`FROM performance_schema.events_statements_summary_by_digest`

	sqlVersion        = `SELECT VERSION()`
	sqlCurrentUser    = `SELECT CURRENT_USER()`
	sqlServerUUID     = `SELECT @@server_uuid`
	sqlServerID       = `SELECT @@server_id`
	sqlStatusProbe    = `SHOW GLOBAL STATUS LIKE 'Uptime'`
	sqlDistinctUsers  = `SELECT COUNT(DISTINCT USER) FROM INFORMATION_SCHEMA.PROCESSLIST`
	sqlHasProcessPriv = `SELECT COUNT(*) FROM INFORMATION_SCHEMA.USER_PRIVILEGES WHERE PRIVILEGE_TYPE = 'PROCESS' AND GRANTEE = ?`
	sqlPerfSchemaOn   = `SELECT @@performance_schema`
	sqlDigestProbe    = `SELECT 1 FROM performance_schema.events_statements_summary_by_digest LIMIT 1`
	sqlDigestConsumer = `SELECT ENABLED FROM performance_schema.setup_consumers WHERE NAME = 'statements_digest'`

	// Preparación de la conexión (no son consultas del monitor: fijan el modo de la sesión).
	sqlSetReadOnly       = `SET SESSION TRANSACTION READ ONLY`
	sqlSetTimeoutMySQL   = `SET SESSION max_execution_time = 5000` // ms; solo afecta a SELECT
	sqlSetTimeoutMariaDB = `SET SESSION max_statement_time = 5`    // s
)

var statusNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// statusQuery arma SHOW GLOBAL STATUS WHERE Variable_name IN (…). SHOW no admite parámetros, así que los nombres se
// validan (solo identificadores) y se entregan como literales.
func statusQuery(names []string) (string, error) {
	if len(names) == 0 {
		return "", errors.New("sin variables de estado")
	}
	q := make([]string, len(names))
	for i, n := range names {
		if !statusNameRe.MatchString(n) {
			return "", fmt.Errorf("nombre de variable de estado inválido %q", n)
		}
		q[i] = "'" + n + "'"
	}
	return "SHOW GLOBAL STATUS WHERE Variable_name IN (" + strings.Join(q, ", ") + ")", nil
}

func digestTextsQuery(n int) string {
	return "SELECT DIGEST, DIGEST_TEXT FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST IN (" +
		strings.TrimSuffix(strings.Repeat("?, ", n), ", ") + ")"
}

// isPrivilegeError reconoce «acceso denegado» a un objeto (no un fallo de autenticación, 1045).
func isPrivilegeError(err error) bool {
	var me *gomysql.MySQLError
	if !errors.As(err, &me) {
		return false
	}
	switch me.Number {
	case 1044, 1142, 1143, 1227, 1370:
		return true
	}
	return false
}

// isUnknownObject: tabla o variable inexistente en este motor/versión.
func isUnknownObject(err error) bool {
	var me *gomysql.MySQLError
	if !errors.As(err, &me) {
		return false
	}
	switch me.Number {
	case 1146, 1109, 1193, 1054:
		return true
	}
	return false
}

// truncateUTF8 recorta a max bytes sin partir un carácter.
func truncateUTF8(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// withMon entrega la conexión única del monitor lista para leer: siempre de solo lectura (aunque el perfil no lo sea) y
// con el tope de ejecución del servidor. Falla cerrado si no puede fijar el modo de solo lectura. El tope es «mejor
// esfuerzo» (MySQL < 5.7.8 no tiene max_execution_time): el timeout del contexto es el límite real.
func (p *pool) withMon(ctx context.Context, fn func(ctx context.Context, c *sql.Conn) error) error {
	ctx, cancel := context.WithTimeout(ctx, monitorTimeout)
	defer cancel()
	c, err := p.mon.Conn(ctx)
	if err != nil {
		return mapError(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, sqlSetReadOnly); err != nil {
		return mapError(err)
	}
	set := sqlSetTimeoutMySQL
	if p.monitorKind(ctx, c) == kindMariaDB {
		set = sqlSetTimeoutMariaDB
	}
	_, _ = c.ExecContext(ctx, set)
	return fn(ctx, c)
}

func (p *pool) monitorKind(ctx context.Context, c *sql.Conn) int {
	p.monMu.Lock()
	defer p.monMu.Unlock()
	if p.monKind != 0 {
		return p.monKind
	}
	var v string
	if err := c.QueryRowContext(ctx, sqlVersion).Scan(&v); err != nil {
		return kindMySQL // sin detectar: se reintenta la próxima vez
	}
	p.monKind = kindMySQL
	if strings.Contains(strings.ToLower(v), "mariadb") {
		p.monKind = kindMariaDB
	}
	return p.monKind
}

// grantTarget convierte CURRENT_USER() (`usuario@host`, sin comillas) en `'usuario'@'host'` para el texto de los Hint y
// para comparar con GRANTEE.
func grantTarget(currentUser string) string {
	user, host := currentUser, "%"
	if i := strings.LastIndex(currentUser, "@"); i >= 0 {
		user, host = currentUser[:i], currentUser[i+1:]
	}
	q := func(s string) string {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", "''") + "'"
	}
	return q(user) + "@" + q(host)
}

func (p *pool) Probe(ctx context.Context) (model.MonitorCaps, error) {
	var caps model.MonitorCaps
	err := p.withMon(ctx, func(ctx context.Context, c *sql.Conn) error {
		if err := c.QueryRowContext(ctx, sqlVersion).Scan(&caps.ServerVersion); err != nil {
			return mapError(err)
		}
		caps.IsMariaDB = strings.Contains(strings.ToLower(caps.ServerVersion), "mariadb")
		var cur string
		if err := c.QueryRowContext(ctx, sqlCurrentUser).Scan(&cur); err != nil {
			return mapError(err)
		}
		target := grantTarget(cur)
		caps.ServerID = serverID(ctx, c)
		caps.Status = probeStatus(ctx, c, target)
		caps.Processlist = probeProcesslist(ctx, c, cur, target)
		caps.Digest = probeDigest(ctx, c, target)
		caps.ProcessDigest = p.probeProcessDigest(ctx, c)
		return nil
	})
	return caps, err
}

// probeProcessDigest comprueba STATEMENT_DIGEST (MySQL 8.0+; MariaDB y 5.7 no la tienen) y lo recuerda para Processes.
func (p *pool) probeProcessDigest(ctx context.Context, c *sql.Conn) model.SourceState {
	var d sql.NullString
	err := c.QueryRowContext(ctx, sqlProcessDigestProbe).Scan(&d)
	if err == nil && d.Valid {
		p.procDigest.Store(1)
		return model.SourceState{State: model.SourceOK, Reason: model.ReasonOK}
	}
	if err == nil || isMissingFunction(err) {
		p.procDigest.Store(-1)
		return unavailable(model.ReasonUnsupported, "Este servidor no tiene STATEMENT_DIGEST (MySQL 8.0+): el ranking no podrá mostrar quién ejecutó cada consulta.")
	}
	return unavailable(model.ReasonError, "")
}

// isMissingFunction: la función o la sintaxis no existen en este servidor (1305 FUNCTION does not exist, 1064 sintaxis, 1630).
func isMissingFunction(err error) bool {
	var me *gomysql.MySQLError
	return errors.As(err, &me) && (me.Number == 1305 || me.Number == 1064 || me.Number == 1630)
}

func serverID(ctx context.Context, c *sql.Conn) string {
	var s sql.NullString
	if err := c.QueryRowContext(ctx, sqlServerUUID).Scan(&s); err == nil && s.String != "" {
		return s.String
	}
	var n sql.NullString
	if err := c.QueryRowContext(ctx, sqlServerID).Scan(&n); err == nil {
		return "id:" + n.String
	}
	return ""
}

func unavailable(reason, hint string) model.SourceState {
	return model.SourceState{State: model.SourceUnavailable, Reason: reason, Hint: hint}
}

// classify traduce el error de una sonda al estado de la fuente.
func classify(err error, grantHint string) model.SourceState {
	switch {
	case isPrivilegeError(err):
		return unavailable(model.ReasonNoPrivilege, grantHint)
	case isUnknownObject(err):
		return unavailable(model.ReasonUnsupported, "Este servidor no expone esta fuente.")
	default:
		return unavailable(model.ReasonError, "")
	}
}

func probeStatus(ctx context.Context, c *sql.Conn, target string) model.SourceState {
	rows, err := c.QueryContext(ctx, sqlStatusProbe)
	if err != nil {
		return classify(err, "GRANT SELECT ON performance_schema.* TO "+target+";")
	}
	defer rows.Close()
	if !rows.Next() {
		return unavailable(model.ReasonError, "")
	}
	return model.SourceState{State: model.SourceOK, Reason: model.ReasonOK}
}

func probeProcesslist(ctx context.Context, c *sql.Conn, cur, target string) model.SourceState {
	hint := "GRANT PROCESS ON *.* TO " + target + ";"
	var users int
	if err := c.QueryRowContext(ctx, sqlDistinctUsers).Scan(&users); err != nil {
		// Sin la tabla (versiones nuevas que la retiren) se prueba SHOW FULL PROCESSLIST, que es lo que usa Processes.
		rows, err2 := c.QueryContext(ctx, sqlProcesslistFallback)
		if err2 != nil {
			return classify(err2, hint)
		}
		_ = rows.Close()
	}
	// ¿PROCESS concedido? Un rol activo no aparece en USER_PRIVILEGES, por eso también cuenta ver a más de un usuario.
	var granted int
	_ = c.QueryRowContext(ctx, sqlHasProcessPriv, target).Scan(&granted)
	if granted > 0 || users > 1 {
		return model.SourceState{State: model.SourceOK, Reason: model.ReasonOK}
	}
	return model.SourceState{State: model.SourceDegraded, Reason: model.ReasonOwnOnly, Hint: hint}
}

func probeDigest(ctx context.Context, c *sql.Conn, target string) model.SourceState {
	var on sql.NullInt64
	if err := c.QueryRowContext(ctx, sqlPerfSchemaOn).Scan(&on); err != nil {
		return classify(err, "")
	}
	if on.Int64 == 0 {
		return unavailable(model.ReasonPerfSchemaOff,
			"performance_schema está desactivado. Se activa en el archivo de configuración del servidor (performance_schema=ON) y requiere reiniciarlo.")
	}
	rows, err := c.QueryContext(ctx, sqlDigestProbe)
	if err != nil {
		return classify(err, "GRANT SELECT ON performance_schema.* TO "+target+";")
	}
	_ = rows.Close()
	var enabled sql.NullString
	if err := c.QueryRowContext(ctx, sqlDigestConsumer).Scan(&enabled); err == nil && strings.EqualFold(enabled.String, "NO") {
		return unavailable(model.ReasonConsumerOff,
			"El consumidor de digests está apagado. Se enciende con UPDATE performance_schema.setup_consumers SET ENABLED = 'YES' "+
				"WHERE NAME IN ('global_instrumentation', 'statements_digest'); no sobrevive a un reinicio del servidor.")
	}
	return model.SourceState{State: model.SourceOK, Reason: model.ReasonOK}
}

func (p *pool) Processes(ctx context.Context) ([]model.Process, error) {
	var out []model.Process
	err := p.withMon(ctx, func(ctx context.Context, c *sql.Conn) error {
		tryDigest := p.procDigest.Load() >= 0
		if tryDigest {
			rows, err := c.QueryContext(ctx, sqlProcesslistDigest)
			if err == nil {
				out, err = scanProcesses(rows, 0, true)
			}
			if err == nil {
				p.procDigest.Store(1)
				return nil
			}
			if ctx.Err() != nil {
				return mapError(err)
			}
			// Solo se recuerda «no existe» si el servidor lo dice; un tiempo agotado u otro fallo pasajero reintenta la próxima vez.
			if isMissingFunction(err) {
				p.procDigest.Store(-1)
			}
		}
		rows, err := c.QueryContext(ctx, sqlProcesslist)
		if err == nil {
			out, err = scanProcesses(rows, 0, false)
		}
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return mapError(err)
		}
		// Respaldo: SHOW FULL PROCESSLIST (la tabla puede estar deprecada o restringida). Sin WHERE: se quita la
		// conexión del monitor en Go.
		var self uint64
		if e := c.QueryRowContext(ctx, sqlConnectionID).Scan(&self); e != nil {
			return mapError(e)
		}
		rows, e := c.QueryContext(ctx, sqlProcesslistFallback)
		if e != nil {
			return mapError(e)
		}
		out, e = scanProcesses(rows, self, false)
		return mapError(e)
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TimeSec != out[j].TimeSec {
			return out[i].TimeSec > out[j].TimeSec
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func scanProcesses(rows *sql.Rows, skipID uint64, withDigest bool) ([]model.Process, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []model.Process{}
	for rows.Next() {
		var (
			p                                model.Process
			user, host, db, cmd, state, info sql.NullString
			digest                           sql.NullString
			t                                sql.NullInt64
		)
		dest := []any{&p.ID, &user, &host, &db, &cmd, &t, &state, &info}
		if withDigest {
			dest = append(dest, &digest)
		}
		// SHOW FULL PROCESSLIST de MariaDB añade una novena columna (Progress); se ignora.
		for len(dest) < len(cols) {
			dest = append(dest, new(sql.RawBytes))
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if skipID != 0 && p.ID == skipID {
			continue
		}
		p.User, p.Host, p.DB, p.Command, p.State = user.String, host.String, db.String, cmd.String, state.String
		p.TimeSec = t.Int64
		p.Digest = digest.String
		p.Info, p.InfoTruncated = truncateUTF8(info.String, infoMaxBytes)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (p *pool) GlobalStatus(ctx context.Context, names []string) (map[string]uint64, error) {
	q, err := statusQuery(names)
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint64, len(names))
	err = p.withMon(ctx, func(ctx context.Context, c *sql.Conn) error {
		rows, err := c.QueryContext(ctx, q)
		if err != nil {
			return mapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var name, val sql.NullString
			if err := rows.Scan(&name, &val); err != nil {
				return mapError(err)
			}
			// ON/OFF, claves públicas y decimales no son contadores: se omiten, no son error.
			if v, perr := strconv.ParseUint(val.String, 10, 64); perr == nil {
				out[name.String] = v
			}
		}
		return mapError(rows.Err())
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (p *pool) MaxConnections(ctx context.Context) (uint64, error) {
	var n uint64
	err := p.withMon(ctx, func(ctx context.Context, c *sql.Conn) error {
		return mapError(c.QueryRowContext(ctx, sqlMaxConnections).Scan(&n))
	})
	return n, err
}

func (p *pool) Digests(ctx context.Context) ([]model.DigestCounters, error) {
	var out []model.DigestCounters
	err := p.withMon(ctx, func(ctx context.Context, c *sql.Conn) error {
		rows, err := c.QueryContext(ctx, sqlDigests)
		if err != nil {
			return mapError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				d              model.DigestCounters
				schema, digest sql.NullString
				noIdx, noGood  uint64
			)
			if err := rows.Scan(&schema, &digest, &d.Execs, &d.TimerPs, &d.MaxTimerPs, &d.RowsExamined, &d.RowsSent,
				&noIdx, &noGood, &d.TmpDiskTables, &d.Errors); err != nil {
				return mapError(err)
			}
			d.Schema, d.Digest, d.NoIndex = schema.String, digest.String, noIdx+noGood
			out = append(out, d)
		}
		return mapError(rows.Err())
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (p *pool) DigestTexts(ctx context.Context, digests []string) (map[string]string, error) {
	out := make(map[string]string, len(digests))
	if len(digests) == 0 {
		return out, nil
	}
	err := p.withMon(ctx, func(ctx context.Context, c *sql.Conn) error {
		for start := 0; start < len(digests); start += digestTextBatch {
			end := min(start+digestTextBatch, len(digests))
			args := make([]any, 0, end-start)
			for _, d := range digests[start:end] {
				args = append(args, d)
			}
			rows, err := c.QueryContext(ctx, digestTextsQuery(len(args)), args...)
			if err != nil {
				return mapError(err)
			}
			for rows.Next() {
				var d, text sql.NullString
				if err := rows.Scan(&d, &text); err != nil {
					_ = rows.Close()
					return mapError(err)
				}
				if _, seen := out[d.String]; !seen { // el mismo digest puede estar en varias bases: basta una
					out[d.String], _ = truncateUTF8(text.String, digestTextMaxBytes)
				}
			}
			err = rows.Err()
			_ = rows.Close()
			if err != nil {
				return mapError(err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
