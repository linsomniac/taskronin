package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/linsomniac/taskronin/internal/api"
	"github.com/linsomniac/taskronin/internal/civil"
	"github.com/linsomniac/taskronin/internal/client"
)

func TestRunRecapSpec(t *testing.T) {
	for _, spec := range []string{"tue 10:00", "nextweek"} {
		t.Run(spec, func(t *testing.T) {
			f := newFake()
			var out bytes.Buffer
			err := Run(t.Context(), RunOptions{API: f, StartMode: ModeRecap, RecapSpec: spec, Out: &out})
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("Run = %v, want ErrUsage", err)
			}
			eq(t, f.calls, []call{{Method: "Clock"}, {Method: "Generation"}})
			eq(t, out.Len(), 0) // Validation must happen before terminal setup.
		})
	}
}

func TestRunUnreachable(t *testing.T) {
	for _, method := range []string{"Clock", "Generation"} {
		t.Run(method, func(t *testing.T) {
			f := newFake()
			want := notSent()
			f.errs[method] = want
			var out bytes.Buffer
			err := Run(t.Context(), RunOptions{API: f, Out: &out})
			eq(t, err, want)
			if !errors.Is(err, client.ErrNotSent) {
				t.Fatalf("Run = %v, want unchanged error %v", err, want)
			}
			wantCalls := []call{{Method: "Clock"}}
			if method == "Generation" {
				wantCalls = append(wantCalls, call{Method: "Generation"})
			}
			eq(t, f.calls, wantCalls)
			eq(t, out.Len(), 0)
		})
	}
}

type runRecapAPI struct {
	*fakeAPI
	day chan civil.Date
}

func (f *runRecapAPI) Day(ctx context.Context, date civil.Date) (*api.Day, error) {
	day, err := f.fakeAPI.Day(ctx, date)
	select {
	case f.day <- date:
	case <-ctx.Done():
	}
	return day, err
}

func TestRunRecapDate(t *testing.T) {
	for _, tc := range []struct{ spec, want string }{
		{"", "2026-10-28"}, {"tue", "2026-10-27"}, {"wed", "2026-10-28"}, {"2026-10-25", "2026-10-25"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			f := &runRecapAPI{fakeAPI: newFake(), day: make(chan civil.Date, 1)}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			in, keys := io.Pipe()
			defer in.Close()
			defer keys.Close()
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, RunOptions{API: f, StartMode: ModeRecap, RecapSpec: tc.spec, In: in, Out: io.Discard})
			}()
			select {
			case date := <-f.day:
				eq(t, date.String(), tc.want)
			case err := <-done:
				t.Fatalf("Run exited before loading recap: %v", err)
			case <-ctx.Done():
				t.Fatal("Run did not load recap")
			}
			writeDone := make(chan error, 1)
			go func() { _, err := io.WriteString(keys, "q"); writeDone <- err }()
			select {
			case err := <-writeDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("Run did not read quit key")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("Run did not exit")
			}
		})
	}
}
