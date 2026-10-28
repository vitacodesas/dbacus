package apperr

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromWrapsUnknownAsInternal(t *testing.T) {
	ae := From(errors.New("boom"))
	require.Equal(t, Internal, ae.Code)
	require.Equal(t, "boom", ae.Detail)
}

func TestFromKeepsAppErrorThroughWrapping(t *testing.T) {
	orig := New(NotFound, "no existe")
	ae := From(fmt.Errorf("ctx: %w", orig))
	require.Same(t, orig, ae)
}

func TestFromNil(t *testing.T) { require.Nil(t, From(nil)) }

func TestMarshalShape(t *testing.T) {
	var got map[string]any
	require.NoError(t, json.Unmarshal(Marshal(New(Validation, "x").AsRetryable().WithDetail("d")), &got))
	require.Equal(t, map[string]any{"code": "VALIDATION", "message": "x", "detail": "d", "retryable": true}, got)
}

func TestErrorsAs(t *testing.T) {
	err := fmt.Errorf("x: %w", Wrap(Timeout, "tarda", context()))
	var ae *AppError
	require.ErrorAs(t, err, &ae)
	require.ErrorIs(t, err, errBase)
}

var errBase = errors.New("base")

func context() error { return errBase }
