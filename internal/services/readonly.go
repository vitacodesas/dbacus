package services

import (
	"context"

	"github.com/vitacodesas/dbacus/internal/apperr"
	"github.com/vitacodesas/dbacus/internal/model"
	"github.com/vitacodesas/dbacus/internal/session"
)

type profileReader interface {
	GetProfile(ctx context.Context, id string) (model.ConnectionProfile, error)
}

// sessionReadOnly dice si la sesión debe tratarse como de solo lectura. El modo se lee del perfil guardado en cada
// llamada: marcar «Solo lectura» con la sesión ya abierta protege al instante (la sesión del servidor sigue en
// lectura-escritura hasta reconectar, pero Go ya no deja pasar escrituras). El sentido contrario (quitar el modo) exige
// reconectar: la sesión conserva el flag con el que se abrió. Es el único sitio donde se decide (specs 10 y 19).
func sessionReadOnly(ctx context.Context, st profileReader, sess *session.Session) bool {
	if sess.ReadOnly {
		return true
	}
	if st == nil {
		return false
	}
	p, err := st.GetProfile(ctx, sess.ProfileID)
	return err == nil && p.ReadOnly
}

// errReadOnly es el rechazo uniforme de una escritura en un perfil de solo lectura.
func errReadOnly(message string) error {
	return apperr.New(apperr.QueryFailed, message).WithDetail("readonly")
}
