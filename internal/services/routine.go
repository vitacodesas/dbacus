package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/vitacodesas/dbacus/internal/alter"
	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/schemacache"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/sqlsplit"
)

const (
	routineMaxSQL = 1 << 20 // 1 MiB
	tmpPrefix     = "__dbacus_tmp_"
)

var systemDatabases = map[string]bool{"mysql": true, "sys": true, "performance_schema": true, "information_schema": true}

type RoutineDeps struct {
	Sessions *session.Manager
	Schema   *SchemaService // para invalidar la lista de rutinas tras aplicar
	Store    EditStore
	History  func(model.HistoryEntry) // opcional: una entrada por sentencia aplicada (spec 07)
}

// RoutineService lista, lee y modifica procedimientos y funciones (spec 21). Editar es DROP + CREATE sin transacción:
// se protege con una pre-validación (CREATE temporal), una restauración automática y la copia en el resultado.
type RoutineService struct {
	d RoutineDeps

	mu       sync.Mutex
	applying map[string]bool
}

func NewRoutineService(d RoutineDeps) *RoutineService {
	return &RoutineService{d: d, applying: map[string]bool{}}
}

func routineReader(sess *session.Session) (driver.RoutineReader, error) {
	r, ok := sess.Pool.Schema().(driver.RoutineReader)
	if !ok {
		return nil, apperr.New(apperr.Unsupported, "Este motor no tiene procedimientos ni funciones en Dbacus")
	}
	return r, nil
}

func validKind(kind string) error {
	if kind != model.KindProcedure && kind != model.KindFunction {
		return apperr.New(apperr.Validation, "Tipo de rutina desconocido").WithDetail("kind")
	}
	return nil
}

// List devuelve las rutinas de una base sin cuerpos, a través de la caché (spec 21, D1). Funciona en solo lectura.
func (s *RoutineService) List(ctx context.Context, sessionID, db string) ([]model.Routine, error) {
	sess, err := s.d.Sessions.Get(sessionID)
	if err != nil {
		return nil, toAppErr(err)
	}
	if err := requireName("database", db); err != nil {
		return nil, err
	}
	rr, err := routineReader(sess)
	if err != nil {
		return nil, err
	}
	out, err := schemacache.Load(ctx, sess.Schema, sess.Context(), schemacache.DefaultTimeout, keyRoutines(db),
		func(c context.Context) ([]model.Routine, error) { return rr.Routines(c, db) })
	return out, toAppErr(err)
}

func fingerprint(d model.RoutineDetail) string {
	sum := sha256.Sum256([]byte(d.CreateSQL + "\x00" + d.SQLMode))
	return hex.EncodeToString(sum[:])
}

func (s *RoutineService) read(ctx context.Context, sess *session.Session, db, kind, name string) (model.RoutineDetail, error) {
	if err := requireName("database", db); err != nil {
		return model.RoutineDetail{}, err
	}
	if err := validKind(kind); err != nil {
		return model.RoutineDetail{}, err
	}
	if strings.TrimSpace(name) == "" {
		return model.RoutineDetail{}, apperr.New(apperr.Validation, "Falta el nombre de la rutina").WithDetail("name")
	}
	rr, err := routineReader(sess)
	if err != nil {
		return model.RoutineDetail{}, err
	}
	d, err := rr.DescribeRoutine(ctx, db, kind, name) // sin caché: lo que se edita tiene que ser lo que hay ahora
	if err != nil {
		return model.RoutineDetail{}, toAppErr(err)
	}
	if d.Params == nil {
		d.Params = []model.RoutineParam{}
	}
	d.Fingerprint = fingerprint(d)
	return d, nil
}

// Get lee el CREATE completo de una rutina, sin caché. Funciona en solo lectura (leer código no escribe nada).
func (s *RoutineService) Get(ctx context.Context, sessionID, db, kind, name string) (model.RoutineDetail, error) {
	sess, err := s.d.Sessions.Get(sessionID)
	if err != nil {
		return model.RoutineDetail{}, toAppErr(err)
	}
	return s.read(ctx, sess, db, kind, name)
}

