package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/client"
	"github.com/linsomniac/taskronin/internal/domain"
	"github.com/linsomniac/taskronin/internal/textsafe"
)

// sendFunc captures a resolved request; an empty key lets the client mint one.
type sendFunc func(ctx context.Context, c API, key string) (*api.OpResponse, error)

type formRetry struct {
	value, key, gen string
	at              time.Time
	send            sendFunc
}

type form struct {
	title, hint string
	input       textinput.Model
	errText     string
	waiting     bool
	retry       *formRetry
	retryBusy   bool
	submit      func(a *App, value string) (label string, send sendFunc, err error)
	after       func(a *App, r *api.OpResponse) tea.Cmd
	label       string // Success label for the resolved request, including a retry.
	onToday     func(*App)
}

var errRetryStale = errors.New("retry snapshot is stale")

// formReadError marks a preflight failure: the mutation has not been attempted.
type formReadError struct{ err error }

func (e *formReadError) Error() string { return e.err.Error() }
func (e *formReadError) Unwrap() error { return e.err }

const retryStaleBanner = "the database may have changed since the first attempt (restore or 7-day limit) — check, then enter sends a new request"

func (a *App) openForm(title, hint, initial string,
	submit func(a *App, value string) (string, sendFunc, error), after func(*App, *api.OpResponse) tea.Cmd,
) *form {
	input := textinput.New()
	styles := textinput.Styles{}
	if a.cfg.Color {
		styles.Focused.Prompt = a.styles.Info
		styles.Focused.Placeholder = a.styles.Dim
	}
	styles.Cursor.Blink = false
	input.SetStyles(styles)
	input.SetVirtualCursor(false) // view renders a static caret in plain and colour modes.
	input.SetValue(initial)
	f := &form{title: title, hint: hint, input: input, submit: submit, after: after}
	a.form = f
	return f
}

func (f *form) focus() tea.Cmd { return f.input.Focus() }

func (*form) load(_ *App) tea.Cmd              { return nil }
func (*form) selection() (int64, string, bool) { return 0, "", false }

func (f *form) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if f.waiting {
		a.setBanner(bannerInfo, "busy…")
		if k.String() == "q" {
			a.setBanner(bannerInfo, "busy… (ctrl+c forces)")
		}
		return nil, true
	}
	switch k.String() {
	case "esc":
		a.clearScreenBanner(f)
		a.form = nil
		if f.retry != nil {
			a.setBanner(bannerError, "closed — the first attempt may have saved; check before re-adding")
		}
		return nil, true
	case "enter":
		return f.send(a), true
	default:
		return f.update(a, k), true
	}
}

func (f *form) update(a *App, msg tea.Msg) tea.Cmd {
	if _, ok := msg.(todayMsg); ok && f.onToday != nil && !f.waiting && f.retry == nil {
		f.onToday(a)
	}
	if f.waiting {
		return nil
	}
	old := f.input.Value()
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	if f.input.Value() != old {
		f.errText = ""
		// AIDEV-NOTE: retain the snapshot until Enter compares the final text.
		// Editing and then restoring the original value must still resend it.
		if f.retry != nil {
			f.retryBanner(a)
		} else if f.onToday != nil {
			f.onToday(a)
		}
	}
	return cmd
}

func (f *form) view(a *App, w, h int) string {
	// Sanitize a display copy before textinput applies styles; preserve the
	// original text and cursor for editing and the request snapshot.
	input := f.input
	if w < 4 {
		input.Prompt = "" // Reserve a cell for the caret even in a tiny terminal.
	}
	input.SetWidth(max(1, w-textsafe.Width(input.Prompt)-1))
	value := []rune(textsafe.SanitizeLine(f.input.Value()))
	pos := min(f.input.Position(), len(value))
	// The caret is display-only. Keeping it just before textinput's hidden
	// cursor also keeps it visible when the input scrolls horizontally.
	input.SetValue(string(value[:pos]) + "▏" + string(value[pos:]))
	input.SetCursor(pos + 1)
	inputLine := input.View()
	if w == 1 {
		inputLine = "▏" // A wide glyph cannot share this sole cell with the caret.
	}
	bottom := a.styles.Dim.Render(a.line(f.hint, w))
	if f.errText != "" {
		bottom = a.styles.Err.Render(a.line(f.errText, w))
	}
	return clip([]string{a.styles.Info.Render(a.line(f.title, w)), inputLine, bottom}, w, h)
}

func (f *form) retryBanner(a *App) {
	if f.input.Value() == f.retry.value {
		text := "outcome unknown — enter resends the same request"
		if f.retryBusy {
			text = "server busy — enter resends the same request"
		}
		a.setBanner(bannerError, text)
	} else {
		a.setBanner(bannerError, "text changed — enter sends a NEW request; the first attempt may have saved")
	}
}

func (f *form) dropRetry(a *App) {
	f.retry = nil
	f.retryBusy = false
	if f.onToday != nil {
		f.onToday(a)
	}
}

