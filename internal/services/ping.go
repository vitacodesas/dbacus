package services

import (
	"context"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// PingTick es el payload del evento "ping:tick" (spike T0.1: verifica eventos Go→Vue).
type PingTick struct {
	Seq int       `json:"seq"`
	At  time.Time `json:"at"`
}

func init() {
	application.RegisterEvent[PingTick]("ping:tick")
}

// PingService existe solo para validar el puente Wails en T0.1 y se elimina cuando haya servicios reales.
type PingService struct{}

func (s *PingService) Hello(name string) string {
	return "Hola, " + name + "!"
}

// PickFile abre el diálogo nativo de archivo y devuelve la ruta ("" si se cancela).
func (s *PingService) PickFile() (string, error) {
	return application.Get().Dialog.OpenFile().
		SetTitle("Seleccionar archivo").
		CanChooseFiles(true).
		PromptForSingleSelection()
}

// ServiceStartup lanza el emisor de ticks; termina al cancelarse ctx (antes del shutdown).
func (s *PingService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for seq := 1; ; seq++ {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				application.Get().Event.Emit("ping:tick", PingTick{Seq: seq, At: now})
			}
		}
	}()
	return nil
}
