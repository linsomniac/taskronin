package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"taskronin/internal/api"
	"taskronin/internal/client"
	"taskronin/internal/textsafe"
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
) tea.Cmd {
	input := textinput.New()
	styles := textinput.Styles{}
	if a.cfg.Color {
		styles.Focused.Prompt = a.styles.Info
		styles.Focused.Placeholder = a.styles.Dim
	}
	styles.Cursor.Blink = false
	input.SetStyles(styles)
	input.SetVirtualCursor(a.cfg.Color)
	input.SetValue(initial)
	f := &form{title: title, hint: hint, input: input, submit: submit, after: after}
	a.form = f
	return f.input.Focus()
}

func (*form) load(_ *App) tea.Cmd              { return nil }
func (*form) selection() (int64, string, bool) { return 0, "", false }

func (f *form) handle(a *App, k tea.KeyPressMsg) (tea.Cmd, bool) {
	if f.waiting {
		a.setBanner(bannerInfo, "busy…")
		return nil, true
	}
	switch k.String() {
	case "esc":
		a.form = nil
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
		f.errText, f.retry = "", nil
		if f.onToday != nil {
			f.onToday(a)
		}
	}
	return cmd
}

func (f *form) view(a *App, w, h int) string {
	// Sanitize a display copy before textinput applies styles; preserve the
	// original text and cursor for editing and the request snapshot.
	input := f.input
	input.SetWidth(max(1, w-3))
	input.SetValue(textsafe.SanitizeLine(f.input.Value()))
	input.SetCursor(f.input.Position())
	bottom := a.styles.Dim.Render(a.line(f.hint, w))
	if f.errText != "" {
		bottom = a.styles.Err.Render(a.line(f.errText, w))
	}
	return clip([]string{a.styles.Info.Render(a.line(f.title, w)), input.View(), bottom}, w, h)
}

func (f *form) send(a *App) tea.Cmd {
	if a.inflight {
		a.setBanner(bannerInfo, "busy…")
		return nil
	}
	value := f.input.Value()
	snapshot := f.retry
	var send sendFunc
	var gen string
	var err error
	at := a.clock.Now.Time
	if snapshot != nil && value == snapshot.value {
		send = snapshot.send
	} else {
		f.retry, snapshot = nil, nil
		f.label, send, err = f.submit(a, value)
		if err != nil {
			f.errText = err.Error()
			return nil
		}
	}
	f.waiting, f.errText = true, ""
	// AIDEV-NOTE: retry snapshot. Resolve once in Update and capture the FIRST
	// submission's server time here, never when its reply arrives. Resends keep
	// the same body/key/gen/at, and check fresh time and identity before sending.
	// gen is written only by this cmd and read by onErr after opMsg delivery;
	// Bubble Tea's message channel orders those accesses. No cmd reads App/form.
	return a.mutate(f.label, func(ctx context.Context, c API) (*api.OpResponse, error) {
		key := ""
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
			var err error
			gen, err = c.Generation(ctx)
			if err != nil {
				return nil, &formReadError{err: err}
			}
		}
		return send(ctx, c, key)
	}, func(a *App, r *api.OpResponse) tea.Cmd {
		a.form = nil
		if f.after != nil {
			return f.after(a, r)
		}
		return nil
	}, func(a *App, err error) {
		f.waiting = false
		var read *formReadError
		if errors.As(err, &read) {
			return
		}
		var unknown *client.OutcomeUnknownError
		switch {
		case errors.Is(err, errRetryStale), snapshot != nil && errors.Is(err, client.ErrGenerationMismatch):
			f.retry = nil
			if f.onToday != nil {
				f.onToday(a)
			}
			a.setBanner(bannerError, retryStaleBanner)
		case errors.As(err, &unknown):
			if snapshot == nil {
				f.retry = &formRetry{value: value, key: unknown.Key, gen: gen, at: at, send: send}
			}
			a.setBanner(bannerError, "outcome unknown — enter resends the same request")
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
	return a.openForm("Add task", "enter saves · esc cancels", "", func(_ *App, value string) (string, sendFunc, error) {
		r := api.AddTaskRequest{Title: value}
		return "added to triage (new)", func(ctx context.Context, c API, key string) (*api.OpResponse, error) {
			return c.AddTask(ctx, client.Mut{Key: key}, r)
		}, nil
	}, func(a *App, r *api.OpResponse) tea.Cmd {
		if r != nil && r.Task != nil {
			a.setBanner(bannerInfo, fmt.Sprintf("#%d %s", r.Task.ID, a.banner.text))
		}
		return nil
	})
}
