package services

import (
	"context"

	"github.com/vitacodesas/dbacus/internal/model"
)

// SettingsStore es lo que SettingsService necesita de la persistencia (lo implementa *store.Store).
type SettingsStore interface {
	GetSettings(ctx context.Context) (model.Settings, error)
	SetSettings(ctx context.Context, st model.Settings) error
}

// SettingsService expone los ajustes de usuario (spec 02). Los consumidores en Go (QueryService, historial)
// releen el store en cada uso, así que un cambio aplica sin reiniciar.
type SettingsService struct {
	st SettingsStore
}

func NewSettingsService(st SettingsStore) *SettingsService { return &SettingsService{st: st} }

func (s *SettingsService) Get(ctx context.Context) (model.Settings, error) {
	st, err := s.st.GetSettings(ctx)
	return st, toAppErr(err)
}

// Set valida y persiste; devuelve lo guardado para que Vue no tenga que releerlo.
func (s *SettingsService) Set(ctx context.Context, st model.Settings) (model.Settings, error) {
	if st.CollapsedGroups == nil {
		st.CollapsedGroups = []string{} // Vue siempre recibe un array, nunca null
	}
	if err := s.st.SetSettings(ctx, st); err != nil {
		return model.Settings{}, toAppErr(err)
	}
	return st, nil
}
