package services

import (
	"context"
	"log/slog"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"tabulariswails/internal/apperr"
	"tabulariswails/internal/model"
	"tabulariswails/internal/store"
)

// HistoryAdded avisa a Vue de que hay entradas nuevas en el historial de un perfil (spec 07).
type HistoryAdded struct {
	ProfileID string `json:"profileId"`
}

func init() { application.RegisterEvent[HistoryAdded]("history:added") }

type HistoryQuery struct {
	ProfileID string `json:"profileId"`
	Search    string `json:"search"`
	Status    string `json:"status"` // "" | "ok" | "error"
	Limit     int    `json:"limit"`
	Cursor    string `json:"cursor"`
}

type HistoryPage struct {
	Entries    []model.HistoryEntry `json:"entries"`
	NextCursor string               `json:"nextCursor"`
}

// HistoryStore es lo que HistoryService necesita de la persistencia (lo implementa *store.Store).
type HistoryStore interface {
	AddHistory(ctx context.Context, e model.HistoryEntry, retention int) (model.HistoryEntry, error)
	ListHistory(ctx context.Context, q store.HistoryQuery) ([]model.HistoryEntry, string, error)
	DeleteHistory(ctx context.Context, id string) error
	ClearHistory(ctx context.Context, profileID string) error
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
	GetSettings(ctx context.Context) (model.Settings, error)
}

type HistoryService struct {
	st HistoryStore
}

func NewHistoryService(st HistoryStore) *HistoryService { return &HistoryService{st: st} }

func (h *HistoryService) List(ctx context.Context, q HistoryQuery) (HistoryPage, error) {
	if strings.TrimSpace(q.ProfileID) == "" {
		return HistoryPage{}, apperr.New(apperr.Validation, "Falta la conexión").WithDetail("profileId")
	}
	switch q.Status {
	case "", "ok", "error":
	default:
		return HistoryPage{}, apperr.New(apperr.Validation, "Filtro de estado no válido").WithDetail("status")
	}
	entries, next, err := h.st.ListHistory(ctx, store.HistoryQuery{
		ProfileID: q.ProfileID, Search: strings.TrimSpace(q.Search), Status: q.Status, Limit: q.Limit, Cursor: q.Cursor,
	})
	if err != nil {
		return HistoryPage{}, toAppErr(err)
	}
	return HistoryPage{Entries: entries, NextCursor: next}, nil
}

func (h *HistoryService) Delete(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return apperr.New(apperr.Validation, "Falta la entrada").WithDetail("id")
	}
	return toAppErr(h.st.DeleteHistory(ctx, id))
}

func (h *HistoryService) Clear(ctx context.Context, profileID string) error {
	if strings.TrimSpace(profileID) == "" {
		return apperr.New(apperr.Validation, "Falta la conexión").WithDetail("profileId")
	}
	return toAppErr(h.st.ClearHistory(ctx, profileID))
}

// Recorder devuelve la función que QueryService llama tras cada sentencia. No es un método exportado para que Wails
// no lo enlace a Vue. Respeta "No guardar historial" del perfil y la retención de los ajustes; los fallos solo se registran.
func NewHistoryRecorder(ctx context.Context, st HistoryStore, emit func(string, any)) func(model.HistoryEntry) {
	return func(e model.HistoryEntry) {
		if p, err := st.GetProfile(ctx, e.ProfileID); err == nil && p.NoHistory {
			return
		}
		retention := model.DefaultSettings().HistoryRetention
		if cur, err := st.GetSettings(ctx); err == nil {
			retention = cur.HistoryRetention
		}
		if _, err := st.AddHistory(ctx, e, retention); err != nil {
			slog.Warn("no se pudo guardar el historial", "err", err)
			return
		}
		if emit != nil {
			emit("history:added", HistoryAdded{ProfileID: e.ProfileID})
		}
	}
}
