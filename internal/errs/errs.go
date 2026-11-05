// Package errs defines the machine-readable error codes, exit statuses and
// error type shared by every simpool command.
package errs

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error identifier. Agents branch on these
// values, so they must not change once released.
type Code string

const (
	CodeInvalidArgs       Code = "invalid_args"
	CodeDeviceNotFound    Code = "device_not_found"
	CodeLeaseNotFound     Code = "lease_not_found"
	CodeLeaseNotLive      Code = "lease_not_live"
	CodeDeviceLeased      Code = "device_leased"
	CodeDeviceMissing     Code = "device_missing"
	CodeNoDeviceAvailable Code = "no_device_available"
	CodeProvider          Code = "provider_error"
	CodeTimeout           Code = "timeout"
	CodeState             Code = "state_error"
	CodeInternal          Code = "internal_error"
)

// Exit statuses. Codes are grouped so that a caller can branch on a class of
// failure without knowing every individual error code.
const (
	ExitOK          = 0
	ExitInternal    = 1
	ExitInvalidArgs = 2
	ExitNotFound    = 3
	ExitConflict    = 4
	ExitUnavailable = 5
	ExitProvider    = 6
	ExitTimeout     = 7
)

// Error carries a code, a human message and optional structured details that
// are rendered verbatim into `--json` output.
type Error struct {
	Code    Code
	Message string
	Details map[string]any
	cause   error
}

func (e *Error) Error() string {
	if e.cause == nil {
		return e.Message
	}

	return fmt.Sprintf("%s: %s", e.Message, e.cause)
}

func (e *Error) Unwrap() error {
	return e.cause
}

// WithDetail attaches a structured detail field and returns the same error.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}

	e.Details[key] = value

	return e
}

// New builds a coded error.
func New(code Code, format string, args ...any) *Error {
	return &Error{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
	}
}

// Wrap builds a coded error that keeps the underlying cause for `errors.Is`.
func Wrap(err error, code Code, format string, args ...any) *Error {
	return &Error{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		cause:   err,
	}
}

// CodeOf reports the code of the first *Error in the chain, or CodeInternal.
func CodeOf(err error) Code {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}

	return CodeInternal
}

// DetailsOf reports the structured details of the first *Error in the chain.
func DetailsOf(err error) map[string]any {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Details
	}

	return nil
}

// ExitCode maps an error to the process exit status.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}

	switch CodeOf(err) {
	case CodeInvalidArgs:
		return ExitInvalidArgs
	case CodeDeviceNotFound, CodeLeaseNotFound:
		return ExitNotFound
	case CodeLeaseNotLive, CodeDeviceLeased, CodeDeviceMissing:
		return ExitConflict
	case CodeNoDeviceAvailable:
		return ExitUnavailable
	case CodeProvider:
		return ExitProvider
	case CodeTimeout:
		return ExitTimeout
	default:
		return ExitInternal
	}
}
