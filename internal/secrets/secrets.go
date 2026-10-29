// Package secrets guarda contraseñas y passphrases en el llavero del SO (spec 04/10, ADR-003).
package secrets

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/zalando/go-keyring"

	"github.com/vitacodesas/dbacus/internal/apperr"
)

const service = "dbacus"

// legacyService es el servicio previo al renombrado (spec 15 §A). El llavero no se puede enumerar de forma
// portable, así que los secretos se migran al leerlos; el original no se borra hasta eliminar el perfil.
const legacyService = "tabularis-clon"

type Kind string

const (
	Password      Kind = "password"
	SSHPassword   Kind = "ssh_password"
	SSHPassphrase Kind = "ssh_passphrase"
)

var allKinds = []Kind{Password, SSHPassword, SSHPassphrase}

// Secret oculta su valor en String/%v/JSON/slog; el valor real solo sale con Reveal.
type Secret string

func (s Secret) Reveal() string               { return string(s) }
func (s Secret) String() string               { return "***" }
func (s Secret) GoString() string             { return "secrets.Secret(***)" }
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"***"`), nil }
func (s Secret) LogValue() slog.Value         { return slog.StringValue("***") }

// Store abstrae el llavero; la implementación real no tiene fallback en claro.
type Store interface {
	Get(profileID string, kind Kind) (Secret, bool, error)
	Set(profileID string, kind Kind, value Secret) error
	Delete(profileID string, kind Kind) error
	// DeleteAll borra todos los secretos del perfil (idempotente).
	DeleteAll(profileID string) error
}

type Keyring struct{}

func NewKeyring() *Keyring { return &Keyring{} }

func account(profileID string, kind Kind) string {
	return fmt.Sprintf("profile:%s:%s", profileID, kind)
}

func (*Keyring) Get(profileID string, kind Kind) (Secret, bool, error) {
	acc := account(profileID, kind)
	v, err := keyring.Get(service, acc)
	if errors.Is(err, keyring.ErrNotFound) {
		// Un fallo del servicio antiguo no debe romper la conexión: se trata como "no hay secreto".
		old, oerr := keyring.Get(legacyService, acc)
		if oerr != nil {
			return "", false, nil
		}
		_ = keyring.Set(service, acc, old) // si falla, se reintenta en la siguiente lectura
		return Secret(old), true, nil
	}
	if err != nil {
		return "", false, unavailable(err)
	}
	return Secret(v), true, nil
}

func (*Keyring) Set(profileID string, kind Kind, value Secret) error {
	if err := keyring.Set(service, account(profileID, kind), value.Reveal()); err != nil {
		return unavailable(err)
	}
	return nil
}

func (*Keyring) Delete(profileID string, kind Kind) error {
	acc := account(profileID, kind)
	err := keyring.Delete(service, acc)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return unavailable(err)
	}
	// También el antiguo: si no, el secreto de un perfil borrado quedaría huérfano en el llavero.
	_ = keyring.Delete(legacyService, acc)
	return nil
}

func (k *Keyring) DeleteAll(profileID string) error {
	var errs []error
	for _, kind := range allKinds {
		if err := k.Delete(profileID, kind); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func unavailable(err error) error {
	return apperr.Wrap(apperr.KeyringUnavailable, "No se pudo acceder al llavero del sistema", err).
		WithDetail("Puedes guardar la conexión sin contraseña y escribirla al conectar.")
}
