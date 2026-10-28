package services

import (
	"context"

	"tabulariswails/internal/edit"
	"tabulariswails/internal/model"
	"tabulariswails/internal/results"
	"tabulariswails/internal/session"
)

// EditStore es lo que EditService necesita de la persistencia (lo implementa *store.Store).
type EditStore interface {
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
}

type EditDeps struct {
	Sessions *session.Manager
	Results  *results.Manager
	Schema   *SchemaService
	Store    EditStore
}

// EditService expone la edición de celdas (spec 13). Go decide qué es editable, valida y genera el SQL; Vue solo pinta.
type EditService struct {
	d EditDeps
}

func NewEditService(d EditDeps) *EditService { return &EditService{d: d} }

// GetEditInfo dice si el resultado `stmtIndex` de la ejecución `queryID` se puede editar y qué columnas.
func (s *EditService) GetEditInfo(ctx context.Context, queryID string, stmtIndex int) (edit.Info, error) {
	plan, err := s.plan(ctx, queryID, stmtIndex)
	return plan.Info, err
}

func (s *EditService) plan(ctx context.Context, queryID string, stmtIndex int) (edit.Plan, error) {
	run, err := s.d.Results.Get(queryID)
	if err != nil {
		return edit.Plan{}, toAppErr(err)
	}
	buf, err := run.Buffer(stmtIndex)
	if err != nil {
		return edit.Plan{}, toAppErr(err)
	}
	sess, err := s.d.Sessions.Get(run.SessionID)
	if err != nil {
		return edit.Plan{}, toAppErr(err)
	}
	// El modo solo lectura se lee del perfil guardado (como en QueryService.Execute): cambiarlo con la sesión abierta aplica ya.
	readOnly := sess.ReadOnly
	if p, err := s.d.Store.GetProfile(ctx, sess.ProfileID); err == nil && p.ReadOnly {
		readOnly = true
	}
	sql, db := buf.Source()
	return edit.Analyze(edit.Input{
		SQL: sql, DefaultDB: db, Cols: buf.Columns(), ReadOnly: readOnly,
		Describe: func(db, table string) (model.TableDetail, error) {
			return s.d.Schema.DescribeTable(ctx, run.SessionID, db, table)
		},
	}), nil
}
