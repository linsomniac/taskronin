package client

import (
	"errors"
	"fmt"

	"taskronin/internal/api"
	"taskronin/internal/domain"
)

// Failure classes used by the CLI and pending-request recovery.
var (
	ErrNotSent            = errors.New("request not sent")
	ErrOutcomeUnknown     = errors.New("request outcome unknown")
	ErrGenerationMismatch = errors.New("database generation changed")
	ErrTokenFile          = errors.New("token file unusable")
	ErrPendingExpired     = errors.New("pending request older than 7 days")
	ErrActorMismatch      = errors.New("pending request belongs to another actor")
)

// ProblemError preserves the complete RFC 9457 problem and HTTP status.
type ProblemError struct {
	Status  int
	Problem api.Problem
}

func (e *ProblemError) Error() string { return e.Problem.Error() }

// Code returns the stable domain error code.
func (e *ProblemError) Code() domain.Code { return domain.Code(e.Problem.Code) }

// Is recognizes the generation-change sentinel without losing problem details.
func (e *ProblemError) Is(target error) bool {
	return target == ErrGenerationMismatch && e.Code() == domain.CodeGenerationMismatch
}

// OutcomeUnknownError indicates that request bytes were written but no complete
// response was received. Saved reports whether pending persistence succeeded.
type OutcomeUnknownError struct {
	Key   string
	Saved bool
	Err   error
}

func (e *OutcomeUnknownError) Error() string {
	return fmt.Sprintf("%s: %v", ErrOutcomeUnknown, e.Err)
}

// Unwrap preserves transport, context and pending-write errors.
func (e *OutcomeUnknownError) Unwrap() error { return e.Err }

// Is recognizes the outcome-unknown sentinel.
func (e *OutcomeUnknownError) Is(target error) bool { return target == ErrOutcomeUnknown }

// IsCode reports whether err contains an API problem with code.
func IsCode(err error, code domain.Code) bool {
	var problem *ProblemError
	return errors.As(err, &problem) && problem.Code() == code
}
