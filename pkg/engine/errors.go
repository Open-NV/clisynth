package engine

import (
	"errors"
	"fmt"
)

const (
	CodeInvalidRequest     = "invalid_request"
	CodeDefinitionNotFound = "definition_not_found"
	CodeInventoryField     = "inventory_field_error"
	CodeProcessing         = "processing_error"
	CodeCanceled           = "canceled"
)

// Error provides a stable machine-readable code without exposing internals in
// HTTP responses.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func errorCode(err error) string {
	var engineErr *Error
	if errors.As(err, &engineErr) {
		return engineErr.Code
	}
	return CodeProcessing
}
