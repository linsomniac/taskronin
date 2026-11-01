package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"syscall"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/config"
	"taskronin/internal/domain"
	"taskronin/internal/textsafe"
)

type usageError struct{ detail string }

func (e *usageError) Error() string        { return e.detail }
func usagef(format string, a ...any) error { return &usageError{detail: fmt.Sprintf(format, a...)} }

// checkFoundError reports that a check-only run found work still to do (exit 8,
// A§9.6). It is not a failure of the command itself, so it has its own exit
// code that scripts can tell apart from errors (exit 1).
type checkFoundError struct{ detail string }

func (e *checkFoundError) Error() string { return e.detail }

func tokenConfigError(err error) error {
	// AIDEV-NOTE: config rejects symlinked token paths before client.New runs.
	// Preserve the same exit code and recovery guidance as other token failures.
	var tokenErr *config.TokenFileError
	if errors.As(err, &tokenErr) {
		return fmt.Errorf("%w: %w — run `tkr init` (or pass --token-file)", client.ErrTokenFile, err)
	}
	return err
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var usage *usageError
	if errors.As(err, &usage) {
		return 2
	}
	var found *checkFoundError
	if errors.As(err, &found) {
		return 8
	}
	if errors.Is(err, client.ErrTokenFile) {
		return 4
	}
	if errors.Is(err, client.ErrNotSent) || errors.Is(err, client.ErrOutcomeUnknown) || errors.Is(err, context.DeadlineExceeded) {
		return 3
	}
	var code domain.Code
	var problem *client.ProblemError
	if errors.As(err, &problem) {
		code = problem.Code()
	} else if de, ok := domain.AsError(err); ok {
		code = de.Code
	}
	switch code {
	case domain.CodeUnauthenticated, domain.CodeForbidden, domain.CodePeerRejected, domain.CodePasswordRequired, domain.CodePasswordInvalid:
		return 4
	case domain.CodeNotFound:
		return 5
	case domain.CodeConflict, domain.CodeDeleted, domain.CodeRevertConflict, domain.CodeNotRevertible, domain.CodeNothingToUndo, domain.CodeNothingToRedo, domain.CodePrecondition, domain.CodePreconditionReq, domain.CodeGenerationMismatch:
		return 6
	case domain.CodeRateLimited, domain.CodeBusy:
		return 7
	default:
		return 1
	}
}

var shellSafeArg = regexp.MustCompile(`^[a-zA-Z0-9_./:@%+=,-]+$`)

func quoteArgs(argv []string) string {
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		if shellSafeArg.MatchString(arg) {
			quoted[i] = arg
		} else {
			quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
		}
	}
	return textsafe.SanitizeLine(strings.Join(quoted, " "))
}

func (e *env) fail(err error) int {
	code := exitCode(err)
	p := problemFor(err)
	if e.draftPath != "" {
		defer e.errorLine("tkr: your draft is at %s", e.draftPath)
	}
	if e.jsonOut {
		if encodeErr := json.NewEncoder(e.out).Encode(p); encodeErr != nil {
			e.errorLine("tkr: write problem: %s", encodeErr)
		}
		return code
	}
	var unknown *client.OutcomeUnknownError
	switch {
	case errors.As(err, &unknown):
		// AIDEV-NOTE: cancellation after writing is still outcome-unknown.
		// Preserve recovery advice before returning the interrupted exit code.
		if unknown.Saved {
			e.errorLine("tkr: outcome unknown — saved as pending request %s; run `tkr pending retry`", unknown.Key)
		} else {
			e.errorLine("tkr: outcome unknown — the server may or may not have applied this, and it was not saved for retry (%v)", unknown.Err)
			e.errorLine("tkr: check with `tkr show` / `tkr entries` before re-running. Your command was:")
			e.commandEcho()
		}
	case errors.Is(err, client.ErrNotSent):
		socket := e.paths.Socket
		if e.ccfg != nil {
			socket = e.ccfg.Socket
		}
		if e.socket != "" {
			socket = e.socket
		}
		e.errorLine("tkr: cannot reach the Taskronin server at %s: %s", socket, notSentCause(err))
		e.errorLine("tkr: nothing was saved. Your command was:")
		e.commandEcho()
		e.errorLine("tkr: start it with: systemctl --user start taskronin.socket")
	case errors.Is(err, client.ErrTokenFile):
		e.errorLine("tkr: %s", err)
		if e.mutates {
			e.errorLine("tkr: nothing was saved. Your command was:")
			e.commandEcho()
		}
	case errors.Is(err, context.Canceled):
		e.errorLine("tkr: interrupted")
	default:
		e.errorLine("tkr: %s", p.Detail)
		for _, field := range p.Errors {
			e.errorLine("  %s: %s", field.Field, field.Message)
		}
		for _, c := range p.Conflicts {
			e.errorLine("  op %.8s %s by %s at %s (%s %d)", c.OpID, c.Kind, c.Actor, c.At.Format("2006-01-02 15:04"), c.Entity, c.EntityID)
		}
		if p.Status == http.StatusPreconditionFailed && len(p.Current) > 0 {
			var current struct{ ID, Version int64 }
			if json.Unmarshal(p.Current, &current) == nil && current.ID > 0 {
				e.errorLine("  current: #%d version %d", current.ID, current.Version)
			}
		}
	}
	return code
}

