// Package apperr define el error tipado que cruza el puente Go→Vue (spec 02).
package apperr

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	Validation         = "VALIDATION"
	NotFound           = "NOT_FOUND"
	AuthFailed         = "AUTH_FAILED"
	NetworkUnreachable = "NETWORK_UNREACHABLE"
	Timeout            = "TIMEOUT"
	TLSError           = "TLS_ERROR"
	SSHAuthFailed      = "SSH_AUTH_FAILED"
	SSHHostKeyMismatch = "SSH_HOSTKEY_MISMATCH"
	SSHHostKeyUnknown  = "SSH_HOSTKEY_UNKNOWN"
	KeyringUnavailable = "KEYRING_UNAVAILABLE"
	PasswordRequired   = "PASSWORD_REQUIRED" // Detail: password, sshPassword o sshKeyPassphrase
	SessionClosed      = "SESSION_CLOSED"
	QuerySyntax        = "QUERY_SYNTAX"
	QueryCanceled      = "QUERY_CANCELED"
	QueryFailed        = "QUERY_FAILED"
	ResultExpired      = "RESULT_EXPIRED"
	Unsupported        = "UNSUPPORTED"
	Internal           = "INTERNAL"
)

type AppError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Detail    string `json:"detail,omitempty"`
	Retryable bool   `json:"retryable"`
	cause     error
}

func (e *AppError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error { return e.cause }

func New(code, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

func Newf(code, format string, args ...any) *AppError {
	return New(code, fmt.Sprintf(format, args...))
}

// Wrap conserva err como causa y como Detail técnico (puede llevar códigos del motor, nunca secretos).
func Wrap(code, message string, err error) *AppError {
	e := &AppError{Code: code, Message: message, cause: err}
	if err != nil {
		e.Detail = err.Error()
	}
	return e
}

func (e *AppError) WithDetail(detail string) *AppError { e.Detail = detail; return e }

func (e *AppError) AsRetryable() *AppError { e.Retryable = true; return e }

// From garantiza un *AppError: lo devuelve tal cual si ya lo es, o envuelve como INTERNAL.
func From(err error) *AppError {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	return Wrap(Internal, "Error interno inesperado", err)
}

// Marshal es el MarshalError de Wails: el front recibe siempre un AppError serializado en `cause`.
func Marshal(err error) []byte {
	b, merr := json.Marshal(From(err))
	if merr != nil {
		return []byte(`{"code":"INTERNAL","message":"Error interno inesperado","retryable":false}`)
	}
	return b
}