func (f *form) send(a *App) tea.Cmd {
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil
	}
	value := f.input.Value()
	snapshot := f.retry
	var send sendFunc
	var gen, key string
	at := a.clock.Now.Time
	if snapshot != nil && value == snapshot.value {
		send = snapshot.send
	} else {
		// A changed log submission resolves its implicit link before capturing
		// the body. Keep the old key, body and label if parsing fails.
		if snapshot != nil && f.onToday != nil {
			f.onToday(a)
		}
		label, resolved, err := f.submit(a, value)
		if err != nil {
			f.errText = err.Error()
			return nil
		}
		f.label, send = label, resolved
		f.retry, f.retryBusy, snapshot = nil, false, nil
	}
	f.waiting, f.errText = true, ""
	// AIDEV-NOTE: retry snapshot. Resolve once in Update and capture the FIRST
	// submission's server time here, never when its reply arrives. Resends keep
	// the same body/key/gen/at, and check fresh time and identity before sending.
	// If no clock has loaded, read it once before the first send instead.
	// gen/at/key are written only by this cmd and read by onErr after opMsg delivery;
	// Bubble Tea's message channel orders those accesses. No cmd reads App/form.
	return a.mutate(f.label, func(ctx context.Context, c API) (*api.OpResponse, error) {
		if snapshot != nil {
			clock, err := c.Clock(ctx)
			if err != nil {
				return nil, &formReadError{err: err}
			}
			if clock.Now.Sub(snapshot.at) >= client.PendingResendWindow {
				return nil, errRetryStale
			}
			fresh, err := c.RefreshGeneration(ctx)
			if err != nil {
				return nil, &formReadError{err: err}
			}
			if fresh != snapshot.gen {
				return nil, errRetryStale
			}
			key = snapshot.key
		} else {
			if at.IsZero() {
				clock, err := c.Clock(ctx)
				if err != nil {
					return nil, &formReadError{err: err}
				}
				at = clock.Now.Time
			}
			var err error
			gen, err = c.Generation(ctx)
			if err != nil {
				return nil, &formReadError{err: err}
			}
			// AIDEV-NOTE: keyed busy can follow commit (S1'). Mint the key here
			// so even a plain busy problem can retain the original P4 snapshot.
			key = api.NewIdempotencyKey()
		}
		return send(ctx, c, key)
	}, func(a *App, r *api.OpResponse) tea.Cmd {
		f.waiting = false
		if a.form == f {
			a.form = nil
		}
		if f.after != nil {
			return f.after(a, r)
		}
		return nil
	}, func(a *App, err error) {
		f.waiting = false
		var read *formReadError
		var unknown *client.OutcomeUnknownError
		var problem *client.ProblemError
		switch {
		case errors.Is(err, errRetryStale):
			f.dropRetry(a)
		case snapshot != nil && errors.Is(err, client.ErrGenerationMismatch):
			f.dropRetry(a)
			if errors.As(err, &unknown) {
				a.setBanner(bannerError, retryStaleBanner)
			}
		case errors.As(err, &read):
			if snapshot != nil {
				a.setBanner(bannerError, "not sent — outcome of the first attempt still unknown; enter resends the same request")
			}
		case errors.As(err, &unknown):
			f.retryBusy = false
			if snapshot == nil && unknown.Key != "" {
				f.retry = &formRetry{value: value, key: unknown.Key, gen: gen, at: at, send: send}
			}
			if f.retry != nil {
				f.retryBanner(a)
			}
		case errors.Is(err, client.ErrNotSent):
			if snapshot != nil {
				a.setBanner(bannerError, "not sent — outcome of the first attempt still unknown; enter resends the same request")
			}
		case errors.As(err, &problem):
			if problem.Code() == domain.CodeBusy {
				if snapshot == nil {
					f.retry = &formRetry{value: value, key: key, gen: gen, at: at, send: send}
				}
				f.retryBusy = true
				f.retryBanner(a)
				break
			}
			// A definitive refusal ends this replay; keep handleErr's problem banner.
			f.dropRetry(a)
			// AIDEV-NOTE: a definitive 412 invalidates the captured If-Match.
			// Close only this form; the next explicit action captures the
			// refreshed record. CF03 unknown/not-sent cases above retain it.
			if problem.Status == http.StatusPreconditionFailed && problem.Problem.Code != "generation_mismatch" && a.form == f {
				a.form = nil
			}
		case snapshot != nil:
			a.setBanner(bannerError, "not sent — outcome of the first attempt still unknown; enter resends the same request")
		}
	})
}

type confirmation struct {
	question string
	yes      func(*App) tea.Cmd
}

func (a *App) confirm(question string, yes func(*App) tea.Cmd) tea.Cmd {
	a.form = &confirmation{question: question, yes: yes}
	return nil
}

func (*confirmation) load(_ *App) tea.Cmd              { return nil }
func (*confirmation) update(_ *App, _ tea.Msg) tea.Cmd { return nil }
func (*confirmation) selection() (int64, string, bool) { return 0, "", false }
func (f *confirmation) view(a *App, w, _ int) string   { return a.line(f.question+" [y/N]", w) }
func (f *confirmation) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	a.form = nil
	if k.String() == "y" {
		return f.yes(a), true
	}
	return nil, true
}

func (a *App) openAdd() tea.Cmd {
	const label = "added to triage (new)"
	return a.openForm("Add task", "enter saves · esc cancels", "", func(_ *App, value string) (string, sendFunc, error) {
		r := api.AddTaskRequest{Title: value}
		return label, func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
			return c.AddTask(ctx, client.Mut{Key: key}, r)
		}, nil
	}, func(a *App, r *api.OpResponse) tea.Cmd {
		if r != nil && r.Task != nil {
			headline := fmt.Sprintf("#%d %s", r.Task.ID, label)
			effects := slices.DeleteFunc(slices.Clone(r.Effects), func(e api.Effect) bool {
				return e.Kind == "reason_added" && e.TaskID == r.Task.ID
			})
			if line := effectsLine(effects); line != "" {
				headline += " · " + line
			}
			a.setBanner(bannerOK, headline)
		}
		return nil
	}).focus()
}
