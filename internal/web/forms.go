package web

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"

	"taskronin/internal/civil"
	"taskronin/internal/datespec"
	"taskronin/internal/domain"
	"taskronin/internal/service"
)

type sessionInfo struct {
	Actor        domain.Actor
	CSRF, Cookie string
}

type sessionKey struct{}

func withSession(ctx context.Context, s sessionInfo) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionFrom(ctx context.Context) (sessionInfo, bool) {
	s, ok := ctx.Value(sessionKey{}).(sessionInfo)
	return s, ok
}

func (a *app) expect(form url.Values, key string) (domain.Expect, error) {
	if form.Get(key) == "" {
		return domain.Expect{}, domain.Errorf(domain.CodePreconditionReq, "missing %s", key)
	}
	return domain.ParseETag(form.Get(key))
}

func safeReturn(raw string) string {
	// AIDEV-NOTE: Open-redirect guard: only local /ui/ targets survive.
	// Check the decoded path too, so escaped traversal/backslashes cannot bypass it.
	if len(raw) > 2048 || !strings.HasPrefix(raw, "/ui/") || strings.ContainsAny(raw, "\r\n\\") {
		return "/ui/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.ContainsAny(u.Path, "\r\n\\") {
		return "/ui/"
	}
	clean := path.Clean(u.Path)
	if clean != "/ui" && !strings.HasPrefix(clean, "/ui/") {
		return "/ui/"
	}
	return raw
}

func idemBody(form url.Values) []byte {
	// AIDEV-NOTE: Canonical web idempotency body (arch §7.5): Encode sorts
	// keys but preserves repeated-value order. Credentials must never be persisted.
	copyForm := make(url.Values, len(form))
	for key, values := range form {
		switch key {
		case "_csrf", "_nonce", "password":
			continue
		}
		copyForm[key] = append([]string(nil), values...)
	}
	return []byte(copyForm.Encode())
}

func moreURL(u *url.URL, cursor string) string {
	query := u.Query()
	query.Set("cursor", cursor)
	return u.EscapedPath() + "?" + query.Encode()
}

func (a *app) meta(r *http.Request) (service.Meta, error) {
	s, ok := sessionFrom(r.Context())
	if !ok {
		return service.Meta{}, domain.Errorf(domain.CodeUnauthenticated, "session required")
	}
	return service.Meta{
		Actor: s.Actor, Gen: domain.Generation(r.PostForm.Get("_gen")),
		Idem: service.Idem{Key: r.PostForm.Get("_nonce"), Method: http.MethodPost,
			Route: "POST " + r.URL.Path, Body: idemBody(r.PostForm)},
	}, nil
}

func formInt(form url.Values, key string) (int64, error) {
	id, err := positiveInt(strings.TrimPrefix(form.Get(key), "#"))
	if err != nil {
		return 0, &domain.Error{Code: domain.CodeMalformed, Detail: "invalid ID",
			Fields: []domain.FieldError{{Field: key, Message: "expected a positive integer ID"}}}
	}
	return id, nil
}

func positiveInt(raw string) (int64, error) {
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, domain.Errorf(domain.CodeMalformed, "invalid ID")
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		return 0, domain.Errorf(domain.CodeMalformed, "invalid ID")
	}
	return id, nil
}

func pathInt(r *http.Request, name string) (int64, error) {
	id, err := positiveInt(r.PathValue(name))
	if err != nil {
		return 0, domain.Errorf(domain.CodeNotFound, "not found")
	}
	return id, nil
}

func (a *app) ref() datespec.Ref {
	return datespec.Ref{Today: a.o.Zone.DateOf(a.o.Clock.Now()), WeekStart: a.o.WeekStart}
}

func (a *app) formMoment(form url.Values, key string, m datespec.Mode) (*domain.Moment, error) {
	if form.Get(key) == "" {
		return nil, nil
	}
	moment, err := datespec.Parse(form.Get(key), m, a.ref())
	if err != nil {
		return nil, domain.ValidationError(domain.FieldError{Field: key, Message: err.Error()})
	}
	return &moment, nil
}

func (a *app) formSpan(form url.Values, key string) (domain.BlockSpan, error) {
	span, err := datespec.ParseSpan(form.Get(key), datespec.Future, a.ref())
	if err != nil {
		return domain.BlockSpan{}, domain.ValidationError(domain.FieldError{Field: key, Message: err.Error()})
	}
	return span, nil
}

func (a *app) formDate(form url.Values, key string, m datespec.Mode) (civil.Date, error) {
	moment, err := a.formMoment(form, key, m)
	if err != nil {
		return civil.Date{}, err
	}
	if moment == nil {
		return civil.Date{}, nil
	}
	if moment.Min.Valid {
		return civil.Date{}, domain.ValidationError(domain.FieldError{Field: key, Message: "expected date only"})
	}
	return moment.Date, nil
}

func formMinute(form url.Values, key string) (sql.Null[civil.Minute], error) {
	if form.Get(key) == "" {
		return sql.Null[civil.Minute]{}, nil
	}
	minute, err := datespec.ParseClock(form.Get(key), false)
	if err != nil {
		return sql.Null[civil.Minute]{}, domain.ValidationError(domain.FieldError{Field: key, Message: err.Error()})
	}
	return sql.Null[civil.Minute]{V: minute, Valid: true}, nil
}

func splitTags(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}
