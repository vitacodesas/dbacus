package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/model"
)

func TestHistoryRecorderStoresFailuresIsolatesProfilesAndEmits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, err := e.svc.Save(ctx, e.profile("A"), ConnectionSecrets{})
	require.NoError(t, err)
	b, err := e.svc.Save(ctx, e.profile("B"), ConnectionSecrets{})
	require.NoError(t, err)

	var events []string
	rec := NewHistoryRecorder(ctx, e.st, func(name string, data any) { events = append(events, name+":"+data.(HistoryAdded).ProfileID) })
	rec(model.HistoryEntry{ProfileID: a.ID, SQL: "SELECT 1", OK: true})
	rec(model.HistoryEntry{ProfileID: a.ID, SQL: "SELEC 2", OK: false, ErrorMessage: "sintaxis"})
	rec(model.HistoryEntry{ProfileID: b.ID, SQL: "SELECT b", OK: true})
	require.Equal(t, []string{"history:added:" + a.ID, "history:added:" + a.ID, "history:added:" + b.ID}, events)

	h := NewHistoryService(e.st)
	page, err := h.List(ctx, HistoryQuery{ProfileID: a.ID})
	require.NoError(t, err)
	require.Len(t, page.Entries, 2, "el historial de B no aparece en A")
	require.Equal(t, "SELEC 2", page.Entries[0].SQL)
	require.Equal(t, "sintaxis", page.Entries[0].ErrorMessage)

	errs, err := h.List(ctx, HistoryQuery{ProfileID: a.ID, Status: "error"})
	require.NoError(t, err)
	require.Len(t, errs.Entries, 1)

	require.NoError(t, h.Delete(ctx, errs.Entries[0].ID))
	require.NoError(t, h.Clear(ctx, a.ID))
	page, _ = h.List(ctx, HistoryQuery{ProfileID: a.ID})
	require.Empty(t, page.Entries)
	other, _ := h.List(ctx, HistoryQuery{ProfileID: b.ID})
	require.Len(t, other.Entries, 1, "limpiar A no toca B")
}

func TestHistoryRecorderSkipsProfilesWithNoHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p := e.profile("Privada")
	p.NoHistory = true
	p, err := e.svc.Save(ctx, p, ConnectionSecrets{})
	require.NoError(t, err)

	emitted := 0
	rec := NewHistoryRecorder(ctx, e.st, func(string, any) { emitted++ })
	rec(model.HistoryEntry{ProfileID: p.ID, SQL: "SELECT secreto", OK: true})
	page, err := NewHistoryService(e.st).List(ctx, HistoryQuery{ProfileID: p.ID})
	require.NoError(t, err)
	require.Empty(t, page.Entries)
	require.Zero(t, emitted)
}

func TestHistoryServiceValidates(t *testing.T) {
	h := NewHistoryService(newEnv(t).st)
	ctx := context.Background()
	_, err := h.List(ctx, HistoryQuery{})
	require.Equal(t, apperr.Validation, apperr.From(err).Code)
	_, err = h.List(ctx, HistoryQuery{ProfileID: "x", Status: "raro"})
	require.Equal(t, apperr.Validation, apperr.From(err).Code)
	require.Equal(t, apperr.Validation, apperr.From(h.Delete(ctx, " ")).Code)
	require.Equal(t, apperr.Validation, apperr.From(h.Clear(ctx, "")).Code)
}
