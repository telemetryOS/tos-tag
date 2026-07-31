// Package tagerr defines stable domain errors translated by outer boundaries.
package tagerr

import "errors"

var (
	ErrInvalid      = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrLeaseLost    = errors.New("lease lost")
	ErrExhausted    = errors.New("attempts exhausted")
	ErrKillSwitched = errors.New("kill switch active")
)
