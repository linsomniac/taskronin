package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// SearchIn selects literal full-text terms, entity kinds and an offset page.
type SearchIn struct {
	Query, Kind string
	Limit       int
	Cursor      string
}

// SearchHit is a live task or entry ranked by full-text relevance.
type SearchHit struct {
	Kind     string
	ID       int64
	TaskID   sql.Null[domain.TaskID]
	Title    string
	Snippet  string
	Status   domain.TaskStatus
	WorkDate sql.Null[civil.Date]
}

// Search returns matching live rows without reconciling time-dependent state.
func (s *Service) Search(ctx context.Context, a domain.Actor, in SearchIn) (Page[SearchHit], error) {
	match, err := ftsQuery(in.Query)
	if err != nil {
		return Page[SearchHit]{}, err
	}
	if in.Kind == "" {
		in.Kind = "all"
	}
	if in.Kind != "task" && in.Kind != "entry" && in.Kind != "all" {
		return Page[SearchHit]{}, validation("kind", errors.New("must be task, entry or all"))
	}
	limit, err := pageLimit(in.Limit)
	if err != nil {
		return Page[SearchHit]{}, err
	}
	cursor, err := decodeOffset(in.Cursor)
	if err != nil {
		return Page[SearchHit]{}, err
	}
	var page Page[SearchHit]
	err = s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		hits, err := store.Search(ctx, q, match, in.Kind, limit+1, cursor.Offset)
		if err != nil {
			return err
		}
		if len(hits) > limit {
			hits = hits[:limit]
			page.NextCursor = domain.EncodeCursor(domain.Cursor{Offset: cursor.Offset + limit})
		}
		page.Items = make([]SearchHit, 0, len(hits))
		for _, hit := range hits {
			page.Items = append(page.Items, SearchHit{Kind: hit.Kind, ID: hit.ID, TaskID: hit.TaskID,
				Title: hit.Title, Snippet: hit.Snippet, Status: hit.Status, WorkDate: hit.WorkDate})
		}
		return nil
	})
	if err != nil {
		return Page[SearchHit]{}, err
	}
	return page, nil
}

func ftsQuery(user string) (string, error) {
	// AIDEV-NOTE: FTS5 treats NUL as the end of its query, including inside a
	// quoted term. Reject it before quoting to avoid an unterminated string.
	if !utf8.ValidString(user) || strings.ContainsRune(user, 0) {
		return "", validation("query", errors.New("must be UTF-8 text without NUL"))
	}
	terms := strings.Fields(user)
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		stem := strings.TrimRight(term, "*")
		if stem == "" {
			continue
		}
		query := `"` + strings.ReplaceAll(stem, `"`, `""`) + `"`
		if len(stem) < len(term) {
			query += "*"
		}
		quoted = append(quoted, query)
	}
	if len(quoted) == 0 {
		return "", validation("query", errors.New("must contain a search term"))
	}
	return strings.Join(quoted, " "), nil
}
