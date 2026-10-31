package services

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/vitacodesas/dbacus/internal/alter"
	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/driver"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/session"
)

type AlterDeps struct {
	Sessions *session.Manager
	Schema   *SchemaService // para invalidar la caché tras aplicar
	Store    EditStore
	History  func(model.HistoryEntry) // opcional: una entrada por sentencia aplicada (spec 07)
}

// AlterService expone el editor de estructura de tabla (spec 19). Vue envía un ChangeSet de operaciones tipadas; Go lo
// valida, genera el ALTER con el generador del driver y lo ejecuta solo tras una Preview con el mismo SQL.
type AlterService struct {
	d AlterDeps

	mu       sync.Mutex
	applying map[string]bool // sessionID/db/tabla con un Apply en curso
}

func NewAlterService(d AlterDeps) *AlterService {
	return &AlterService{d: d, applying: map[string]bool{}}
}

// plan reúne lo necesario para previsualizar o aplicar: sesión, lectura fresca de la tabla y el SQL generado.
type alterPlan struct {
	sess    *session.Session
	model   model.AlterModel
	preview model.AlterPreview
}

// guard rechaza antes de tocar el servidor: solo lectura (perfil guardado OR sesión). Se repite en Preview y en Apply.
func (s *AlterService) guard(ctx context.Context, sessionID string) (*session.Session, error) {
	sess, err := s.d.Sessions.Get(sessionID)
	if err != nil {
		return nil, toAppErr(err)
	}
	if sessionReadOnly(ctx, s.d.Store, sess) {
		return nil, errReadOnly("Esta conexión es de solo lectura: no se puede modificar la estructura de las tablas")
	}
	return sess, nil
}

func (s *AlterService) readModel(ctx context.Context, sess *session.Session, db, table string) (model.AlterModel, error) {
	if err := requireName("database", db); err != nil {
		return model.AlterModel{}, err
	}
	if err := requireName("table", table); err != nil {
		return model.AlterModel{}, err
	}
	d, err := sess.Pool.Schema().Describe(ctx, db, table) // sin caché: lo que se edita tiene que ser lo que hay ahora
	if err != nil {
		return model.AlterModel{}, toAppErr(err)
	}
	if d.Type == "view" {
		return model.AlterModel{}, apperr.New(apperr.Validation, "Una vista no tiene estructura editable").WithDetail("view")
	}
	return model.AlterModel{
		Database: db, Table: table, Columns: d.Columns, Indexes: d.Indexes, ForeignKeys: d.ForeignKeys, IncomingFKs: d.IncomingFKs,
		CreateSQL: d.CreateSQL, TableCollation: d.Collation, Fingerprint: alter.Fingerprint(d.CreateSQL), RowEstimate: d.RowEstimate,
	}, nil
}

// GetModel lee la tabla sin caché. Un perfil de solo lectura no puede abrir el editor.
func (s *AlterService) GetModel(ctx context.Context, sessionID, database, table string) (model.AlterModel, error) {
	sess, err := s.guard(ctx, sessionID)
	if err != nil {
		return model.AlterModel{}, err
	}
	return s.readModel(ctx, sess, database, table)
}

func (s *AlterService) plan(ctx context.Context, sessionID string, cs model.ChangeSet) (alterPlan, error) {
	sess, err := s.guard(ctx, sessionID)
	if err != nil {
		return alterPlan{}, err
	}
	gen, ok := sess.Driver.(driver.DDLGenerator)
	if !ok {
		return alterPlan{}, apperr.New(apperr.Unsupported, "Este motor no permite editar la estructura desde Dbacus")
	}
	m, err := s.readModel(ctx, sess, cs.Database, cs.Table)
	if err != nil {
		return alterPlan{}, err
	}
	if cs.Fingerprint != "" && cs.Fingerprint != m.Fingerprint {
		return alterPlan{}, apperr.New(apperr.Validation, "La tabla cambió desde que se abrió el editor: vuelve a abrirlo").WithDetail("stale_model")
	}
	if err := alter.Validate(cs, m); err != nil {
		return alterPlan{}, err
	}
	stmts, err := gen.GenerateDDL(cs, m, driver.ServerInfo{Version: sess.ServerVersion, MariaDB: strings.Contains(strings.ToLower(sess.ServerVersion), "mariadb")})
	if err != nil {
		return alterPlan{}, toAppErr(err)
	}
	warnings, destructive := alter.Warnings(cs, m)
	if stmts == nil {
		stmts = []string{}
	}
	if warnings == nil {
		warnings = []model.AlterWarning{}
	}
	return alterPlan{sess: sess, model: m, preview: model.AlterPreview{
		Statements: stmts, SQLHash: alter.Hash(stmts), Warnings: warnings, Destructive: destructive,
		NoRollback: len(stmts) > 0, EstimatedRows: m.RowEstimate,
	}}, nil
}

