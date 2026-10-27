// Package apperr defines the error kinds services return, so every caller
// (REST, CLI, MCP) can map them the same way with errors.Is.
package apperr

import "errors"

var (
	ErrNotFound        = errors.New("not found")
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrConflict        = errors.New("conflict")
	ErrInvalid         = errors.New("invalid input")
	ErrTooLarge        = errors.New("too large")
	// ErrBusy means the caller already has a run of this kind in progress
	// and must wait for it.
	ErrBusy = errors.New("busy")
)