// Template da el esqueleto de una rutina nueva (Vue no arma SQL).
func (s *RoutineService) Template(kind string) (string, error) {
	switch kind {
	case model.KindProcedure:
		return "CREATE PROCEDURE nueva_rutina()\nBEGIN\n  SELECT 1;\nEND", nil
	case model.KindFunction:
		return "CREATE FUNCTION nueva_rutina() RETURNS INT\nDETERMINISTIC\nBEGIN\n  RETURN 0;\nEND", nil
	}
	return "", validKind(kind)
}

// CallStatement arma el CALL (o SELECT para una función) de una rutina con sus parámetros como marcadores.
func (s *RoutineService) CallStatement(ctx context.Context, sessionID, db, kind, name string) (string, error) {
	sess, err := s.d.Sessions.Get(sessionID)
	if err != nil {
		return "", toAppErr(err)
	}
	d, err := s.read(ctx, sess, db, kind, name)
	if err != nil {
		return "", err
	}
	args := make([]string, 0, len(d.Params))
	for _, p := range d.Params {
		args = append(args, "/* "+p.Name+" "+p.Type+" */ NULL")
	}
	q := sess.Driver.QuoteIdent
	call := q(db) + "." + q(name) + "(" + strings.Join(args, ", ") + ")"
	if kind == model.KindFunction {
		return "SELECT " + call, nil
	}
	return "CALL " + call, nil
}

type routineStep struct {
	role string // set | tmpCreate | tmpDrop | drop | create
	sql  string
}

type routinePlan struct {
	sess    *session.Session
	change  model.RoutineChange
	steps   []routineStep
	backup  string // CREATE original (solo replace)
	preview model.RoutinePreview
}

func (s *RoutineService) guard(ctx context.Context, sessionID string) (*session.Session, error) {
	sess, err := s.d.Sessions.Get(sessionID)
	if err != nil {
		return nil, toAppErr(err)
	}
	if sessionReadOnly(ctx, s.d.Store, sess) {
		return nil, errReadOnly("Esta conexión es de solo lectura: no se pueden crear, modificar ni borrar rutinas")
	}
	return sess, nil
}

func tmpNameFor(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return tmpPrefix + hex.EncodeToString(sum[:4]) // determinista: Preview y Apply generan el mismo SQL (y el mismo hash)
}

// prepareSQL valida el CREATE que escribió el usuario y devuelve su cabecera.
func prepareSQL(ch model.RoutineChange) (sqlsplit.RoutineHead, error) {
	v := func(detail, msg string) error { return apperr.New(apperr.Validation, msg).WithDetail(detail) }
	if strings.TrimSpace(ch.SQL) == "" {
		return sqlsplit.RoutineHead{}, v("empty", "El código de la rutina está vacío")
	}
	if len(ch.SQL) > routineMaxSQL {
		return sqlsplit.RoutineHead{}, v("too_large", "El código de la rutina supera 1 MiB")
	}
	if sqlsplit.HasDelimiterLine(ch.SQL) {
		return sqlsplit.RoutineHead{}, v("delimiter", "Quita las líneas DELIMITER: el servidor no las entiende, Dbacus envía el CREATE completo")
	}
	h := sqlsplit.ParseRoutineHead(ch.SQL)
	if !h.OK {
		return h, v("head", "El texto debe empezar por CREATE PROCEDURE o CREATE FUNCTION")
	}
	if h.Kind != ch.Kind {
		return h, v("name_changed", "El tipo de la rutina no puede cambiar")
	}
	if h.Database != "" && !strings.EqualFold(h.Database, ch.Database) {
		return h, v("database", "El nombre de la rutina apunta a otra base de datos")
	}
	if strings.HasPrefix(h.Name, tmpPrefix) {
		return h, v("name", "El nombre empieza por un prefijo reservado de Dbacus")
	}
	if len(h.Name) > 64 {
		return h, v("name", "El nombre de la rutina supera 64 caracteres")
	}
	return h, nil
}

