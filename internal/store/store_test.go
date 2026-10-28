package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/model"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "app.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	for range 2 {
		s, err := Open(context.Background(), path)
		require.NoError(t, err)
		var n int
		require.NoError(t, s.db.QueryRow(`SELECT COUNT(1) FROM schema_migrations`).Scan(&n))
		require.Equal(t, 1, n)
		require.NoError(t, s.Close())
	}
}

func TestProfilesCRUD(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	p, err := s.SaveProfile(ctx, model.ConnectionProfile{Name: "Local", Driver: "mysql", Host: "127.0.0.1", Port: 3306, User: "root",
		SSH: model.SSHConfig{Enabled: true, Host: "bastion", Port: 22}, Params: map[string]string{"charset": "utf8mb4"}})
	require.NoError(t, err)
	require.NotEmpty(t, p.ID)

	got, err := s.GetProfile(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, p, got)

	got.Name = "Local 2"
	updated, err := s.SaveProfile(ctx, got)
	require.NoError(t, err)
	require.Equal(t, p.CreatedAt, updated.CreatedAt)
	require.False(t, updated.UpdatedAt.Before(p.UpdatedAt))

	list, err := s.ListProfiles(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "Local 2", list[0].Name)

	require.NoError(t, s.DeleteProfile(ctx, p.ID))
	_, err = s.GetProfile(ctx, p.ID)
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.NotFound, ae.Code)
	require.Error(t, s.DeleteProfile(ctx, p.ID))
}

func TestProfileDuplicateNameCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_, err := s.SaveProfile(ctx, model.ConnectionProfile{Name: "Prod", Driver: "mysql"})
	require.NoError(t, err)
	_, err = s.SaveProfile(ctx, model.ConnectionProfile{Name: "prod", Driver: "mysql"})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.Validation, ae.Code)
	require.Equal(t, "name", ae.Detail)
}

func TestSaveProfileUnknownIDIsNotFound(t *testing.T) {
	_, err := newStore(t).SaveProfile(context.Background(), model.ConnectionProfile{ID: "nope", Name: "x"})
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, apperr.NotFound, ae.Code)
}

func TestHistoryPaginationSearchRetention(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for i := 1; i <= 7; i++ {
		_, err := s.AddHistory(ctx, model.HistoryEntry{ProfileID: "p1", SQL: fmt.Sprintf("SELECT %d", i), OK: i%2 == 0}, 5)
		require.NoError(t, err)
	}
	_, err := s.AddHistory(ctx, model.HistoryEntry{ProfileID: "p2", SQL: "SELECT other", OK: true}, 5)
	require.NoError(t, err)

	page1, next, err := s.ListHistory(ctx, HistoryQuery{ProfileID: "p1", Limit: 3})
	require.NoError(t, err)
	require.Len(t, page1, 3)
	require.Equal(t, "SELECT 7", page1[0].SQL)
	require.NotEmpty(t, next)

	page2, next, err := s.ListHistory(ctx, HistoryQuery{ProfileID: "p1", Limit: 3, Cursor: next})
	require.NoError(t, err)
	require.Len(t, page2, 2, "retención 5 purgó las 2 más antiguas")
	require.Empty(t, next)

	errs, _, err := s.ListHistory(ctx, HistoryQuery{ProfileID: "p1", Status: "error"})
	require.NoError(t, err)
	for _, e := range errs {
		require.False(t, e.OK)
	}

	found, _, err := s.ListHistory(ctx, HistoryQuery{ProfileID: "p1", Search: "SELECT 6"})
	require.NoError(t, err)
	require.Len(t, found, 1)

	wild, _, err := s.ListHistory(ctx, HistoryQuery{ProfileID: "p1", Search: "%"})
	require.NoError(t, err)
	require.Empty(t, wild, "los comodines LIKE se escapan")

	require.NoError(t, s.ClearHistory(ctx, "p1"))
	left, _, _ := s.ListHistory(ctx, HistoryQuery{ProfileID: "p1"})
	require.Empty(t, left)
	other, _, _ := s.ListHistory(ctx, HistoryQuery{ProfileID: "p2"})
	require.Len(t, other, 1)
}

func TestHistoryTruncatesLargeSQL(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_, err := s.AddHistory(ctx, model.HistoryEntry{ProfileID: "p", SQL: strings.Repeat("é", MaxHistorySQLBytes)}, 0)
	require.NoError(t, err)
	got, _, _ := s.ListHistory(ctx, HistoryQuery{ProfileID: "p"})
	require.LessOrEqual(t, len(got[0].SQL), MaxHistorySQLBytes)
}

func TestSettingsDefaultsAndValidation(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	got, err := s.GetSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, model.DefaultSettings(), got)

	got.Theme, got.SidebarWidth = "dark", 350
	require.NoError(t, s.SetSettings(ctx, got))
	again, _ := s.GetSettings(ctx)
	require.Equal(t, got, again)

	got.SplitRatio = 5
	err = s.SetSettings(ctx, got)
	var ae *apperr.AppError
	require.ErrorAs(t, err, &ae)
	require.Equal(t, "splitRatio", ae.Detail)
}

func TestTabsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	empty, err := s.LoadTabs(ctx)
	require.NoError(t, err)
	require.Empty(t, empty)

	tabs := []model.TabState{{ID: "t1", Title: "Q1", SQL: "SELECT 1", Order: 0}, {ID: "t2", Title: "Q2", Order: 1}}
	require.NoError(t, s.SaveTabs(ctx, tabs))
	got, err := s.LoadTabs(ctx)
	require.NoError(t, err)
	require.Equal(t, tabs, got)
}
