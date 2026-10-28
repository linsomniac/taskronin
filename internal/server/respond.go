package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/domain"
	"taskronin/internal/service"
	"taskronin/internal/textsafe"
)

func statusFor(c domain.Code) int {
	switch c {
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
	case domain.CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, err error) {
	de, _ := domain.AsError(err)
	status := statusFor(de.Code)
	p := api.Problem{Type: api.ProblemTypePrefix + string(de.Code), Title: http.StatusText(status), Status: status, Code: string(de.Code), Detail: de.Detail, RequestID: w.Header().Get(api.HeaderRequestID)}
	if status == http.StatusInternalServerError {
		p.Code = string(domain.CodeInternal)
		p.Type = api.ProblemTypePrefix + p.Code
		p.Detail = "internal error"
	} else {
		for _, field := range de.Fields {
			p.Errors = append(p.Errors, api.FieldError{Field: field.Field, Message: field.Message})
		}
		for _, c := range de.Conflicts {
			p.Conflicts = append(p.Conflicts, api.Conflict{OpID: c.OpID.String(), Kind: string(c.Kind), Actor: c.Actor, At: s.conv.Time(c.At), Entity: string(c.Entity), EntityID: c.EntityID})
		}
		if de.Current != nil {
			current, err := s.currentResponse(de.Current)
			if err != nil {
				s.writeProblem(w, r, err)
				return
			}
			p.Current = current
		}
	}
	if status == http.StatusInternalServerError || de.Err != nil {
		ctx := context.Background()
		if r != nil {
			ctx = r.Context()
		}
		diagnostic := error(de)
		if err != nil {
			diagnostic = err
		}
		if de.Err != nil {
			diagnostic = de.Err
		}
		// AIDEV-NOTE: Keep private causes in sanitized diagnostics, never in
		// the problem body. writeJSON has no request, so correlate its failures
		// with the generated response ID rather than requiring request state.
		s.log.ErrorContext(ctx, "HTTP request failed", "request_id", p.RequestID,
			"code", textsafe.SanitizeLine(p.Code), "error", textsafe.SanitizeLine(diagnostic.Error()))
	}
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		seconds := de.RetryAfter / time.Second
		if de.RetryAfter%time.Second > 0 {
			seconds++
		}
		if seconds < 1 {
			seconds = 1
		}
		p.RetryAfter = int(seconds)
		w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfter))
	}
	w.Header().Set("Content-Type", api.ContentTypeProblem)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		s.writeProblem(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", api.ContentTypeJSON)
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func (s *Server) writeOp(w http.ResponseWriter, _ *http.Request, res *service.OpResult, location string) {
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
		w.Header().Set("Location", location)
	}
	s.writeJSON(w, status, s.opResponse(res))
}