func (s *RoutineService) plan(ctx context.Context, sessionID string, ch model.RoutineChange) (routinePlan, error) {
	sess, err := s.guard(ctx, sessionID)
	if err != nil {
		return routinePlan{}, err
	}
	if _, err := routineReader(sess); err != nil {
		return routinePlan{}, err
	}
	if err := requireName("database", ch.Database); err != nil {
		return routinePlan{}, err
	}
	if systemDatabases[strings.ToLower(ch.Database)] {
		return routinePlan{}, apperr.New(apperr.Validation, "Las bases de sistema no se modifican desde Dbacus").WithDetail("system")
	}
	if err := validKind(ch.Kind); err != nil {
		return routinePlan{}, err
	}
	q := sess.Driver.QuoteIdent
	qs := sess.Driver.QuoteString
	upper := strings.ToUpper(ch.Kind)
	p := routinePlan{sess: sess, change: ch}
	warns := []model.AlterWarning{}
	warns = append(warns, model.AlterWarning{Code: "no_rollback", Severity: "danger",
		Message: "MySQL confirma el DDL al ejecutarlo: no hay transacción ni vuelta atrás automática más allá de la restauración descrita."})

	switch ch.Op {
	case model.RoutineCreate:
		h, err := prepareSQL(ch)
		if err != nil {
			return routinePlan{}, err
		}
		_ = h
		p.steps = []routineStep{{"create", ch.SQL}}

	case model.RoutineDrop:
		if strings.TrimSpace(ch.Name) == "" {
			return routinePlan{}, apperr.New(apperr.Validation, "Falta el nombre de la rutina").WithDetail("name")
		}
		cur, err := s.read(ctx, sess, ch.Database, ch.Kind, ch.Name)
		if err != nil {
			return routinePlan{}, err
		}
		if ch.Fingerprint != "" && ch.Fingerprint != cur.Fingerprint {
			return routinePlan{}, apperr.New(apperr.Validation, "La rutina cambió desde que se abrió: vuelve a abrirla").WithDetail("stale_model")
		}
		p.backup = cur.CreateSQL
		p.steps = []routineStep{{"drop", "DROP " + upper + " " + q(ch.Database) + "." + q(ch.Name)}}
		warns = append(warns, grantsWarning())

	case model.RoutineReplace:
		h, err := prepareSQL(ch)
		if err != nil {
			return routinePlan{}, err
		}
		if h.Name != ch.Name {
			return routinePlan{}, apperr.New(apperr.Validation, "El nombre de la rutina no puede cambiar: crea otra y borra esta").WithDetail("name_changed")
		}
		cur, err := s.read(ctx, sess, ch.Database, ch.Kind, ch.Name)
		if err != nil {
			return routinePlan{}, err
		}
		if cur.BodyHidden {
			return routinePlan{}, apperr.New(apperr.Validation, "No tienes privilegio para ver el código de esta rutina").WithDetail("body_hidden")
		}
		if ch.Fingerprint != "" && ch.Fingerprint != cur.Fingerprint {
			return routinePlan{}, apperr.New(apperr.Validation, "La rutina cambió desde que se abrió: vuelve a abrirla").WithDetail("stale_model")
		}
		final := ch.SQL
		if ch.RecreateDefiner && h.DefinerStart >= 0 {
			final = final[:h.DefinerStart] + strings.TrimLeft(final[h.DefinerEnd:], " \t\r\n")
			h = sqlsplit.ParseRoutineHead(final)
		}
		tmp := final[:h.NameStart] + q(tmpNameFor(ch.SQL)) + final[h.NameEnd:]
		tmpDrop := "DROP " + upper + " " + q(ch.Database) + "." + q(tmpNameFor(ch.SQL))
		// Se reaplican el sql_mode y la collation de la rutina: con otros valores el cuerpo puede cambiar de significado.
		// El charset del cliente NO se toca: el texto viaja en UTF-8 y declararlo distinto lo corrompería.
		var sets []string
		sets = append(sets, "sql_mode = "+qs(cur.SQLMode))
		if cur.CollationConnection != "" {
			sets = append(sets, "collation_connection = "+qs(cur.CollationConnection))
		}
		p.backup = cur.CreateSQL
		p.steps = []routineStep{
			{"set", "SET SESSION " + strings.Join(sets, ", ")},
			{"tmpCreate", tmp},
			{"tmpDrop", tmpDrop},
			{"drop", "DROP " + upper + " " + q(ch.Database) + "." + q(ch.Name)},
			{"create", final},
		}
		warns = append(warns, grantsWarning(), model.AlterWarning{Code: "temp_routine", Severity: "info",
			Message: "Antes de tocar la original se crea y se borra una rutina temporal para comprobar que el código compila."})
		if w, ok := s.definerWarning(ctx, sess, cur, ch.RecreateDefiner); ok {
			warns = append(warns, w)
		}

	default:
		return routinePlan{}, apperr.New(apperr.Validation, "Operación desconocida").WithDetail("op")
	}

	stmts := make([]string, len(p.steps))
	for i, st := range p.steps {
		stmts[i] = st.sql
	}
	p.preview = model.RoutinePreview{Statements: stmts, SQLHash: alter.Hash(stmts), Warnings: warns,
		Destructive: ch.Op != model.RoutineCreate, NoRollback: true}
	return p, nil
}

