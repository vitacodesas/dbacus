package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

func TestGroupsCRUD(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	g, err := s.SaveGroup(ctx, model.ConnectionGroup{Name: "Producción", Color: "#ff0000"})
	require.NoError(t, err)
	require.NotEmpty(t, g.ID)

	got, err := s.GetGroup(ctx, g.ID)
	require.NoError(t, err)
	require.Equal(t, g, got)

	got.Color = "#00ff00"
	updated, err := s.SaveGroup(ctx, got)
	require.NoError(t, err)
	require.Equal(t, g.CreatedAt, updated.CreatedAt)
	require.Equal(t, "#00ff00", updated.Color)

	_, err = s.SaveGroup(ctx, model.ConnectionGroup{ID: "no-existe", Name: "x"})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.NotFound, ae.Code)
}

func TestGroupsListOrderedByNameIgnoringCase(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, n := range []string{"staging", "Producción", "Dev"} {
		_, err := s.SaveGroup(ctx, model.ConnectionGroup{Name: n})
		require.NoError(t, err)
	}
	gs, err := s.ListGroups(ctx)
	require.NoError(t, err)
	names := []string{}
	for _, g := range gs {
		names = append(names, g.Name)
	}
	require.Equal(t, []string{"Dev", "Producción", "staging"}, names)
}

func TestGroupDuplicateNameCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_, err := s.SaveGroup(ctx, model.ConnectionGroup{Name: "Prod"})
	require.NoError(t, err)
	_, err = s.SaveGroup(ctx, model.ConnectionGroup{Name: "prod"})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.Validation, ae.Code)
	require.Equal(t, "name", ae.Detail)
}

func TestProfileKeepsItsGroupID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	g, err := s.SaveGroup(ctx, model.ConnectionGroup{Name: "Prod"})
	require.NoError(t, err)
	p, err := s.SaveProfile(ctx, model.ConnectionProfile{Name: "A", Driver: "mysql", GroupID: g.ID})
	require.NoError(t, err)
	got, err := s.GetProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, g.ID, got.GroupID)
}

func TestDeleteGroupMovesItsProfilesToNoGroupAndKeepsTheRest(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	gProd, _ := s.SaveGroup(ctx, model.ConnectionGroup{Name: "Prod"})
	gDev, _ := s.SaveGroup(ctx, model.ConnectionGroup{Name: "Dev"})
	a, _ := s.SaveProfile(ctx, model.ConnectionProfile{Name: "A", Driver: "mysql", Color: "#112233", GroupID: gProd.ID})
	b, _ := s.SaveProfile(ctx, model.ConnectionProfile{Name: "B", Driver: "mysql", GroupID: gProd.ID})
	c, _ := s.SaveProfile(ctx, model.ConnectionProfile{Name: "C", Driver: "mysql", GroupID: gDev.ID})
	d, _ := s.SaveProfile(ctx, model.ConnectionProfile{Name: "D", Driver: "mysql"})

	require.NoError(t, s.DeleteGroup(ctx, gProd.ID))

	for _, id := range []string{a.ID, b.ID} {
		got, err := s.GetProfile(ctx, id)
		require.NoError(t, err, "borrar el grupo no borra la conexión")
		require.Empty(t, got.GroupID)
	}
	gotA, _ := s.GetProfile(ctx, a.ID)
	require.Equal(t, "#112233", gotA.Color, "el resto del perfil no se toca")
	gotC, _ := s.GetProfile(ctx, c.ID)
	require.Equal(t, gDev.ID, gotC.GroupID, "otros grupos intactos")
	gotD, _ := s.GetProfile(ctx, d.ID)
	require.Empty(t, gotD.GroupID)

	_, err := s.GetGroup(ctx, gProd.ID)
	require.Error(t, err)
	require.NoError(t, s.DeleteGroup(ctx, gProd.ID), "idempotente")
}

// Una base creada antes de existir los grupos (solo con la migración 001) debe abrir con sus perfiles intactos.
func TestOldDatabaseWithoutGroupsMigratesKeepingProfiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	s, err := Open(ctx, path)
	require.NoError(t, err)
	p, err := s.SaveProfile(ctx, model.ConnectionProfile{Name: "Antigua", Driver: "mysql", Host: "h", Color: "#abcdef"})
	require.NoError(t, err)
	// Se simula el estado previo: sin la tabla de grupos y sin la fila de la migración 002.
	_, err = s.db.Exec(`DROP TABLE connection_groups`)
	require.NoError(t, err)
	_, err = s.db.Exec(`DELETE FROM schema_migrations WHERE version = 2`)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.GetProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, p, got, "los perfiles no cambian")
	require.Empty(t, got.GroupID)
	gs, err := s.ListGroups(ctx)
	require.NoError(t, err)
	require.Empty(t, gs)
}
