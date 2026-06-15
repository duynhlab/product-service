package domain

import "errors"

// Common domain errors
var (
	ErrNotFound     = errors.New("resource not found")
	ErrInvalidInput = errors.New("invalid input")
	ErrConflict     = errors.New("resource conflict")
	// ErrInsufficientStock is returned by ReserveStock when one or more items
	// lack the requested quantity (the whole reservation is rejected).
	ErrInsufficientStock = errors.New("insufficient stock")
)