func grantsWarning() model.AlterWarning {
	return model.AlterWarning{Code: "grants_lost", Severity: "warning",
		Message: "Al borrar una rutina el servidor elimina también los permisos (GRANT) concedidos sobre ella; Dbacus no los restaura."}
}

// definerWarning avisa si la rutina pertenece a otro usuario: recrearla con ese DEFINER puede fallar sin SUPER/SET_USER_ID.
func (s *RoutineService) definerWarning(ctx context.Context, sess *session.Session, cur model.RoutineDetail, recreate bool) (model.AlterWarning, bool) {
	if recreate || cur.Definer == "" {
		return model.AlterWarning{}, false
	}
	conn, err := sess.Pool.Acquire(ctx)
	if err != nil {
		return model.AlterWarning{}, false
	}
	defer conn.Release()
	res, err := conn.Run(ctx, "SELECT CURRENT_USER()")
	if err != nil {
		return model.AlterWarning{}, false
	}
	defer res.Close()
	if !res.Next() || len(res.Row()) == 0 {
		return model.AlterWarning{}, false
	}
	var me string
	switch v := res.Row()[0].(type) {
	case string:
		me = v
	case []byte:
		me = string(v)
	}
	if me == "" || strings.EqualFold(me, cur.Definer) {
		return model.AlterWarning{}, false
	}
	return model.AlterWarning{Code: "definer_foreign", Severity: "warning",
		Message: "La rutina pertenece a " + cur.Definer + " y tú eres " + me + ": recrearla puede fallar sin privilegios suficientes. Puedes recrearla con tu usuario."}, true
}

// Preview genera el SQL sin ejecutar nada (la revisión muestra exactamente esto). Rechaza en solo lectura.
func (s *RoutineService) Preview(ctx context.Context, sessionID string, ch model.RoutineChange) (model.RoutinePreview, error) {
	p, err := s.plan(ctx, sessionID, ch)
	return p.preview, err
}

