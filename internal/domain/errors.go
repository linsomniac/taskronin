package domain

import (
	"errors"
	"fmt"
	"time"
)

// Code is a stable service error code.
type Code string

const (
	// CodeMalformed denotes malformed input (400).
	CodeMalformed Code = "malformed"
	// CodeUnauthenticated denotes missing or invalid authentication (401).
	CodeUnauthenticated Code = "unauthenticated"
	// CodeForbidden denotes insufficient authority (403).
	CodeForbidden Code = "forbidden"
	// CodePeerRejected denotes a rejected socket peer (403).
	CodePeerRejected Code = "peer_rejected"
	// CodePasswordRequired denotes missing password confirmation (403).
	CodePasswordRequired Code = "password_required"
	// CodePasswordInvalid denotes invalid password confirmation (403).
	CodePasswordInvalid Code = "password_invalid"
	// CodeNotFound denotes an unknown entity (404).
	CodeNotFound Code = "not_found"
	// CodeDeleted denotes a mutation involving a tombstoned entity (409).
	CodeDeleted Code = "deleted"
	// CodeConflict denotes a state or invariant conflict (409).
	CodeConflict Code = "conflict"
	// CodeRevertConflict denotes conflicting changes that prevent revert (409).
	CodeRevertConflict Code = "revert_conflict"
	// CodeNotRevertible denotes an operation that cannot be reverted (409).
	CodeNotRevertible Code = "not_revertible"
	// CodePrecondition denotes a stale entity precondition (412).
	CodePrecondition Code = "precondition_failed"
	// CodeTooLarge denotes an oversized request body (413).
	CodeTooLarge Code = "body_too_large"
	// CodeUnsupportedMedia denotes an unsupported content type (415).
	CodeUnsupportedMedia Code = "unsupported_media_type"
	// CodeValidation denotes invalid field values (422).
	CodeValidation Code = "validation"
	// CodeIdemMismatch denotes a reused idempotency key with different input (422).
	CodeIdemMismatch Code = "idempotency_mismatch"
	// CodeNothingToUndo denotes an empty undo stack (409).
	CodeNothingToUndo Code = "nothing_to_undo"
	// CodeNothingToRedo denotes an empty redo stack (409).
	CodeNothingToRedo Code = "nothing_to_redo"
	// CodePreconditionReq denotes a missing precondition (428).
	CodePreconditionReq Code = "precondition_required"
	// CodeIdemRequired denotes a missing idempotency key (428).
	CodeIdemRequired Code = "idempotency_key_required"
	// CodeGenerationMismatch denotes a precondition for a different database generation (412).
	CodeGenerationMismatch Code = "generation_mismatch"
	// CodeRateLimited denotes a rate limit (429).
	CodeRateLimited Code = "rate_limited"
	// CodeInternal denotes an internal failure (500).
	CodeInternal Code = "internal"
	// CodeBusy denotes temporary unavailability (503).
	CodeBusy Code = "busy"
)

// ErrorCodes returns every defined service error code. Adapter drift tests also
// check this catalog against the declarations so additions cannot be omitted.
func ErrorCodes() []Code {
	return []Code{
		CodeMalformed,
		CodeUnauthenticated,
		CodeForbidden,
		CodePeerRejected,
		CodePasswordRequired,
		CodePasswordInvalid,
		CodeNotFound,
		CodeDeleted,
		CodeConflict,
		CodeRevertConflict,
		CodeNotRevertible,
		CodePrecondition,
		CodeTooLarge,
		CodeUnsupportedMedia,
		CodeValidation,
		CodeIdemMismatch,
		CodeNothingToUndo,
		CodeNothingToRedo,
		CodePreconditionReq,
		CodeIdemRequired,
		CodeGenerationMismatch,
		CodeRateLimited,
		CodeInternal,
		CodeBusy,
	}
}

// Error carries a service error's safe detail, structured context, and private cause.
type Error struct {
	Code       Code
	Detail     string // safe for users; never contains secrets
	Fields     []FieldError
	Conflicts  []RevertConflict
	Current    any // current representation for a failed precondition
	RetryAfter time.Duration
	Err        error // wrapped cause; logged, never sent
}

// Error returns the code and safe detail, without the private cause.
func (e *Error) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

// Unwrap returns the private cause for errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Errorf constructs an error with a formatted, user-safe detail.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// AsError finds a wrapped domain error, or returns an internal error with err
// as its private cause and false.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return &Error{Code: CodeInternal, Err: err}, false
}

// FieldError identifies a field and its validation failure.
type FieldError struct{ Field, Message string }

// ValidationError constructs a validation error with the supplied field failures.
func ValidationError(fields ...FieldError) *Error {
	return &Error{Code: CodeValidation, Detail: "invalid input", Fields: fields}
}

// RevertConflict describes a later operation that conflicts with a revert.
type RevertConflict struct {
	OpID     OpID
	Kind     OpKind
	Actor    string
	At       time.Time
	Entity   EntityKind
	EntityID int64
}
