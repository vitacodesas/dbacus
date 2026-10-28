package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	gomysql "github.com/go-sql-driver/mysql"

	"tabulariswails/internal/apperr"
)

// mapError traduce errores del driver/red a AppError (spec 02). Devuelve nil si err es nil.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperr.AppError
	if errors.As(err, &ae) { // p. ej. errores del túnel SSH que atravesaron el dialer
		return ae
	}

	var me *gomysql.MySQLError
	if errors.As(err, &me) {
		detail := fmt.Sprintf("ERROR %d: %s", me.Number, me.Message)
		switch me.Number {
		case 1045, 1698, 1044, 1227:
			return apperr.Wrap(apperr.AuthFailed, "Usuario o contraseña incorrectos, o sin permisos", err).WithDetail(detail)
		case 1049:
			return apperr.Wrap(apperr.Validation, "La base de datos no existe", err).WithDetail(detail)
		case 1064, 1149:
			return apperr.Wrap(apperr.QuerySyntax, me.Message, err).WithDetail(detail)
		case 1317:
			return apperr.Wrap(apperr.QueryCanceled, "La consulta fue cancelada", err).WithDetail(detail)
		case 3024, 1205:
			return apperr.Wrap(apperr.Timeout, me.Message, err).WithDetail(detail).AsRetryable()
		default:
			return apperr.Wrap(apperr.QueryFailed, me.Message, err).WithDetail(detail)
		}
	}

	switch {
	case errors.Is(err, context.Canceled):
		return apperr.Wrap(apperr.QueryCanceled, "La operación fue cancelada", err)
	case errors.Is(err, context.DeadlineExceeded):
		return apperr.Wrap(apperr.Timeout, "La operación excedió el tiempo límite", err).AsRetryable()
	case errors.Is(err, gomysql.ErrNoTLS):
		return apperr.Wrap(apperr.TLSError, "El servidor no soporta TLS", err)
	case isTLSError(err):
		return apperr.Wrap(apperr.TLSError, "Falló la negociación TLS", err)
	case errors.Is(err, driver.ErrBadConn), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, gomysql.ErrInvalidConn):
		return apperr.Wrap(apperr.SessionClosed, "La conexión con el servidor se perdió", err).AsRetryable()
	}

	var dns *net.DNSError
	if errors.As(err, &dns) {
		return apperr.Wrap(apperr.NetworkUnreachable, "No se pudo resolver el host", err).AsRetryable()
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return apperr.Wrap(apperr.Timeout, "Tiempo de espera agotado al conectar", err).AsRetryable()
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return apperr.Wrap(apperr.NetworkUnreachable, "No se pudo conectar con el servidor: ¿host y puerto correctos?", err).AsRetryable()
	}
	return apperr.Wrap(apperr.QueryFailed, "Error al comunicarse con la base de datos", err)
}

func isTLSError(err error) bool {
	var (
		cv  *tls.CertificateVerificationError
		ua  x509.UnknownAuthorityError
		hn  x509.HostnameError
		ci  x509.CertificateInvalidError
		rh  tls.RecordHeaderError
		alt tls.AlertError
	)
	if errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci) || errors.As(err, &rh) || errors.As(err, &alt) {
		return true
	}
	return strings.Contains(err.Error(), "tls: ")
}
