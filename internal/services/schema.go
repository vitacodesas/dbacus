package services

import (
	"context"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/schemacache"
	"github.com/vitacodesas/dbacus/internal/session"
)

// SchemaInvalidated avisa a Vue de que debe volver a leer el esquema (spec 02).
type SchemaInvalidated struct {
	SessionID string `json:"sessionId"`
	Database  string `json:"database"` // "" = toda la sesión
}

func init() { application.RegisterEvent[SchemaInvalidated]("schema:invalidated") }

// SchemaService lee el esquema a través de la caché de la sesión (spec 05).
type SchemaService struct {
	sessions *session.Manager
	emit     func(name string, data any)
}

func NewSchemaService(sessions *session.Manager, emit func(name string, data any)) *SchemaService {
	return &SchemaService{sessions: sessions, emit: emit}
}

// Claves de caché: "db:<base>:..." permite invalidar una base entera por prefijo.
const keyDatabases = "dbs"

func keyTables(db string) string       { return "db:" + db + ":tables" }
func keyTable(db, table string) string { return "db:" + db + ":table:" + table }
func keyCatalog(db string) string      { return "db:" + db + ":catalog" }

func (s *SchemaService) ListDatabases(ctx context.Context, sessionID string) ([]model.Database, error) {
	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		return nil, toAppErr(err)
	}
	out, err := schemacache.Load(ctx, sess.Schema, sess.Context(), schemacache.DefaultTimeout, keyDatabases, sess.Pool.Schema().Databases)
	return out, toAppErr(err)
}

func (s *SchemaService) ListTables(ctx context.Context, sessionID, db string) ([]model.Table, error) {
	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		return nil, toAppErr(err)
	}
	if err := requireName("database", db); err != nil {
		return nil, err
	}
	out, err := schemacache.Load(ctx, sess.Schema, sess.Context(), schemacache.DefaultTimeout, keyTables(db),
		func(c context.Context) ([]model.Table, error) { return sess.Pool.Schema().Tables(c, db) })
	return out, toAppErr(err)
}

func (s *SchemaService) DescribeTable(ctx context.Context, sessionID, db, table string) (model.TableDetail, error) {
	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		return model.TableDetail{}, toAppErr(err)
	}
	if err := requireName("database", db); err != nil {
		return model.TableDetail{}, err
	}
	if err := requireName("table", table); err != nil {
		return model.TableDetail{}, err
	}
	out, err := schemacache.Load(ctx, sess.Schema, sess.Context(), schemacache.DefaultTimeout, keyTable(db, table),
		func(c context.Context) (model.TableDetail, error) { return sess.Pool.Schema().Describe(c, db, table) })
	return out, toAppErr(err)
}

// GetCompletionCatalog devuelve tablas y columnas de una base para el autocompletado del editor.
func (s *SchemaService) GetCompletionCatalog(ctx context.Context, sessionID, db string) (model.CompletionCatalog, error) {
	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		return model.CompletionCatalog{}, toAppErr(err)
	}
	if err := requireName("database", db); err != nil {
		return model.CompletionCatalog{}, err
	}
	out, err := schemacache.Load(ctx, sess.Schema, sess.Context(), schemacache.DefaultTimeout, keyCatalog(db),
		func(c context.Context) (model.CompletionCatalog, error) { return sess.Pool.Schema().Catalog(c, db) })
	return out, toAppErr(err)
}

// SelectStatement arma un SELECT * con los identificadores escapados por el driver (el front nunca arma SQL).
func (s *SchemaService) SelectStatement(sessionID, db, table string, limit int) (string, error) {
	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		return "", toAppErr(err)
	}
	if err := requireName("database", db); err != nil {
		return "", err
	}
	if err := requireName("table", table); err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000
	}
	q := sess.Driver.QuoteIdent
	return "SELECT * FROM " + q(db) + "." + q(table) + " LIMIT " + strconv.Itoa(limit), nil
}

// Refresh invalida la caché (de una base o de toda la sesión si db == "") y avisa a Vue.
func (s *SchemaService) Refresh(sessionID, db string) error {
	return s.invalidate(sessionID, db)
}

// invalidate lo usarán también los servicios que ejecutan DDL (spec 05); no se expone a Vue.
func (s *SchemaService) invalidate(sessionID, db string) error {
	sess, err := s.sessions.Get(sessionID)
	if err != nil {
		return toAppErr(err)
	}
	if db == "" {
		sess.Schema.Invalidate("")
	} else {
		sess.Schema.Invalidate("db:" + db + ":")
	}
	if s.emit != nil {
		s.emit("schema:invalidated", SchemaInvalidated{SessionID: sessionID, Database: db})
	}
	return nil
}

func requireName(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return apperr.New(apperr.Validation, "Falta el nombre de la "+map[string]string{"database": "base de datos", "table": "tabla"}[field]).WithDetail(field)
	}
	return nil
}
