package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"taskronin/internal/civil"
	"taskronin/internal/domain"
	"taskronin/internal/store"
)

// AIChanges contains review progress and a page of token or retention-lowering operations.
type AIChanges struct {
	ReviewedThrough domain.OpSeq
	Unreviewed      int
	Ops             []OpSummary
	NextCursor      string
}

// ListAIChangesIn selects unreviewed operations, or all matching operations when requested.
type ListAIChangesIn struct {
	IncludeReviewed bool
	Limit           int
	Cursor          string
}

// MarkAIReviewedIn acknowledges AI changes through an inclusive operation sequence.
type MarkAIReviewedIn struct {
	Through domain.OpSeq
}

// ListAIChanges returns AI changes newest first, with the total unreviewed count.
func (s *Service) ListAIChanges(ctx context.Context, a domain.Actor, in ListAIChangesIn) (AIChanges, error) {
	limit, err := pageLimit(in.Limit)
	if err != nil {
		return AIChanges{}, err
	}
	cursor, err := decodeKeyset("ops", in.Cursor)
	if err != nil {
		return AIChanges{}, err
	}
	var changes AIChanges
	err = s.read(ctx, a, domain.ScopeRead, false, func(q store.Q, _ time.Time, _ civil.Date) error {
		var err error
		changes.ReviewedThrough, err = aiReviewedThrough(ctx, q)
		if err != nil {
			return err
		}
		changes.Unreviewed, err = store.CountAIOps(ctx, q, changes.ReviewedThrough)
		if err != nil {
			return err
		}
		query := store.OpQuery{AIOnly: true, BeforeSeq: domain.OpSeq(cursor.ID), Limit: limit}
		if !in.IncludeReviewed {
			query.SinceSeq = changes.ReviewedThrough
		}
		page, err := opPage(ctx, q, query)
		if err != nil {
			return err
		}
		changes.Ops, changes.NextCursor = page.Items, page.NextCursor
		return nil
	})
	if err != nil {
		return AIChanges{}, err
	}
	return changes, nil
}

// MarkAIReviewed advances the owner's review watermark without permitting reversal.
func (s *Service) MarkAIReviewed(ctx context.Context, m Meta, in MarkAIReviewedIn) (*OpResult, error) {
	return s.run(ctx, m, opSpec{kind: domain.OpAIReview, class: classForward,
		scopes: domain.ScopeWrite, ownerOnly: true, idem: true}, func(o *op) error {
		// AIDEV-NOTE: The runner has already inserted this op. Its own sequence
		// is excluded so acknowledgement cannot hide the next committed AI change.
		if in.Through >= o.rec.Seq {
			return validation("through", errors.New("must precede the review operation's sequence"))
		}
		current, err := aiReviewedThrough(o.ctx, o.tx)
		if err != nil {
			return err
		}
		if in.Through <= current {
			return nil
		}
		before := map[string]string{"key": "ai_reviewed_through", "value": strconv.FormatInt(int64(current), 10)}
		after := map[string]string{"key": "ai_reviewed_through", "value": strconv.FormatInt(int64(in.Through), 10)}
		bj, err := json.Marshal(before)
		if err != nil {
			return err
		}
		aj, err := json.Marshal(after)
		if err != nil {
			return err
		}
		if err := store.SetSetting(o.ctx, o.tx, "ai_reviewed_through", after["value"]); err != nil {
			return err
		}
		return o.record(domain.Event{Entity: domain.EntSetting, Action: domain.ActUpdate, Before: bj, After: aj})
	})
}

func aiReviewedThrough(ctx context.Context, q store.Q) (domain.OpSeq, error) {
	value, err := store.GetSetting(ctx, q, "ai_reviewed_through")
	if err != nil {
		return 0, err
	}
	seq, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	if seq < 0 {
		return 0, errors.New("negative ai_reviewed_through setting")
	}
	return domain.OpSeq(seq), nil
}
