package tui

import (
	"context"
	"errors"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"

	"taskronin/internal/datespec"
)

// ErrUsage identifies invalid launch options before the terminal is opened.
var ErrUsage = errors.New("usage")

// RunOptions supplies the client, initial view and terminal dependencies.
type RunOptions struct {
	API       API
	StartTab  Tab
	StartMode Mode
	RecapSpec string // Past-mode date only; empty means today.
	Color     bool
	Editor    EditorEnv
	In        io.Reader
	Out       io.Writer
}

// Run checks server connectivity and launch options, then owns the terminal
// until the app quits or ctx is canceled.
func Run(ctx context.Context, o RunOptions) error {
	clock, err := o.API.Clock(ctx)
	if err != nil {
		return err
	}
	if _, err := o.API.Generation(ctx); err != nil {
		return err
	}
	date := clock.Today
	if o.RecapSpec != "" {
		ref := datespec.Ref{Today: clock.Today, WeekStart: weekdayOf(clock.WeekStart)}
		moment, err := datespec.Parse(o.RecapSpec, datespec.Past, ref)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUsage, err)
		}
		if moment.Min.Valid {
			return fmt.Errorf("%w: expected a date without a time", ErrUsage)
		}
		date = moment.Date
	}
	app := NewApp(Config{
		API: o.API, Ctx: ctx, Clock: *clock, StartTab: o.StartTab, StartMode: o.StartMode,
		RecapDate: date, Color: o.Color, Editor: o.Editor,
	})
	_, err = tea.NewProgram(app, tea.WithContext(ctx), tea.WithInput(o.In), tea.WithOutput(o.Out)).Run()
	return err
}
