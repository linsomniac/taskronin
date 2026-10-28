package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"runtime/debug"
	"strings"

	"taskronin/internal/api"
	"taskronin/internal/buildinfo"
	"taskronin/internal/domain"
	"taskronin/internal/textsafe"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

// Unwrap keeps ResponseController's deadlines and streaming operations available.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mandatory response headers precede every guard, including panic recovery.
		setAPIHeaders(w)
		r = r.WithContext(context.WithValue(r.Context(), requestStateKey{}, &requestState{}))
		writer := &statusWriter{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				// AIDEV-NOTE: Exports abort after partial output. Preserve net/http's
				// sentinel; appending a problem would corrupt a streamed response.
				if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(http.ErrAbortHandler)
				}
				s.log.ErrorContext(r.Context(), "HTTP handler panic", "request_id", state(r).id, "stack", textsafe.Sanitize(string(debug.Stack())))
				if writer.status != 0 {
					panic(http.ErrAbortHandler)
				}
				s.writeProblem(writer, r, domain.Errorf(domain.CodeInternal, "internal error"))
			}
		}()
		next.ServeHTTP(writer, r)
	})
}

func (*Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw [8]byte
		_, _ = rand.Read(raw[:])
		id := hex.EncodeToString(raw[:])
		state(r).id = id
		w.Header().Set(api.HeaderRequestID, id)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.clock.Now()
		writer := &statusWriter{ResponseWriter: w}
		defer func() {
			p := recover()
			status := writer.status
			if status == 0 {
				status = http.StatusOK
				if p != nil {
					status = http.StatusInternalServerError
				}
			}
			st := state(r)
			s.log.InfoContext(r.Context(), "HTTP request", "method", textsafe.SanitizeLine(r.Method), "route", textsafe.SanitizeLine(st.route),
				"status", status, "duration", s.clock.Now().Sub(start), "request_id", st.id, "actor", textsafe.SanitizeLine(st.actor.Key()))
			if p != nil {
				panic(p)
			}
		}()
		next.ServeHTTP(writer, r)
	})
}

func (s *Server) socketGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ci := ConnInfoFrom(r.Context())
		// AIDEV-NOTE: Only SO_PEERCRED from the accepted Unix socket establishes
		// trust. Fail closed on missing credentials, foreign UIDs and proxy
		// headers (even empty ones). authn may pass verifiedSocket=true only
		// behind this guard: owner tokens are valid exclusively on this socket.
		if ci.Transport != TransportUnix || !ci.PeerOK || ci.PeerUID != ourUID() {
			s.writeProblem(w, r, domain.Errorf(domain.CodePeerRejected, "unverified socket peer"))
			return
		}
		for name := range r.Header {
			name = strings.ToLower(name)
			if name == "forwarded" || name == "via" || strings.HasPrefix(name, "x-forwarded-") {
				s.writeProblem(w, r, domain.Errorf(domain.CodePeerRejected, "forwarding headers are not permitted"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routeClass(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") && r.URL.Path != "/healthz" {
			s.writeProblem(w, r, domain.Errorf(domain.CodeNotFound, "route not found"))
			return
		}
		for name := range r.Header {
			if strings.EqualFold(name, "Cookie") {
				delete(r.Header, name)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, api.MaxBodyBytes)
			// Read through the limit even when a handler ignores its body or the
			// sender uses chunked transfer. Content-Length alone is not proof.
			body, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				s.writeProblem(w, r, bodyReadError(err))
				return
			}
			if len(body) > 0 {
				media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || media != api.ContentTypeJSON {
					s.writeProblem(w, r, domain.Errorf(domain.CodeUnsupportedMedia, "request body requires application/json"))
					return
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		channel := domain.Channel(r.Header.Get(api.HeaderChannel))
		switch channel {
		case "":
			channel = domain.ChanAPI
		case domain.ChanCLI, domain.ChanTUI, domain.ChanMCP, domain.ChanAPI:
		default:
			s.writeProblem(w, r, domain.Errorf(domain.CodeMalformed, "invalid API channel"))
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			s.writeProblem(w, r, domain.Errorf(domain.CodeUnauthenticated, "bearer token required"))
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			s.writeProblem(w, r, domain.Errorf(domain.CodeUnauthenticated, "bearer token required"))
			return
		}
		actor, err := s.svc.AuthenticateToken(r.Context(), parts[1], true, channel, "socket")
		if err != nil {
			s.writeProblem(w, r, err)
			return
		}
		state(r).actor = actor
		next.ServeHTTP(w, r)
	})
}

func setAPIHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set(api.HeaderVersion, buildinfo.Version)
}

func (*Server) apiHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAPIHeaders(w)
		next.ServeHTTP(w, r)
	})
}
