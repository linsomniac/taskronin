package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"taskronin/internal/api"
	"taskronin/internal/domain"
)

type callKind uint8

const (
	callRead   callKind = iota + 1 // GET; transport retries, busy retry, never pending
	callKeyed                      // keyed mutation; retries and pending eligible
	callExempt                     // no transport retry or pending; busy retry only
)

type call struct {
	Kind         callKind
	Method, Path string
	Query        url.Values
	Body         any
	Mut          Mut // used only by callKeyed
	Budget       time.Duration
	IfNoneMatch  string
	NoRetry      bool // BF22: status --short must make exactly one attempt
}

// do owns the total budget, including identity lookup, backoff and body reads.
// The returned response supplies status/headers; its body has been closed.
func (c *Client) do(ctx context.Context, k call, out any) (*http.Response, error) {
	budget := k.Budget
	if budget == 0 {
		budget = c.opts.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var body []byte
	if k.Body != nil {
		var err error
		body, err = json.Marshal(k.Body)
		if err != nil {
			return nil, fmt.Errorf("%w: encode request: %w", ErrNotSent, err)
		}
	}
	var key string
	var who api.WhoAmI
	if k.Kind == callKeyed {
		key = k.Mut.Key
		if key == "" {
			key = c.opts.NewKey()
		}
	}
	if k.Kind != callRead {
		var err error
		who, err = c.who(ctx, false)
		if err != nil {
			var problem *ProblemError
			if errors.As(err, &problem) {
				return nil, err
			}
			// The identity read can fail after transmission, but the mutation
			// itself has not been attempted and must never become pending.
			var unknown *OutcomeUnknownError
			if errors.As(err, &unknown) {
				err = unknown.Err
			}
			return nil, fmt.Errorf("%w: fetch identity: %w", ErrNotSent, err)
		}
	}
	transportRetries, busyRetries := 0, 0
	mayHaveSent := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, c.failed(k, body, key, who, mayHaveSent, err)
		}
		resp, data, sent, err := c.attempt(ctx, k, body, key, who.Generation)
		// AIDEV-NOTE: transmission is sticky across the WHOLE call. A later
		// failed dial cannot prove an earlier transmitted mutation was lost.
		mayHaveSent = mayHaveSent || sent
		if err != nil {
			is4xx := resp != nil && resp.StatusCode >= 400 && resp.StatusCode < 500
			if !k.NoRetry && k.Kind != callExempt && !is4xx && transportRetries < 2 && ctx.Err() == nil {
				delay := [...]time.Duration{100 * time.Millisecond, 400 * time.Millisecond}[transportRetries]
				transportRetries++
				waitErr := c.wait(ctx, delay)
				if waitErr == nil {
					continue
				}
				err = errors.Join(err, waitErr)
			}
			return resp, c.failed(k, body, key, who, mayHaveSent, err)
		}
		if resp.StatusCode == http.StatusNotModified {
			return resp, nil
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			var p api.Problem
			if err := json.Unmarshal(data, &p); err != nil {
				return resp, fmt.Errorf("HTTP %d: decode problem: %w", resp.StatusCode, err)
			}
			problem := &ProblemError{Status: resp.StatusCode, Problem: p}
			if resp.StatusCode == http.StatusPreconditionFailed && problem.Code() == domain.CodeGenerationMismatch {
				_, _ = c.RefreshGeneration(ctx)
				return resp, problem
			}
			if !k.NoRetry && resp.StatusCode == http.StatusServiceUnavailable && problem.Code() == domain.CodeBusy && busyRetries == 0 {
				if delay, ok := busyDelay(resp, p); ok {
					busyRetries++
					if err := c.wait(ctx, delay); err != nil {
						// Busy is a definitive response, even when its backoff
						// consumes the remaining budget: do not save it.
						return resp, errors.Join(problem, err)
					}
					continue
				}
			}
			return resp, problem
		}
		if out != nil && resp.StatusCode != http.StatusNoContent {
			if err := json.Unmarshal(data, out); err != nil {
				return resp, fmt.Errorf("decode response: %w", err)
			}
		}
		return resp, nil
	}
}

func (c *Client) wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.opts.Clock.After(delay):
		return ctx.Err()
	}
}

func busyDelay(resp *http.Response, p api.Problem) (time.Duration, bool) {
	seconds := p.RetryAfter
	if header := resp.Header.Get("Retry-After"); header != "" {
		var err error
		seconds, err = strconv.Atoi(header)
		if err != nil {
			return 0, false
		}
	}
	if seconds < 0 || seconds > 2 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func (c *Client) failed(k call, body []byte, key string, who api.WhoAmI, sent bool, err error) error {
	if !sent {
		return fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	unknown := &OutcomeUnknownError{Key: key, Err: err}
	if k.Kind == callKeyed && c.opts.Pending != nil {
		p := PendingRequest{Key: key, Method: k.Method, Path: k.Path, IfMatch: k.Mut.IfMatch, Generation: who.Generation, Actor: who.Actor, Body: body}
		if saveErr := c.opts.Pending.Save(p); saveErr != nil {
			unknown.Err = errors.Join(err, saveErr)
		} else {
			unknown.Saved = true
		}
	}
	return unknown
}
