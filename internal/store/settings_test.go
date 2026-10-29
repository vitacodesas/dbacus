package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Los ajustes guardados antes de existir CollapsedGroups se leen sin el campo y conservan el valor por defecto (un array).
func TestSettingsSavedBeforeCollapsedGroupsKeepDefault(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	old := `{"theme":"dark","locale":"es","maxRows":5000,"maxResultMemoryMiB":512,"historyRetention":5000,"sidebarWidth":300,"splitRatio":0.5}`
	require.NoError(t, s.setKV(ctx, "settings", settingsKey, old))

	got, err := s.GetSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, "dark", got.Theme)
	require.NotNil(t, got.CollapsedGroups)
	require.Empty(t, got.CollapsedGroups)
}
