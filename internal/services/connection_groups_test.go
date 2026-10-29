package services

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/secrets"
	"github.com/vitacodesas/dbacus/internal/store"
)

func TestSaveGroupValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cases := []struct {
		name  string
		g     model.ConnectionGroup
		field string
	}{
		{"nombre vacío", model.ConnectionGroup{Name: "   "}, "name"},
		{"nombre largo", model.ConnectionGroup{Name: strings.Repeat("a", maxNameLen+1)}, "name"},
		{"color inválido", model.ConnectionGroup{Name: "X", Color: "rojo"}, "color"},
		{"color corto", model.ConnectionGroup{Name: "X", Color: "#fff"}, "color"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.svc.SaveGroup(ctx, c.g)
			require.Equal(t, apperr.Validation, code(t, err))
			require.Equal(t, c.field, detail(t, err))
		})
	}
}

func TestSaveGroupTrimsAndRejectsDuplicateNames(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g, err := e.svc.SaveGroup(ctx, model.ConnectionGroup{Name: "  Producción  ", Color: " #FF0000 "})
	require.NoError(t, err)
	require.Equal(t, "Producción", g.Name)
	require.Equal(t, "#FF0000", g.Color)

	_, err = e.svc.SaveGroup(ctx, model.ConnectionGroup{Name: "PRODUCCIÓN"})
	require.Equal(t, apperr.Validation, code(t, err))
	require.Equal(t, "name", detail(t, err))

	// El mismo grupo puede cambiar solo de capitalización (no choca consigo mismo).
	g.Name = "PRODUCCIÓN"
	renamed, err := e.svc.SaveGroup(ctx, g)
	require.NoError(t, err)
	require.Equal(t, "PRODUCCIÓN", renamed.Name)

	// Sin color es válido (hereda el aspecto por defecto).
	_, err = e.svc.SaveGroup(ctx, model.ConnectionGroup{Name: "Sin color"})
	require.NoError(t, err)
	gs, err := e.svc.ListGroups(ctx)
	require.NoError(t, err)
	require.Len(t, gs, 2)
}

func TestSaveProfileWithUnknownGroupIsRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.profile("A")
	p.GroupID = "no-existe"
	_, err := e.svc.Save(ctx, p, ConnectionSecrets{})
	require.Equal(t, apperr.Validation, code(t, err))
	require.Equal(t, "groupId", detail(t, err))
	ps, _ := e.svc.List(ctx)
	require.Empty(t, ps, "no se guardó nada")
}

func TestSaveProfileInGroupAndCloneKeepsIt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g, _ := e.svc.SaveGroup(ctx, model.ConnectionGroup{Name: "Prod", Color: "#ff0000"})
	p := e.profile("A")
	p.GroupID = g.ID
	saved, err := e.svc.Save(ctx, p, ConnectionSecrets{Password: sp("pw")})
	require.NoError(t, err)
	require.Equal(t, g.ID, saved.GroupID)

	c, err := e.svc.Clone(ctx, saved.ID)
	require.NoError(t, err)
	require.Equal(t, g.ID, c.GroupID, "la copia conserva el grupo")
}

func TestDeleteGroupKeepsConnectionsSecretsAndHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g, _ := e.svc.SaveGroup(ctx, model.ConnectionGroup{Name: "Prod"})
	p := e.profile("A")
	p.GroupID = g.ID
	saved, err := e.svc.Save(ctx, p, ConnectionSecrets{Password: sp("pw")})
	require.NoError(t, err)
	_, err = e.st.AddHistory(ctx, model.HistoryEntry{ProfileID: saved.ID, SQL: "SELECT 1", OK: true}, 0)
	require.NoError(t, err)

	require.NoError(t, e.svc.DeleteGroup(ctx, g.ID))

	got, err := e.svc.Get(ctx, saved.ID)
	require.NoError(t, err, "la conexión sigue ahí")
	require.Empty(t, got.GroupID, "pasa a «Sin grupo»")
	require.True(t, got.HasPassword)
	pw, ok, _ := e.sec.Get(saved.ID, secrets.Password)
	require.True(t, ok, "el secreto no se toca")
	require.Equal(t, "pw", pw.Reveal())
	hist, _, _ := e.st.ListHistory(ctx, store.HistoryQuery{ProfileID: saved.ID})
	require.Len(t, hist, 1, "el historial no se toca")

	require.NoError(t, e.svc.DeleteGroup(ctx, g.ID), "idempotente")
	// Con la conexión ya sin grupo se puede volver a guardar sin error.
	got.Host = "otro"
	_, err = e.svc.Save(ctx, got, ConnectionSecrets{})
	require.NoError(t, err)
}

func TestDeleteGroupKeepsOpenSessionAlive(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	g, _ := e.svc.SaveGroup(ctx, model.ConnectionGroup{Name: "Prod"})
	p := e.profile("A")
	p.GroupID = g.ID
	saved, _ := e.svc.Save(ctx, p, ConnectionSecrets{Password: sp("pw")})
	info, err := e.svc.Connect(ctx, saved.ID, nil)
	require.NoError(t, err)

	require.NoError(t, e.svc.DeleteGroup(ctx, g.ID))
	_, err = e.sessions.Get(info.SessionID)
	require.NoError(t, err, "la sesión abierta sigue viva")
}