// Preview genera el SQL sin ejecutar nada (el panel «SQL en vivo» y la revisión muestran exactamente esto).
func (s *AlterService) Preview(ctx context.Context, sessionID string, cs model.ChangeSet) (model.AlterPreview, error) {
	p, err := s.plan(ctx, sessionID, cs)
	return p.preview, err
}

// Apply ejecuta el SQL de la Preview con ese sqlHash. MySQL/MariaDB confirman el DDL al ejecutarlo: no hay transacción
// ni vuelta atrás. Si una sentencia falla se detiene y el resultado dice cuáles se aplicaron; Model es la lectura real.
func (s *AlterService) Apply(ctx context.Context, sessionID string, cs model.ChangeSet, sqlHash string) (model.AlterResult, error) {
	p, err := s.plan(ctx, sessionID, cs)
	if err != nil {
		return model.AlterResult{}, err
	}
	if len(p.preview.Statements) == 0 {
		return model.AlterResult{}, apperr.New(apperr.Validation, "No hay cambios que aplicar").WithDetail("empty")
	}
	if sqlHash != p.preview.SQLHash {
		return model.AlterResult{}, apperr.New(apperr.Validation, "El SQL cambió desde la revisión: vuelve a revisarlo").WithDetail("stale_preview")
	}

	key := sessionID + "\x00" + cs.Database + "\x00" + cs.Table
	s.mu.Lock()
	if s.applying[key] {
		s.mu.Unlock()
		return model.AlterResult{}, apperr.New(apperr.QueryFailed, "Ya se está modificando esta tabla").WithDetail("busy")
	}
	s.applying[key] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.applying, key); s.mu.Unlock() }()

	conn, err := p.sess.Pool.AcquireEdit(ctx) // aplica SET SESSION TRANSACTION READ ONLY si el perfil lo es: falla cerrado
	if err != nil {
		return model.AlterResult{}, toAppErr(err)
	}
	defer conn.Release()
	// Cancelar corta la sentencia en el servidor, no solo la espera del cliente.
	stop := context.AfterFunc(ctx, func() {
		kctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Kill(kctx)
	})
	defer stop()

	res := model.AlterResult{Applied: []string{}}
	for i, stmt := range p.preview.Statements {
		started := time.Now()
		_, _, execErr := conn.Exec(ctx, stmt)
		s.record(p.sess, cs.Database, stmt, started, execErr)
		if execErr != nil {
			res.Failed = &model.AlterFailure{Index: i, Message: apperr.From(toAppErr(execErr)).Message}
			if ctx.Err() != nil {
				res.Failed.Message = "Operación cancelada"
			}
			break
		}
		res.Applied = append(res.Applied, stmt)
	}
	if len(res.Applied) > 0 && s.d.Schema != nil {
		_ = s.d.Schema.invalidate(sessionID, cs.Database)
	}
	// Tras el intento (con o sin éxito) se devuelve el estado real del servidor; el contexto de la petición puede estar cancelado.
	rctx, cancel := context.WithTimeout(p.sess.Context(), 30*time.Second)
	defer cancel()
	if m, err := s.readModel(rctx, p.sess, cs.Database, cs.Table); err == nil {
		res.Model = &m
	}
	return res, nil
}

func (s *AlterService) record(sess *session.Session, db, stmt string, started time.Time, err error) {
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
