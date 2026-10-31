// Package app concentra el wiring de Wails y el ciclo de vida; el resto del código no debe importar Wails (ADR-001).
package app

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/vitacodesas/dbacus/internal/apperr"
	_ "github.com/vitacodesas/dbacus/internal/driver/mysql" // registra el driver MySQL/MariaDB
	"github.com/vitacodesas/dbacus/internal/logging"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/paths"
	"github.com/vitacodesas/dbacus/internal/results"
	"github.com/vitacodesas/dbacus/internal/secrets"
	"github.com/vitacodesas/dbacus/internal/services"
	"github.com/vitacodesas/dbacus/internal/session"
	"github.com/vitacodesas/dbacus/internal/store"
	"github.com/vitacodesas/dbacus/internal/tunnel"
)

const (
	appName         = "Dbacus"
	shutdownTimeout = 5 * time.Second
)

// Run inicializa infraestructura, construye la app y bloquea hasta que se cierra.
func Run(assets fs.FS) error {
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	lc := NewLifecycle()

	logCloser, err := logging.Setup(p.LogFile(), slog.LevelInfo)
	if err != nil {
		return fmt.Errorf("logging: %w", err)
	}
	lc.OnShutdown("logging", logCloser.Close)
	slog.Info("arranque", "config", p.Config, "logs", p.Logs)
	if p.MigratedFrom != "" {
		slog.Info("estado migrado desde la carpeta anterior", "origen", p.MigratedFrom)
	}
	if p.MigrationErr != nil {
		slog.Error("migración del estado anterior incompleta; se reintentará al reiniciar", "err", p.MigrationErr)
	}

	st, err := store.Open(lc.Context(), p.DB())
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	lc.OnShutdown("store", st.Close)

	// Los eventos se emiten con la app ya creada; hasta entonces (o tras cerrarla) se descartan.
	var appRef atomic.Pointer[application.App]
	knownHosts := tunnel.NewKnownHosts(p.KnownHosts())
	sessions := session.NewManager(session.Options{
		KnownHosts: knownHosts,
		OnClosed: func(id, reason string) {
			if a := appRef.Load(); a != nil {
				a.Event.Emit("session:closed", services.SessionClosed{SessionID: id, Reason: reason})
			}
		},
	})
	lc.OnShutdown("sessions", func() error { sessions.CloseAll(); return nil })

	emit := func(name string, data any) {
		if a := appRef.Load(); a != nil {
			a.Event.Emit(name, data)
		}
	}
	schemaSvc := services.NewSchemaService(sessions, emit)
	resultMgr := results.NewManager(int64(model.DefaultSettings().MaxResultMemoryMiB) << 20)
	history := services.NewHistoryRecorder(lc.Context(), st, emit)
	queries := services.NewQueryService(services.QueryDeps{
		Sessions: sessions, Results: resultMgr, Schema: schemaSvc, Store: st, Emit: emit,
		// El historial se escribe fuera del camino de la consulta; respeta "No guardar historial" del perfil.
		History: history,
	})
	lc.OnShutdown("queries", queries.ServiceShutdown) // se registra después que sessions: se cierra antes (LIFO)

	exports := services.NewExportService(services.ExportDeps{
		Sessions: sessions, Results: resultMgr, Emit: emit,
		PickSave: func(name string) (string, error) {
			return application.Get().Dialog.SaveFile().SetFilename(name).CanCreateDirectories(true).PromptForSingleSelection()
		},
	})
	lc.OnShutdown("exports", exports.ServiceShutdown)

	home, _ := os.UserHomeDir()
	queryFiles := services.NewQueryFilesService(services.QueryFilesDeps{
		Store: st, DefaultRoot: p.Queries, Home: home,
		PickDir: func(title string) (string, error) {
			return application.Get().Dialog.OpenFile().SetTitle(title).CanChooseFiles(false).CanChooseDirectories(true).CanCreateDirectories(true).PromptForSingleSelection()
		},
		Reveal: revealPath,
	})
	queryFiles.PurgeTrash(lc.Context())

	svcs := []application.Service{
		application.NewService(&services.PingService{}),
		application.NewService(services.NewConnectionService(services.ConnectionDeps{
			Store: st, Secrets: secrets.NewKeyring(), Sessions: sessions, KnownHosts: knownHosts,
			PickFile: func(title string) (string, error) {
				return application.Get().Dialog.OpenFile().SetTitle(title).CanChooseFiles(true).PromptForSingleSelection()
			},
		})),
		application.NewService(schemaSvc),
		application.NewService(queries),
		application.NewService(services.NewHistoryService(st)),
		application.NewService(exports),
		application.NewService(services.NewAlterService(services.AlterDeps{Sessions: sessions, Schema: schemaSvc, Store: st, History: history})),
		application.NewService(services.NewEditService(services.EditDeps{Sessions: sessions, Results: resultMgr, Schema: schemaSvc, Store: st, History: history, Emit: emit})),
		application.NewService(queryFiles),
		application.NewService(services.NewSettingsService(st)),
	}

	app := newApp(assets, lc, svcs)
	appRef.Store(app)
	runErr := app.Run()
	appRef.Store(nil)
	// OnShutdown de Wails no se dispara si Run falla antes de arrancar; el cierre es idempotente.
	if err := lc.Shutdown(shutdownTimeout); err != nil {
		slog.Error("shutdown", "err", err)
	}
	return runErr
}

func newApp(assets fs.FS, lc *Lifecycle, svcs []application.Service) *application.App {
	app := application.New(application.Options{
		Name:         appName,
		Description:  "Cliente SQL de escritorio",
		MarshalError: apperr.Marshal,
		Services:     svcs,
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		OnShutdown: func() {
			if err := lc.Shutdown(shutdownTimeout); err != nil {
				slog.Error("shutdown", "err", err)
			}
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     appName,
		Width:     1280,
		Height:    800,
		MinWidth:  960,
		MinHeight: 600,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(15, 23, 42),
		URL:              "/",
	})
	return app
}
