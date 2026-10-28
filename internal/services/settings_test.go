package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/model"
	"tabulariswails/internal/store"
)

func newSettingsSvc(t *testing.T) *SettingsService {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/t.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return NewSettingsService(st)
}

func TestSettingsServiceRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc := newSettingsSvc(t)

	got, err := svc.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, model.DefaultSettings(), got)

	got.Theme, got.MaxRows, got.ShowSystemDatabases = "dark", 5000, true
	saved, err := svc.Set(ctx, got)
	require.NoError(t, err)
	require.Equal(t, got, saved)

	again, err := svc.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, got, again)
}

func TestSettingsServiceRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	svc := newSettingsSvc(t)

	bad := model.DefaultSettings()
	bad.MaxRows = 5
	_, err := svc.Set(ctx, bad)
	ae := apperr.From(err)
	require.Equal(t, apperr.Validation, ae.Code)
	require.Equal(t, "maxRows", ae.Detail)

	// Un rechazo no pisa lo guardado.
	got, err := svc.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, model.DefaultSettings(), got)
}
