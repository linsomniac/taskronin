package server

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/backup"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	v, err := decodeRequest[api.SettingsRequest](r)
	if err != nil {
		v.Password = ""
		s.writeProblem(w, r, err)
		return
	}
	out, err := s.svc.UpdateSettings(r.Context(), s.exemptMeta(r), service.UpdateSettingsIn{KeepDaily: v.BackupKeepDaily, KeepWeekly: v.BackupKeepWeekly, Password: v.Password})
	v.Password = ""
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, api.Settings{BackupKeepDaily: out.KeepDaily, BackupKeepWeekly: out.KeepWeekly})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.svc.ListTokens(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	out := make([]api.TokenInfo, 0, len(tokens))
	for _, token := range tokens {
		out = append(out, s.conv.Token(token))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	v, err := decodeRequest[api.CreateTokenRequest](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	if !v.ExpiresAt.Set {
		s.writeProblem(w, r, domain.ValidationError(domain.FieldError{Field: "expires_at", Message: "must be present; use null for no expiry"}))
		return
	}
	scopes, err := domain.ParseScopes(v.Scopes)
	if err != nil {
		s.writeProblem(w, r, domain.ValidationError(domain.FieldError{Field: "scopes", Message: err.Error()}))
		return
	}
	expiry := sql.Null[time.Time]{}
	if !v.ExpiresAt.Null {
		expiry = sql.Null[time.Time]{V: v.ExpiresAt.V.Time, Valid: true}
	}
	out, err := s.svc.CreateToken(r.Context(), s.exemptMeta(r), service.CreateTokenIn{Name: v.Name, Scopes: scopes, ExpiresAt: expiry, MutationsPerHour: v.MutationsPerHour})
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tokens/"+strconv.FormatInt(int64(out.Token.ID), 10))
	s.writeJSON(w, http.StatusCreated, api.TokenCreated{Token: s.conv.Token(out.Token), Secret: out.Secret})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	_, body, err := decodeBody[struct{}](r)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	id, err := pathInt(r, "id")
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	out, err := s.svc.RevokeToken(r.Context(), s.meta(r, body), service.RevokeTokenIn{ID: domain.TokenID(id)})
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.conv.Token(out))
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	v, err := decodeRequest[api.PasswordRequest](r)
	if err != nil {
		v.Current, v.New = "", ""
		s.writeProblem(w, r, err)
		return
	}
	err = s.svc.ChangePassword(r.Context(), s.exemptMeta(r), service.ChangePasswordIn{Current: v.Current, New: v.New})
	// AIDEV-NOTE: Password DTOs never enter canonical/idempotency bodies; drop
	// their fields immediately after the one service call, before responding.
	v.Current, v.New = "", ""
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.BackupStatus(r.Context(), state(r).actor)
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	out := api.BackupStatus{Dir: v.Dir, BackupHealth: api.BackupHealth{LastSuccessAt: s.conv.OptTime(v.LastSuccessAt), LastError: v.LastError, Healthy: v.Healthy}, Snapshots: make([]api.Snapshot, 0, len(v.Snapshots))}
	for _, snapshot := range v.Snapshots {
		out.Snapshots = append(out.Snapshots, s.snapshot(snapshot))
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) runBackup(w http.ResponseWriter, r *http.Request) {
	if _, err := decodeRequest[struct{}](r); err != nil {
		s.writeProblem(w, r, err)
		return
	}
	// AIDEV-NOTE: BF14 keeps RunBackup usable by scheduler/offline callers;
	// the HTTP adapter supplies the generation precondition absent from its API.
	gen := s.exemptMeta(r).Gen
	if gen == "" {
		s.writeProblem(w, r, domain.Errorf(domain.CodePreconditionReq, "database generation is required"))
		return
	}
	if gen != s.svc.Generation() {
		s.writeProblem(w, r, domain.Errorf(domain.CodeGenerationMismatch, "database generation does not match"))
		return
	}
	v, err := s.svc.RunBackup(r.Context(), state(r).actor, backup.KindManual)
	if s.longStopped(r) {
		panic(http.ErrAbortHandler)
	}
	if err != nil {
		s.writeProblem(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/backups")
	s.writeJSON(w, http.StatusCreated, s.snapshot(v))
}

func (s *Server) snapshot(v backup.SnapshotInfo) api.Snapshot {
	return api.Snapshot{Name: v.Name, Path: v.Path, Kind: string(v.Kind), At: s.conv.Time(v.At), Size: v.Size, SchemaVersion: v.SchemaVersion}
}

type exportWriter struct {
	http.ResponseWriter
	started bool
}

func (w *exportWriter) Write(p []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(p)
}

func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", api.ContentTypeJSON)
	w.Header().Set("Content-Disposition", `attachment; filename="taskronin-export.json"`)
	out := &exportWriter{ResponseWriter: w}
	err := s.svc.Export(r.Context(), state(r).actor, out)
	// AIDEV-NOTE: Never turn a partial export into a successful HTTP document
	// or append a problem to it. net/http closes the connection on this sentinel.
	if s.longStopped(r) || (err != nil && out.started) {
		panic(http.ErrAbortHandler)
	}
	if err != nil {
		w.Header().Del("Content-Disposition")
		s.writeProblem(w, r, err)
	}
}

func (s *Server) longStopped(r *http.Request) bool {
	if r.Context().Err() != nil {
		return true
	}
	select {
	case <-s.svc.Stopping():
		return true
	default:
		return false
	}
}