// Apply ejecuta el SQL de la Preview con ese sqlHash. Si el CREATE final de un replace falla, restaura la original.
func (s *RoutineService) Apply(ctx context.Context, sessionID string, ch model.RoutineChange, sqlHash string) (model.RoutineResult, error) {
	p, err := s.plan(ctx, sessionID, ch)
	if err != nil {
		return model.RoutineResult{}, err
	}
	if sqlHash != p.preview.SQLHash {
		return model.RoutineResult{}, apperr.New(apperr.Validation, "El SQL cambió desde la revisión: vuelve a revisarlo").WithDetail("stale_preview")
	}

	name := ch.Name
	if ch.Op == model.RoutineCreate {
		name = sqlsplit.ParseRoutineHead(ch.SQL).Name
	}
	key := sessionID + "\x00" + ch.Database + "\x00" + ch.Kind + "\x00" + name
	s.mu.Lock()
	if s.applying[key] {
		s.mu.Unlock()
		return model.RoutineResult{}, apperr.New(apperr.QueryFailed, "Ya se está modificando esta rutina").WithDetail("busy")
	}
	s.applying[key] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.applying, key); s.mu.Unlock() }()

	conn, err := p.sess.Pool.AcquireEdit(ctx) // falla cerrado en solo lectura
	if err != nil {
		return model.RoutineResult{}, toAppErr(err)
	}
	defer conn.Release()
	if err := conn.Use(ctx, ch.Database); err != nil {
		return model.RoutineResult{}, toAppErr(err)
	}
	stop := context.AfterFunc(ctx, func() {
		kctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Kill(kctx)
	})
	defer stop()

	res := model.RoutineResult{Applied: []string{}}
	for i, st := range p.steps {
		started := time.Now()
		_, _, execErr := conn.Exec(ctx, st.sql)
		if st.role == "drop" || st.role == "create" { // el SET y la rutina temporal no son del usuario
			s.record(p.sess, ch.Database, st.sql, started, execErr)
		}
		if execErr == nil {
			if st.role == "drop" || st.role == "create" {
				res.Applied = append(res.Applied, st.sql)
			}
			continue
		}
		res.Failed = &model.AlterFailure{Index: i, Message: apperr.From(toAppErr(execErr)).Message}
		if ctx.Err() != nil {
			res.Failed.Message = "Operación cancelada"
		}
		if st.role == "create" && ch.Op == model.RoutineReplace {
			s.restore(p, conn, &res)
		}
		break
	}
	if len(res.Applied) > 0 && s.d.Schema != nil {
		_ = s.d.Schema.invalidateRoutines(sessionID, ch.Database)
	}
	// Estado real tras el intento; el contexto de la petición puede estar cancelado.
	rctx, cancel := context.WithTimeout(p.sess.Context(), 30*time.Second)
	defer cancel()
	if ch.Op != model.RoutineDrop || res.Failed != nil {
		if d, err := s.read(rctx, p.sess, ch.Database, ch.Kind, name); err == nil {
			res.Detail = &d
		}
	}
	return res, nil
}

// restore recrea la original tras un CREATE fallido. Usa un contexto propio: puede que el de la petición esté cancelado.
func (s *RoutineService) restore(p routinePlan, conn driver.Conn, res *model.RoutineResult) {
	rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	_, _, err := conn.Exec(rctx, p.backup)
	if err == nil {
		res.Restored = true
		s.record(p.sess, p.change.Database, p.backup, started, nil)
		return
	}
	res.BackupSQL = p.backup
	s.record(p.sess, p.change.Database, p.backup, started, err) // la copia queda en el historial
}

func (s *RoutineService) record(sess *session.Session, db, stmt string, started time.Time, err error) {
	if s.d.History == nil {
		return
	}
	e := model.HistoryEntry{
		ProfileID: sess.ProfileID, SQL: stmt, Database: db, ExecutedAt: started.UTC(),
		ElapsedMs: time.Since(started).Milliseconds(), OK: err == nil,
	}
	if err != nil {
		e.ErrorMessage = apperr.From(toAppErr(err)).Message
	}
	s.d.History(e)
}
