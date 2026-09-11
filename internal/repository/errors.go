package repository

import "errors"

// Sentinel errors returned by repository implementations.
// Service layer checks these with errors.Is to decide HTTP status codes.
var (
	ErrMerchantNotFound    = errors.New("merchant not found")
	ErrTransactionNotFound = errors.New("transaction not found")
	ErrDuplicateOrder      = errors.New("duplicate merchant order id")
)