func (e *env) commandEcho() { e.errorLine("  tkr %s", quoteArgs(e.argv)) }

func notSentCause(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return op.Err.Error()
	}
	return strings.TrimPrefix(err.Error(), client.ErrNotSent.Error()+": ")
}

func problemFor(err error) api.Problem {
	var pe *client.ProblemError
	if errors.As(err, &pe) {
		return pe.Problem
	}
	p := api.Problem{Code: string(domain.CodeInternal), Detail: textsafe.SanitizeLine(err.Error()), Status: http.StatusInternalServerError}
	if de, ok := domain.AsError(err); ok {
		p.Code, p.Detail, p.Status = string(de.Code), de.Detail, problemStatus(de.Code)
		// AIDEV-NOTE: domain errors here are local (offline commands, config,
		// the CLI itself): API failures arrive as ProblemError above and never
		// carry a cause. A local internal failure shows its sanitized cause to
		// the owner's own terminal instead of a bare "internal" (B-I1).
		if p.Detail == "" && de.Code == domain.CodeInternal && de.Err != nil {
			p.Detail = textsafe.SanitizeLine(de.Err.Error())
		}
		if p.Detail == "" {
			p.Detail = string(de.Code)
		}
		for _, f := range de.Fields {
			p.Errors = append(p.Errors, api.FieldError{Field: f.Field, Message: f.Message})
		}
		for _, c := range de.Conflicts {
			p.Conflicts = append(p.Conflicts, api.Conflict{OpID: c.OpID.String(), Kind: string(c.Kind), Actor: c.Actor, At: api.Time{Time: c.At}, Entity: string(c.Entity), EntityID: c.EntityID})
		}
		if de.Current != nil {
			p.Current, _ = json.Marshal(de.Current)
		}
		p.RetryAfter = int(de.RetryAfter / time.Second)
	}
	p.Type, p.Title = api.ProblemTypePrefix+p.Code, http.StatusText(p.Status)
	return p
}

// Local/offline domain failures use the same problem vocabulary as the API.
func problemStatus(code domain.Code) int {
	switch code {
	case domain.CodeMalformed:
		return http.StatusBadRequest
	case domain.CodeUnauthenticated:
		return http.StatusUnauthorized
	case domain.CodeForbidden, domain.CodePeerRejected, domain.CodePasswordRequired, domain.CodePasswordInvalid:
		return http.StatusForbidden
	case domain.CodeNotFound:
		return http.StatusNotFound
	case domain.CodeDeleted, domain.CodeConflict, domain.CodeRevertConflict, domain.CodeNotRevertible, domain.CodeNothingToUndo, domain.CodeNothingToRedo:
		return http.StatusConflict
	case domain.CodePrecondition, domain.CodeGenerationMismatch:
		return http.StatusPreconditionFailed
	case domain.CodeTooLarge:
		return http.StatusRequestEntityTooLarge
	case domain.CodeUnsupportedMedia:
		return http.StatusUnsupportedMediaType
	case domain.CodeValidation, domain.CodeIdemMismatch:
		return http.StatusUnprocessableEntity
	case domain.CodePreconditionReq, domain.CodeIdemRequired:
		return http.StatusPreconditionRequired
	case domain.CodeRateLimited:
		return http.StatusTooManyRequests
	case domain.CodeBusy:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
