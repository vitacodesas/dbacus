package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
)

const (
	settingsKey  = "app"
	tabsKey      = "tabs"
	maxRowsConst = 20
	// Tope de ids de grupo recordados como plegados: evita que la lista crezca sin límite con grupos ya borrados.
	maxCollapsedGroups = 500
)

// GetSettings devuelve los ajustes guardados sobre los valores por defecto.
func (s *Store) GetSettings(ctx context.Context) (model.Settings, error) {
	st := model.DefaultSettings()
	raw, ok, err := s.getKV(ctx, "settings", settingsKey)
	if err != nil || !ok {
		return st, err
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return model.DefaultSettings(), err
	}
	return st, nil
}

func (s *Store) SetSettings(ctx context.Context, st model.Settings) error {
	if err := ValidateSettings(st); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.setKV(ctx, "settings", settingsKey, string(b))
}

func ValidateSettings(st model.Settings) *apperr.AppError {
	bad := func(field string) *apperr.AppError {
		return apperr.New(apperr.Validation, "Ajuste inválido").WithDetail(field)
	}
	switch {
	case !slices.Contains([]string{"light", "dark", "system"}, st.Theme):
		return bad("theme")
	case !slices.Contains([]string{"es", "en"}, st.Locale):
		return bad("locale")
	case st.MaxRows < maxRowsConst || st.MaxRows > 10_000_000:
		return bad("maxRows")
	case st.MaxResultMemoryMiB < 64 || st.MaxResultMemoryMiB > 16384:
		return bad("maxResultMemoryMiB")
	case st.HistoryRetention < 0 || st.HistoryRetention > 100_000:
		return bad("historyRetention")
	case st.SidebarWidth < 240 || st.SidebarWidth > 480:
		return bad("sidebarWidth")
	case st.SplitRatio < 0.1 || st.SplitRatio > 0.9:
		return bad("splitRatio")
	case len(st.CollapsedGroups) > maxCollapsedGroups || slices.ContainsFunc(st.CollapsedGroups, func(id string) bool { return len(id) > 64 }):
		return bad("collapsedGroups")
	}
	return nil
}

func (s *Store) SaveTabs(ctx context.Context, tabs []model.TabState) error {
	b, err := json.Marshal(tabs)
	if err != nil {
		return err
	}
	return s.setKV(ctx, "ui_state", tabsKey, string(b))
}

func (s *Store) LoadTabs(ctx context.Context) ([]model.TabState, error) {
	raw, ok, err := s.getKV(ctx, "ui_state", tabsKey)
	tabs := []model.TabState{}
	if err != nil || !ok {
		return tabs, err
	}
	return tabs, json.Unmarshal([]byte(raw), &tabs)
}

// table se pasa solo desde constantes internas ("settings" | "ui_state"), nunca desde input externo.
func (s *Store) getKV(ctx context.Context, table, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM `+table+` WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) setKV(ctx context.Context, table, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+table+` (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
